// Package config loads and validates the exporter's runtime configuration from
// environment variables, per selected provider.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

const defaultPollInterval = 5 * time.Minute

// Config holds the exporter's runtime settings. Exporter-backend selection
// (OTEL_METRICS_EXPORTER and friends) is read by the OpenTelemetry SDK, not here.
type Config struct {
	Provider     string
	PollInterval time.Duration
	GitHub       GitHubConfig
	GitLab       GitLabConfig
}

// GitHubConfig holds GitHub provider settings.
type GitHubConfig struct {
	Org               string
	Token             string
	AppID             int64
	AppInstallationID int64
	AppPrivateKeyPath string
	CodeScanningTool  string
}

// GitLabConfig holds GitLab provider settings.
type GitLabConfig struct {
	Group   string
	Token   string
	BaseURL string
}

// Load reads and validates the configuration for the given provider. It fails fast
// with an actionable error when required values are missing or malformed.
func Load(providerName string) (Config, error) {
	cfg := Config{Provider: providerName}

	var err error
	if cfg.PollInterval, err = getenvDuration("POLL_INTERVAL", defaultPollInterval); err != nil {
		return Config{}, err
	}

	switch providerName {
	case "github":
		cfg.GitHub = GitHubConfig{
			Org:               os.Getenv("GITHUB_ORG"),
			Token:             os.Getenv("GITHUB_TOKEN"),
			AppPrivateKeyPath: os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH"),
			CodeScanningTool:  os.Getenv("GITHUB_CODE_SCANNING_TOOL"),
		}
		if cfg.GitHub.AppID, err = getenvInt64("GITHUB_APP_ID"); err != nil {
			return Config{}, err
		}
		if cfg.GitHub.AppInstallationID, err = getenvInt64("GITHUB_APP_INSTALLATION_ID"); err != nil {
			return Config{}, err
		}
		if err := cfg.GitHub.validate(); err != nil {
			return Config{}, err
		}
	case "gitlab":
		cfg.GitLab = GitLabConfig{
			Group:   os.Getenv("GITLAB_GROUP"),
			Token:   os.Getenv("GITLAB_TOKEN"),
			BaseURL: os.Getenv("GITLAB_URL"),
		}
		if err := cfg.GitLab.validate(); err != nil {
			return Config{}, err
		}
	default:
		return Config{}, fmt.Errorf("config: unknown provider %q (supported: github, gitlab)", providerName)
	}
	return cfg, nil
}

// Target returns the poll target (organization or group) for the selected provider.
func (c Config) Target() string {
	if c.Provider == "gitlab" {
		return c.GitLab.Group
	}
	return c.GitHub.Org
}

// validate enforces GITHUB_ORG plus exactly one usable auth method (App trio takes
// precedence over a PAT).
func (c GitHubConfig) validate() error {
	if c.Org == "" {
		return errors.New("config: GITHUB_ORG is required")
	}
	appComplete := c.AppID != 0 && c.AppInstallationID != 0 && c.AppPrivateKeyPath != ""
	appPartial := !appComplete && (c.AppID != 0 || c.AppInstallationID != 0 || c.AppPrivateKeyPath != "")
	switch {
	case appComplete:
		return nil
	case c.Token != "":
		return nil
	case appPartial:
		return errors.New("config: incomplete GitHub App auth: set all of GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID, and GITHUB_APP_PRIVATE_KEY_PATH (or set GITHUB_TOKEN)")
	default:
		return errors.New("config: no GitHub credentials: set GITHUB_TOKEN or the GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID, and GITHUB_APP_PRIVATE_KEY_PATH trio")
	}
}

func (c GitLabConfig) validate() error {
	if c.Group == "" {
		return errors.New("config: GITLAB_GROUP is required")
	}
	if c.Token == "" {
		return errors.New("config: GITLAB_TOKEN is required")
	}
	return nil
}

func getenvInt64(key string) (int64, error) {
	v := os.Getenv(key)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("config: %s=%q: %w", key, v, err)
	}
	return n, nil
}

func getenvDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s=%q: %w", key, v, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("config: %s=%q must be positive", key, v)
	}
	return d, nil
}
