# Roadmap

This roadmap is directional, not a set of commitments — priorities may shift.
Items are grouped into milestones roughly in the order they're likely to land.
See [CHANGELOG.md](CHANGELOG.md) for what has actually shipped.

Legend: ✅ done · 🚧 in progress · 🔭 planned · 💡 exploring

## v0.1.0 — MVP ✅

- ✅ Replicate ConfigMaps and Secrets across namespaces via the
  `sync` annotation (label selector or cluster-wide).
- ✅ Fan out to newly created matching namespaces automatically.
- ✅ Finalizer-based cleanup on source or annotation removal.
- ✅ In-place adoption of AppsCode config-syncer copies (gap-free migration).
- ✅ Action logging with no-op write suppression.
- ✅ Helm chart and multi-arch (amd64/arm64) images published by CI.

## v0.2.0 — Reliability & reusability ✅

- ✅ **Instant drift correction** — watch managed copies and restore a deleted
  or hand-edited replica immediately, instead of waiting for the periodic
  resync.
- ✅ **Kubernetes Events on the source** — surface "replicated to N namespaces"
  and errors via `kubectl describe`, not just controller logs.
- ✅ **Configurable annotation domain** — a flag/Helm value so the project is
  reusable under any domain, not hardcoded to one.

## v0.3.0 — Observability & safety ✅

- ✅ **Prometheus metrics** (reconciles by result, copy operations by type) plus
  a chart `Service` and optional `ServiceMonitor`.
- ✅ **Namespace exclusions** — `--exclude-namespaces`, defaulting to protect
  system namespaces.
- ✅ **Test suite** — controller tests covering fan-out, adoption, cleanup, and
  drift before a `1.0` line.
- ✅ **Chart hardening** — `PodDisruptionBudget`, `priorityClassName`, and node
  anti-affinity options.

## v0.4.0 — 1.0 readiness 🚧 (next)

- ✅ **Source lookup via a field indexer** — a namespace change now resolves
  affected sources in O(sources) instead of scanning every object.
- ✅ **Validating admission webhook** — rejects a source whose sync selector
  doesn't parse at apply time, rather than only logging it during reconcile.

## Later / exploring 💡

- 💡 **Cross-cluster replication** via kubeconfig contexts.
- 💡 Replicate additional resource kinds beyond ConfigMaps and Secrets.

## Toward 1.0

`1.0.0` means the annotation contract and behavior are considered stable, with
meaningful test coverage and the observability pieces above in place. With the
test suite, metrics, field indexer, and selector webhook now in place, the
remaining step is declaring the `<domain>/sync` contract frozen. Until then,
`0.x` releases may change behavior between minor versions.
