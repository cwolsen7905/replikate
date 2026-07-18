# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/cwolsen7905/replikate/commits/main
