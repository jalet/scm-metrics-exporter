package config

import (
	"testing"
	"time"
)

var envKeys = []string{
	"GITHUB_TARGET_TYPE", "GITHUB_ORG", "GITHUB_USER", "GITHUB_TOKEN", "GITHUB_APP_ID",
	"GITHUB_APP_INSTALLATION_ID", "GITHUB_APP_PRIVATE_KEY_PATH", "GITHUB_CODE_SCANNING_TOOL",
	"POLL_INTERVAL", "SCM_FINDING_DIMENSIONS",
	"GITLAB_TARGET_TYPE", "GITLAB_GROUP", "GITLAB_USER", "GITLAB_TOKEN", "GITLAB_URL",
}

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range envKeys {
		t.Setenv(k, "")
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		env      map[string]string
		wantErr  bool
		check    func(t *testing.T, c Config)
	}{
		{
			name: "github pat with default interval", provider: "github",
			env: map[string]string{"GITHUB_ORG": "acme", "GITHUB_TOKEN": "ghp_x"},
			check: func(t *testing.T, c Config) {
				if c.PollInterval != 5*time.Minute {
					t.Errorf("PollInterval = %s, want 5m", c.PollInterval)
				}
				if c.Target() != "acme" {
					t.Errorf("Target() = %q, want acme", c.Target())
				}
			},
		},
		{
			name: "github app complete", provider: "github",
			env: map[string]string{
				"GITHUB_ORG": "acme", "GITHUB_APP_ID": "12",
				"GITHUB_APP_INSTALLATION_ID": "34", "GITHUB_APP_PRIVATE_KEY_PATH": "/k.pem",
			},
			check: func(t *testing.T, c Config) {
				if c.GitHub.AppID != 12 || c.GitHub.AppInstallationID != 34 {
					t.Errorf("app ids = %d/%d, want 12/34", c.GitHub.AppID, c.GitHub.AppInstallationID)
				}
			},
		},
		{
			name: "github custom interval", provider: "github",
			env: map[string]string{"GITHUB_ORG": "acme", "GITHUB_TOKEN": "ghp_x", "POLL_INTERVAL": "30s"},
			check: func(t *testing.T, c Config) {
				if c.PollInterval != 30*time.Second {
					t.Errorf("PollInterval = %s, want 30s", c.PollInterval)
				}
			},
		},
		{
			name: "github user target", provider: "github",
			env: map[string]string{"GITHUB_TARGET_TYPE": "user", "GITHUB_USER": "octocat", "GITHUB_TOKEN": "ghp_x"},
			check: func(t *testing.T, c Config) {
				if c.Target() != "octocat" {
					t.Errorf("Target() = %q, want octocat", c.Target())
				}
				if c.GitHub.TargetType != TargetUser {
					t.Errorf("TargetType = %q, want user", c.GitHub.TargetType)
				}
			},
		},
		{
			name: "finding dimensions", provider: "github",
			env: map[string]string{"GITHUB_ORG": "acme", "GITHUB_TOKEN": "ghp_x", "SCM_FINDING_DIMENSIONS": "ecosystem, tool"},
			check: func(t *testing.T, c Config) {
				if !c.Dimensions.Ecosystem || !c.Dimensions.Tool {
					t.Errorf("Dimensions = %+v, want both ecosystem and tool", c.Dimensions)
				}
			},
		},
		{
			name: "no finding dimensions by default", provider: "github",
			env: map[string]string{"GITHUB_ORG": "acme", "GITHUB_TOKEN": "ghp_x"},
			check: func(t *testing.T, c Config) {
				if c.Dimensions.Ecosystem || c.Dimensions.Tool {
					t.Errorf("Dimensions = %+v, want both off by default", c.Dimensions)
				}
			},
		},
		{name: "github user missing user", provider: "github", env: map[string]string{"GITHUB_TARGET_TYPE": "user", "GITHUB_TOKEN": "ghp_x"}, wantErr: true},
		{name: "github invalid target type", provider: "github", env: map[string]string{"GITHUB_TARGET_TYPE": "team", "GITHUB_ORG": "acme", "GITHUB_TOKEN": "ghp_x"}, wantErr: true},
		{name: "github missing org", provider: "github", env: map[string]string{"GITHUB_TOKEN": "ghp_x"}, wantErr: true},
		{name: "github no credentials", provider: "github", env: map[string]string{"GITHUB_ORG": "acme"}, wantErr: true},
		{name: "github partial app", provider: "github", env: map[string]string{"GITHUB_ORG": "acme", "GITHUB_APP_ID": "12"}, wantErr: true},
		{name: "github bad app id", provider: "github", env: map[string]string{"GITHUB_ORG": "acme", "GITHUB_TOKEN": "x", "GITHUB_APP_ID": "nope"}, wantErr: true},
		{name: "github bad interval", provider: "github", env: map[string]string{"GITHUB_ORG": "acme", "GITHUB_TOKEN": "x", "POLL_INTERVAL": "soon"}, wantErr: true},
		{name: "github zero interval", provider: "github", env: map[string]string{"GITHUB_ORG": "acme", "GITHUB_TOKEN": "x", "POLL_INTERVAL": "0s"}, wantErr: true},
		{
			name: "gitlab group + token", provider: "gitlab",
			env: map[string]string{"GITLAB_GROUP": "acme", "GITLAB_TOKEN": "glpat", "GITLAB_URL": "https://gitlab.example.com"},
			check: func(t *testing.T, c Config) {
				if c.Target() != "acme" {
					t.Errorf("Target() = %q, want acme", c.Target())
				}
				if c.GitLab.BaseURL != "https://gitlab.example.com" {
					t.Errorf("BaseURL = %q", c.GitLab.BaseURL)
				}
			},
		},
		{
			name: "gitlab user target", provider: "gitlab",
			env: map[string]string{"GITLAB_TARGET_TYPE": "user", "GITLAB_USER": "alice", "GITLAB_TOKEN": "glpat"},
			check: func(t *testing.T, c Config) {
				if c.Target() != "alice" {
					t.Errorf("Target() = %q, want alice", c.Target())
				}
			},
		},
		{name: "gitlab user missing user", provider: "gitlab", env: map[string]string{"GITLAB_TARGET_TYPE": "user", "GITLAB_TOKEN": "glpat"}, wantErr: true},
		{name: "gitlab invalid target type", provider: "gitlab", env: map[string]string{"GITLAB_TARGET_TYPE": "namespace", "GITLAB_GROUP": "acme", "GITLAB_TOKEN": "glpat"}, wantErr: true},
		{name: "gitlab missing group", provider: "gitlab", env: map[string]string{"GITLAB_TOKEN": "glpat"}, wantErr: true},
		{name: "gitlab missing token", provider: "gitlab", env: map[string]string{"GITLAB_GROUP": "acme"}, wantErr: true},
		{name: "unknown provider", provider: "bitbucket", env: map[string]string{}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.env)
			cfg, err := Load(tt.provider)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load(%q) error = nil, want error", tt.provider)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load(%q) error = %v, want nil", tt.provider, err)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func TestLifecycleConfig(t *testing.T) {
	t.Setenv("GITHUB_ORG", "acme")
	t.Setenv("GITHUB_TOKEN", "t")
	t.Setenv("SCM_COLLECT_LIFECYCLE", "true")
	t.Setenv("SCM_RESOLUTION_WINDOW", "720h")
	t.Setenv("VALKEY_ADDR", "valkey:6379")

	cfg, err := Load("github")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Lifecycle.Enabled || cfg.Lifecycle.ResolutionWindow != 720*time.Hour || cfg.Lifecycle.ValkeyAddr != "valkey:6379" {
		t.Fatalf("lifecycle config wrong: %+v", cfg.Lifecycle)
	}
}

func TestLifecycleRequiresValkeyAddr(t *testing.T) {
	t.Setenv("GITHUB_ORG", "acme")
	t.Setenv("GITHUB_TOKEN", "t")
	t.Setenv("SCM_COLLECT_LIFECYCLE", "true")
	// no VALKEY_ADDR
	if _, err := Load("github"); err == nil {
		t.Fatal("expected error when lifecycle enabled without VALKEY_ADDR")
	}
}
