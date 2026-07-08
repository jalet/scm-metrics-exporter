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
| `scm.repo.info` | gauge | provider, repo, visibility, archived, branch_protected, dependabot_enabled | `scm_repo_info` |

`severity` is one of `critical`, `high`, `medium`, `low`, or `unknown` (GitHub
secret-scanning alerts carry no severity). `category` is one of `dependency`,
`static_analysis`, `secret`, `container`. `source` is `graphql`, `rest`, or
`secret_scanning`; `resource` is `graphql` or `rest`. Optional `ecosystem` (Dependabot
package ecosystem) and `tool` (scanning tool) labels are added to
`scm_security_findings_open` only when enabled via `SCM_FINDING_DIMENSIONS`.

`scm_repo_info` is a constant `1` carrying each repository's security posture on its
labels (the info-metric pattern; join it against the other series by `provider,repo`).
`visibility` is `public`/`private`/`internal`, `branch_protected` means the default
branch has a protection rule, and `dependabot_enabled` means automated
dependency-vulnerability alerting is on (GitHub Dependabot alerts, or GitLab dependency
scanning). Some fields are admin-gated, so a token without the required access may report
them as `false`.

- **GitHub** captures posture from the existing GraphQL repo page at no extra API cost.
- **GitLab** captures posture for **group** targets via a GraphQL sweep of the group's
  projects (visibility, archived, default-branch `branchRules`, and `securityScanners`);
  **user** targets emit MR counts only, so they carry no `scm_repo_info`.

Example: repos in an org missing branch protection --
`scm_repo_info{branch_protected="false"}`.

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
| `metrics.bindAddress` | `:8080` | Operator's own controller-runtime metrics port (not scraped by the chart; set `0` to disable). |
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
      archived: false
```

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

**GitLab** collection through the operator is **deferred** in the discovery/dispatch model
(the GitLab provider has no single-repo collection path yet). A `GitLabMetricsExporter`
reconciles to `Ready=False` / `Unsupported` and dispatches nothing. The GitLab provider
still works when the exporter binary is run directly against a group (see below).

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
  check the credentials and that the token/App can see the target's repos.
- **GitLab CR `Ready=False` / `Unsupported`** -- expected: GitLab operator collection is
  deferred (run the exporter binary directly for GitLab).

## Documentation

- [CRD reference](docs/crd-reference.md) -- every spec field, default, and validation rule.
- [Operator runbook](docs/runbook.md) -- deploy, rotate credentials, upgrade CRDs, cut a release.

## License

Apache License 2.0. See [LICENSE](LICENSE).
