# Changelog

## [0.7.0](https://github.com/jalet/scm-metrics-exporter/compare/v0.6.1...v0.7.0) (2026-07-10)


### Features

* **chart:** add optional operator ServiceMonitor ([#36](https://github.com/jalet/scm-metrics-exporter/issues/36)) ([3e05267](https://github.com/jalet/scm-metrics-exporter/commit/3e05267a0a98094ed91aeeb1e3daf51acd09a476))
* pause dispatch when SCM rate-limit budget is low ([#35](https://github.com/jalet/scm-metrics-exporter/issues/35)) ([7a1082d](https://github.com/jalet/scm-metrics-exporter/commit/7a1082d6e5dce02c3e88f0c21ba2666ad9c3c094))

## [0.6.1](https://github.com/jalet/scm-metrics-exporter/compare/v0.6.0...v0.6.1) (2026-07-10)


### Bug Fixes

* **chart:** drop version-varying labels from valkey volumeClaimTemplates ([#33](https://github.com/jalet/scm-metrics-exporter/issues/33)) ([a4607df](https://github.com/jalet/scm-metrics-exporter/commit/a4607dfc95e65a679727d96da04c7dc4664bdf22))

## [0.6.0](https://github.com/jalet/scm-metrics-exporter/compare/v0.5.0...v0.6.0) (2026-07-10)


### Features

* close deferred metrics (severity, open-age, secret-scanning posture) ([#32](https://github.com/jalet/scm-metrics-exporter/issues/32)) ([f18f86b](https://github.com/jalet/scm-metrics-exporter/commit/f18f86b9f254f279ef9861dc8244911569d27b34))


### Bug Fixes

* **github:** detect ruleset-based default-branch protection ([#30](https://github.com/jalet/scm-metrics-exporter/issues/30)) ([eefad41](https://github.com/jalet/scm-metrics-exporter/commit/eefad412128b172a660a1b445225f3b1ff458c1c))

## [0.5.0](https://github.com/jalet/scm-metrics-exporter/compare/v0.4.3...v0.5.0) (2026-07-09)


### Features

* alert lifecycle metrics and Valkey-backed MTTR histogram ([#28](https://github.com/jalet/scm-metrics-exporter/issues/28)) ([a9b5653](https://github.com/jalet/scm-metrics-exporter/commit/a9b56530ba3f721ecfbb6d4238059c558b9323bc))

## [0.4.3](https://github.com/jalet/scm-metrics-exporter/compare/v0.4.2...v0.4.3) (2026-07-09)


### Bug Fixes

* **release:** don't fail the release on the code-scanning SARIF upload ([#26](https://github.com/jalet/scm-metrics-exporter/issues/26)) ([84e0731](https://github.com/jalet/scm-metrics-exporter/commit/84e0731375bafa1f960546d78975f8e3ff464478))

## [0.4.2](https://github.com/jalet/scm-metrics-exporter/compare/v0.4.1...v0.4.2) (2026-07-09)


### Bug Fixes

* **chart:** allow Helm's reserved `global` key in values.schema.json ([#24](https://github.com/jalet/scm-metrics-exporter/issues/24)) ([8424801](https://github.com/jalet/scm-metrics-exporter/commit/842480108b438b10c65be57789435449a4e34e98))
* **release:** sync appVersion via release-please yaml jsonpath updater ([#23](https://github.com/jalet/scm-metrics-exporter/issues/23)) ([c58ffaa](https://github.com/jalet/scm-metrics-exporter/commit/c58ffaae02cf0cc6397f33f27b7da854975aba3b))

## [0.4.1](https://github.com/jalet/scm-metrics-exporter/compare/v0.4.0...v0.4.1) (2026-07-09)


### Bug Fixes

* **chart:** keep appVersion in sync with version ([#18](https://github.com/jalet/scm-metrics-exporter/issues/18)) ([2f296e9](https://github.com/jalet/scm-metrics-exporter/commit/2f296e909e66d4ff762448315baed8ababc64d10))
* **operator:** make the mounted GitHub App key readable by the non-root Job ([#20](https://github.com/jalet/scm-metrics-exporter/issues/20)) ([a1689b8](https://github.com/jalet/scm-metrics-exporter/commit/a1689b849a6ee4df90beb30450a12a45a66e55b9))
* **operator:** route klog through the controller-runtime logger ([#19](https://github.com/jalet/scm-metrics-exporter/issues/19)) ([0a7933d](https://github.com/jalet/scm-metrics-exporter/commit/0a7933dc15194fdc887f654dff0fd0e9aa1c296b))

## [0.4.0](https://github.com/jalet/scm-metrics-exporter/compare/v0.3.0...v0.4.0) (2026-07-08)


### Features

* discovery + dispatched per-repo OTLP collection ([#16](https://github.com/jalet/scm-metrics-exporter/issues/16)) ([1050917](https://github.com/jalet/scm-metrics-exporter/commit/10509173fac112c838172c5c70961b64ed4030e7))
* expose repository security posture as scm_repo_info (GitHub + GitLab) ([#15](https://github.com/jalet/scm-metrics-exporter/issues/15)) ([4b6ed24](https://github.com/jalet/scm-metrics-exporter/commit/4b6ed24e3213283c567199b016e2dfc77287385a))
* **github:** add secret-scanning findings (category=secret) ([#12](https://github.com/jalet/scm-metrics-exporter/issues/12)) ([cbdf746](https://github.com/jalet/scm-metrics-exporter/commit/cbdf746516127ccbb3f59910cd75ca1648b7400b))
* **operator:** always-on validating admission webhook (cert-manager) ([#14](https://github.com/jalet/scm-metrics-exporter/issues/14)) ([9049005](https://github.com/jalet/scm-metrics-exporter/commit/9049005096ab1f63590aa0e1b090751fd6e2afcb))
* optional finding dimensions (ecosystem, tool) ([#13](https://github.com/jalet/scm-metrics-exporter/issues/13)) ([6211036](https://github.com/jalet/scm-metrics-exporter/commit/621103618a41384867be7344b9ada8b486164894))
* support user (non-org) targets for GitHub and GitLab ([#10](https://github.com/jalet/scm-metrics-exporter/issues/10)) ([7e7d905](https://github.com/jalet/scm-metrics-exporter/commit/7e7d9056fae3891864e32733cd64de2671e9634e))

## [0.3.0](https://github.com/jalet/scm-metrics-exporter/compare/v0.2.0...v0.3.0) (2026-07-08)


### Features

* **exporter:** add poll-lifecycle and per-source logging ([#9](https://github.com/jalet/scm-metrics-exporter/issues/9)) ([3f4df7c](https://github.com/jalet/scm-metrics-exporter/commit/3f4df7cbb2c34fdcc2d8473a888bd5134643d859))
* **metrics:** set service.name and service.version on the resource ([#8](https://github.com/jalet/scm-metrics-exporter/issues/8)) ([70859a6](https://github.com/jalet/scm-metrics-exporter/commit/70859a6b8c3f71bf7543941d687190b85cd77e74))
* **metrics:** stamp otel_scope_version with the build version ([#6](https://github.com/jalet/scm-metrics-exporter/issues/6)) ([f0b4c14](https://github.com/jalet/scm-metrics-exporter/commit/f0b4c1410f7718acb94240ee84f72d04e4774f2f))

## [0.2.0](https://github.com/jalet/scm-metrics-exporter/compare/v0.1.0...v0.2.0) (2026-07-07)


### Features

* **chart:** Helm 4 operator chart with gated CRDs and RBAC ([43c9992](https://github.com/jalet/scm-metrics-exporter/commit/43c9992e0af02065b0654fe60824ea5a558993a5))
* **collector:** poller and thread-safe snapshot cache ([5c45ce2](https://github.com/jalet/scm-metrics-exporter/commit/5c45ce27bbfba0beb60423687fe9791f049c05f1))
* **exporter:** wire config, provider, collector, and metrics ([7d2ade3](https://github.com/jalet/scm-metrics-exporter/commit/7d2ade37470ba2ed65532c715d79af7235bbc776))
* **github:** GitHub provider (GraphQL + REST + auth) ([eea742c](https://github.com/jalet/scm-metrics-exporter/commit/eea742c16e5c6c41ec517ecdafeaddbeeffe3a3a))
* **gitlab:** GitLab provider (open MRs + vulnerability findings) ([600d31d](https://github.com/jalet/scm-metrics-exporter/commit/600d31de0d0b5cd2e2626df32f14f2e2a9603eef))
* **metrics:** OTel dual-exporter pipeline ([60a33ad](https://github.com/jalet/scm-metrics-exporter/commit/60a33ada8fba025881657929bb49da81c8966626))
* **operator:** GitHubMetricsExporter and GitLabMetricsExporter CRDs ([a102329](https://github.com/jalet/scm-metrics-exporter/commit/a102329da6b3426441075c4028ee0f413b906e47))
* **operator:** GitLab reconciler + provider-neutral rendering ([a10a760](https://github.com/jalet/scm-metrics-exporter/commit/a10a760f6d3427fdf87c7fe529e23d1df9129730))
* **operator:** reconcile GitHubMetricsExporter into an exporter Deployment ([26da904](https://github.com/jalet/scm-metrics-exporter/commit/26da904d488ad32b8096b427ba974b7d2db89102))
* **operator:** render a per-CR ServiceMonitor, gated on the CRD ([62c26b5](https://github.com/jalet/scm-metrics-exporter/commit/62c26b51e5013aaa9be5b9d11060a4e934b4e28a))
* **operator:** scaffold controller-manager, scheme, and generation tooling ([2ddc86e](https://github.com/jalet/scm-metrics-exporter/commit/2ddc86e6a885d32b4134d7271bda9fd93d1bf81b))
* **provider:** define Provider interface and domain types ([680117c](https://github.com/jalet/scm-metrics-exporter/commit/680117c11e7ed8c5475c02596d1d1b6257b93861))
* scaffold binary entrypoints, package layout, and README ([233ddf9](https://github.com/jalet/scm-metrics-exporter/commit/233ddf91dd42ccdf0493b0d30e58392647725aa8))
