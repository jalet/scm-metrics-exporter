// Package metrics is the single OpenTelemetry instrumentation surface. The
// exporter backend (Prometheus, OTLP, or console) is selected at runtime by the
// contrib autoexport package from the OTEL_METRICS_EXPORTER environment variable,
// so the same code path serves every backend.
//
// The package depends only on provider.Snapshot and the SnapshotSource interface,
// never on a concrete provider, so adding a provider requires no change here. The
// three gauges are observed from the collector cache inside a single callback; the
// scrape-error counter is a synchronous instrument incremented from outside the
// callback via the returned ScrapeErrorRecorder.
package metrics

import (
	"context"
	"fmt"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

const meterName = "github.com/jalet/scm-metrics-exporter"

// Instrument names (provider-neutral; no github.* prefix).
const (
	metricReviewItemsOpen    = "scm.review_items.open"
	metricSecurityFindings   = "scm.security_findings.open"
	metricRateLimitRemaining = "scm.api.rate_limit_remaining"
	metricScrapeErrors       = "scm.exporter.scrape_errors"
)

// Metric attribute keys.
const (
	attrProvider = "provider"
	attrRepo     = "repo"
	attrSeverity = "severity"
	attrCategory = "category"
	attrResource = "resource"
	attrSource   = "source"
)

// SnapshotSource is the read side of the collector cache that the metrics callback
// observes on every scrape or push. The collector implements it.
type SnapshotSource interface {
	// ProviderNames returns the configured provider names, in a stable order.
	ProviderNames() []string
	// Latest returns the most recent snapshot for a provider, or ok=false if none
	// has been polled yet.
	Latest(name string) (provider.Snapshot, bool)
}

// ScrapeErrorRecorder increments the scm.exporter.scrape_errors counter for a
// provider and source. An empty source records a provider-level (whole-poll)
// failure. It is safe for concurrent use and must be called OUTSIDE the
// observable-gauge callback (synchronous instruments only).
type ScrapeErrorRecorder func(providerName, source string)

// Setup builds the metric reader from OTEL_METRICS_EXPORTER via autoexport, wires a
// MeterProvider, registers the instruments and the observing callback, and returns
// the provider plus the scrape-error recorder. The caller owns shutdown: call
// MeterProvider.Shutdown, which also flushes a periodic (OTLP/console) reader and
// stops the Prometheus HTTP server that autoexport manages.
//
// In Prometheus mode autoexport serves /metrics itself on
// OTEL_EXPORTER_PROMETHEUS_HOST:PORT (default localhost:9464); set the host to
// 0.0.0.0 in a container. Do not stand up a separate HTTP server.
func Setup(ctx context.Context, src SnapshotSource) (*sdkmetric.MeterProvider, ScrapeErrorRecorder, error) {
	reader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("metrics: create reader: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(mp)

	record, err := register(mp.Meter(meterName), src)
	if err != nil {
		_ = mp.Shutdown(ctx)
		return nil, nil, err
	}
	return mp, record, nil
}

// register creates the four instruments, registers the gauge callback, and returns
// a recorder for the synchronous counter. It is separated from Setup so tests can
// drive it with a ManualReader-backed meter.
func register(meter metric.Meter, src SnapshotSource) (ScrapeErrorRecorder, error) {
	reviewItems, err := meter.Int64ObservableGauge(metricReviewItemsOpen,
		metric.WithDescription("Open review items (pull or merge requests) per repository."))
	if err != nil {
		return nil, fmt.Errorf("metrics: gauge %s: %w", metricReviewItemsOpen, err)
	}
	findings, err := meter.Int64ObservableGauge(metricSecurityFindings,
		metric.WithDescription("Open security findings per repository, by severity and category."))
	if err != nil {
		return nil, fmt.Errorf("metrics: gauge %s: %w", metricSecurityFindings, err)
	}
	rateLimit, err := meter.Int64ObservableGauge(metricRateLimitRemaining,
		metric.WithDescription("Remaining API rate-limit quota per provider resource."))
	if err != nil {
		return nil, fmt.Errorf("metrics: gauge %s: %w", metricRateLimitRemaining, err)
	}
	scrapeErrors, err := meter.Int64Counter(metricScrapeErrors,
		metric.WithDescription("Provider scrape errors, by source."))
	if err != nil {
		return nil, fmt.Errorf("metrics: counter %s: %w", metricScrapeErrors, err)
	}

	callback := func(_ context.Context, o metric.Observer) error {
		for _, name := range src.ProviderNames() {
			snap, ok := src.Latest(name)
			if !ok {
				continue
			}
			observeSnapshot(o, name, snap, reviewItems, findings, rateLimit)
		}
		return nil
	}
	if _, err := meter.RegisterCallback(callback, reviewItems, findings, rateLimit); err != nil {
		return nil, fmt.Errorf("metrics: register callback: %w", err)
	}

	record := func(providerName, source string) {
		attrs := []attribute.KeyValue{attribute.String(attrProvider, providerName)}
		if source != "" {
			attrs = append(attrs, attribute.String(attrSource, source))
		}
		scrapeErrors.Add(context.Background(), 1, metric.WithAttributes(attrs...))
	}
	return record, nil
}

// observeSnapshot emits the three gauges for one provider's snapshot.
func observeSnapshot(o metric.Observer, name string, snap provider.Snapshot, reviewItems, findings, rateLimit metric.Int64ObservableGauge) {
	for _, repo := range snap.Repos {
		o.ObserveInt64(reviewItems, int64(repo.OpenReviewItems),
			metric.WithAttributes(
				attribute.String(attrProvider, name),
				attribute.String(attrRepo, repo.Name),
			))

		counts := make(map[[2]string]int64, len(repo.Findings))
		for _, f := range repo.Findings {
			counts[[2]string{f.Severity, f.Category}]++
		}
		for key, count := range counts {
			o.ObserveInt64(findings, count,
				metric.WithAttributes(
					attribute.String(attrProvider, name),
					attribute.String(attrRepo, repo.Name),
					attribute.String(attrSeverity, key[0]),
					attribute.String(attrCategory, key[1]),
				))
		}
	}

	for _, rl := range snap.RateLimits {
		o.ObserveInt64(rateLimit, rl.Remaining,
			metric.WithAttributes(
				attribute.String(attrProvider, name),
				attribute.String(attrResource, rl.Resource),
			))
	}
}
