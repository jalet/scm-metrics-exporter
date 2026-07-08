# CRD reference

API group/version: **`scm.jalet.io/v1alpha1`**. Two kinds share a common
`ExporterSpec` (inlined) and `ExporterStatus`.

## Shared spec (`ExporterSpec`)

These fields are present on both `GitHubMetricsExporter` and `GitLabMetricsExporter`.

| Field | Type | Default | Notes |
|---|---|---|---|
| `pollInterval` | duration string | `5m` | How often the exporter polls the provider. |
| `image` | string | (operator image) | Override the exporter container image. |
| `replicas` | integer | `1` | Exporter pod count (minimum 1). |
| `resources` | ResourceRequirements | none | Exporter container compute resources. |
| `export.exporter` | enum `prometheus`\|`otlp` | `prometheus` | OTel metrics backend. |
| `export.otlpEndpoint` | string | none | OTLP endpoint, used when `exporter: otlp`. |
| `serviceMonitor` | boolean | `false` | Operator creates a ServiceMonitor for the exporter (requires the prometheus-operator CRD). |
| `findingDimensions` | `[]string` (enum `ecosystem`\|`tool`) | none | Optional extra labels on `scm_security_findings_open`. Off by default (raises cardinality). |
| `credentialsSecret.name` | string | (required) | Secret in the CR namespace holding the credentials. |

## `GitHubMetricsExporter`

Short name `ghme`. Spec = `ExporterSpec` plus:

| Field | Type | Default | Notes |
|---|---|---|---|
| `targetType` | enum `org`\|`user` | `org` | Poll an organization or a user account. |
| `org` | string | (required for `org`) | GitHub organization to poll. |
| `user` | string | (required for `user`) | GitHub user to poll; code scanning is gathered per-repository. |
| `authMode` | enum `token`\|`app` | `token` | Credential type in `credentialsSecret`. |
| `tokenKey` | string | | Secret key holding a PAT (required when `authMode: token`). |
| `appID` | integer | | GitHub App ID (required when `authMode: app`). |
| `appInstallationID` | integer | | GitHub App installation ID (required when `authMode: app`). |
| `appPrivateKeyKey` | string | | Secret key holding the App private key PEM (required when `authMode: app`). |
| `codeScanningTool` | string | (all tools) | Filter code scanning alerts to one SARIF tool. |

**Validation (CEL, enforced by the API server):**

- `targetType: org` requires a non-empty `org`; `targetType: user` requires a non-empty `user`.
- `authMode: app` requires `appID > 0`, `appInstallationID > 0`, and a non-empty `appPrivateKeyKey`.
- `authMode: token` requires a non-empty `tokenKey`.

**Printer columns:** `Type`, `Org`, `User`, `Auth`, `Ready`, `Age`.

## `GitLabMetricsExporter`

Short name `glme`. Spec = `ExporterSpec` plus:

| Field | Type | Default | Notes |
|---|---|---|---|
| `targetType` | enum `group`\|`user` | `group` | Poll a group or a user namespace. |
| `group` | string | (required for `group`) | GitLab group to poll. |
| `user` | string | (required for `user`) | GitLab user namespace to poll (merge-request counts only). |
| `tokenKey` | string | (required) | Secret key holding the GitLab access token. |
| `baseURL` | string | (gitlab.com) | API base URL for a self-hosted instance. |

**Validation (CEL):** `targetType: group` requires a non-empty `group`; `targetType: user` requires a non-empty `user`.

**Printer columns:** `Type`, `Group`, `User`, `Ready`, `Age`.

Note: vulnerability findings require the GitLab Ultimate tier and are group-scoped; open
merge-request counts work on all tiers. For a **group** target, when findings are
unavailable the exporter records a scrape error and still reports MR counts. A **user**
target has no vulnerabilities API, so the exporter reports MR counts only and skips
findings entirely (no scrape error).

## Status (`ExporterStatus`)

| Field | Type | Notes |
|---|---|---|
| `observedGeneration` | integer | `.metadata.generation` last reconciled. |
| `conditions` | `[]metav1.Condition` | Map-typed (keyed by `type`). |

Conditions set by the controller (type `Ready`):

| Reason | Status | Meaning |
|---|---|---|
| `DeploymentAvailable` | True | Exporter Deployment is available. |
| `DeploymentProgressing` | False | Waiting for the Deployment to become available. |
| `CredentialsInvalid` | False | The referenced Secret is missing or lacks the required key. |
