package metrics

import (
	"context"
	"math"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/metric/metricdata/metricdatatest"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

type fakeSource struct {
	names     []string
	snapshots map[string]provider.Snapshot
}

func (f fakeSource) ProviderNames() []string { return f.names }

func (f fakeSource) Latest(name string) (provider.Snapshot, bool) {
	s, ok := f.snapshots[name]
	return s, ok
}

func setupReader(t *testing.T, src SnapshotSource, dims Dimensions, remediation RemediationReader) (*sdkmetric.ManualReader, ScrapeErrorRecorder) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	record, err := register(mp.Meter("test"), src, dims, remediation)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return reader, record
}

func collect(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Aggregation {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	out := make(map[string]metricdata.Aggregation)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m.Data
		}
	}
	return out
}

func TestObservableGauges(t *testing.T) {
	src := fakeSource{
		names: []string{"github", "gitlab"}, // gitlab has no snapshot yet -> skipped
		snapshots: map[string]provider.Snapshot{
			"github": {
				Repos: []provider.RepoMetrics{
					{
						Name:            "alpha",
						OpenReviewItems: 3,
						Findings: []provider.Finding{
							{Severity: provider.SeverityHigh, Category: provider.CategoryDependency},
							{Severity: provider.SeverityHigh, Category: provider.CategoryDependency},
							{Severity: provider.SeverityCritical, Category: provider.CategoryStaticAnalysis},
						},
					},
					{Name: "beta", OpenReviewItems: 0},
				},
				RateLimits: []provider.RateLimit{
					{Resource: provider.ResourceREST, Remaining: 4999},
					{Resource: provider.ResourceGraphQL, Remaining: 4990},
				},
			},
		},
	}

	reader, _ := setupReader(t, src, Dimensions{}, nil)
	got := collect(t, reader)

	gh := func(kv ...attribute.KeyValue) attribute.Set {
		return attribute.NewSet(append([]attribute.KeyValue{attribute.String(attrProvider, "github")}, kv...)...)
	}

	wantReview := metricdata.Gauge[int64]{DataPoints: []metricdata.DataPoint[int64]{
		{Attributes: gh(attribute.String(attrRepo, "alpha")), Value: 3},
		{Attributes: gh(attribute.String(attrRepo, "beta")), Value: 0},
	}}
	metricdatatest.AssertAggregationsEqual(t, wantReview, got[metricReviewItemsOpen], metricdatatest.IgnoreTimestamp())

	wantFindings := metricdata.Gauge[int64]{DataPoints: []metricdata.DataPoint[int64]{
		{Attributes: gh(attribute.String(attrRepo, "alpha"), attribute.String(attrSeverity, "high"), attribute.String(attrCategory, "dependency")), Value: 2},
		{Attributes: gh(attribute.String(attrRepo, "alpha"), attribute.String(attrSeverity, "critical"), attribute.String(attrCategory, "static_analysis")), Value: 1},
	}}
	metricdatatest.AssertAggregationsEqual(t, wantFindings, got[metricSecurityFindings], metricdatatest.IgnoreTimestamp())

	wantRate := metricdata.Gauge[int64]{DataPoints: []metricdata.DataPoint[int64]{
		{Attributes: gh(attribute.String(attrResource, "rest")), Value: 4999},
		{Attributes: gh(attribute.String(attrResource, "graphql")), Value: 4990},
	}}
	metricdatatest.AssertAggregationsEqual(t, wantRate, got[metricRateLimitRemaining], metricdatatest.IgnoreTimestamp())
}

