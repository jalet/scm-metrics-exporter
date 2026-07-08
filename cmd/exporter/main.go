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
	"github.com/jalet/scm-metrics-exporter/internal/metrics"
	"github.com/jalet/scm-metrics-exporter/internal/provider"
	providergithub "github.com/jalet/scm-metrics-exporter/internal/provider/github"
	providergitlab "github.com/jalet/scm-metrics-exporter/internal/provider/gitlab"
)

// Build metadata, populated via -ldflags "-X main.version=...".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	providerName := flag.String("provider", "github", "source-control provider to poll (github|gitlab)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("scm-metrics-exporter %s (commit %s, built %s)\n", version, commit, date)
		return
	}

	setupLogging()

	if err := run(context.Background(), *providerName); err != nil {
		zlog.Error().Err(err).Msg("exporter failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, providerName string) error {
	cfg, err := config.Load(providerName)
	if err != nil {
		return err
	}

	prov, err := buildProvider(cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	coll := collector.New(collector.Entry{Provider: prov, Target: cfg.Target()})

	mp, recordScrapeErr, err := metrics.Setup(ctx, coll, version, metrics.Dimensions{
		Ecosystem: cfg.Dimensions.Ecosystem,
		Tool:      cfg.Dimensions.Tool,
	})
	if err != nil {
		return err
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
		Dur("poll_interval", cfg.PollInterval).
		Str("exporter", os.Getenv("OTEL_METRICS_EXPORTER")).
		Msg("scm-metrics-exporter starting")

	// Blocks until a signal cancels ctx; autoexport owns the Prometheus HTTP server
	// (or the periodic OTLP/console reader), stopped by the deferred mp.Shutdown.
	return coll.Run(ctx, cfg.PollInterval, recordScrapeErr)
}

func buildProvider(cfg config.Config) (provider.Provider, error) {
	switch cfg.Provider {
	case "github":
		p, err := providergithub.New(providergithub.Options{
			Token:             cfg.GitHub.Token,
			AppID:             cfg.GitHub.AppID,
			AppInstallationID: cfg.GitHub.AppInstallationID,
			AppPrivateKeyPath: cfg.GitHub.AppPrivateKeyPath,
			TargetType:        cfg.GitHub.TargetType,
			CodeScanningTool:  cfg.GitHub.CodeScanningTool,
		})
		if err != nil {
			return nil, err
		}
		return p, nil
	case "gitlab":
		p, err := providergitlab.New(providergitlab.Options{
			Token:      cfg.GitLab.Token,
			TargetType: cfg.GitLab.TargetType,
			BaseURL:    cfg.GitLab.BaseURL,
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
