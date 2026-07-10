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

// GitLabMetricsExporterReconciler discovers a GitLab group's (or user's) projects and
// dispatches one per-project collection Job for each, capped by spec.parallelism.
type GitLabMetricsExporterReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	ExporterImage string
	// DiscoverProjects lists the target's project paths. Defaults to live GitLab discovery
	// and is overridable in tests so reconciliation needs no network.
	DiscoverProjects func(ctx context.Context, auth discovery.GitLabAuth, target, targetType string, sel discovery.Selector) ([]string, error)
	// RateBudget reports the credential's remaining API budget. It defaults to a live probe
	// and is overridable in tests so reconciliation needs no network.
	RateBudget func(ctx context.Context, auth discovery.GitLabAuth) (discovery.Budget, error)
}

func discoverGitLabProjects(ctx context.Context, auth discovery.GitLabAuth, target, targetType string, sel discovery.Selector) ([]string, error) {
	return discovery.ListGitLabProjects(ctx, auth, target, targetType, sel)
}

// gitlabRateBudget is the default RateBudget: probe the instance's RateLimit-* headers.
func gitlabRateBudget(ctx context.Context, auth discovery.GitLabAuth) (discovery.Budget, error) {
	return discovery.GitLabRateBudget(ctx, auth)
}

// +kubebuilder:rbac:groups=scm.jalet.io,resources=gitlabmetricsexporters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=scm.jalet.io,resources=gitlabmetricsexporters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=scm.jalet.io,resources=gitlabmetricsexporters/finalizers,verbs=update

// Reconcile discovers projects, dispatches collection Jobs capped by parallelism, and
// reflects discovery state in the CR status.
func (r *GitLabMetricsExporterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var cr scmv1alpha1.GitLabMetricsExporter
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	auth, target, err := r.gitlabAuth(ctx, &cr)
	if err != nil {
		setReadyCondition(&cr.Status.Conditions, cr.Generation, metav1.ConditionFalse, reasonCredentialInvalid, err.Error())
		return r.updateStatus(ctx, &cr, ctrl.Result{RequeueAfter: credentialRequeue})
	}

	// Pause before discovery and dispatch when the credential's API budget is low, so the
	// operator stops spending quota (discovery included) until the rate-limit window resets.
	rateBudget := r.RateBudget
	if rateBudget == nil {
		rateBudget = gitlabRateBudget
	}
	if limited, requeue, msg := rateLimited(ctx, cr.Spec.RateLimitThreshold,
		func(c context.Context) (discovery.Budget, error) { return rateBudget(c, auth) }); limited {
		setReadyCondition(&cr.Status.Conditions, cr.Generation, metav1.ConditionFalse, reasonRateLimited, msg)
		return r.updateStatus(ctx, &cr, ctrl.Result{RequeueAfter: requeue})
	}

	discover := r.DiscoverProjects
	if discover == nil {
		discover = discoverGitLabProjects
	}
	repos := cr.Status.DiscoveredRepositories
	if needsDiscovery(cr.Status.LastDiscoveryTime, len(repos), cr.Spec.DiscoveryInterval.Duration) {
		newRepos, derr := discover(ctx, auth, target, cr.Spec.TargetType, selectorFrom(cr.Spec.AutoDiscover))
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
		func(repo string) *batchv1.Job { return gitlabJob(&cr, image, repo) })
	if err != nil {
		setReadyCondition(&cr.Status.Conditions, cr.Generation, metav1.ConditionFalse, reasonDispatchFailed, err.Error())
		return r.updateStatus(ctx, &cr, ctrl.Result{RequeueAfter: credentialRequeue})
	}

	setReadyCondition(&cr.Status.Conditions, cr.Generation, metav1.ConditionTrue, reasonDiscovered,
		fmt.Sprintf("discovered %d projects", len(repos)))
	requeue := cr.Spec.DiscoveryInterval.Duration
	if pending {
		requeue = pendingRequeue
	}
	return r.updateStatus(ctx, &cr, ctrl.Result{RequeueAfter: requeue})
}

// gitlabAuth loads the token Secret and builds the discovery auth plus the target (group or
// user). It returns an actionable error when the Secret or the token key is missing.
func (r *GitLabMetricsExporterReconciler) gitlabAuth(ctx context.Context, cr *scmv1alpha1.GitLabMetricsExporter) (discovery.GitLabAuth, string, error) {
	secret, err := loadSecret(ctx, r.Client, cr.Namespace, cr.Spec.CredentialsSecret.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return discovery.GitLabAuth{}, "", fmt.Errorf("credentials Secret %q not found", cr.Spec.CredentialsSecret.Name)
		}
		return discovery.GitLabAuth{}, "", err
	}
	token, ok := secret.Data[cr.Spec.TokenKey]
	if !ok || len(token) == 0 {
		return discovery.GitLabAuth{}, "", fmt.Errorf("credentials Secret %q is missing key %q", cr.Spec.CredentialsSecret.Name, cr.Spec.TokenKey)
	}
	target := cr.Spec.Group
	if cr.Spec.TargetType == "user" {
		target = cr.Spec.User
	}
	return discovery.GitLabAuth{Token: string(token), BaseURL: cr.Spec.BaseURL}, target, nil
}

func (r *GitLabMetricsExporterReconciler) updateStatus(ctx context.Context, cr *scmv1alpha1.GitLabMetricsExporter, result ctrl.Result) (ctrl.Result, error) {
	cr.Status.ObservedGeneration = cr.Generation
	if err := r.Status().Update(ctx, cr); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}
	return result, nil
}

// SetupWithManager registers the reconciler and the collection Jobs it owns.
func (r *GitLabMetricsExporterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&scmv1alpha1.GitLabMetricsExporter{}).
		Owns(&batchv1.Job{}).
		Named("gitlabmetricsexporter").
		Complete(r)
}
