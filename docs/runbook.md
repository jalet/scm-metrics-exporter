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

## Onboard a repository target

1. Create the credentials Secret (PAT under `token`, or the App PEM under `app.pem`).
2. Apply a `GitHubMetricsExporter` referencing it (see the README examples).
3. Confirm the exporter came up:

```sh
kubectl get ghme,deploy,svc -n scm-system -l app.kubernetes.io/managed-by=scm-metrics-operator
kubectl describe ghme <name> -n scm-system   # Ready condition
```

## Rotate credentials

The exporter reads the Secret at pod start (token via env, App key via a mounted
file), so rotation requires a restart of the exporter pod.

```sh
kubectl create secret generic <name> -n scm-system \
  --from-literal=token=ghp_new --dry-run=client -o yaml | kubectl apply -f -
kubectl rollout restart deploy/<cr-name> -n scm-system
```

## Diagnose scrape errors

`scm_exporter_scrape_errors_total` rising means a data source failed. The exporter
keeps serving the last good snapshot.

```sh
kubectl logs deploy/<cr-name> -n scm-system | grep "source failed"
```

- `source="graphql"` -- the PR / Dependabot query failed. A common cause is a token
  lacking Dependabot-alerts read access (distinct from code-scanning access).
- `source="rest"` -- code scanning failed, often because GitHub Advanced Security is
  not enabled for the org, or the token lacks `security-events` access.

A CR stuck `Ready=False` with reason `CredentialsInvalid` means the Secret is missing
or lacks the key named by `tokenKey` / `appPrivateKeyKey`.

## Upgrade CRDs

CRDs are managed by the chart (`crds.enabled: true`) and updated by `helm upgrade`.
Because `crds.keep: true` sets `helm.sh/resource-policy: keep`, `helm uninstall` never
deletes the CRDs (and therefore never cascades to your custom resources).

To install CRDs out of band (GitOps managing them separately):

```sh
kubectl apply -f config/crd/bases/
helm upgrade ... --set crds.enabled=false
```

## Enable metrics scraping

- Per-exporter: set `spec.serviceMonitor: true` on the CR; the operator creates a
  ServiceMonitor selecting that exporter's Service (needs the prometheus-operator CRD).
- The operator's own metrics: `--set serviceMonitor.enabled=true` at install.

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
