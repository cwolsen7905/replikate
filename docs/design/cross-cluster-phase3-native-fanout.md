# Design: Cross-Cluster Phase 3 — Native Fan-Out & Remote Drift

Status: **Proposed** · Target: **v1.3** (additive; opt-in per source)

## Summary

Phases 1–2 gave Replikate cross-cluster replication that places **one copy per
spoke**, in the source's namespace or a chosen one. Phase 3 makes cross-cluster
behave like Replikate does locally:

1. **Native selector fan-out** — evaluate the source's `sync` selector against
   each spoke's namespaces and copy into *all* matching ones, including
   namespaces created later.
2. **Remote drift correction** — self-heal a spoke copy that is hand-edited or
   deleted, the way the local managed-copy watch already does, instead of
   waiting for the periodic resync.

Both are additive and opt-in; existing `target-clusters` behavior is unchanged.

## Motivation

Fan-out to matching namespaces (including new ones) is Replikate's signature
behavior locally; cross-cluster currently loses it. And remote copies today only
re-sync when the source changes or on the ~2-minute credential-recheck resync
(the interim stand-in shipped in v1.2). Phase 3 closes both gaps, at the cost of
**watching each spoke** — the piece the earlier phases deliberately deferred.

## The core change: a cache per spoke

Phases 1–2 use a plain client per spoke (no cache, no informers). Fan-out-to-new
-namespaces and drift correction both require *watching* the spoke:

- a **Namespace** informer per spoke → a new/changed namespace re-drives the
  sources targeting that spoke in fan-out mode, and
- a **managed-copy** informer per spoke (ConfigMaps + Secrets carrying the
  managed-by label) → a remote copy edit/delete re-drives its source.

So each registered spoke gets a controller-runtime `cluster.Cluster` (its own
cache + client). To bound memory, the cache is **scoped**: all namespaces, but
only *managed* ConfigMaps/Secrets (a label selector on those types via
`cache.Options.ByObject`). Memory scales with (spokes × managed copies + spoke
namespaces).

Spoke RBAC already grants `watch` on namespaces/configmaps/secrets
(`deploy/spoke-rbac.yaml`), so no permission change is needed.

## Informer lifecycle

Tied to credential registration, extending the Phase 1 registry:

- **On register** (a credential passes vetting): build a `cluster.Cluster` for
  the spoke, start its cache under a per-spoke `context`, and add its Namespace
  and managed-copy caches as event **sources** to the running ConfigMap/Secret
  controllers via `controller.Controller.Watch(...)`. The registry stores the
  cancel func alongside the client.
- **On deregister** (credential removed / spoke unreachable past a threshold):
  cancel the per-spoke context, stopping its informers and ending the watch
  goroutines.

The dynamic `Watch()` on already-started controllers is the crux — the builder
wires static watches at setup, so the source controllers must keep a handle to
their underlying `controller.Controller` and add per-spoke sources at runtime.
(Verify: controller-runtime supports adding sources to a started controller; if
not, fall back to a single long-lived multi-cluster source fed by the registry.)

## Contract: opt-in via the namespace token

Reuse the Phase 2 `cluster[:namespace]` syntax, adding a `*` token that means
"fan out by the sync selector":

```
target-clusters: "spoke-a, spoke-b:shared, spoke-c:*"
#   spoke-a  -> one copy in the source's namespace           (Phase 2 default)
#   spoke-b  -> one copy in namespace "shared"               (Phase 2 override)
#   spoke-c  -> a copy in every namespace on spoke-c matching the sync selector
```

- Backward compatible: bare and `:namespace` entries behave exactly as today.
- `:*` fans out using the **same `sync` selector** the source already declares
  for local replication (shared selector; no separate remote selector for now).
- Exclusions (`--exclude-namespaces`) and the source's own-namespace skip apply
  per spoke, as locally.

## Remote drift correction

Once a managed-copy informer exists per spoke, drift correction applies to **all**
cross-cluster copies (every mode, not just `:*`): a remote copy that is edited or
deleted maps back to a reconcile of the hub source, which restores it — the exact
analog of `mapCopyToSource` locally. This supersedes the v1.2 resync stand-in;
the periodic recheck can then drop to a slower cadence or be removed.

## Phasing (within Phase 3)

| Slice | Deliverable |
|------|-------------|
| **3a — cache infrastructure** | Per-spoke `cluster.Cluster` + scoped cache, started/stopped on register/deregister; no behavior change yet. |
| **3b — remote drift correction** | Wire the managed-copy informer → enqueue the owning hub source; drop/relax the resync stand-in. |
| **3c — selector fan-out (`:*`)** | Namespace informer + fan-out into matching namespaces, prune on selector/namespace change. |

Each slice is independently reviewable and testable.

## Testing

- **Dual-envtest** already runs a real hub + spoke. Extend it:
  - **3b:** delete a spoke copy → it is restored promptly (not on resync).
  - **3c:** `spoke:*` populates matching namespaces on the spoke, including one
    created *after* the source; a namespace that stops matching is pruned.
- **Unit:** `:*` parsing/mode selection; fan-out target computation per spoke.

## Security & scale

- Memory scales with spokes × cached objects; the label-scoped cache keeps it to
  *our* copies plus namespaces. Document a rough budget and consider a cap on the
  number of registered spokes.
- Per-spoke informers mean N persistent watch connections from the hub; a flaky
  spoke should degrade (cache resync/backoff) without affecting others.
- The hub already holds write creds to every spoke (Phase 1); Phase 3 adds only
  read-watch load, no new privilege.

## Open questions

1. **Dynamic watch API** — confirm `controller.Controller.Watch` on a started
   controller is supported in the pinned controller-runtime; else use one
   registry-fed multiplexing source.
2. **Resync stand-in** — remove entirely once 3b lands, or keep a long-interval
   safety resync?
3. **Per-cluster selector** — `:*` reuses the local `sync` selector; is a
   distinct remote selector ever wanted, or is shared sufficient (as decided for
   Phase 2)?
4. **Namespace deletion on a spoke** — a `:*` copy in a deleted namespace is
   removed by Kubernetes GC; confirm no reconcile churn results.
