// Command exporter runs the scm-metrics-exporter: it polls a source-control
// provider for open review items and security findings and exposes them as
// OpenTelemetry metrics, with the exporter (Prometheus or OTLP) selected at runtime
// via OTEL_METRICS_EXPORTER.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"

	"github.com/jalet/scm-metrics-exporter/internal/collector"
	"github.com/jalet/scm-metrics-exporter/internal/config"
	"github.com/jalet/scm-metrics-exporter/internal/lifecycle"
	"github.com/jalet/scm-metrics-exporter/internal/metrics"
	"github.com/jalet/scm-metrics-exporter/internal/provider"
	providergithub "github.com/jalet/scm-metrics-exporter/internal/provider/github"
	providergitlab "github.com/jalet/scm-metrics-exporter/internal/provider/gitlab"
	"github.com/jalet/scm-metrics-exporter/internal/store"
)

// Build metadata, populated via -ldflags "-X main.version=...".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	providerName := flag.String("provider", "github", "source-control provider to poll (github|gitlab)")
	once := flag.Bool("once", false, "collect a single snapshot, push it via OTLP, and exit (run-once mode)")
	repo := flag.String("repo", "", "with --once, collect only this repository (bare name; the owner is the configured target)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("scm-metrics-exporter %s (commit %s, built %s)\n", version, commit, date)
		return
	}

	setupLogging()

	if err := run(context.Background(), *providerName, *once, *repo); err != nil {
		zlog.Error().Err(err).Msg("exporter failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, providerName string, once bool, repo string) error {
	cfg, err := config.Load(providerName)
	if err != nil {
		return err
	}

	prov, err := buildProvider(cfg, repo)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	coll := collector.New(collector.Entry{Provider: prov, Target: cfg.Target(), Repo: repo})

	lcStore, connected := openRemediationStore(cfg.Lifecycle)
	if lcStore != nil {
		defer func() { _ = lcStore.Close() }()
	}
	var remediation metrics.RemediationReader
	if lcStore != nil {
		remediation = lcStore
	}

	mp, recordScrapeErr, err := metrics.Setup(ctx, coll, version, metrics.Dimensions{
		Ecosystem: cfg.Dimensions.Ecosystem,
		Tool:      cfg.Dimensions.Tool,
	}, remediation)
	if err != nil {
		return err
	}
	if cfg.Lifecycle.Enabled && !connected {
		// Valkey was configured but unreachable: never fatal to the Job. The remediation
		// histogram is skipped this run, but every other signal still pushes; record one
		// lifecycle scrape error so the gap is visible (scm_exporter_scrape_errors{source="lifecycle"}).
		recordScrapeErr(prov.Name(), provider.SourceLifecycle)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := mp.Shutdown(shutdownCtx); err != nil {
			zlog.Warn().Err(err).Msg("metrics shutdown")
		}
	}()

	zlog.Info().
		Str("provider", prov.Name()).
		Str("target", cfg.Target()).
		Str("repo", repo).
		Bool("once", once).
		Str("exporter", os.Getenv("OTEL_METRICS_EXPORTER")).
		Msg("scm-metrics-exporter starting")

	if once {
		// Collect one snapshot and return; the deferred mp.Shutdown flushes the pending
		// snapshot over OTLP and stops the reader (a single export -- no separate
		// ForceFlush). A hard whole-provider failure returns an error (exit 1); a partial
		// snapshot (recorded scrape errors) returns nil (exit 0).
		perr := coll.PollOnce(ctx, recordScrapeErr)
		if lcStore != nil {
			if snap, ok := coll.Latest(prov.Name()); ok {
				if rerr := lifecycle.Record(ctx, lcStore, prov.Name(), snap, cfg.Lifecycle.ResolutionWindow); rerr != nil {
					zlog.Warn().Err(rerr).Msg("recording resolved findings")
					recordScrapeErr(prov.Name(), provider.SourceLifecycle)
				}
			}
		}
		return perr
	}

	// Blocks until a signal cancels ctx; the periodic OTLP/console reader is stopped by
	// the deferred mp.Shutdown.
	return coll.Run(ctx, cfg.PollInterval, recordScrapeErr)
}

// openRemediationStore attempts to open the lifecycle store when enabled. It returns the
// store (nil when disabled or unreachable) and connected=false when lifecycle is enabled
// but the store could not be reached -- a non-fatal condition the caller records as a
// lifecycle scrape error. It never returns an error for a reachability failure: config.Load
// already rejects a lifecycle-enabled config with no ValkeyAddr as a fatal misconfiguration,
// so by the time this runs, a failure here is purely a runtime connectivity problem.
func openRemediationStore(cfg config.LifecycleConfig) (st *store.Valkey, connected bool) {
	if !cfg.Enabled {
		return nil, false
	}
	s, err := store.NewValkey(store.Options{Addr: cfg.ValkeyAddr, Password: cfg.ValkeyPassword})
	if err != nil {
		zlog.Warn().Err(err).Str("addr", cfg.ValkeyAddr).Msg("lifecycle: Valkey unreachable; remediation histogram disabled for this run")
		return nil, false
	}
	return s, true
}

func buildProvider(cfg config.Config, repo string) (provider.Provider, error) {
	switch cfg.Provider {
	case "github":
		p, err := providergithub.New(providergithub.Options{
			Token:             cfg.GitHub.Token,
			AppID:             cfg.GitHub.AppID,
			AppInstallationID: cfg.GitHub.AppInstallationID,
			AppPrivateKeyPath: cfg.GitHub.AppPrivateKeyPath,
			TargetType:        cfg.GitHub.TargetType,
			RepoScope:         repo,
			CodeScanningTool:  cfg.GitHub.CodeScanningTool,
			CollectWorkflows:  cfg.GitHub.CollectWorkflows,
			WorkflowLookback:  cfg.GitHub.WorkflowLookback,
			CollectLifecycle:  cfg.Lifecycle.Enabled,
			ResolutionWindow:  cfg.Lifecycle.ResolutionWindow,
		})
		if err != nil {
			return nil, err
		}
		return p, nil
	case "gitlab":
		p, err := providergitlab.New(providergitlab.Options{
			Token:            cfg.GitLab.Token,
			TargetType:       cfg.GitLab.TargetType,
			BaseURL:          cfg.GitLab.BaseURL,
			CollectWorkflows: cfg.GitLab.CollectWorkflows,
			WorkflowLookback: cfg.GitLab.WorkflowLookback,
			CollectLifecycle: cfg.Lifecycle.Enabled,
			ResolutionWindow: cfg.Lifecycle.ResolutionWindow,
		})
		if err != nil {
			return nil, err
		}
		return p, nil
	default:
		return nil, fmt.Errorf("unknown provider %q (supported: github, gitlab)", cfg.Provider)
	}
}

func setupLogging() {
	if os.Getenv("LOG_FORMAT") == "console" {
		zlog.Logger = zlog.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	}
	// Default to info: poll summaries and warnings are visible, while the noisier
	// per-page provider progress is opt-in via LOG_LEVEL=debug.
	level := zerolog.InfoLevel
	if raw := os.Getenv("LOG_LEVEL"); raw != "" {
		if lvl, err := zerolog.ParseLevel(raw); err == nil {
			level = lvl
		} else {
			zlog.Warn().Str("LOG_LEVEL", raw).Msg("invalid LOG_LEVEL; using info")
		}
	}
	zerolog.SetGlobalLevel(level)
}
