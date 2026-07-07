package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	scmv1alpha1 "github.com/jalet/scm-metrics-exporter/api/v1alpha1"
)

const (
	conditionReady          = "Ready"
	reasonReconciled        = "DeploymentAvailable"
	reasonProgressing       = "DeploymentProgressing"
	reasonCredentialInvalid = "CredentialsInvalid"
	credentialRequeue       = time.Minute
)

// GitHubMetricsExporterReconciler reconciles a GitHubMetricsExporter into an
// exporter Deployment and Service.
type GitHubMetricsExporterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// ExporterImage is the image used for exporter Deployments when the CR does not
	// override spec.image.
	ExporterImage string
	// serviceMonitorAvailable records whether monitoring.coreos.com/v1 ServiceMonitor
	// was served by the API server at manager startup. It gates both the .Owns watch
	// and per-reconcile ServiceMonitor create/update.
	serviceMonitorAvailable bool
}

// +kubebuilder:rbac:groups=scm.jalet.io,resources=githubmetricsexporters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=scm.jalet.io,resources=githubmetricsexporters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=scm.jalet.io,resources=githubmetricsexporters/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;list;watch;create;update;patch;delete

// Reconcile ensures the exporter Deployment and Service match the CR, and reflects
// readiness (or a credentials problem) in the CR status.
func (r *GitHubMetricsExporterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var cr scmv1alpha1.GitHubMetricsExporter
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.checkCredentials(ctx, &cr); err != nil {
		setReady(&cr, metav1.ConditionFalse, reasonCredentialInvalid, err.Error())
		return r.updateStatus(ctx, &cr, ctrl.Result{RequeueAfter: credentialRequeue})
	}

	image := cr.Spec.Image
	if image == "" {
		image = r.ExporterImage
	}

	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: cr.Name, Namespace: cr.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, dep, func() error {
		desired := githubDeployment(&cr, image)
		dep.Labels = desired.Labels
		dep.Spec = desired.Spec
		return controllerutil.SetControllerReference(&cr, dep, r.Scheme)
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile deployment: %w", err)
	}

	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: cr.Name, Namespace: cr.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		desired := githubService(&cr)
		svc.Labels = desired.Labels
		// Set only the fields we own; leave server-populated fields (clusterIP) intact.
		svc.Spec.Selector = desired.Spec.Selector
		svc.Spec.Ports = desired.Spec.Ports
		return controllerutil.SetControllerReference(&cr, svc, r.Scheme)
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile service: %w", err)
	}

	if err := r.reconcileServiceMonitor(ctx, &cr); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile servicemonitor: %w", err)
	}

	if deploymentAvailable(dep) {
		setReady(&cr, metav1.ConditionTrue, reasonReconciled, "exporter deployment is available")
	} else {
		setReady(&cr, metav1.ConditionFalse, reasonProgressing, "waiting for exporter deployment to become available")
	}
	return r.updateStatus(ctx, &cr, ctrl.Result{})
}

// checkCredentials verifies the referenced Secret exists and holds the key required
// by the CR's auth mode.
func (r *GitHubMetricsExporterReconciler) checkCredentials(ctx context.Context, cr *scmv1alpha1.GitHubMetricsExporter) error {
	var secret corev1.Secret
	name := types.NamespacedName{Name: cr.Spec.CredentialsSecret.Name, Namespace: cr.Namespace}
	if err := r.Get(ctx, name, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("credentials Secret %q not found", cr.Spec.CredentialsSecret.Name)
		}
		return err
	}

	wantKey := cr.Spec.TokenKey
	if cr.Spec.AuthMode == "app" {
		wantKey = cr.Spec.AppPrivateKeyKey
	}
	if _, ok := secret.Data[wantKey]; !ok {
		return fmt.Errorf("credentials Secret %q is missing key %q", cr.Spec.CredentialsSecret.Name, wantKey)
	}
	return nil
}

