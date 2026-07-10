# scm-metrics-exporter

Polls source-control platforms for open review items and security findings and
exposes them as [OpenTelemetry](https://opentelemetry.io) metrics, pushed via OTLP. A
companion Kubernetes operator reconciles per-provider custom resources: it discovers a
target's repositories on an interval and dispatches one ephemeral run-once collection
Job per repository (bounded by a parallelism cap), each pushing its metrics over OTLP.
Applying one custom resource per organization shards collection across independent
credentials and rate budgets.

- **GitHub** (open pull requests, Dependabot alerts, code scanning alerts, secret scanning alerts).
- **GitLab** (open merge requests, and vulnerability findings on the Ultimate tier:
  dependency, SAST, secret, and container scanning).

## Contents

- [Metrics](#metrics)
- [Remediation time (MTTR)](#remediation-time-mttr)
- [Components](#components)
- [Install the operator (Helm)](#install-the-operator-helm)
- [Custom resource examples](#custom-resource-examples)
- [Run the exporter directly](#run-the-exporter-directly)
- [Configuration](#configuration)
- [Develop](#develop)
- [Troubleshooting](#troubleshooting)
- [Documentation](#documentation)

## Metrics

| OTel instrument | Type | Attributes | Prometheus name |
|---|---|---|---|
| `scm.review_items.open` | gauge | provider, repo | `scm_review_items_open` |
| `scm.security_findings.open` | gauge | provider, repo, severity, category | `scm_security_findings_open` |
| `scm.api.rate_limit_remaining` | gauge | provider, resource | `scm_api_rate_limit_remaining` |
| `scm.exporter.scrape_errors` | counter | provider, source | `scm_exporter_scrape_errors_total` |
| `scm.repo.info` | gauge | provider, repo, visibility, archived, branch_protected, dependabot_enabled, secret_scanning_enabled | `scm_repo_info` |
| `scm.workflow_runs.recent` | gauge | provider, repo, workflow, conclusion | `scm_workflow_runs_recent` |
| `scm.finding_remediation_seconds.bucket` | monotonic counter (histogram bucket) | provider, repo, category, resolution, le, [severity] | `scm_finding_remediation_seconds_bucket` |
| `scm.finding_remediation_seconds.sum` | monotonic counter | provider, repo, category, resolution, [severity] | `scm_finding_remediation_seconds_sum` |
| `scm.finding_remediation_seconds.count` | monotonic counter | provider, repo, category, resolution, [severity] | `scm_finding_remediation_seconds_count` |
| `scm.finding_open_age_seconds.bucket` | gauge (histogram bucket) | provider, repo, category, le | `scm_finding_open_age_seconds_bucket` |
| `scm.finding_open_age_seconds.sum` | gauge | provider, repo, category | `scm_finding_open_age_seconds_sum` |
| `scm.finding_open_age_seconds.count` | gauge | provider, repo, category | `scm_finding_open_age_seconds_count` |
| `scm.findings.by_state` | gauge | provider, repo, category, state | `scm_findings_by_state` |

`severity` is one of `critical`, `high`, `medium`, `low`, or `unknown` (GitHub
secret-scanning alerts carry no severity). `category` is one of `dependency`,
`static_analysis`, `secret`, `container`. `source` is `graphql`, `rest`, or
`secret_scanning`; `resource` is `graphql` or `rest`. Optional `ecosystem` (Dependabot
package ecosystem) and `tool` (scanning tool) labels are added to
`scm_security_findings_open`, and an optional `severity` label to the remediation
histogram, only when enabled via `SCM_FINDING_DIMENSIONS` (`spec.findingDimensions`:
`ecosystem`, `tool`, `severity`). Enabling or disabling `severity` changes the remediation
scope key, so the affected counters restart from zero once (benign; `rate()` tolerates it).

`scm_repo_info` is a constant `1` carrying each repository's security posture on its
labels (the info-metric pattern; join it against the other series by `provider,repo`).
`visibility` is `public`/`private`/`internal`, `branch_protected` means the default
branch is protected by a classic branch-protection rule or an active repository ruleset,
`dependabot_enabled` means automated dependency-vulnerability alerting is on (GitHub
Dependabot alerts, or GitLab dependency scanning), and `secret_scanning_enabled` means
secret scanning is on (GitHub secret scanning, or GitLab `SECRET_DETECTION`). Some fields
are admin-gated, so a token without the required access may report them as `false`.

`scm_workflow_runs_recent` (opt-in via `spec.collectWorkflows`) counts recent CI runs in a
lookback window (`spec.workflowLookback`, default 7d) by `workflow` and `conclusion`;
in-progress/transient runs are skipped. It is a gauge because collection is run-once per
cycle: compute a failure ratio in the query, e.g. `failure / (success+failure)`.

- **GitHub**: Actions runs. `workflow` = workflow name, `conclusion` = run conclusion
  (`success`, `failure`, `cancelled`, ...).
- **GitLab**: pipelines. `workflow` = pipeline source (`push`, `schedule`,
  `merge_request_event`, ...), `conclusion` = pipeline status (`success`, `failed`,
  `canceled`, `skipped`).

- **GitHub** captures posture from the existing GraphQL repo page at no extra API cost.
- **GitLab** captures posture for **group** targets via a GraphQL sweep of the group's
  projects (visibility, archived, default-branch `branchRules`, and `securityScanners`);
  **user** targets emit MR counts only, so they carry no `scm_repo_info`.

Example: repos in an org missing branch protection --
`scm_repo_info{branch_protected="false"}`.

The Prometheus names above are the mapping this project's own OTLP-to-Prometheus path
produces (dots become underscores; `bucket`/`sum`/`count` are the histogram suffixes, not
the `_total` suffix a plain monotonic counter gets). A different OTLP collector or exporter
configuration may map instrument names differently -- verify the names actually emitted at
your collector before wiring alerts or dashboards against them.

## Remediation time (MTTR)

Opt-in via `spec.collectLifecycle: true` (requires Valkey, see [Configuration](#configuration)
and the [runbook](docs/runbook.md)). Each collection Job additionally fetches **resolved**
findings (state fixed/dismissed/resolved) within `spec.resolutionWindow` (default `2160h` /
90d), deduplicates them by provider-stable alert id in Valkey, and emits cumulative bucket
counters for `scm_finding_remediation_seconds` -- a true Prometheus histogram usable with
`histogram_quantile()` over any PromQL window, not a rolling-window gauge.

Every resolved finding carries a normalized `resolution` label with exactly three values,
so a query decides what "closed" means instead of lumping every dismissal into remediation
(which would game MTTR and hide accepted-risk backlog):

- `fixed` -- actually remediated: code changed, or a secret revoked/rotated.
- `dismissed_not_a_risk` -- false positive, used in tests, inaccurate, or not applicable.
- `dismissed_accepted_risk` -- won't-fix, tolerable risk, no bandwidth, or otherwise
  accepted. Any provider dismissal reason this project does not recognize also lands here
  (conservative default: treat the unknown case as still-a-risk).

Three views follow from the label:

- **Security KPI (MTTR)** -- `resolution="fixed"` only.
- **"No longer a real risk"** -- `resolution=~"fixed|dismissed_not_a_risk"`.
- **Accepted-risk backlog** -- `resolution="dismissed_accepted_risk"`.

```promql
histogram_quantile(0.9,
  sum(rate(scm_finding_remediation_seconds_bucket{resolution="fixed"}[30d])) by (le))
```

`scm_findings_by_state` is a point-in-time gauge (not deduplicated) observed from the same
snapshot each cycle: open findings contribute `state="open"`; resolved-in-window findings
contribute their provider-reported lifecycle state (`fixed`, `dismissed`, `auto_dismissed`,
or `resolved`) -- a finer-grained, provider-facing view than the three-way `resolution`
label on the histogram.

**Failure modes:** if Valkey is unreachable, the histogram is skipped, a
`scm_exporter_scrape_errors_total{source="lifecycle"}` is recorded, and every other metric
(open findings, posture, workflow runs) still flows -- lifecycle collection failure is never
fatal to a collection Job. If Valkey loses its data, the cumulative counters reset to zero
and climb again as findings are re-counted; this is a benign counter reset that `rate()` and
`increase()` already tolerate.

An optional `severity` label can be added to the remediation histogram via
`SCM_FINDING_DIMENSIONS=severity` (`spec.findingDimensions`), off by default because it
multiplies series cardinality on top of `provider,repo,category,resolution`. Toggling it
changes the scope key, so the affected counters restart from zero once (benign; `rate()`
tolerates it).

`scm_finding_open_age_seconds` is an always-on, point-in-time gauge histogram of how long
open (not yet resolved) findings have been open, per `provider,repo,category`, observed
from each finding's creation time against the current time. It needs no Valkey (unlike the
remediation histogram). "Open longer than 30d" is `count - bucket{le="2592000"}`; mean open
age is `sum / count`. Findings whose creation time is unknown are excluded.

## Components

| Binary | Path | Role |
|---|---|---|
| `exporter` | `cmd/exporter` | Metrics collector. In-cluster it runs once per repo (`--provider github --once --repo <name>`), pushes OTLP, and exits; it can also run a full-target poll for local use. |
| `operator` | `cmd/operator` | Controller-manager reconciling `GitHubMetricsExporter` / `GitLabMetricsExporter` CRs: discovery + per-repo Job dispatch. |

Both binaries ship in one container image; the operator's entrypoint is `/operator`
and the collection Jobs it dispatches override the command to `/exporter`.

## Install the operator (Helm)

The chart is published as an OCI artifact. It installs the operator, its RBAC, and
the CRDs (templated and gated by `crds.enabled` / `crds.keep`).

**Prerequisite: [cert-manager](https://cert-manager.io).** The chart ships an always-on
validating admission webhook that rejects a CR at apply time when its `credentialsSecret`
is missing or lacks the referenced key (the one cross-object check CEL cannot do).
cert-manager issues the webhook serving certificate (a self-signed `Issuer` + `Certificate`,
with the CA injected into the `ValidatingWebhookConfiguration`). The webhook uses
`failurePolicy: Fail` scoped to `scm.jalet.io` resources, so if the operator/webhook is
down, only scm CR writes are blocked -- never other cluster writes.

```sh
helm install scm-metrics-exporter \
  oci://ghcr.io/jalet/charts/scm-metrics-exporter \
  --namespace scm-system --create-namespace
```

Useful values (see `charts/scm-metrics-exporter/values.yaml` for the full set):

| Value | Default | Purpose |
|---|---|---|
| `image.repository` / `image.tag` | `ghcr.io/jalet/scm-metrics-exporter` / chart appVersion | Operator image. |
| `exporterImage.repository` | (operator image) | Image injected into the collection Jobs. |
| `replicaCount` / `leaderElection.enabled` | `1` / `true` | HA via leader election. |
| `crds.enabled` / `crds.keep` | `true` / `true` | Manage CRDs; keep them on uninstall. |
| `metrics.bindAddress` | `:8080` | Operator's own controller-runtime metrics port (set `0` to disable the endpoint). |
| `operator.serviceMonitor.enabled` | `true` | Create a Prometheus Operator ServiceMonitor (plus a metrics Service) for the operator's metrics. Renders only when the `monitoring.coreos.com/v1` CRD is present, so it is a no-op on clusters without Prometheus Operator. |
| `watchNamespaces` | (all) | Reserved for namespaced mode. |

## Custom resource examples

First create the credentials Secret, then the CR (same namespace).

**GitHub, PAT auth:**

```sh
kubectl create secret generic acme-github --namespace scm-system \
  --from-literal=token=ghp_your_token
```

```yaml
apiVersion: scm.jalet.io/v1alpha1
kind: GitHubMetricsExporter
metadata:
  name: acme
  namespace: scm-system
spec:
  org: acme
  authMode: token
  tokenKey: token                 # key in the Secret
  credentialsSecret:
    name: acme-github
  export:
    otlpEndpoint: http://otel-collector.observability:4318   # required: where collection Jobs push
  discoveryInterval: 15m          # how often to re-discover + re-dispatch
  parallelism: 3                  # max concurrent collection Jobs (rate-limit governor)
  autoDiscover:                   # optional; empty include matches all repos
    include:
      visibility: [private, internal]
      namePatterns: ["service-*"]
    exclude:                      # removed from the include set; empty excludes nothing
      archived: true
      topics: [deprecated]
```

`autoDiscover.include` picks the candidate repositories (empty matches all); `exclude` then
drops any repo that matches every criterion it sets. Criteria within a block are ANDed;
`namePatterns` are shell globs (GitHub matches the bare name, GitLab the full path).

**GitHub, App auth:** create a Secret with the App private key (PEM), then:

```yaml
apiVersion: scm.jalet.io/v1alpha1
kind: GitHubMetricsExporter
metadata:
  name: acme
  namespace: scm-system
spec:
  org: acme
  authMode: app
  appID: 123456
  appInstallationID: 7890123
  appPrivateKeyKey: app.pem        # key in the Secret holding the PEM
  credentialsSecret:
    name: acme-github-app
  codeScanningTool: CodeQL         # optional: count only this SARIF tool
  export:
    otlpEndpoint: http://otel-collector.observability:4318
```

With App auth, each collection Job mints a repository-scoped installation token (least
privilege). Install one App per organization to give each `GitHubMetricsExporter` its own
rate budget.

**GitLab:** create a Secret with a group or personal access token under `token`, then:

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
  baseURL: https://gitlab.com      # or your self-hosted instance
  export:
    otlpEndpoint: http://otel-collector.observability:4318
```

The operator discovers the group's projects (including subgroups) and dispatches one
collection Job per project, keyed by `path_with_namespace`. Vulnerability findings require
GitLab Ultimate; open merge-request counts and posture work on all tiers. GitLab has no
per-project token scoping, so Jobs use the configured group/personal token.

Inspect status:

```sh
kubectl get githubmetricsexporter,gitlabmetricsexporter -n scm-system   # shortnames: ghme, glme
kubectl describe ghme acme -n scm-system                                # see the Ready / CredentialsInvalid condition
```

## Run the exporter directly

For local development, without Kubernetes. Collect a single repository once and print the
metrics as JSON (the console exporter needs no OTLP collector):

```sh
OTEL_METRICS_EXPORTER=console LOG_FORMAT=console \
GITHUB_ORG=acme GITHUB_TOKEN=ghp_xxx \
  go run ./cmd/exporter --provider=github --once --repo=my-repo
```

`--once --repo=<name>` collects just `<org>/<name>` and exits (the owner is the target env).
Drop those flags to run a full-target poll of the whole org. To push OTLP instead of
printing, set `OTEL_METRICS_EXPORTER=otlp` and `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`.

## Configuration

The exporter is configured entirely by environment variables.

| Variable | Default | Purpose |
|---|---|---|
| `GITHUB_TARGET_TYPE` | `org` | Poll an `org` or a `user`. |
| `GITHUB_ORG` / `GITHUB_USER` | (one required) | Login of the org or user, per target type. |
| `GITHUB_TOKEN` | | PAT auth. |
| `GITHUB_APP_ID` / `GITHUB_APP_INSTALLATION_ID` / `GITHUB_APP_PRIVATE_KEY_PATH` | | GitHub App auth. |
| `GITHUB_CODE_SCANNING_TOOL` | (all tools) | Optional SARIF tool filter (for example `CodeQL`). |
| `SCM_FINDING_DIMENSIONS` | (none) | Comma list of optional finding labels: `ecosystem`, `tool`. Off by default (raises cardinality). |
| `SCM_COLLECT_LIFECYCLE` | `false` | Enable resolved-finding collection: `scm_finding_remediation_seconds` and `scm_findings_by_state`. Requires `VALKEY_ADDR`. |
| `SCM_RESOLUTION_WINDOW` | `2160h` | How far back resolved findings are collected, and the Valkey dedup TTL. Used when `SCM_COLLECT_LIFECYCLE=true`. |
| `VALKEY_ADDR` | | Valkey `host:port` backing the remediation histogram (required when `SCM_COLLECT_LIFECYCLE=true`). |
| `VALKEY_PASSWORD` | (none) | Valkey auth password. Omit for a passwordless Valkey. |
| `POLL_INTERVAL` | `5m` | Poll cadence for the full-target (non-`--once`) mode. |
| `OTEL_METRICS_EXPORTER` | `otlp` | `otlp` or `console`. The Prometheus pull backend has been removed. |
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` | | OTLP push target (required in `otlp` mode). |
| `OTEL_METRIC_EXPORT_INTERVAL` | `60s` | Push interval for the long-running mode (irrelevant to `--once`). |
| `LOG_LEVEL` / `LOG_FORMAT` | `info` / json | zerolog level; `LOG_FORMAT=console` for human output. |

The `--once`/`--repo` run mode is set by CLI flags, not env; in-cluster the operator passes
them to each collection Job.

**Auth precedence:** if the full App trio is set it is used; otherwise `GITHUB_TOKEN`;
otherwise startup fails. Provide credentials by env var or file path only -- never
commit tokens or private keys.

**Target types:** GitHub polls an organization (`GITHUB_TARGET_TYPE=org`, the default) or
a user (`GITHUB_TARGET_TYPE=user` with `GITHUB_USER`). For a user, code-scanning findings
are gathered per-repository (one extra REST call per repo, tolerating repos without code
scanning enabled). GitLab mirrors this with `GITLAB_TARGET_TYPE=group|user`
(`GITLAB_GROUP` / `GITLAB_USER`); a GitLab **user** target yields merge-request counts
only -- security findings are unavailable because GitLab vulnerabilities are
Ultimate/group-scoped.

## Develop

Tooling is pinned with [mise](https://mise.jdx.dev).

```sh
mise run build          # build both binaries into ./bin
mise run test           # go test -race -shuffle=on ./...
mise run test:envtest   # controller tests against a real API server (downloads envtest binaries)
mise run lint:go        # golangci-lint
mise run ci             # everything CI runs
mise run image:buildx   # multi-arch container image
```

After changing API types or RBAC markers, run `mise run generate manifests` and
`mise run chart:sync` (the `ci` task's `manifests:check` fails if they are stale).

## Troubleshooting

- **`scm_exporter_scrape_errors_total` increasing** -- a data source is failing. Check
  the exporter logs; a `source="graphql"` error is often a token missing Dependabot
  access, `source="rest"` is often code scanning access or the feature not enabled, and
  `source="secret_scanning"` is often the token missing secret-scanning access.
- **CR stuck `Ready=False` / `CredentialsInvalid`** -- the referenced Secret is missing
  or lacks the key named by `tokenKey` / `appPrivateKeyKey`.
- **No `scm_*` series at the collector** -- collection Jobs push over OTLP, so confirm
  `export.otlpEndpoint` is reachable from the Job pods and the OTLP collector is ingesting;
  check the Job logs (`kubectl logs job/<name>`).
- **CR `Ready=False` / `DiscoveryFailed`** -- the operator could not list repositories:
  check the credentials and that the token/App can see the target's repos/projects.
- **`scm_exporter_scrape_errors_total{source="lifecycle"}` increasing** -- Valkey is
  unreachable from the collection Jobs (`collectLifecycle` is on). The histogram is skipped
  but every other metric still flows; fix reachability. See
  [Enable remediation-time (MTTR) metrics](docs/how-to-enable-mttr.md#troubleshooting).

## Documentation

- [CRD reference](docs/crd-reference.md) -- every spec field, default, and validation rule.
- [Enable remediation-time (MTTR) metrics](docs/how-to-enable-mttr.md) -- provision Valkey, turn on `collectLifecycle`, verify, and query MTTR.
- [Operator runbook](docs/runbook.md) -- deploy, rotate credentials, Valkey operations, upgrade CRDs, cut a release.

## License

Apache License 2.0. See [LICENSE](LICENSE).
