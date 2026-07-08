# CRD reference

API group/version: **`scm.jalet.io/v1alpha1`**. Two kinds share a common
`ExporterSpec` (inlined) and `ExporterStatus`.

The operator discovers the target's repositories on `discoveryInterval` and dispatches one
run-once collection Job per repository (capped by `parallelism`); each Job pushes metrics
over OTLP. There is no long-running exporter Deployment and no scrape endpoint.

## Shared spec (`ExporterSpec`)

These fields are present on both `GitHubMetricsExporter` and `GitLabMetricsExporter`.

| Field | Type | Default | Notes |
|---|---|---|---|
| `discoveryInterval` | duration string | `15m` | How often the operator re-discovers repositories and re-dispatches Jobs. |
| `parallelism` | integer | `3` | Max concurrent collection Jobs (minimum 1). The rate-limit governor. |
| `image` | string | (operator image) | Override the collection-Job container image. |
| `resources` | ResourceRequirements | none | Collection-Job container compute resources. |
| `export.otlpEndpoint` | string | (required) | OTLP metrics endpoint the Jobs push to. Ephemeral Jobs cannot be scraped, so this is mandatory. |
| `autoDiscover.include` | RepoFilter | (empty = all) | Candidate repositories to collect. |
| `autoDiscover.exclude` | RepoFilter | (empty = none) | Repositories removed from the include set. |
| `findingDimensions` | `[]string` (enum `ecosystem`\|`tool`) | none | Optional extra labels on `scm_security_findings_open`. Off by default (raises cardinality). |
| `credentialsSecret.name` | string | (required) | Secret in the CR namespace holding the credentials. |

### `RepoFilter` (used by `autoDiscover.include` / `.exclude`)

| Field | Type | Notes |
|---|---|---|
| `topics` | `[]string` | Match repositories carrying any of these topics. |
| `visibility` | `[]string` (enum `public`\|`private`\|`internal`) | Match any of these visibilities. |
| `namePatterns` | `[]string` | Shell globs. GitHub matches the bare repo name; GitLab the full path. |
| `archived` | boolean | Match only archived (`true`) or only non-archived (`false`); unset matches both. |

Criteria within a filter are ANDed. `include` selects the candidate set (empty include
matches every repository); `exclude` then drops any repository it matches (empty exclude
removes nothing).

## `GitHubMetricsExporter`

Short name `ghme`. Spec = `ExporterSpec` plus:

| Field | Type | Default | Notes |
|---|---|---|---|
| `targetType` | enum `org`\|`user` | `org` | Discover an organization or a user account. |
| `org` | string | (required for `org`) | GitHub organization. |
| `user` | string | (required for `user`) | GitHub user. |
| `authMode` | enum `token`\|`app` | `token` | Credential type in `credentialsSecret`. |
| `tokenKey` | string | | Secret key holding a PAT (required when `authMode: token`). |
| `appID` | integer | | GitHub App ID (required when `authMode: app`). |
| `appInstallationID` | integer | | GitHub App installation ID (required when `authMode: app`). |
| `appPrivateKeyKey` | string | | Secret key holding the App private key PEM (required when `authMode: app`). |
| `codeScanningTool` | string | (all tools) | Filter code scanning alerts to one SARIF tool. |
| `collectWorkflows` | boolean | `false` | Collect recent GitHub Actions workflow-run metrics (`scm_workflow_runs_recent`). Opt-in: adds an API call per repo and extra cardinality. |
| `workflowLookback` | duration string | `168h` | How far back to count workflow runs (used when `collectWorkflows: true`). |

With `authMode: app`, each collection Job mints a repository-scoped installation token
(least privilege). Install one App per organization to give each CR its own rate budget.

**Validation (CEL, enforced by the API server):**

- `targetType: org` requires a non-empty `org`; `targetType: user` requires a non-empty `user`.
- `authMode: app` requires `appID > 0`, `appInstallationID > 0`, and a non-empty `appPrivateKeyKey`.
- `authMode: token` requires a non-empty `tokenKey`.

**Printer columns:** `Type`, `Org`, `User`, `Auth`, `Ready`, `Age`.

## `GitLabMetricsExporter`

Short name `glme`. Spec = `ExporterSpec` plus:

| Field | Type | Default | Notes |
|---|---|---|---|
| `targetType` | enum `group`\|`user` | `group` | Discover a group (including subgroups) or a user namespace. |
| `group` | string | (required for `group`) | GitLab group. |
| `user` | string | (required for `user`) | GitLab user namespace. |
| `tokenKey` | string | (required) | Secret key holding the GitLab access token. |
| `baseURL` | string | (gitlab.com) | API base URL for a self-hosted instance. |

**Validation (CEL):** `targetType: group` requires a non-empty `group`; `targetType: user`
requires a non-empty `user`.

**Printer columns:** `Type`, `Group`, `User`, `Ready`, `Age`.

Projects are keyed by `path_with_namespace`. Per-project vulnerability findings require the
GitLab Ultimate tier; open merge-request counts and posture work on all tiers (a project
without vulnerabilities access yields a partial snapshot). GitLab has no per-project token
scoping, so Jobs use the configured group/personal token.

## Status (`ExporterStatus`)

| Field | Type | Notes |
|---|---|---|
| `observedGeneration` | integer | `.metadata.generation` last reconciled. |
| `discoveredRepositories` | `[]string` | Repositories/projects found at the last successful discovery. |
| `lastDiscoveryTime` | timestamp | When discovery last succeeded. |
| `conditions` | `[]metav1.Condition` | Map-typed (keyed by `type`). |

Conditions set by the controller (type `Ready`):

| Reason | Status | Meaning |
|---|---|---|
| `Discovered` | True | Discovery succeeded; collection Jobs are dispatched. |
| `DiscoveryFailed` | False | Could not list repositories (check credentials/access). |
| `DispatchFailed` | False | Could not create collection Jobs. |
| `CredentialsInvalid` | False | The referenced Secret is missing or lacks the required key. |
