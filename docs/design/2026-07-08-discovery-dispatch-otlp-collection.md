# Design: discovery + dispatched per-repo OTLP collection

- Status: Proposed
- Date: 2026-07-08
- Author: jalet
- Supersedes: the long-running Deployment / Prometheus-pull collection model

## 1. Context and motivation

Today the operator reconciles each exporter CR into a long-running Deployment that
polls a single target on an interval and serves `/metrics` for Prometheus to scrape
(pull-based, via OTel autoexport). That model is simple but does not scale: one pod
polls every repository serially, and every source-control API call draws from a
single credential's rate budget.

The goal is to scale collection across many orgs and large repository counts. The
pivotal facts that shape the design:

- GitHub App rate limits are bucketed **per installation**, not per token. The bucket
  auto-scales with installation size (5,000/hr baseline, +50/hr per repo beyond 20 and
  per user beyond 20, capped at 12,500/hr; 15,000/hr on Enterprise Cloud). A
  repo-scoped installation token still draws from that same bucket, so minting one
  token per repo does not create independent budgets.
  (See https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/rate-limits-for-github-apps)
- Fan-out multiplies budget only **across installations** (each org is its own bucket).
- Within one installation, the real levers are (a) collecting concurrently up to the
  rate ceiling and (b) **spreading requests over time** to stay under GitHub's
  secondary (burst / concurrent-request) limits, which bite before the hourly one.

This design adopts the shape proven by the mogenius/renovate-operator -- a **discovery**
phase that produces a repository inventory, then an **executor** that dispatches ephemeral
collection Jobs bounded by a **parallelism cap** that serves as the rate-limit governor --
adapted in one way: **discovery runs inside the operator**, not as a Job. Discovery is
cheap (a few paginated list calls), and running it in the operator keeps the exporter
binary Kubernetes-agnostic (no in-cluster client, no log-parsing handoff). The expensive
per-repo collection stays isolated in Jobs.
(See https://github.com/mogenius/renovate-operator and
https://deepwiki.com/mogenius/renovate-operator/4.3-project-discovery)

## 2. Decisions

1. **OTLP-only. The Prometheus pull backend is removed entirely.** No `/metrics`
   server, no Service, no ServiceMonitor, no long-running exporter Deployment. Every
   collector pushes over OTLP and exits. Rationale: ephemeral Jobs cannot be scraped,
   and keeping a second export path doubles the surface for no benefit under the new
   model.
2. **Per-repository collection grain, governed by a parallelism cap.** Collection Jobs
   are ephemeral and capped by `spec.parallelism`, so per-repo grain does not cause a
   standing object explosion (only N exist at once, then they are garbage-collected).
   Per-repo grain gives clean isolation, temporal spreading, and least-privilege
   repo-scoped tokens.
3. **Self-minted repo-scoped tokens.** Each collection Job mounts the GitHub App
   private key (read-only Secret) and mints a repository-scoped installation token at
   runtime. No long-lived token is passed in transit; each Job holds the minimum scope
   for its one repository (aligns with the least-privilege baseline, SEC-004).
4. **Discovery runs in the operator** (not a Job), writing the inventory to the CR
   `.status`. Keeps the exporter binary Kubernetes-agnostic. Bounded by autodiscover
   filters; a very large org is a known scaling edge (see Open Questions).
5. **`spec.discoveryInterval` (a duration) drives the cadence** via the operator's
   RequeueAfter -- not a cron string, since discovery is operator-driven. Collection Jobs
   are transient per discovery cycle; freshness is interval-bound.
6. **Autodiscover supports include and exclude filters** by design. `include` ships
   first; `exclude` may land in a later phase but the schema reserves it now.
7. **GitHub first. GitLab discovery/dispatch is deferred** to a later milestone; the
   shared spec fields are provider-neutral where possible.

## 3. Architecture

One operator control loop plus ephemeral collection Jobs.

```
   [Operator reconcile]  RequeueAfter = spec.discoveryInterval
        |  build github client from CR credentialsSecret (App key bytes)
        |  list org/user repos, apply autodiscover filters
        v
   writes .status.discoveredRepositories (+ lastDiscoveryTime, Discovered condition)
        |
        v  dispatch, at most spec.parallelism active, owner-refs, GC
   [Collection Job per repo]  --once --repo=<owner>/<name>
        mints repo-scoped token from mounted App key
        collects review items + findings + posture + workflow runs
        pushes OTLP  -->  [OTLP collector / OTLP-ingesting Prometheus]
        exits (0 = ok/partial, non-zero = hard failure)
```

### 3.1 Discovery
Runs inside the operator on each reconcile (requeued every `spec.discoveryInterval`). The
operator builds a GitHub client from the CR's `credentialsSecret` (App key bytes via
`ghinstallation.New`), enumerates repositories, applies `spec.autodiscover` filters, and
writes the resulting inventory plus a discovery timestamp/condition to the CR `.status`.
No discovery Job or in-cluster client in the exporter binary.

### 3.2 Dispatch (operator executor loop)
The operator watches CR status. On a fresh inventory it builds a work queue and creates
one ephemeral collection Job per repository, owner-referenced, capped by
`spec.parallelism`. Finished Jobs are garbage-collected via history limits. The cap is
the rate-limit governor: it bounds concurrent pressure on the shared installation
bucket and keeps request bursts under GitHub's secondary limits.

### 3.3 Collection (per-repo Job)
`/exporter --once --repo=<owner>/<name>`: mount the App private key, mint a
repo-scoped installation token, run one poll of all signals for that repository,
`ForceFlush` the meter provider over OTLP, `Shutdown`, exit. Exit code: `0` on success
or partial (SourceErrors still push the scrape-error counter); non-zero only on a hard
whole-repo failure so Job backoff and alerting see real outages.