func TestObservableGaugesFindingDimensions(t *testing.T) {
	src := fakeSource{
		names: []string{"github"},
		snapshots: map[string]provider.Snapshot{
			"github": {Repos: []provider.RepoMetrics{{
				Name: "alpha",
				Findings: []provider.Finding{
					{Severity: provider.SeverityHigh, Category: provider.CategoryDependency, Ecosystem: "npm"},
					{Severity: provider.SeverityHigh, Category: provider.CategoryDependency, Ecosystem: "pip"},
					{Severity: provider.SeverityCritical, Category: provider.CategoryStaticAnalysis, Tool: "CodeQL"},
				},
			}}},
		},
	}
	gh := func(kv ...attribute.KeyValue) attribute.Set {
		return attribute.NewSet(append([]attribute.KeyValue{attribute.String(attrProvider, "github"), attribute.String(attrRepo, "alpha")}, kv...)...)
	}

	// Dimensions off: aggregate by severity+category only (npm+pip collapse into one series).
	off, _ := setupReader(t, src, Dimensions{}, nil)
	wantOff := metricdata.Gauge[int64]{DataPoints: []metricdata.DataPoint[int64]{
		{Attributes: gh(attribute.String(attrSeverity, "high"), attribute.String(attrCategory, "dependency")), Value: 2},
		{Attributes: gh(attribute.String(attrSeverity, "critical"), attribute.String(attrCategory, "static_analysis")), Value: 1},
	}}
	metricdatatest.AssertAggregationsEqual(t, wantOff, collect(t, off)[metricSecurityFindings], metricdatatest.IgnoreTimestamp())

	// Dimensions on: npm and pip split; the code-scanning finding gains a tool label.
	on, _ := setupReader(t, src, Dimensions{Ecosystem: true, Tool: true}, nil)
	wantOn := metricdata.Gauge[int64]{DataPoints: []metricdata.DataPoint[int64]{
		{Attributes: gh(attribute.String(attrSeverity, "high"), attribute.String(attrCategory, "dependency"), attribute.String(attrEcosystem, "npm")), Value: 1},
		{Attributes: gh(attribute.String(attrSeverity, "high"), attribute.String(attrCategory, "dependency"), attribute.String(attrEcosystem, "pip")), Value: 1},
		{Attributes: gh(attribute.String(attrSeverity, "critical"), attribute.String(attrCategory, "static_analysis"), attribute.String(attrTool, "CodeQL")), Value: 1},
	}}
	metricdatatest.AssertAggregationsEqual(t, wantOn, collect(t, on)[metricSecurityFindings], metricdatatest.IgnoreTimestamp())
}

func TestObservableRepoInfo(t *testing.T) {
	src := fakeSource{
		names: []string{"github"},
		snapshots: map[string]provider.Snapshot{
			"github": {Repos: []provider.RepoMetrics{
				{Name: "alpha", Posture: &provider.RepoPosture{Visibility: "private", DependabotEnabled: true, BranchProtected: true}},
				{Name: "beta"}, // no posture -> no scm_repo_info series
			}},
		},
	}
	reader, _ := setupReader(t, src, Dimensions{}, nil)

	want := metricdata.Gauge[int64]{DataPoints: []metricdata.DataPoint[int64]{
		{Attributes: attribute.NewSet(
			attribute.String(attrProvider, "github"),
			attribute.String(attrRepo, "alpha"),
			attribute.String(attrVisibility, "private"),
			attribute.String(attrArchived, "false"),
			attribute.String(attrBranchProtected, "true"),
			attribute.String(attrDependabotEnabled, "true"),
		), Value: 1},
	}}
	metricdatatest.AssertAggregationsEqual(t, want, collect(t, reader)[metricRepoInfo], metricdatatest.IgnoreTimestamp())
}

func TestObservableWorkflowRuns(t *testing.T) {
	src := fakeSource{
		names: []string{"github"},
		snapshots: map[string]provider.Snapshot{
			"github": {Repos: []provider.RepoMetrics{{
				Name: "alpha",
				WorkflowRuns: []provider.WorkflowRunStat{
					{Workflow: "ci", Conclusion: "success", Count: 8},
					{Workflow: "ci", Conclusion: "failure", Count: 2},
				},
			}}},
		},
	}
	reader, _ := setupReader(t, src, Dimensions{}, nil)

	gh := func(workflow, conclusion string) attribute.Set {
		return attribute.NewSet(
			attribute.String(attrProvider, "github"),
			attribute.String(attrRepo, "alpha"),
			attribute.String(attrWorkflow, workflow),
			attribute.String(attrConclusion, conclusion),
		)
	}
	want := metricdata.Gauge[int64]{DataPoints: []metricdata.DataPoint[int64]{
		{Attributes: gh("ci", "success"), Value: 8},
		{Attributes: gh("ci", "failure"), Value: 2},
	}}
	metricdatatest.AssertAggregationsEqual(t, want, collect(t, reader)[metricWorkflowRuns], metricdatatest.IgnoreTimestamp())
}

func TestScrapeErrorCounter(t *testing.T) {
	reader, record := setupReader(t, fakeSource{}, Dimensions{}, nil)

	record("github", provider.SourceGraphQL)
	record("github", provider.SourceGraphQL)
	record("github", provider.SourceREST)
	record("gitlab", "") // whole-poll failure: no source attribute

	got := collect(t, reader)

	want := metricdata.Sum[int64]{
		Temporality: metricdata.CumulativeTemporality,
		IsMonotonic: true,
		DataPoints: []metricdata.DataPoint[int64]{
			{Attributes: attribute.NewSet(attribute.String(attrProvider, "github"), attribute.String(attrSource, "graphql")), Value: 2},
			{Attributes: attribute.NewSet(attribute.String(attrProvider, "github"), attribute.String(attrSource, "rest")), Value: 1},
			{Attributes: attribute.NewSet(attribute.String(attrProvider, "gitlab")), Value: 1},
		},
	}
	metricdatatest.AssertAggregationsEqual(t, want, got[metricScrapeErrors], metricdatatest.IgnoreTimestamp())
}