func (r *GitHubMetricsExporterReconciler) updateStatus(ctx context.Context, cr *scmv1alpha1.GitHubMetricsExporter, result ctrl.Result) (ctrl.Result, error) {
	cr.Status.ObservedGeneration = cr.Generation
	if err := r.Status().Update(ctx, cr); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}
	return result, nil
}

// reconcileServiceMonitor creates or updates the ServiceMonitor when it is both wanted
// (spec.serviceMonitor) and possible (the CRD was installed at startup); otherwise it
// ensures none exists. It never errors when the prometheus-operator CRD is absent.
func (r *GitHubMetricsExporterReconciler) reconcileServiceMonitor(ctx context.Context, cr *scmv1alpha1.GitHubMetricsExporter) error {
	if r.serviceMonitorAvailable && cr.Spec.ServiceMonitor {
		return r.applyServiceMonitor(ctx, cr)
	}
	return r.deleteServiceMonitor(ctx, cr)
}

func (r *GitHubMetricsExporterReconciler) applyServiceMonitor(ctx context.Context, cr *scmv1alpha1.GitHubMetricsExporter) error {
	sm := newServiceMonitor()
	sm.SetName(cr.Name)
	sm.SetNamespace(cr.Namespace)
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sm, func() error {
		desired := githubServiceMonitor(cr)
		sm.SetLabels(desired.GetLabels())
		sm.Object["spec"] = desired.Object["spec"]
		return controllerutil.SetControllerReference(cr, sm, r.Scheme)
	})
	if meta.IsNoMatchError(err) {
		return nil // CRD uninstalled between startup and now
	}
	return err
}

// deleteServiceMonitor removes the ServiceMonitor if present, tolerating both the
// object already being gone and the CRD being absent, so clusters without
// prometheus-operator never error here.
func (r *GitHubMetricsExporterReconciler) deleteServiceMonitor(ctx context.Context, cr *scmv1alpha1.GitHubMetricsExporter) error {
	sm := newServiceMonitor()
	sm.SetName(cr.Name)
	sm.SetNamespace(cr.Namespace)
	if err := r.Delete(ctx, sm); err != nil {
		if meta.IsNoMatchError(err) {
			return nil
		}
		return client.IgnoreNotFound(err)
	}
	return nil
}

// serviceMonitorInstalled reports whether monitoring.coreos.com/v1 ServiceMonitor is
// served by the API server. A discovery/transport failure returns an error rather than
// assuming absent.
func serviceMonitorInstalled(mapper meta.RESTMapper) (bool, error) {
	_, err := mapper.RESTMapping(serviceMonitorGVK.GroupKind(), serviceMonitorGVK.Version)
	switch {
	case err == nil:
		return true, nil
	case meta.IsNoMatchError(err):
		return false, nil
	default:
		return false, err
	}
}

// SetupWithManager registers the reconciler and the child objects it owns. The
// ServiceMonitor watch is added only when the prometheus-operator CRD is present at
// startup, since a watch cannot be added to a running controller.
func (r *GitHubMetricsExporterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	available, err := serviceMonitorInstalled(mgr.GetRESTMapper())
	if err != nil {
		ctrl.Log.WithName("setup").Error(err, "could not determine ServiceMonitor CRD availability; assuming absent")
		available = false
	}
	r.serviceMonitorAvailable = available

	b := ctrl.NewControllerManagedBy(mgr).
		For(&scmv1alpha1.GitHubMetricsExporter{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{})
	if available {
		// OnlyMetadata: the owner-ref watch needs metadata only, and it avoids the cache
		// deep-decoding a type the scheme does not know.
		b = b.Owns(newServiceMonitor(), builder.OnlyMetadata)
	}
	return b.Named("githubmetricsexporter").Complete(r)
}

func setReady(cr *scmv1alpha1.GitHubMetricsExporter, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
		Type:               conditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: cr.Generation,
	})
}

func deploymentAvailable(dep *appsv1.Deployment) bool {
	for _, c := range dep.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
