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

## v0.4.0 — 1.0 readiness ✅

- ✅ **Source lookup via a field indexer** — a namespace change now resolves
  affected sources in O(sources) instead of scanning every object.
- ✅ **Validating admission webhook** — rejects a source whose sync selector
  doesn't parse at apply time, rather than only logging it during reconcile.

## v1.0.0 — Correctness & stability ✅

- ✅ **Integration tests** — an `envtest`-backed suite runs the real manager
  against a live API server, covering fan-out, drift restore, and
  indexer-driven fan-out to namespaces created after the source.
- ✅ **Same-name source conflict guard** — when two sources of the same name in
  different namespaces target one namespace, the copy's owner is honored: a
  copy belonging to a *different* source is left untouched and a `Conflict`
  event is emitted, so the first writer wins instead of a silent clobber war.
- ✅ **Live webhook smoke test** — a `kind` + cert-manager script
  (`make smoke-test`, `hack/webhook-smoke-test.sh`) and a CI job exercise the
  admission webhook end to end (real `ValidatingWebhookConfiguration` + CA
  injection over TLS) — the one path `envtest` can't model. Green in CI.

## Later / exploring 💡

- 🔭 **Cross-cluster replication** (hub-and-spoke) — replicate a source into
  selected namespaces of other clusters via a new, optional
  `target-clusters` annotation and labeled per-spoke credential Secrets.
  Additive (existing sources are unaffected), so it lands in a 1.x minor. See
  the design doc — [`docs/design/cross-cluster-replication.md`](docs/design/cross-cluster-replication.md) — for the topology,
  contract extension, and the four-phase plan (registry → fan-out → remote
  drift → webhook/metrics). The largest open item from the launch write-up.
- 💡 Replicate additional resource kinds beyond ConfigMaps and Secrets.

## 1.0 and the stable contract

As of **1.0.0**, the `<domain>/sync` annotation contract and Replikate's
replication behavior are considered **stable**: the annotation shape, the
managed-copy labels, and the fan-out/cleanup semantics won't change in a
backward-incompatible way without a `2.0`. Everything the 1.0 line required —
a real test suite, metrics, the field indexer, the selector webhook,
envtest integration tests, the same-name conflict guard, and a green live
webhook smoke test — is in place. Post-1.0 work (the 💡 items above) is
additive and won't break existing sources.
