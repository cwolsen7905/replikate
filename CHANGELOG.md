# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Optional validating admission webhook (`--enable-webhook`, chart
  `webhook.enabled`) that rejects a ConfigMap/Secret whose sync-annotation
  selector doesn't parse, at apply time instead of only logging it during
  reconcile. The chart provisions the serving certificate via cert-manager and
  exempts the controller's own namespace and `kube-system` so a webhook outage
  can't wedge recovery.

### Changed

- Namespace changes now look up affected sources through a field index
  (`replikate.source`) instead of listing every ConfigMap/Secret in the
  cluster, so fan-out scales with the number of sources rather than the total
  object count.

## [0.3.0] - 2026-07-18

### Added

- Namespace exclusions via `--exclude-namespaces` (and the chart's
  `excludeNamespaces` value): listed namespaces never receive copies, regardless
  of a source's selector. Defaults to protecting `kube-system`, `kube-public`,
  and `kube-node-lease`; set to empty to exclude none.
- Prometheus metrics exposed on the existing metrics endpoint:
  `replikate_reconcile_total{kind,result}` and
  `replikate_copy_operations_total{kind,operation}`. The chart now ships a
  metrics `Service` and an optional `ServiceMonitor`
  (`metrics.serviceMonitor.enabled`).
- Chart hardening: optional `PodDisruptionBudget`
  (`podDisruptionBudget.enabled`), `priorityClassName`, and documented pod
  anti-affinity via the existing `affinity` value.
- Controller test suite (fake-client) covering fan-out, cluster-wide fan-out,
  config-syncer adoption, refusal to overwrite unmanaged objects, stale-copy
  removal, finalizer cleanup, drift correction, namespace exclusion, and metric
  recording.

## [0.2.0] - 2026-07-17

### Added

- Near-instant drift correction: managed copies are watched, so a copy that is
  hand-edited or deleted is restored right away instead of waiting for the
  periodic resync.
- Kubernetes Events on the source object — `Replicated` on change, `Skipped`
  when an unmanaged object blocks a copy, and `InvalidSelector` for a bad
  selector — visible via `kubectl describe`.
- Configurable annotation domain via `--annotation-domain` (and the chart's
  `annotationDomain` value), so Replikate can be reused under any domain instead
  of the hardcoded default.
- `CONTRIBUTING.md` and README status badges.

### Changed

- CI skips the image build for markdown/LICENSE-only changes.

## [0.1.0] - 2026-07-17

### Added

- Initial controller that replicates ConfigMaps and Secrets across namespaces
  based on the `replikate.brainchurts.com/sync` annotation (namespace label
  selector, or empty for all namespaces).
- Automatic fan-out to newly created namespaces that match a source's selector.
- Finalizer-based cleanup: copies are removed when the source is deleted or its
  sync annotation is removed.
- Adoption of AppsCode config-syncer (kubed) copies: objects carrying the
  `kubed.appscode.com/origin` marker are taken over in place, enabling a
  gap-free migration from config-syncer without deleting replicated data.
- Action logging — an entry is emitted whenever a copy is created, updated,
  adopted, or deleted.
- Helm chart (`charts/replikate`) with Deployment, ServiceAccount, ClusterRole,
  and ClusterRoleBinding.
- Multi-architecture container images (`linux/amd64`, `linux/arm64`) built and
  pushed to GHCR by GitHub Actions on every push to `main`.

### Changed

- Skip writes when a copy already matches its source, avoiding redundant API
  updates on every resync and keeping the logs meaningful.

### Security

- Runs as a distroless `nonroot` image with a read-only root filesystem, all
  Linux capabilities dropped, and least-privilege RBAC.

[Unreleased]: https://github.com/cwolsen7905/replikate/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/cwolsen7905/replikate/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/cwolsen7905/replikate/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/cwolsen7905/replikate/releases/tag/v0.1.0
