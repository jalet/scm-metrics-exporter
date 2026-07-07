// Package controller holds the operator reconcilers and the shared rendering of the
// child objects (Deployment, Service, ServiceMonitor) each exporter CR produces.
package controller

import (
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	scmv1alpha1 "github.com/jalet/scm-metrics-exporter/api/v1alpha1"
)

// serviceMonitorGVK identifies the Prometheus Operator ServiceMonitor kind. It is a
// soft dependency: the operator only renders ServiceMonitors when this kind is served
// by the API server. ServiceMonitors are built as unstructured objects so the module
// takes no dependency on the prometheus-operator API types.
var serviceMonitorGVK = schema.GroupVersionKind{
	Group:   "monitoring.coreos.com",
	Version: "v1",
	Kind:    "ServiceMonitor",
}

// newServiceMonitor returns an empty ServiceMonitor shell with only its GVK set. The
// GVK must be present before any client call so the client can resolve the REST mapping.
func newServiceMonitor() *unstructured.Unstructured {
	sm := &unstructured.Unstructured{}
	sm.SetGroupVersionKind(serviceMonitorGVK)
	return sm
}

// githubServiceMonitor renders the desired ServiceMonitor for a GitHub CR. Its selector
// matches the exporter Service's labels and it scrapes the named metrics port; the zero
// namespaceSelector limits scraping to the CR's own namespace.
func githubServiceMonitor(cr *scmv1alpha1.GitHubMetricsExporter) *unstructured.Unstructured {
	labels := selectorLabels(cr.Name)
	sm := newServiceMonitor()
	sm.SetName(cr.Name)
	sm.SetNamespace(cr.Namespace)
	sm.SetLabels(labels)
	sm.Object["spec"] = map[string]any{
		"selector": map[string]any{
			"matchLabels": labelsToAny(labels),
		},
		"endpoints": []any{
			map[string]any{"port": metricsPortName},
		},
	}
	return sm
}

// labelsToAny converts a label map to the map[string]any form apimachinery requires
// inside unstructured objects.
func labelsToAny(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

const (
	metricsPort     = 9464
	metricsPortName = "metrics"
	containerName   = "exporter"
	appKeyVolume    = "github-app-key"
	appPEMMountPath = "/etc/scm/github-app"
	appPEMFileName  = "app.pem"
	runAsUser       = int64(65532)
)

func selectorLabels(instance string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "scm-metrics-exporter",
		"app.kubernetes.io/instance":   instance,
		"app.kubernetes.io/managed-by": "scm-metrics-operator",
	}
}

// githubDeployment renders the desired exporter Deployment for a GitHub CR. image is
// the resolved exporter image (spec override or the operator's default).
func githubDeployment(cr *scmv1alpha1.GitHubMetricsExporter, image string) *appsv1.Deployment {
	labels := selectorLabels(cr.Name)
	volumes, mounts := githubCredentialVolume(cr)

	replicas := cr.Spec.Replicas
	if replicas < 1 {
		replicas = 1
	}

	container := corev1.Container{
		Name:            containerName,
		Image:           image,
		Command:         []string{"/exporter"},
		Args:            []string{"--provider=github"},
		Env:             githubEnv(cr),
		Ports:           []corev1.ContainerPort{{Name: metricsPortName, ContainerPort: metricsPort}},
		Resources:       cr.Spec.Resources,
		VolumeMounts:    mounts,
		SecurityContext: restrictedContainerSecurityContext(),
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: cr.Name, Namespace: cr.Namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers:      []corev1.Container{container},
					Volumes:         volumes,
					SecurityContext: restrictedPodSecurityContext(),
				},
			},
		},
	}
}

// githubService renders the metrics Service for a GitHub CR.
func githubService(cr *scmv1alpha1.GitHubMetricsExporter) *corev1.Service {
	labels := selectorLabels(cr.Name)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: cr.Name, Namespace: cr.Namespace, Labels: labels},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{{
				Name:       metricsPortName,
				Port:       metricsPort,
				TargetPort: intstr.FromString(metricsPortName),
			}},
		},
	}
}

func githubEnv(cr *scmv1alpha1.GitHubMetricsExporter) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: "GITHUB_ORG", Value: cr.Spec.Org},
		{Name: "OTEL_METRICS_EXPORTER", Value: exporterBackend(cr.Spec.Export)},
		{Name: "OTEL_EXPORTER_PROMETHEUS_HOST", Value: "0.0.0.0"},
		{Name: "OTEL_EXPORTER_PROMETHEUS_PORT", Value: strconv.Itoa(metricsPort)},
	}
	if pi := cr.Spec.PollInterval.Duration; pi > 0 {
		env = append(env, corev1.EnvVar{Name: "POLL_INTERVAL", Value: pi.String()})
	}
	if cr.Spec.Export.Exporter == "otlp" && cr.Spec.Export.OTLPEndpoint != "" {
		env = append(env, corev1.EnvVar{Name: "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", Value: cr.Spec.Export.OTLPEndpoint})
	}
	if cr.Spec.CodeScanningTool != "" {
		env = append(env, corev1.EnvVar{Name: "GITHUB_CODE_SCANNING_TOOL", Value: cr.Spec.CodeScanningTool})
	}

	if cr.Spec.AuthMode == "app" {
		return append(env,
			corev1.EnvVar{Name: "GITHUB_APP_ID", Value: strconv.FormatInt(cr.Spec.AppID, 10)},
			corev1.EnvVar{Name: "GITHUB_APP_INSTALLATION_ID", Value: strconv.FormatInt(cr.Spec.AppInstallationID, 10)},
			corev1.EnvVar{Name: "GITHUB_APP_PRIVATE_KEY_PATH", Value: appPEMMountPath + "/" + appPEMFileName},
		)
	}
	return append(env, corev1.EnvVar{
		Name: "GITHUB_TOKEN",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: cr.Spec.CredentialsSecret,
				Key:                  cr.Spec.TokenKey,
			},
		},
	})
}

// githubCredentialVolume returns the App private-key Secret volume and mount for
// app auth mode; token mode needs neither (it uses a secretKeyRef env var).
func githubCredentialVolume(cr *scmv1alpha1.GitHubMetricsExporter) ([]corev1.Volume, []corev1.VolumeMount) {
	if cr.Spec.AuthMode != "app" {
		return nil, nil
	}
	volume := corev1.Volume{
		Name: appKeyVolume,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName:  cr.Spec.CredentialsSecret.Name,
				Items:       []corev1.KeyToPath{{Key: cr.Spec.AppPrivateKeyKey, Path: appPEMFileName}},
				DefaultMode: ptr.To(int32(0o400)),
			},
		},
	}
	mount := corev1.VolumeMount{Name: appKeyVolume, MountPath: appPEMMountPath, ReadOnly: true}
	return []corev1.Volume{volume}, []corev1.VolumeMount{mount}
}

func exporterBackend(e scmv1alpha1.ExportConfig) string {
	if e.Exporter != "" {
		return e.Exporter
	}
	return "prometheus"
}

func restrictedContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		ReadOnlyRootFilesystem:   ptr.To(true),
		RunAsNonRoot:             ptr.To(true),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

func restrictedPodSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr.To(true),
		RunAsUser:      ptr.To(runAsUser),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}
