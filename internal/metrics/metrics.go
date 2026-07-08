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
	"strconv"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

const (
	meterName = "github.com/jalet/scm-metrics-exporter"
	// serviceName is the default service.name resource attribute (surfaced as
	// target_info{service_name=...}); OTEL_SERVICE_NAME overrides it.
	serviceName = "scm-metrics-exporter"
)

// Instrument names (provider-neutral; no github.* prefix).
const (
	metricReviewItemsOpen    = "scm.review_items.open"
	metricSecurityFindings   = "scm.security_findings.open"
	metricRateLimitRemaining = "scm.api.rate_limit_remaining"
	metricScrapeErrors       = "scm.exporter.scrape_errors"
	metricRepoInfo           = "scm.repo.info"
)

// Metric attribute keys.
const (
	attrProvider          = "provider"
	attrRepo              = "repo"
	attrSeverity          = "severity"
	attrCategory          = "category"
	attrResource          = "resource"
	attrSource            = "source"
	attrEcosystem         = "ecosystem"
	attrTool              = "tool"
	attrVisibility        = "visibility"
	attrArchived          = "archived"
	attrBranchProtected   = "branch_protected"
	attrDependabotEnabled = "dependabot_enabled"
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

// Dimensions selects optional finding labels on scm.security_findings.open. They default
// off because ecosystem and tool multiply the series cardinality.
type Dimensions struct {
	Ecosystem bool
	Tool      bool
}

// Setup builds the metric reader from OTEL_METRICS_EXPORTER via autoexport, wires a
// MeterProvider, registers the instruments and the observing callback, and returns
// the provider plus the scrape-error recorder. The caller owns shutdown: call
// MeterProvider.Shutdown, which also flushes a periodic (OTLP/console) reader and
// stops the Prometheus HTTP server that autoexport manages.
//
// In Prometheus mode autoexport serves /metrics itself on
// OTEL_EXPORTER_PROMETHEUS_HOST:PORT (default localhost:9464); set the host to
// 0.0.0.0 in a container. Do not stand up a separate HTTP server.
//
// version stamps both the instrumentation scope (otel_scope_version label /
// OTLP scope version) and the resource service.version (target_info /
// resource). It also sets service.name so target_info reports
// "scm-metrics-exporter" instead of the default "unknown_service:exporter".
// OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES override the resource defaults.
// Pass the build version; an empty string is valid. dims selects optional finding labels.
func Setup(ctx context.Context, src SnapshotSource, version string, dims Dimensions) (*sdkmetric.MeterProvider, ScrapeErrorRecorder, error) {
	reader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("metrics: create reader: %w", err)
	}

	// WithFromEnv is applied last so OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRIBUTES
	// win over the built-in defaults; no builtin unknown_service fallback is used.
	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			attribute.String("service.name", serviceName),
			attribute.String("service.version", version),
		),
		resource.WithFromEnv(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("metrics: build resource: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	record, err := register(mp.Meter(meterName, metric.WithInstrumentationVersion(version)), src, dims)
	if err != nil {
		_ = mp.Shutdown(ctx)
		return nil, nil, err
	}
	return mp, record, nil
}

// register creates the four instruments, registers the gauge callback, and returns
// a recorder for the synchronous counter. It is separated from Setup so tests can
// drive it with a ManualReader-backed meter.
func register(meter metric.Meter, src SnapshotSource, dims Dimensions) (ScrapeErrorRecorder, error) {
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
	repoInfo, err := meter.Int64ObservableGauge(metricRepoInfo,
		metric.WithDescription("Repository security posture (constant 1); the posture is carried on the labels."))
	if err != nil {
		return nil, fmt.Errorf("metrics: gauge %s: %w", metricRepoInfo, err)
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
			observeSnapshot(o, name, snap, reviewItems, findings, rateLimit, repoInfo, dims)
		}
		return nil
	}
	if _, err := meter.RegisterCallback(callback, reviewItems, findings, rateLimit, repoInfo); err != nil {
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

// findingKey aggregates findings for the security-findings gauge. ecosystem and tool are
// left empty unless the corresponding dimension is enabled, so the default output keeps
// exactly the severity+category label set.
type findingKey struct {
	severity, category, ecosystem, tool string
}

// observeSnapshot emits the per-provider gauges for one snapshot. dims selects the
// optional ecosystem/tool finding labels.
func observeSnapshot(o metric.Observer, name string, snap provider.Snapshot, reviewItems, findings, rateLimit, repoInfo metric.Int64ObservableGauge, dims Dimensions) {
	for _, repo := range snap.Repos {
		o.ObserveInt64(reviewItems, int64(repo.OpenReviewItems),
			metric.WithAttributes(
				attribute.String(attrProvider, name),
				attribute.String(attrRepo, repo.Name),
			))

		if p := repo.Posture; p != nil {
			o.ObserveInt64(repoInfo, 1, metric.WithAttributes(
				attribute.String(attrProvider, name),
				attribute.String(attrRepo, repo.Name),
				attribute.String(attrVisibility, p.Visibility),
				attribute.String(attrArchived, strconv.FormatBool(p.Archived)),
				attribute.String(attrBranchProtected, strconv.FormatBool(p.BranchProtected)),
				attribute.String(attrDependabotEnabled, strconv.FormatBool(p.DependabotEnabled)),
			))
		}

		counts := make(map[findingKey]int64, len(repo.Findings))
		for _, f := range repo.Findings {
			k := findingKey{severity: f.Severity, category: f.Category}
			if dims.Ecosystem {
				k.ecosystem = f.Ecosystem
			}
			if dims.Tool {
				k.tool = f.Tool
			}
			counts[k]++
		}
		for key, count := range counts {
			attrs := []attribute.KeyValue{
				attribute.String(attrProvider, name),
				attribute.String(attrRepo, repo.Name),
				attribute.String(attrSeverity, key.severity),
				attribute.String(attrCategory, key.category),
			}
			if key.ecosystem != "" {
				attrs = append(attrs, attribute.String(attrEcosystem, key.ecosystem))
			}
			if key.tool != "" {
				attrs = append(attrs, attribute.String(attrTool, key.tool))
			}
			o.ObserveInt64(findings, count, metric.WithAttributes(attrs...))
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
