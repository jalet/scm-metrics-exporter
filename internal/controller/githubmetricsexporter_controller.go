package controller

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	scmv1alpha1 "github.com/jalet/scm-metrics-exporter/api/v1alpha1"
	"github.com/jalet/scm-metrics-exporter/internal/discovery"
)

// GitHubMetricsExporterReconciler discovers a GitHub target's repositories and dispatches
// one per-repo collection Job for each, capped by spec.parallelism.
type GitHubMetricsExporterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// ExporterImage is the image used for collection Jobs when the CR does not override
	// spec.image.
	ExporterImage string
	// DiscoverRepos lists the target's repositories. It defaults to live GitHub discovery
	// and is overridable in tests so reconciliation needs no network.
	DiscoverRepos func(ctx context.Context, auth discovery.GitHubAuth, owner, targetType string, f discovery.Filter) ([]string, error)
}

// discoverGitHubRepos is the default DiscoverRepos: build a client and list repositories.
func discoverGitHubRepos(ctx context.Context, auth discovery.GitHubAuth, owner, targetType string, f discovery.Filter) ([]string, error) {
	c, err := discovery.NewGitHubClient(auth)
	if err != nil {
		return nil, err
	}
	return discovery.ListRepos(ctx, c, owner, targetType, f)
}

// +kubebuilder:rbac:groups=scm.jalet.io,resources=githubmetricsexporters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=scm.jalet.io,resources=githubmetricsexporters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=scm.jalet.io,resources=githubmetricsexporters/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile discovers repositories, dispatches collection Jobs capped by parallelism, and
// reflects discovery state in the CR status.
func (r *GitHubMetricsExporterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var cr scmv1alpha1.GitHubMetricsExporter
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	auth, owner, err := r.githubAuth(ctx, &cr)
	if err != nil {
		setReadyCondition(&cr.Status.Conditions, cr.Generation, metav1.ConditionFalse, reasonCredentialInvalid, err.Error())
		return r.updateStatus(ctx, &cr, ctrl.Result{RequeueAfter: credentialRequeue})
	}

	// Rediscover only when the interval has elapsed (or nothing found yet); otherwise reuse
	// the cached inventory so frequent top-up requeues do not hammer the API.
	discover := r.DiscoverRepos
	if discover == nil {
		discover = discoverGitHubRepos
	}
	repos := cr.Status.DiscoveredRepositories
	if needsDiscovery(cr.Status.LastDiscoveryTime, len(repos), cr.Spec.DiscoveryInterval.Duration) {
		newRepos, derr := discover(ctx, auth, owner, cr.Spec.TargetType, includeFilter(cr.Spec.AutoDiscover))
		if derr != nil {
			setReadyCondition(&cr.Status.Conditions, cr.Generation, metav1.ConditionFalse, reasonDiscoveryFailed, derr.Error())
			return r.updateStatus(ctx, &cr, ctrl.Result{RequeueAfter: credentialRequeue})
		}
		repos = newRepos
		cr.Status.DiscoveredRepositories = repos
		now := metav1.Now()
		cr.Status.LastDiscoveryTime = &now
	}

	image := cr.Spec.Image
	if image == "" {
		image = r.ExporterImage
	}
	pending, err := dispatchJobs(ctx, r.Client, r.Scheme, &cr, cr.Name, cr.Namespace, cr.Spec.Parallelism, repos,
		func(repo string) *batchv1.Job { return githubJob(&cr, image, repo) })
	if err != nil {
		setReadyCondition(&cr.Status.Conditions, cr.Generation, metav1.ConditionFalse, reasonDispatchFailed, err.Error())
		return r.updateStatus(ctx, &cr, ctrl.Result{RequeueAfter: credentialRequeue})
	}

	setReadyCondition(&cr.Status.Conditions, cr.Generation, metav1.ConditionTrue, reasonDiscovered,
		fmt.Sprintf("discovered %d repositories", len(repos)))
	requeue := cr.Spec.DiscoveryInterval.Duration
	if pending {
		requeue = pendingRequeue
	}
	return r.updateStatus(ctx, &cr, ctrl.Result{RequeueAfter: requeue})
}

// githubAuth loads the credentials Secret and builds the discovery auth plus the owner
// login. It returns an actionable error when the Secret or the required key is missing.
func (r *GitHubMetricsExporterReconciler) githubAuth(ctx context.Context, cr *scmv1alpha1.GitHubMetricsExporter) (discovery.GitHubAuth, string, error) {
	secret, err := loadSecret(ctx, r.Client, cr.Namespace, cr.Spec.CredentialsSecret.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return discovery.GitHubAuth{}, "", fmt.Errorf("credentials Secret %q not found", cr.Spec.CredentialsSecret.Name)
		}
		return discovery.GitHubAuth{}, "", err
	}

	owner := cr.Spec.Org
	if cr.Spec.TargetType == "user" {
		owner = cr.Spec.User
	}

	if cr.Spec.AuthMode == "app" {
		pem, ok := secret.Data[cr.Spec.AppPrivateKeyKey]
		if !ok || len(pem) == 0 {
			return discovery.GitHubAuth{}, "", fmt.Errorf("credentials Secret %q is missing key %q", cr.Spec.CredentialsSecret.Name, cr.Spec.AppPrivateKeyKey)
		}
		return discovery.GitHubAuth{AppID: cr.Spec.AppID, AppInstallationID: cr.Spec.AppInstallationID, AppPrivateKeyPEM: pem}, owner, nil
	}

	token, ok := secret.Data[cr.Spec.TokenKey]
	if !ok || len(token) == 0 {
		return discovery.GitHubAuth{}, "", fmt.Errorf("credentials Secret %q is missing key %q", cr.Spec.CredentialsSecret.Name, cr.Spec.TokenKey)
	}
	return discovery.GitHubAuth{Token: string(token)}, owner, nil
}

func (r *GitHubMetricsExporterReconciler) updateStatus(ctx context.Context, cr *scmv1alpha1.GitHubMetricsExporter, result ctrl.Result) (ctrl.Result, error) {
	cr.Status.ObservedGeneration = cr.Generation
	if err := r.Status().Update(ctx, cr); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}
	return result, nil
}

// SetupWithManager registers the reconciler and the collection Jobs it owns.
func (r *GitHubMetricsExporterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&scmv1alpha1.GitHubMetricsExporter{}).
		Owns(&batchv1.Job{}).
		Named("githubmetricsexporter").
		Complete(r)
}
