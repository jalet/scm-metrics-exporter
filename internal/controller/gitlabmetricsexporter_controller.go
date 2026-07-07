package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	scmv1alpha1 "github.com/jalet/scm-metrics-exporter/api/v1alpha1"
)

// GitLabMetricsExporterReconciler reconciles a GitLabMetricsExporter into an exporter
// Deployment, Service, and (optionally) ServiceMonitor. It shares the provider-neutral
// rendering and reconcile helpers with the GitHub reconciler.
type GitLabMetricsExporterReconciler struct {
	client.Client
	Scheme                  *runtime.Scheme
	ExporterImage           string
	serviceMonitorAvailable bool
}

// +kubebuilder:rbac:groups=scm.jalet.io,resources=gitlabmetricsexporters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=scm.jalet.io,resources=gitlabmetricsexporters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=scm.jalet.io,resources=gitlabmetricsexporters/finalizers,verbs=update

// Reconcile ensures the exporter children match the CR and reflects readiness in status.
func (r *GitLabMetricsExporterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var cr scmv1alpha1.GitLabMetricsExporter
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.checkCredentials(ctx, &cr); err != nil {
		setReadyCondition(&cr.Status.Conditions, cr.Generation, metav1.ConditionFalse, reasonCredentialInvalid, err.Error())
		return r.updateStatus(ctx, &cr, ctrl.Result{RequeueAfter: credentialRequeue})
	}

	image := cr.Spec.Image
	if image == "" {
		image = r.ExporterImage
	}

	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: cr.Name, Namespace: cr.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, dep, func() error {
		desired := gitlabDeployment(&cr, image)
		dep.Labels = desired.Labels
		dep.Spec = desired.Spec
		return controllerutil.SetControllerReference(&cr, dep, r.Scheme)
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile deployment: %w", err)
	}

	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: cr.Name, Namespace: cr.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		desired := exporterService(cr.Name, cr.Namespace)
		svc.Labels = desired.Labels
		svc.Spec.Selector = desired.Spec.Selector
		svc.Spec.Ports = desired.Spec.Ports
		return controllerutil.SetControllerReference(&cr, svc, r.Scheme)
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile service: %w", err)
	}

	if err := reconcileServiceMonitor(ctx, r.Client, r.Scheme, &cr, r.serviceMonitorAvailable, cr.Spec.ServiceMonitor); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile servicemonitor: %w", err)
	}

	if deploymentAvailable(dep) {
		setReadyCondition(&cr.Status.Conditions, cr.Generation, metav1.ConditionTrue, reasonReconciled, "exporter deployment is available")
	} else {
		setReadyCondition(&cr.Status.Conditions, cr.Generation, metav1.ConditionFalse, reasonProgressing, "waiting for exporter deployment to become available")
	}
	return r.updateStatus(ctx, &cr, ctrl.Result{})
}

func (r *GitLabMetricsExporterReconciler) checkCredentials(ctx context.Context, cr *scmv1alpha1.GitLabMetricsExporter) error {
	var secret corev1.Secret
	name := types.NamespacedName{Name: cr.Spec.CredentialsSecret.Name, Namespace: cr.Namespace}
	if err := r.Get(ctx, name, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("credentials Secret %q not found", cr.Spec.CredentialsSecret.Name)
		}
		return err
	}
	if _, ok := secret.Data[cr.Spec.TokenKey]; !ok {
		return fmt.Errorf("credentials Secret %q is missing key %q", cr.Spec.CredentialsSecret.Name, cr.Spec.TokenKey)
	}
	return nil
}

func (r *GitLabMetricsExporterReconciler) updateStatus(ctx context.Context, cr *scmv1alpha1.GitLabMetricsExporter, result ctrl.Result) (ctrl.Result, error) {
	cr.Status.ObservedGeneration = cr.Generation
	if err := r.Status().Update(ctx, cr); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}
	return result, nil
}

// SetupWithManager registers the reconciler and the child objects it owns.
func (r *GitLabMetricsExporterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	available, err := serviceMonitorInstalled(mgr.GetRESTMapper())
	if err != nil {
		ctrl.Log.WithName("setup").Error(err, "could not determine ServiceMonitor CRD availability; assuming absent")
		available = false
	}
	r.serviceMonitorAvailable = available

	b := ctrl.NewControllerManagedBy(mgr).
		For(&scmv1alpha1.GitLabMetricsExporter{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{})
	if available {
		b = b.Owns(newServiceMonitor(), builder.OnlyMetadata)
	}
	return b.Named("gitlabmetricsexporter").Complete(r)
}
