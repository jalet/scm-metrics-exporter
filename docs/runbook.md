# Operator runbook

Task-focused operations for scm-metrics-exporter. See the
[CRD reference](crd-reference.md) for field details and the
[README](../README.md) for configuration.

## Deploy or upgrade the operator

```sh
helm upgrade --install scm-metrics-exporter \
  oci://ghcr.io/jalet/charts/scm-metrics-exporter \
  --namespace scm-system --create-namespace \
  --version <chart-version>
```

Verify:

```sh
kubectl rollout status deploy/scm-metrics-exporter -n scm-system
kubectl logs deploy/scm-metrics-exporter -n scm-system | grep "starting manager"
```

## Onboard a target

1. Create the credentials Secret (PAT under `token`, or the App PEM under `app.pem`; a
   GitLab access token under `token`).
2. Apply a `GitHubMetricsExporter` or `GitLabMetricsExporter` referencing it, with
   `spec.export.otlpEndpoint` pointing at your OTLP collector (see the README examples).
3. Confirm discovery ran and collection Jobs were dispatched:

```sh
kubectl describe ghme <name> -n scm-system   # Ready=True/Discovered; status.discoveredRepositories
kubectl get jobs -n scm-system -l app.kubernetes.io/instance=<name>
```

Metrics arrive at the OTLP collector, not on a scrape endpoint. Confirm ingestion there.

## Rotate credentials

No restart is needed. The operator reads the Secret on each discovery cycle, and each
collection Job reads it at start, so a rotated token takes effect on the next cycle
(`spec.discoveryInterval`). To pick it up immediately, bump the CR (any annotation change)
to force a reconcile.

```sh
kubectl create secret generic <name> -n scm-system \
  --from-literal=token=ghp_new --dry-run=client -o yaml | kubectl apply -f -
kubectl annotate ghme <cr-name> -n scm-system scm.jalet.io/rotated="$(date +%s)" --overwrite
```

## Diagnose collection failures

- **CR `Ready=False` / `DiscoveryFailed`** -- the operator could not list repositories.
  Check the credentials and that the token/App can see the target's repos/projects.
- **CR `Ready=False` / `CredentialsInvalid`** -- the Secret is missing or lacks the key
  named by `tokenKey` / `appPrivateKeyKey`.
- **No metrics for a repo** -- inspect that repo's collection Job:

```sh
kubectl logs -n scm-system job/<job-name>          # per-repo Job; grep "source failed"
kubectl get jobs -n scm-system -l app.kubernetes.io/instance=<cr-name>
```

`scm_exporter_scrape_errors_total` (by `source`) rises when a data source fails within a
Job: `graphql` is often a token lacking Dependabot access, `rest` is code scanning access
or the feature not enabled, `secret_scanning` is secret-scanning access. A failed source is
partial -- the Job still pushes the other signals and exits 0.

## Valkey operations (alert lifecycle / MTTR)

`spec.collectLifecycle: true` (see the [CRD reference](crd-reference.md)) backs the
remediation-time histogram with Valkey: a dedup set per scope plus cumulative bucket
counters, so re-running collection Jobs never double-counts a resolved finding.

**Bundled vs bring-your-own:**

- The chart can deploy a minimal Valkey via `valkey.deploy: true` -- a single-replica,
  unauthenticated, no-HA, no-backup StatefulSet with a `data` PVC. It is meant for
  evaluation and small deployments only.
- **Prefer bring-your-own (BYO) in production.** Point `spec.valkey.endpoint` (and
  optionally `spec.valkey.secretRef` / `passwordKey` for auth) at a properly operated
  Valkey/Redis with HA and backups, and leave `valkey.deploy: false` (the chart default).

**Caveat on the bundled StatefulSet:** it runs with the chart's restricted
`securityContext` (`readOnlyRootFilesystem: true`), so the container can only write to the
`data` PVC mounted at `/data`. This works with the pinned Valkey image at the time of
writing. If a future Valkey image version needs to write anywhere else (a temp directory,
a different data path), it will fail under `readOnlyRootFilesystem: true` and need either
an additional `emptyDir` mount or a relaxed `securityContext` on that container. Validate
the bundled option with a real deploy (not just `helm template`) before relying on it,
especially across a Valkey image upgrade.

**Failure is non-fatal:** if Valkey is unreachable from a collection Job, the histogram
pass is skipped -- a `scm_exporter_scrape_errors_total{source="lifecycle"}` is recorded --
but every other signal (open findings, posture, workflow runs) still pushes over OTLP. A
lifecycle failure never fails the Job.

**Data loss is a benign counter reset:** if Valkey's data is lost (volume wiped, instance
replaced without the PVC), the cumulative bucket/sum/count counters reset to zero and climb
again as findings are re-counted on the next cycles. PromQL `rate()` and `increase()`
already tolerate counter resets, so no manual remediation is required; a `histogram_quantile`
dashboard over a window spanning the reset will show a transient dip, not a spike or an
error.

## Upgrade CRDs

CRDs are managed by the chart (`crds.enabled: true`) and updated by `helm upgrade`.
Because `crds.keep: true` sets `helm.sh/resource-policy: keep`, `helm uninstall` never
deletes the CRDs (and therefore never cascades to your custom resources).

To install CRDs out of band (GitOps managing them separately):

```sh
kubectl apply -f config/crd/bases/
helm upgrade ... --set crds.enabled=false
```

## Collect the metrics

Collection Jobs push over OTLP -- there is no scrape endpoint. Point
`spec.export.otlpEndpoint` at an OTLP metrics collector (or an OTLP-ingesting Prometheus)
reachable from the Job pods; scrape/store from there. Freshness is bounded by
`spec.discoveryInterval` (metrics are pushed once per cycle, per repo).

The operator's own controller-runtime metrics are exposed on its pod
(`metrics.bindAddress`, default `:8080`) for local inspection; the chart does not manage a
scrape path for them.

## Cut a release

Releases are automated with release-please.

1. Merge Conventional-Commit PRs to `main`. release-please maintains a release PR that
   bumps the version, `CHANGELOG.md`, and `Chart.yaml` (`version` + `appVersion`).
2. Merge the release PR. release-please tags `vX.Y.Z`.
3. The `release` workflow then, from that tag:
   - builds the multi-arch image, pushes it to GHCR with an SBOM and provenance, signs
     it with cosign (keyless), and uploads a grype scan;
   - packages the Helm chart, pushes it to `oci://ghcr.io/jalet/charts`, and cosign-signs it.

The git tag drives the image tag, the binary version (`-ldflags -X main.version`), and
the chart `appVersion`, so they stay aligned.

## Verify a signed artifact

```sh
cosign verify ghcr.io/jalet/scm-metrics-exporter:<tag> \
  --certificate-identity-regexp 'https://github.com/jalet/scm-metrics-exporter/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```
