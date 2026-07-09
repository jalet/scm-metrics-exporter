# Enable remediation-time (MTTR) metrics

Turn on alert lifecycle collection so the exporter emits the
`scm_finding_remediation_seconds` histogram (mean-time-to-remediate) and the
`scm_findings_by_state` gauge for a target. This is opt-in and requires a Valkey
instance for deduplication.

For the full field list and validation rules, see the
[CRD reference](crd-reference.md); for ongoing Valkey operations, see the
[runbook](runbook.md).

## Prerequisites

- The operator is installed and already reconciling a `GitHubMetricsExporter` or
  `GitLabMetricsExporter` CR (see the [README](../README.md)).
- A reachable OTLP metrics collector (you already set `spec.export.otlpEndpoint`).
- Network reachability from the collection-Job pods to a Valkey `host:port`.
- The token/App used by the CR can read resolved alerts: GitHub code
  scanning, secret scanning, and Dependabot alerts; GitLab vulnerabilities
  (Ultimate tier).

## Step 1: Provision Valkey

The remediation histogram deduplicates resolved findings by alert id in Valkey,
so re-running collection Jobs never double-counts. Choose one option.

| Option | Use when | Trade-off |
|---|---|---|
| Bring your own (recommended) | Production | You operate Valkey/Redis with HA and backups |
| Bundled chart StatefulSet | Evaluation, small setups | Single replica, no HA, no backups |

### Option A: Bring your own (recommended)

Point the CR at an existing Valkey/Redis you operate. No chart change is needed;
continue to Step 2. The endpoint is `host:port`, for example
`valkey.observability:6379`.

### Option B: Bundled Valkey

Deploy the chart's minimal single-replica Valkey into the operator's namespace:

```sh
helm upgrade scm-metrics-exporter \
  oci://ghcr.io/jalet/charts/scm-metrics-exporter \
  --namespace scm-system --reuse-values \
  --set valkey.deploy=true
```

This creates a `valkey` Service on port 6379 and a StatefulSet with a `1Gi`
`data` PVC. Its in-cluster endpoint is `valkey.<namespace>:6379` (for example
`valkey.scm-system:6379`). It is unauthenticated, so skip Step 2.

> **Warning:** The bundled StatefulSet runs with `readOnlyRootFilesystem: true`
> and has no HA or backups. Validate it with a real deploy before relying on it,
> and prefer Option A in production.

## Step 2: Store the Valkey password (authenticated BYO only)

Skip this step for a passwordless Valkey or the bundled option.

Put the password in the CR's existing `credentialsSecret`, or a separate Secret
in the CR namespace, under a key you choose (default key name: `password`):

```sh
kubectl create secret generic valkey-auth \
  --namespace scm-system \
  --from-literal=password='<valkey-password>'
```

## Step 3: Enable lifecycle collection on the CR

Add `collectLifecycle`, an optional `resolutionWindow`, and the `valkey` block
to the CR spec. `resolutionWindow` (default `2160h`, 90 days) bounds how far back
resolved findings are collected and doubles as the Valkey dedup TTL.

GitHub:

```yaml
apiVersion: scm.jalet.io/v1alpha1
kind: GitHubMetricsExporter
metadata:
  name: acme
  namespace: scm-system
spec:
  org: acme
  authMode: token
  tokenKey: token
  credentialsSecret:
    name: acme-github
  export:
    otlpEndpoint: http://otel-collector.observability:4318
  collectLifecycle: true
  resolutionWindow: 2160h
  valkey:
    endpoint: valkey.observability:6379
    # Omit secretRef for a passwordless / bundled Valkey.
    secretRef:
      name: valkey-auth
    passwordKey: password
```

GitLab is identical from `collectLifecycle` down; only the kind and target
fields differ:

```yaml
apiVersion: scm.jalet.io/v1alpha1
kind: GitLabMetricsExporter
metadata:
  name: acme
  namespace: scm-system
spec:
  group: acme
  tokenKey: token
  credentialsSecret:
    name: acme-gitlab
  export:
    otlpEndpoint: http://otel-collector.observability:4318
  collectLifecycle: true
  valkey:
    endpoint: valkey.observability:6379
```

> **Note:** `collectLifecycle: true` requires a populated `valkey` block. The API
> server rejects the CR at apply time (CEL) if it is missing.

## Step 4: Apply and confirm dispatch

```sh
kubectl apply -f exporter.yaml
kubectl get ghme,glme -n scm-system
```

Wait for the next `discoveryInterval` (default 15m) or delete a completed
collection Job to force a re-dispatch. Confirm the Jobs carry the lifecycle env:

```sh
kubectl get pods -n scm-system -l app.kubernetes.io/name=scm-metrics-exporter
kubectl get job -n scm-system -o yaml \
  | grep -E 'SCM_COLLECT_LIFECYCLE|VALKEY_ADDR'
```

## Step 5: Verify the metrics arrive

Query your metrics backend for the new series (the histogram is cumulative, so it
appears once at least one finding has been resolved within the window):

```promql
scm_findings_by_state
scm_finding_remediation_seconds_count
```

If `scm_findings_by_state` is present but the histogram is absent, no resolved
findings fell inside `resolutionWindow` yet, or Valkey is unreachable (see
Troubleshooting).

## Step 6: Query MTTR

Every resolved finding carries a `resolution` label with three values: `fixed`
(actually remediated), `dismissed_not_a_risk` (false positive / used in tests),
and `dismissed_accepted_risk` (won't-fix / accepted). Filter on it so dismissals
do not distort the security KPI.

90th-percentile time-to-fix over the last 30 days:

```promql
histogram_quantile(0.9,
  sum(rate(scm_finding_remediation_seconds_bucket{resolution="fixed"}[30d])) by (le))
```

Mean time-to-fix per category:

```promql
sum(rate(scm_finding_remediation_seconds_sum{resolution="fixed"}[30d])) by (category)
/
sum(rate(scm_finding_remediation_seconds_count{resolution="fixed"}[30d])) by (category)
```

> **Note:** The Prometheus names above assume this project's OTLP-to-Prometheus
> mapping. Verify the names your collector actually emits before wiring alerts or
> dashboards. See the [README metrics section](../README.md#metrics).

## Troubleshooting

- **CR rejected at apply time** -- `collectLifecycle: true` without a `valkey`
  block. Add `valkey.endpoint`.
- **`scm_exporter_scrape_errors_total{source="lifecycle"}` climbing** -- Valkey
  is unreachable from the Job pods. Lifecycle collection is non-fatal, so every
  other metric still flows; fix reachability (network policy, endpoint, auth) and
  the histogram resumes. Check a Job's logs: `kubectl logs job/<name> -n
  scm-system`.
- **Histogram never appears** -- no findings resolved within `resolutionWindow`
  yet, the token lacks access to resolved alerts, or (GitLab) the tier is not
  Ultimate. Confirm `scm_findings_by_state{state="fixed"}` is non-zero.
- **A `histogram_quantile` dashboard shows a transient dip** -- Valkey lost its
  data (volume wiped or instance replaced without the PVC). The cumulative
  counters reset and climb again; `rate()`/`increase()` tolerate the reset and no
  action is required.
