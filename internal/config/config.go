// Package config loads and validates the exporter's runtime configuration from
// environment variables, per selected provider.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultPollInterval = 5 * time.Minute

// Target types. GitHub polls an organization (default) or a user; GitLab polls a
// group (default) or a user. The type selects which target field is required and
// which one Target() returns.
const (
	TargetOrg   = "org"
	TargetUser  = "user"
	TargetGroup = "group"
)

// Config holds the exporter's runtime settings. Exporter-backend selection
// (OTEL_METRICS_EXPORTER and friends) is read by the OpenTelemetry SDK, not here.
type Config struct {
	Provider     string
	PollInterval time.Duration
	Dimensions   FindingDimensions
	GitHub       GitHubConfig
	GitLab       GitLabConfig
}

// FindingDimensions selects optional labels on the security-findings metric. Off by
// default because they multiply cardinality; enabled via SCM_FINDING_DIMENSIONS
// (a comma-separated list, for example "ecosystem,tool").
type FindingDimensions struct {
	Ecosystem bool
	Tool      bool
}

// GitHubConfig holds GitHub provider settings. TargetType selects org (default) or
// user; exactly the matching field (Org or User) must be set.
type GitHubConfig struct {
	TargetType        string
	Org               string
	User              string
	Token             string
	AppID             int64
	AppInstallationID int64
	AppPrivateKeyPath string
	CodeScanningTool  string
}

// GitLabConfig holds GitLab provider settings. TargetType selects group (default) or
// user; exactly the matching field (Group or User) must be set.
type GitLabConfig struct {
	TargetType string
	Group      string
	User       string
	Token      string
	BaseURL    string
}

// Load reads and validates the configuration for the given provider. It fails fast
// with an actionable error when required values are missing or malformed.
func Load(providerName string) (Config, error) {
	cfg := Config{Provider: providerName}

	var err error
	if cfg.PollInterval, err = getenvDuration("POLL_INTERVAL", defaultPollInterval); err != nil {
		return Config{}, err
	}
	cfg.Dimensions = parseDimensions(os.Getenv("SCM_FINDING_DIMENSIONS"))

	switch providerName {
	case "github":
		cfg.GitHub = GitHubConfig{
			TargetType:        getenvDefault("GITHUB_TARGET_TYPE", TargetOrg),
			Org:               os.Getenv("GITHUB_ORG"),
			User:              os.Getenv("GITHUB_USER"),
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
			TargetType: getenvDefault("GITLAB_TARGET_TYPE", TargetGroup),
			Group:      os.Getenv("GITLAB_GROUP"),
			User:       os.Getenv("GITLAB_USER"),
			Token:      os.Getenv("GITLAB_TOKEN"),
			BaseURL:    os.Getenv("GITLAB_URL"),
		}
		if err := cfg.GitLab.validate(); err != nil {
			return Config{}, err
		}
	default:
		return Config{}, fmt.Errorf("config: unknown provider %q (supported: github, gitlab)", providerName)
	}
	return cfg, nil
}

// Target returns the poll target (organization, group, or user login) for the selected
// provider and target type.
func (c Config) Target() string {
	switch c.Provider {
	case "gitlab":
		if c.GitLab.TargetType == TargetUser {
			return c.GitLab.User
		}
		return c.GitLab.Group
	default:
		if c.GitHub.TargetType == TargetUser {
			return c.GitHub.User
		}
		return c.GitHub.Org
	}
}

// validate enforces the target field for the chosen type plus exactly one usable auth
// method (App trio takes precedence over a PAT).
func (c GitHubConfig) validate() error {
	switch c.TargetType {
	case TargetOrg:
		if c.Org == "" {
			return errors.New("config: GITHUB_ORG is required (GITHUB_TARGET_TYPE=org)")
		}
	case TargetUser:
		if c.User == "" {
			return errors.New("config: GITHUB_USER is required (GITHUB_TARGET_TYPE=user)")
		}
	default:
		return fmt.Errorf("config: invalid GITHUB_TARGET_TYPE %q (want org or user)", c.TargetType)
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
	switch c.TargetType {
	case TargetGroup:
		if c.Group == "" {
			return errors.New("config: GITLAB_GROUP is required (GITLAB_TARGET_TYPE=group)")
		}
	case TargetUser:
		if c.User == "" {
			return errors.New("config: GITLAB_USER is required (GITLAB_TARGET_TYPE=user)")
		}
	default:
		return fmt.Errorf("config: invalid GITLAB_TARGET_TYPE %q (want group or user)", c.TargetType)
	}
	if c.Token == "" {
		return errors.New("config: GITLAB_TOKEN is required")
	}
	return nil
}

// parseDimensions reads a comma-separated dimension list (for example "ecosystem,tool").
// Whitespace is trimmed and unknown entries are ignored.
func parseDimensions(raw string) FindingDimensions {
	var d FindingDimensions
	for _, part := range strings.Split(raw, ",") {
		switch strings.TrimSpace(part) {
		case "ecosystem":
			d.Ecosystem = true
		case "tool":
			d.Tool = true
		}
	}
	return d
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
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
