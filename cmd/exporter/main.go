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
)

// Build metadata, populated via -ldflags "-X main.version=...".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	providerName := flag.String("provider", "github", "source-control provider to poll (github)")
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
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	prov, err := buildProvider(providerName, cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	coll := collector.New(collector.Entry{Provider: prov, Target: cfg.GithubOrg})

	mp, recordScrapeErr, err := metrics.Setup(ctx, coll)
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
		Str("target", cfg.GithubOrg).
		Dur("poll_interval", cfg.PollInterval).
		Str("exporter", os.Getenv("OTEL_METRICS_EXPORTER")).
		Msg("scm-metrics-exporter starting")

	// Blocks until a signal cancels ctx; autoexport owns the Prometheus HTTP server
	// (or the periodic OTLP/console reader), stopped by the deferred mp.Shutdown.
	return coll.Run(ctx, cfg.PollInterval, recordScrapeErr)
}

func buildProvider(name string, cfg config.Config) (provider.Provider, error) {
	switch name {
	case "github":
		p, err := providergithub.New(providergithub.Options{
			Token:             cfg.Token,
			AppID:             cfg.AppID,
			AppInstallationID: cfg.AppInstallationID,
			AppPrivateKeyPath: cfg.AppPrivateKeyPath,
			CodeScanningTool:  cfg.CodeScanningTool,
		})
		if err != nil {
			return nil, err
		}
		return p, nil
	case "gitlab":
		return nil, fmt.Errorf("provider %q is not yet built into this binary (see Epic 16)", name)
	default:
		return nil, fmt.Errorf("unknown provider %q (supported: github)", name)
	}
}

func setupLogging() {
	if os.Getenv("LOG_FORMAT") == "console" {
		zlog.Logger = zlog.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	}
	if raw := os.Getenv("LOG_LEVEL"); raw != "" {
		if lvl, err := zerolog.ParseLevel(raw); err == nil {
			zerolog.SetGlobalLevel(lvl)
		}
	}
}
