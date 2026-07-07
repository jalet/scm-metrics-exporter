package config

import (
	"testing"
	"time"
)

// envKeys is every variable Load reads; setEnv clears them all first so a test is
// not influenced by the developer's shell environment.
var envKeys = []string{
	"GITHUB_ORG",
	"GITHUB_TOKEN",
	"GITHUB_APP_ID",
	"GITHUB_APP_INSTALLATION_ID",
	"GITHUB_APP_PRIVATE_KEY_PATH",
	"GITHUB_CODE_SCANNING_TOOL",
	"POLL_INTERVAL",
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
		name    string
		env     map[string]string
		wantErr bool
		check   func(t *testing.T, c Config)
	}{
		{
			name: "pat auth with default interval",
			env:  map[string]string{"GITHUB_ORG": "acme", "GITHUB_TOKEN": "ghp_x"},
			check: func(t *testing.T, c Config) {
				if c.PollInterval != 5*time.Minute {
					t.Errorf("PollInterval = %s, want 5m default", c.PollInterval)
				}
			},
		},
		{
			name: "app auth complete",
			env: map[string]string{
				"GITHUB_ORG":                  "acme",
				"GITHUB_APP_ID":               "12",
				"GITHUB_APP_INSTALLATION_ID":  "34",
				"GITHUB_APP_PRIVATE_KEY_PATH": "/etc/scm/app.pem",
			},
			check: func(t *testing.T, c Config) {
				if c.AppID != 12 || c.AppInstallationID != 34 {
					t.Errorf("app ids = %d/%d, want 12/34", c.AppID, c.AppInstallationID)
				}
			},
		},
		{
			name: "both auth methods set is allowed",
			env: map[string]string{
				"GITHUB_ORG": "acme", "GITHUB_TOKEN": "ghp_x",
				"GITHUB_APP_ID": "1", "GITHUB_APP_INSTALLATION_ID": "2", "GITHUB_APP_PRIVATE_KEY_PATH": "/k.pem",
			},
		},
		{
			name: "custom poll interval",
			env:  map[string]string{"GITHUB_ORG": "acme", "GITHUB_TOKEN": "ghp_x", "POLL_INTERVAL": "30s"},
			check: func(t *testing.T, c Config) {
				if c.PollInterval != 30*time.Second {
					t.Errorf("PollInterval = %s, want 30s", c.PollInterval)
				}
			},
		},
		{name: "missing org", env: map[string]string{"GITHUB_TOKEN": "ghp_x"}, wantErr: true},
		{name: "no credentials", env: map[string]string{"GITHUB_ORG": "acme"}, wantErr: true},
		{
			name:    "partial app auth",
			env:     map[string]string{"GITHUB_ORG": "acme", "GITHUB_APP_ID": "12"},
			wantErr: true,
		},
		{
			name:    "bad app id",
			env:     map[string]string{"GITHUB_ORG": "acme", "GITHUB_TOKEN": "ghp_x", "GITHUB_APP_ID": "notnumeric"},
			wantErr: true,
		},
		{
			name:    "bad poll interval",
			env:     map[string]string{"GITHUB_ORG": "acme", "GITHUB_TOKEN": "ghp_x", "POLL_INTERVAL": "soon"},
			wantErr: true,
		},
		{
			name:    "non-positive poll interval",
			env:     map[string]string{"GITHUB_ORG": "acme", "GITHUB_TOKEN": "ghp_x", "POLL_INTERVAL": "0s"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.env)
			cfg, err := Load()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v, want nil", err)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}
