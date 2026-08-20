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

- 🚧 **Cross-cluster replication** (hub-and-spoke) — replicate a source into
  selected namespaces of other clusters via a new, optional
  `target-clusters` annotation and labeled per-spoke credential Secrets.
  Additive (existing sources are unaffected), so it lands in a 1.x minor. See
  the design doc — [`docs/design/cross-cluster-replication.md`](docs/design/cross-cluster-replication.md) — for the topology,
  contract extension, and the four-phase plan. The largest open item from the
  launch write-up.
  - ✅ **Phase 1 — cluster registry** (`--enable-cross-cluster`): discover
    spokes from labeled credential Secrets, build a client per spoke, health
    check + `replikate_cluster_up` gauge, spoke RBAC in `deploy/spoke-rbac.yaml`.
  - 🚧 **Phase 2 — cross-cluster fan-out.** Decided to mirror config-syncer:
    `target-clusters` is additive remote clusters, and each remote gets one copy
    in the **source's own namespace** (no remote selector fan-out).
    - ✅ **Pass 1 — parity:** `target-clusters` annotation; one copy per targeted
      spoke in the source namespace; prunes de-listed spokes and cleans up on
      delete; best-effort per spoke (a down spoke never blocks the local path).
    - ✅ **Pass 2 — safety:** reject a credential pointing at the hub's own API
      server (removes the self-as-spoke data-loss risk); requeue on partial
      spoke failure so transient errors self-heal; per-cluster
      `replikate_remote_copy_operations_total` metric; 10s spoke request timeout.
    - ✅ **Dual-envtest** — an integration test runs the real manager against
      two separate control planes and asserts a source in the hub replicates
      into the spoke, prunes on target drop, and cleans up on delete.
    - ✅ **Robust self-cluster identity** — the self-cluster guard now compares
      the spoke's `kube-system` UID against the hub's, immune to the same
      cluster being reached via a different URL (the old API-host check).
    - ✅ **Multi-hub coexistence** — cross-cluster copies are stamped with an
      `origin-cluster` label (the hub's cluster UID); pruning and the conflict
      guard are scoped to it, so two hubs can target one spoke without deleting
      or clobbering each other's copies.
    - ✅ **Prompt fan-out** — when a spoke registers, the credential reconciler
      notifies the source controllers (per-kind channel) to re-drive sources
      targeting it, so a new spoke fans out at once instead of on a requeue. An
      unresolved (typo) target no longer requeue-polls forever.
    - ✅ **Per-target namespace override** — a `target-clusters` entry may be
      `cluster:namespace` to place that spoke's copy in a chosen namespace
      instead of the source's; changing it prunes the copy from the old
      namespace.
  - 🚧 **Phase 3 — native fan-out + remote drift correction** — a cache per
    spoke enables selector fan-out into matching namespaces (opt-in via a `:*`
    target-clusters token, reusing the `sync` selector) and self-healing of
    remote copies, superseding the v1.2 resync stand-in. See the design doc —
    [`docs/design/cross-cluster-phase3-native-fanout.md`](docs/design/cross-cluster-phase3-native-fanout.md) — for the
    per-spoke informer lifecycle and the three build slices (cache
    infrastructure → remote drift → selector fan-out). The heaviest remaining
    cross-cluster lift.
  - 🔭 **Phase 4 — webhook + metrics + packaging.**
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
