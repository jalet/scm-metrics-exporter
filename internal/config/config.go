// Package config loads and validates the exporter's runtime configuration from
// environment variables.
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
	GithubOrg         string
	Token             string
	AppID             int64
	AppInstallationID int64
	AppPrivateKeyPath string
	CodeScanningTool  string
	PollInterval      time.Duration
}

// Load reads the configuration from the environment and validates it. It fails fast
// with an actionable error when required values are missing or malformed.
func Load() (Config, error) {
	cfg := Config{
		GithubOrg:         os.Getenv("GITHUB_ORG"),
		Token:             os.Getenv("GITHUB_TOKEN"),
		AppPrivateKeyPath: os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH"),
		CodeScanningTool:  os.Getenv("GITHUB_CODE_SCANNING_TOOL"),
	}

	var err error
	if cfg.AppID, err = getenvInt64("GITHUB_APP_ID"); err != nil {
		return Config{}, err
	}
	if cfg.AppInstallationID, err = getenvInt64("GITHUB_APP_INSTALLATION_ID"); err != nil {
		return Config{}, err
	}
	if cfg.PollInterval, err = getenvDuration("POLL_INTERVAL", defaultPollInterval); err != nil {
		return Config{}, err
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// validate enforces that GITHUB_ORG is set and exactly one usable auth method is
// configured. GitHub App auth (the complete trio) takes precedence over a PAT.
func (c Config) validate() error {
	if c.GithubOrg == "" {
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