## 4. Exporter binary modes

The binary factors over the existing provider / collector / metrics pieces and gains one
run mode (the default long-running poll+serve mode is removed):

- `--once --repo=<owner>/<repo>`: single-repository collection, self-minted repo-scoped
  token, OTLP push, exit.

It always uses the OTLP exporter. The Prometheus autoexport path and the `/metrics` HTTP
server are removed. Discovery is not a binary mode -- it runs in the operator (the binary
stays Kubernetes-agnostic).

## 5. CRD changes

Added to the shared `ExporterSpec` (or a nested `collection` block):

- `discoveryInterval` (duration, default 15m): how often the operator re-discovers and
  re-dispatches (drives RequeueAfter).
- `parallelism` (int, default 3, min 1): max concurrent collection Jobs.
- `autodiscover` (object):
  - `include` (object): `topics` ([]string), `visibility` (enum list), `namePatterns`
    ([]glob), `archived` (bool tri-state).
  - `exclude` (object): same shape as `include`. Reserved now; may implement later.
- `otlpEndpoint` retained (from the existing `ExportConfig`), now mandatory.

Removed / inert:

- `ExportConfig.Exporter` enum (Prometheus option gone; OTLP is implicit).
- `Replicas`, `ServiceMonitor`, `PollInterval` (no long-running Deployment; the schedule
  replaces the poll cadence). Removed rather than left inert, since this is a
  pre-release breaking change.

Validation (CEL): `schedule` and `otlpEndpoint` required; `parallelism >= 1`. The
existing target-type XOR rule stays.

## 6. What is removed

Recently built, working code obsoleted by this milestone (pre-release, so acceptable,
but explicit teardown):

- `render.go`: `exporterDeployment`, `exporterService`, `serviceMonitorFor`, the
  metrics port constant.
- Controllers: Deployment/Service/ServiceMonitor reconcile branches; add discovery
  CronJob reconcile + the executor loop + collection-Job dispatch and GC.
- Chart: Deployment/Service/ServiceMonitor-scrape templates for exporters; keep the
  operator's own Deployment. Add RBAC for CronJobs/Jobs.
- Binary: the poll-forever + Prometheus `/metrics` serving path.
- Webhook + tests that assert the removed spec fields.

## 7. Rate-limit governance and error handling

- `spec.parallelism` bounds concurrent API pressure per installation and dodges
  secondary/burst limits. Default small (3).
- Collection Jobs honor Retry-After via the existing failsafe-go transport.
- A repo without a given feature (no Actions, no code scanning) is not a fault: the
  per-repo collector degrades to a partial snapshot (SourceErrors), Job exits 0.
- A hard whole-repo failure (repo gone, token mint failed) exits non-zero: Job backoff
  and the scrape-error signal surface it.

## 8. Security

- Least privilege: repo-scoped installation tokens, minted per Job, never persisted.
- The App private key is mounted read-only; no token is written to a Secret or passed
  between pods.
- Operator RBAC extends to create/list/delete Jobs and CronJobs in the CR namespace.
- Encryption in transit: OTLP endpoint must be TLS (documented; validated where
  feasible).

## 9. Freshness

Push-based and schedule-bound: series in the sink are the last-pushed values between
discovery cycles. Documented as an accepted tradeoff. Operators tune `schedule` for the
freshness/cost balance.

## 10. Testing

- Binary: `--discover` mode (fake provider, filter application) and `--once --repo=`
  (single-repo collect, token mint stub, OTLP via ManualReader/console, exit-code
  matrix: ok / partial / hard failure).
- render: golden tests for the discovery CronJob and collection Job (args, token-key
  mount, concurrency, no Service/ServiceMonitor rendered).
- Controller (envtest): CR -> discovery CronJob created; status inventory ->
  executor dispatches N Jobs capped at `parallelism`; Jobs GC'd; CR delete -> children
  GC'd; CEL rejects missing schedule/otlpEndpoint.
- e2e (kind): install chart, apply a CR against a mocked GitHub API, assert discovery
  runs, collection Jobs dispatch, and `scm_*` metrics reach a test OTLP sink.

## 11. Backlog (new milestone)

Rough epic decomposition (sequenced in the implementation plan):

1. Provider single-repo collection path (`SnapshotRepo`) + autodiscover include filters.
2. Binary single-repo `--once` mode + self-minted repo-scoped token + OTLP-only metrics.
3. CRD reshape (new fields, remove Prometheus/Deployment fields) + CEL + generated
   manifests.
4. Operator: in-process discovery + collection-Job dispatch, parallelism cap, GC.
5. Remove the Prometheus/Deployment/Service/ServiceMonitor stack + update webhooks + chart
   and RBAC.
6. Tests + kind e2e.
7. Docs (README, CRD reference, OTLP sink + sharding runbook).

Deferred to later phases:

- `autodiscover.exclude` implementation.
- GitLab discovery/dispatch (group discovery -> per-project Jobs, group/project
  tokens).
- E21 workflow-run metrics ride on top as one more signal the per-repo collector
  gathers (no longer a standalone architecture problem).

## 12. Open questions

- **Discovery inventory size.** A very large org's repo list may strain the CR status
  object (etcd object size ~1.5MB). Mitigations: autodiscover filters, a separate
  inventory object, or paged/chunked status. Decide the threshold and fallback.
- **Token mint cost.** Minting a repo-scoped token per Job is one extra API call per
  repo per cycle. Confirm this stays well within the per-installation budget at target
  scale, or batch token minting.
- **Cross-installation scheduling.** With one CR per org (installation), staggering the
  discovery schedules avoids synchronized bursts across installations.
