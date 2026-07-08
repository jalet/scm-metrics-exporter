package controller

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	scmv1alpha1 "github.com/jalet/scm-metrics-exporter/api/v1alpha1"
)

// GitLabMetricsExporterReconciler reconciles a GitLabMetricsExporter. GitLab collection is
// deferred in the discovery/dispatch model: the GitLab provider has no single-repo
// collection path yet, so this reconciler dispatches nothing and surfaces the deferral in
// the CR status rather than creating Jobs that would fail at runtime.
type GitLabMetricsExporterReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	ExporterImage string
}

// +kubebuilder:rbac:groups=scm.jalet.io,resources=gitlabmetricsexporters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=scm.jalet.io,resources=gitlabmetricsexporters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=scm.jalet.io,resources=gitlabmetricsexporters/finalizers,verbs=update

// Reconcile records that GitLab collection is not yet implemented in this model.
func (r *GitLabMetricsExporterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var cr scmv1alpha1.GitLabMetricsExporter
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	setReadyCondition(&cr.Status.Conditions, cr.Generation, metav1.ConditionFalse, reasonUnsupported,
		"GitLab collection is not yet implemented in the discovery/dispatch model (deferred)")
	cr.Status.ObservedGeneration = cr.Generation
	if err := r.Status().Update(ctx, &cr); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}
	return ctrl.Result{}, nil
}

// SetupWithManager registers the reconciler.
func (r *GitLabMetricsExporterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&scmv1alpha1.GitLabMetricsExporter{}).
		Named("gitlabmetricsexporter").
		Complete(r)
}
