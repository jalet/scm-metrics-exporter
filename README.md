# scm-metrics-exporter

Polls source-control platforms (GitHub, GitLab) for open review items and
security findings, and exposes them as [OpenTelemetry](https://opentelemetry.io)
metrics. The exporter backend (Prometheus scrape endpoint or OTLP push) is chosen
at runtime from a single instrumentation surface. A companion Kubernetes operator
reconciles per-provider custom resources into exporter Deployments.

> Status: under construction. This repository is being built incrementally
> against a milestone/epic backlog. Contributors: see the local `tasks/` folder
> (`tasks/PROGRESS.md`) for the current state and the next task.

## Components

| Binary | Path | Role |
|---|---|---|
| `exporter` | `cmd/exporter` | Long-running metrics exporter (`--provider github\|gitlab`). |
| `operator` | `cmd/operator` | Controller-manager reconciling `GitHubMetricsExporter` / `GitLabMetricsExporter` CRs. |

## Metrics

| Metric | Type | Attributes |
|---|---|---|
| `scm.review_items.open` | gauge | provider, repo |
| `scm.security_findings.open` | gauge | provider, repo, severity, category |
| `scm.api.rate_limit_remaining` | gauge | provider, resource |
| `scm.exporter.scrape_errors` | counter | provider, source |

Prometheus renders these as `scm_review_items_open`, `scm_security_findings_open`,
`scm_api_rate_limit_remaining`, and `scm_exporter_scrape_errors_total`.

## Configuration (exporter, environment variables)

| Variable | Default | Purpose |
|---|---|---|
| `GITHUB_ORG` | (required for GitHub) | Organization to poll. |
| `GITHUB_TOKEN` | | PAT auth (local development). |
| `GITHUB_APP_ID` / `GITHUB_APP_INSTALLATION_ID` / `GITHUB_APP_PRIVATE_KEY_PATH` | | GitHub App auth (preferred when all three are set). |
| `GITHUB_CODE_SCANNING_TOOL` | (all tools) | Optional SARIF tool filter (e.g. `CodeQL`). |
| `POLL_INTERVAL` | `5m` | Poll cadence (Go duration). |
| `OTEL_METRICS_EXPORTER` | `otlp` | `prometheus`, `otlp`, or `console`. |
| `OTEL_EXPORTER_PROMETHEUS_PORT` | `9464` | Prometheus scrape port. |
| `OTEL_EXPORTER_PROMETHEUS_HOST` | `localhost` | Set to `0.0.0.0` when running in a container/pod. |
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` | | OTLP push target. |

Exactly one auth method is required: either `GITHUB_TOKEN`, or the App trio.
Never commit tokens or private keys; provide them by path or environment only.

## Develop

Tooling is pinned with [mise](https://mise.jdx.dev) (PGT ADR 011). Common tasks:

```sh
mise run build      # build both binaries into ./bin
mise run test       # go test -race -shuffle=on ./...
mise run lint:go    # golangci-lint
mise run ci         # everything CI runs
```

## Run locally (Prometheus)

```sh
GITHUB_ORG=my-org GITHUB_TOKEN=ghp_xxx \
OTEL_METRICS_EXPORTER=prometheus OTEL_EXPORTER_PROMETHEUS_HOST=0.0.0.0 \
  go run ./cmd/exporter
curl -s localhost:9464/metrics | grep scm_
```

## License

Apache License 2.0. See [LICENSE](LICENSE).
