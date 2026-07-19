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

## v0.5.0 — Correctness & 1.0 hardening 🚧 (next)

- ✅ **Integration tests** — an `envtest`-backed suite runs the real manager
  against a live API server, covering fan-out, drift restore, and
  indexer-driven fan-out to namespaces created after the source.
- ✅ **Same-name source conflict guard** — when two sources of the same name in
  different namespaces target one namespace, the copy's owner is honored: a
  copy belonging to a *different* source is left untouched and a `Conflict`
  event is emitted, so the first writer wins instead of a silent clobber war.
- 🔭 **Live webhook smoke test** — exercise the admission webhook end to end on
  a `kind` cluster with cert-manager (real `ValidatingWebhookConfiguration` +
  CA injection), the one path `envtest` can't model, before tagging `1.0`.

## Later / exploring 💡

- 💡 **Cross-cluster replication** via kubeconfig contexts — the largest open
  item from the launch write-up; deliberately post-`1.0` because it expands the
  annotation contract.
- 💡 Replicate additional resource kinds beyond ConfigMaps and Secrets.

## Toward 1.0

`1.0.0` means the annotation contract and behavior are considered stable, with
meaningful test coverage and the observability pieces above in place. The test
suite, metrics, field indexer, selector webhook, integration tests, and the
same-name conflict guard are in place; the one remaining gate is a **live
webhook smoke test**, after which the `<domain>/sync` contract can be declared
frozen. Until then, `0.x` releases may change behavior between minor versions.
