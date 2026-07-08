package v1alpha1

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// fakeReader returns a client.Reader holding the given Secret in namespace "ns". A nil
// data map means the Secret exists but is empty; an empty name means no Secret at all.
func fakeReader(t *testing.T, ns, name string, data map[string][]byte) client.Reader {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	b := fake.NewClientBuilder().WithScheme(scheme)
	if name != "" {
		b = b.WithObjects(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Data:       data,
		})
	}
	return b.Build()
}

func TestGitHubValidator(t *testing.T) {
	cr := func(authMode, tokenKey, appKey string) *GitHubMetricsExporter {
		return &GitHubMetricsExporter{
			ObjectMeta: metav1.ObjectMeta{Name: "gh", Namespace: "ns"},
			Spec: GitHubMetricsExporterSpec{
				ExporterSpec:     ExporterSpec{CredentialsSecret: corev1.LocalObjectReference{Name: "creds"}},
				Org:              "acme",
				AuthMode:         authMode,
				TokenKey:         tokenKey,
				AppPrivateKeyKey: appKey,
			},
		}
	}
	tests := []struct {
		name       string
		cr         *GitHubMetricsExporter
		secretName string
		secretData map[string][]byte
		wantErr    bool
	}{
		{"token ok", cr("token", "token", ""), "creds", map[string][]byte{"token": []byte("x")}, false},
		{"token secret missing", cr("token", "token", ""), "", nil, true},
		{"token key missing", cr("token", "token", ""), "creds", map[string][]byte{"other": []byte("x")}, true},
		{"app ok", cr("app", "", "app.pem"), "creds", map[string][]byte{"app.pem": []byte("x")}, false},
		{"app key missing", cr("app", "", "app.pem"), "creds", map[string][]byte{"token": []byte("x")}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &gitHubValidator{reader: fakeReader(t, "ns", tt.secretName, tt.secretData)}
			_, err := v.ValidateCreate(context.Background(), tt.cr)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCreate error = %v, wantErr %v", err, tt.wantErr)
			}
			// ValidateUpdate must behave identically to ValidateCreate.
			if _, uerr := v.ValidateUpdate(context.Background(), nil, tt.cr); (uerr != nil) != tt.wantErr {
				t.Errorf("ValidateUpdate error = %v, wantErr %v", uerr, tt.wantErr)
			}
		})
	}
}

func TestGitLabValidator(t *testing.T) {
	cr := &GitLabMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: "gl", Namespace: "ns"},
		Spec: GitLabMetricsExporterSpec{
			ExporterSpec: ExporterSpec{CredentialsSecret: corev1.LocalObjectReference{Name: "creds"}},
			Group:        "acme",
			TokenKey:     "token",
		},
	}
	tests := []struct {
		name       string
		secretName string
		secretData map[string][]byte
		wantErr    bool
	}{
		{"ok", "creds", map[string][]byte{"token": []byte("x")}, false},
		{"secret missing", "", nil, true},
		{"key missing", "creds", map[string][]byte{"other": []byte("x")}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &gitLabValidator{reader: fakeReader(t, "ns", tt.secretName, tt.secretData)}
			if _, err := v.ValidateCreate(context.Background(), cr); (err != nil) != tt.wantErr {
				t.Errorf("ValidateCreate error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
