// Package controller holds the operator reconcilers and the shared rendering of the
// per-repository collection Jobs each exporter CR produces. The rendering is
// provider-neutral: collectionJob builds the common Job shape, and per-provider helpers
// supply only the env and volumes.
package controller

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	scmv1alpha1 "github.com/jalet/scm-metrics-exporter/api/v1alpha1"
)

const (
	containerName   = "exporter"
	appKeyVolume    = "github-app-key"
	appPEMMountPath = "/etc/scm/github-app"
	appPEMFileName  = "app.pem"
	runAsUser       = int64(65532)

	// jobBackoffLimit bounds retries of a failed collection Job; jobTTLSeconds auto-deletes
	// a finished Job so the next discovery cycle recreates it (metrics are pushed once per
	// run, so a lingering completed Job serves no purpose).
	jobBackoffLimit = int32(2)
	jobTTLSeconds   = int32(600)
)

func selectorLabels(instance string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "scm-metrics-exporter",
		"app.kubernetes.io/instance":   instance,
		"app.kubernetes.io/managed-by": "scm-metrics-operator",
	}
}

// jobName is the deterministic Job name for one (CR, repo) pair: a sanitized, length-bounded
// prefix plus a hash of the repo, so it is a valid DNS-1123 name, unique per repo, and
// stable across reconciles (idempotent create, one Job per repo per discovery cycle).
func jobName(crName, repo string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(repo))
	suffix := fmt.Sprintf("%08x", h.Sum32())
	const maxBase = 63 - 9 // leave room for "-" + 8 hex
	base := sanitizeName(crName + "-" + repo)
	if len(base) > maxBase {
		base = base[:maxBase]
	}
	return base + "-" + suffix
}

func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "repo"
	}
	return out
}

// renderInput carries the provider-specific pieces of a per-repo collection Job.
type renderInput struct {
	crName    string
	namespace string
	repo      string // bare repository name (the collection target)
	image     string
	provider  string // exporter --provider value
	resources corev1.ResourceRequirements
	env       []corev1.EnvVar
	volumes   []corev1.Volume
	mounts    []corev1.VolumeMount
}

// collectionJob renders the run-once, single-repo collection Job common to all providers.
func collectionJob(in renderInput) *batchv1.Job {
	labels := selectorLabels(in.crName)
	container := corev1.Container{
		Name:            containerName,
		Image:           in.image,
		Command:         []string{"/exporter"},
		Args:            []string{"--provider=" + in.provider, "--once", "--repo=" + in.repo},
		Env:             in.env,
		Resources:       in.resources,
		VolumeMounts:    in.mounts,
		SecurityContext: restrictedContainerSecurityContext(),
	}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName(in.crName, in.repo), Namespace: in.namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To(jobBackoffLimit),
			TTLSecondsAfterFinished: ptr.To(jobTTLSeconds),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:   corev1.RestartPolicyNever,
					Containers:      []corev1.Container{container},
					Volumes:         in.volumes,
					SecurityContext: restrictedPodSecurityContext(),
				},
			},
		},
	}
}

// commonExporterEnv returns the OTLP and finding-dimension env shared by every provider.
// Collection is OTLP-only (ephemeral Jobs cannot be scraped).
func commonExporterEnv(spec scmv1alpha1.ExporterSpec) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: "OTEL_METRICS_EXPORTER", Value: "otlp"},
		{Name: "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", Value: spec.Export.OTLPEndpoint},
	}
	if len(spec.FindingDimensions) > 0 {
		env = append(env, corev1.EnvVar{Name: "SCM_FINDING_DIMENSIONS", Value: strings.Join(spec.FindingDimensions, ",")})
	}
	return env
}

func secretKeyRef(ref corev1.LocalObjectReference, key string) *corev1.EnvVarSource {
	return &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: ref, Key: key}}
}

// ----- GitHub -----

func githubJob(cr *scmv1alpha1.GitHubMetricsExporter, image, repo string) *batchv1.Job {
	volumes, mounts := githubCredentialVolume(cr)
	return collectionJob(renderInput{
		crName:    cr.Name,
		namespace: cr.Namespace,
		repo:      repo,
		image:     image,
		provider:  "github",
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
	}, commonExporterEnv(cr.Spec.ExporterSpec)...)
	if cr.Spec.CodeScanningTool != "" {
		env = append(env, corev1.EnvVar{Name: "GITHUB_CODE_SCANNING_TOOL", Value: cr.Spec.CodeScanningTool})
	}
	if cr.Spec.CollectWorkflows {
		env = append(env,
			corev1.EnvVar{Name: "GITHUB_COLLECT_WORKFLOWS", Value: "true"},
			corev1.EnvVar{Name: "GITHUB_WORKFLOW_LOOKBACK", Value: cr.Spec.WorkflowLookback.Duration.String()},
		)
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

func gitlabJob(cr *scmv1alpha1.GitLabMetricsExporter, image, repo string) *batchv1.Job {
	return collectionJob(renderInput{
		crName:    cr.Name,
		namespace: cr.Namespace,
		repo:      repo,
		image:     image,
		provider:  "gitlab",
		resources: cr.Spec.Resources,
		env:       gitlabEnv(cr),
	})
}

func gitlabEnv(cr *scmv1alpha1.GitLabMetricsExporter) []corev1.EnvVar {
	env := append([]corev1.EnvVar{
		{Name: "GITLAB_TARGET_TYPE", Value: cr.Spec.TargetType},
		{Name: "GITLAB_GROUP", Value: cr.Spec.Group},
		{Name: "GITLAB_USER", Value: cr.Spec.User},
	}, commonExporterEnv(cr.Spec.ExporterSpec)...)
	if cr.Spec.BaseURL != "" {
		env = append(env, corev1.EnvVar{Name: "GITLAB_URL", Value: cr.Spec.BaseURL})
	}
	if cr.Spec.CollectWorkflows {
		env = append(env,
			corev1.EnvVar{Name: "GITLAB_COLLECT_WORKFLOWS", Value: "true"},
			corev1.EnvVar{Name: "GITLAB_WORKFLOW_LOOKBACK", Value: cr.Spec.WorkflowLookback.Duration.String()},
		)
	}
	return append(env, corev1.EnvVar{
		Name:      "GITLAB_TOKEN",
		ValueFrom: secretKeyRef(cr.Spec.CredentialsSecret, cr.Spec.TokenKey),
	})
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
