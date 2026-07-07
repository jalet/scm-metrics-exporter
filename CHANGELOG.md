# Changelog

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
