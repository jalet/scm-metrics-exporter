package controller

import (
	"context"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	conditionReady          = "Ready"
	reasonReconciled        = "DeploymentAvailable"
	reasonProgressing       = "DeploymentProgressing"
	reasonCredentialInvalid = "CredentialsInvalid"
	credentialRequeue       = time.Minute
)

// setReadyCondition sets the shared Ready condition on a CR's status conditions.
func setReadyCondition(conds *[]metav1.Condition, generation int64, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(conds, metav1.Condition{
		Type:               conditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
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

// reconcileServiceMonitor creates/updates the owned ServiceMonitor when it is both
// wanted (spec.serviceMonitor) and possible (CRD installed at startup); otherwise it
// ensures none exists. It never errors when the prometheus-operator CRD is absent.
func reconcileServiceMonitor(ctx context.Context, c client.Client, scheme *runtime.Scheme, owner client.Object, available, wanted bool) error {
	name, ns := owner.GetName(), owner.GetNamespace()
	if available && wanted {
		return applyServiceMonitor(ctx, c, scheme, owner)
	}
	return deleteServiceMonitor(ctx, c, name, ns)
}

func applyServiceMonitor(ctx context.Context, c client.Client, scheme *runtime.Scheme, owner client.Object) error {
	sm := newServiceMonitor()
	sm.SetName(owner.GetName())
	sm.SetNamespace(owner.GetNamespace())
	_, err := controllerutil.CreateOrUpdate(ctx, c, sm, func() error {
		desired := serviceMonitorFor(owner.GetName(), owner.GetNamespace())
		sm.SetLabels(desired.GetLabels())
		sm.Object["spec"] = desired.Object["spec"]
		return controllerutil.SetControllerReference(owner, sm, scheme)
	})
	if meta.IsNoMatchError(err) {
		return nil // CRD uninstalled between startup and now
	}
	return err
}

func deleteServiceMonitor(ctx context.Context, c client.Client, name, ns string) error {
	sm := newServiceMonitor()
	sm.SetName(name)
	sm.SetNamespace(ns)
	if err := c.Delete(ctx, sm); err != nil {
		if meta.IsNoMatchError(err) {
			return nil
		}
		return client.IgnoreNotFound(err)
	}
	return nil
}