type fakeRemediation struct{ series []provider.RemediationSeries }

func (f fakeRemediation) Remediation(context.Context) ([]provider.RemediationSeries, error) {
	return f.series, nil
}

func TestRemediationHistogramEmitted(t *testing.T) {
	src := fakeSource{
		names:     []string{"github"},
		snapshots: map[string]provider.Snapshot{"github": {}},
	}
	rem := fakeRemediation{series: []provider.RemediationSeries{{
		Provider: "github", Repo: "acme/svc", Category: provider.CategoryDependency, Resolution: provider.ResolutionFixed,
		Buckets: []provider.RemediationBucket{{LE: 3600, Count: 0}, {LE: 86400, Count: 2}, {LE: math.Inf(1), Count: 2}},
		Sum:     7200, Count: 2,
	}}}

	reader, _ := setupReader(t, src, Dimensions{}, rem)
	got := collect(t, reader)

	withLE := func(le string) attribute.Set {
		return attribute.NewSet(
			attribute.String(attrProvider, "github"),
			attribute.String(attrRepo, "acme/svc"),
			attribute.String(attrCategory, provider.CategoryDependency),
			attribute.String(attrResolution, provider.ResolutionFixed),
			attribute.String(attrLE, le),
		)
	}
	wantBucket := metricdata.Sum[int64]{
		Temporality: metricdata.CumulativeTemporality,
		IsMonotonic: true,
		DataPoints: []metricdata.DataPoint[int64]{
			{Attributes: withLE("3600"), Value: 0},
			{Attributes: withLE("86400"), Value: 2},
			{Attributes: withLE("+Inf"), Value: 2},
		},
	}
	metricdatatest.AssertAggregationsEqual(t, wantBucket, got[metricRemediationBucket], metricdatatest.IgnoreTimestamp())

	base := attribute.NewSet(
		attribute.String(attrProvider, "github"),
		attribute.String(attrRepo, "acme/svc"),
		attribute.String(attrCategory, provider.CategoryDependency),
		attribute.String(attrResolution, provider.ResolutionFixed),
	)
	wantSum := metricdata.Sum[float64]{
		Temporality: metricdata.CumulativeTemporality,
		IsMonotonic: true,
		DataPoints: []metricdata.DataPoint[float64]{
			{Attributes: base, Value: 7200},
		},
	}
	metricdatatest.AssertAggregationsEqual(t, wantSum, got[metricRemediationSum], metricdatatest.IgnoreTimestamp())

	wantCount := metricdata.Sum[int64]{
		Temporality: metricdata.CumulativeTemporality,
		IsMonotonic: true,
		DataPoints: []metricdata.DataPoint[int64]{
			{Attributes: base, Value: 2},
		},
	}
	metricdatatest.AssertAggregationsEqual(t, wantCount, got[metricRemediationCount], metricdatatest.IgnoreTimestamp())
}

func TestFindingsByStateGauge(t *testing.T) {
	src := fakeSource{
		names: []string{"github"},
		snapshots: map[string]provider.Snapshot{
			"github": {Repos: []provider.RepoMetrics{{
				Name: "alpha",
				Findings: []provider.Finding{
					{Severity: provider.SeverityHigh, Category: provider.CategoryDependency},
				},
				ResolvedFindings: []provider.ResolvedFinding{
					{Category: provider.CategoryDependency, State: provider.StateFixed, Resolution: provider.ResolutionFixed},
				},
			}}},
		},
	}
	reader, _ := setupReader(t, src, Dimensions{}, nil)
	got := collect(t, reader)

	gh := func(state string) attribute.Set {
		return attribute.NewSet(
			attribute.String(attrProvider, "github"),
			attribute.String(attrRepo, "alpha"),
			attribute.String(attrCategory, provider.CategoryDependency),
			attribute.String(attrState, state),
		)
	}
	want := metricdata.Gauge[int64]{DataPoints: []metricdata.DataPoint[int64]{
		{Attributes: gh(provider.StateOpen), Value: 1},
		{Attributes: gh(provider.StateFixed), Value: 1},
	}}
	metricdatatest.AssertAggregationsEqual(t, want, got[metricFindingsByState], metricdatatest.IgnoreTimestamp())
}

func TestSetupSelectsExporterAndShutsDown(t *testing.T) {
	// "none" avoids binding a port or emitting output while still exercising the
	// autoexport reader selection, MeterProvider wiring, and shutdown path.
	t.Setenv("OTEL_METRICS_EXPORTER", "none")

	mp, record, err := Setup(context.Background(), fakeSource{}, "test", Dimensions{}, nil)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if record == nil {
		t.Fatal("Setup returned a nil recorder")
	}
	if err := mp.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}
