// Package controller holds the operator reconcilers and the shared rendering of the
// child objects (Deployment, Service, ServiceMonitor) each exporter CR produces. The
// rendering is provider-neutral: exporterDeployment/exporterService/serviceMonitorFor
// build the common shape, and per-provider helpers supply only the env and volumes.
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

// renderInput carries the provider-specific pieces of an exporter Deployment.
type renderInput struct {
	name      string
	namespace string
	image     string
	provider  string // exporter --provider value
	replicas  int32
	resources corev1.ResourceRequirements
	env       []corev1.EnvVar
	volumes   []corev1.Volume
	mounts    []corev1.VolumeMount
}

// exporterDeployment renders the exporter Deployment common to all providers.
func exporterDeployment(in renderInput) *appsv1.Deployment {
	labels := selectorLabels(in.name)
	replicas := in.replicas
	if replicas < 1 {
		replicas = 1
	}
	container := corev1.Container{
		Name:            containerName,
		Image:           in.image,
		Command:         []string{"/exporter"},
		Args:            []string{"--provider=" + in.provider},
		Env:             in.env,
		Ports:           []corev1.ContainerPort{{Name: metricsPortName, ContainerPort: metricsPort}},
		Resources:       in.resources,
		VolumeMounts:    in.mounts,
		SecurityContext: restrictedContainerSecurityContext(),
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: in.name, Namespace: in.namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers:      []corev1.Container{container},
					Volumes:         in.volumes,
					SecurityContext: restrictedPodSecurityContext(),
				},
			},
		},
	}
}

// exporterService renders the metrics Service for an exporter CR (provider-neutral).
func exporterService(name, namespace string) *corev1.Service {
	labels := selectorLabels(name)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
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

// commonExporterEnv returns the OTel + poll-interval env shared by every provider.
func commonExporterEnv(export scmv1alpha1.ExportConfig, poll metav1.Duration) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: "OTEL_METRICS_EXPORTER", Value: exporterBackend(export)},
		{Name: "OTEL_EXPORTER_PROMETHEUS_HOST", Value: "0.0.0.0"},
		{Name: "OTEL_EXPORTER_PROMETHEUS_PORT", Value: strconv.Itoa(metricsPort)},
	}
	if poll.Duration > 0 {
		env = append(env, corev1.EnvVar{Name: "POLL_INTERVAL", Value: poll.Duration.String()})
	}
	if export.Exporter == "otlp" && export.OTLPEndpoint != "" {
		env = append(env, corev1.EnvVar{Name: "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", Value: export.OTLPEndpoint})
	}
	return env
}

func secretKeyRef(ref corev1.LocalObjectReference, key string) *corev1.EnvVarSource {
	return &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: ref, Key: key}}
}

// ----- GitHub -----

func githubDeployment(cr *scmv1alpha1.GitHubMetricsExporter, image string) *appsv1.Deployment {
	volumes, mounts := githubCredentialVolume(cr)
	return exporterDeployment(renderInput{
		name:      cr.Name,
		namespace: cr.Namespace,
		image:     image,
		provider:  "github",
		replicas:  cr.Spec.Replicas,
		resources: cr.Spec.Resources,
		env:       githubEnv(cr),
		volumes:   volumes,
		mounts:    mounts,
	})
}

func githubEnv(cr *scmv1alpha1.GitHubMetricsExporter) []corev1.EnvVar {
	env := append([]corev1.EnvVar{
		{Name: "GITHUB_TARGET_TYPE", Value: cr.Spec.TargetType},
		{Name: "GITHUB_ORG", Value: cr.Spec.Org},
		{Name: "GITHUB_USER", Value: cr.Spec.User},
	}, commonExporterEnv(cr.Spec.Export, cr.Spec.PollInterval)...)
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
		Name:      "GITHUB_TOKEN",
		ValueFrom: secretKeyRef(cr.Spec.CredentialsSecret, cr.Spec.TokenKey),
	})
}

// githubCredentialVolume returns the App private-key Secret volume and mount for app
// auth mode; token mode needs neither.
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

// ----- GitLab -----

func gitlabDeployment(cr *scmv1alpha1.GitLabMetricsExporter, image string) *appsv1.Deployment {
	return exporterDeployment(renderInput{
		name:      cr.Name,
		namespace: cr.Namespace,
		image:     image,
		provider:  "gitlab",
		replicas:  cr.Spec.Replicas,
		resources: cr.Spec.Resources,
		env:       gitlabEnv(cr),
	})
}

func gitlabEnv(cr *scmv1alpha1.GitLabMetricsExporter) []corev1.EnvVar {
	env := append([]corev1.EnvVar{
		{Name: "GITLAB_TARGET_TYPE", Value: cr.Spec.TargetType},
		{Name: "GITLAB_GROUP", Value: cr.Spec.Group},
		{Name: "GITLAB_USER", Value: cr.Spec.User},
	}, commonExporterEnv(cr.Spec.Export, cr.Spec.PollInterval)...)
	if cr.Spec.BaseURL != "" {
		env = append(env, corev1.EnvVar{Name: "GITLAB_URL", Value: cr.Spec.BaseURL})
	}
	return append(env, corev1.EnvVar{
		Name:      "GITLAB_TOKEN",
		ValueFrom: secretKeyRef(cr.Spec.CredentialsSecret, cr.Spec.TokenKey),
	})
}

// ----- ServiceMonitor (provider-neutral) -----

var serviceMonitorGVK = schema.GroupVersionKind{
	Group:   "monitoring.coreos.com",
	Version: "v1",
	Kind:    "ServiceMonitor",
}

func newServiceMonitor() *unstructured.Unstructured {
	sm := &unstructured.Unstructured{}
	sm.SetGroupVersionKind(serviceMonitorGVK)
	return sm
}

// serviceMonitorFor renders the ServiceMonitor for an exporter CR by name; its selector
// matches the exporter Service's labels and it scrapes the named metrics port.
func serviceMonitorFor(name, namespace string) *unstructured.Unstructured {
	labels := selectorLabels(name)
	sm := newServiceMonitor()
	sm.SetName(name)
	sm.SetNamespace(namespace)
	sm.SetLabels(labels)
	sm.Object["spec"] = map[string]any{
		"selector":  map[string]any{"matchLabels": labelsToAny(labels)},
		"endpoints": []any{map[string]any{"port": metricsPortName}},
	}
	return sm
}

func labelsToAny(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
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

func exporterBackend(e scmv1alpha1.ExportConfig) string {
	if e.Exporter != "" {
		return e.Exporter
	}
	return "prometheus"
}
