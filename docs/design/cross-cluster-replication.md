# Design: Cross-Cluster Replication

Status: **Proposed** · Target: **v1.x** (additive; does not require a 2.0)

## Summary

Today Replikate replicates ConfigMaps and Secrets across namespaces **within a
single cluster**. This document proposes replicating them **across clusters**: a
source in one cluster is copied into selected namespaces of one or more other
clusters, kept in sync the same way intra-cluster copies are.

The feature is designed to be **additive**: a source with no cross-cluster
annotation behaves exactly as it does today, so the frozen `<domain>/sync`
contract is preserved and this can ship in a 1.x minor.

## Motivation

The single most requested follow-up from Replikate's launch. Real fleets run
more than one cluster (prod/staging, per-region, per-tenant) and hit the same
wall the project already solves intra-cluster — a registry pull secret, a CA
bundle, a wildcard TLS cert, or a shared config block is needed in *every*
cluster, and there's no native way to keep it in sync. Copying it by hand across
clusters is the same push-once-and-rot problem, one level up.

## Non-goals

- Bidirectional / active-active replication. Replication is one-way from a
  source cluster to target clusters.
- Conflict resolution between clusters beyond the existing ownership model.
- Replicating cluster-scoped objects, or kinds beyond ConfigMaps/Secrets (that's
  a separate roadmap item and composes with this one).
- A service mesh or network tunnel. Replikate uses the Kubernetes API of each
  target cluster; connectivity to those API servers is the operator's problem.

## Topology: hub-and-spoke (push)

The controller runs in one **hub** cluster and **pushes** copies out to **spoke**
clusters. The source object lives in the hub; the hub holds credentials to the
spokes and writes copies into them.

```
                +------------------- hub cluster -------------------+
                |  Replikate manager                                |
   source  -->  |    - watches sources + namespaces (hub)           |
  (hub ns)      |    - holds spoke clients (from credential Secrets) |
                +---------------------------------------------------+
                        |                         |
                        v                         v
                 +-- spoke-a --+           +-- spoke-b --+
                 | copies into |           | copies into |
                 | matching ns |           | matching ns |
                 +-------------+           +-------------+
```

Rejected alternative — **controller-per-cluster pull**: each cluster runs a
Replikate that pulls from a designated source cluster. More moving parts, N
controllers to upgrade, and a harder security story (every cluster needs read
creds to the source). Hub-and-spoke keeps one control point and one place that
holds credentials.

## Cluster registry

Spokes are registered by dropping a **credential Secret** in the controller's
namespace:

- Labeled `replikate.brainchurts.com/cluster-credential=true` so Replikate
  discovers it by a label selector (and can watch for add/remove).
- The Secret's `metadata.name` is the **cluster id** referenced by sources.
- Data holds a kubeconfig (`kubeconfig` key) or a server URL + CA + token.

On startup and on credential-Secret changes, Replikate builds a client (and, for
Phase 3, a cache) per spoke, and emits `ClusterConnected` / `ClusterUnreachable`
events plus a `replikate_cluster_up{cluster}` gauge.

**Spoke RBAC**: each spoke needs a ServiceAccount whose token is in the
credential Secret, bound to a ClusterRole that can `list` Namespaces and
`get/list/create/update/delete` ConfigMaps and Secrets. The chart ships these as
a separate, applies-on-the-spoke manifest (`charts/replikate/spoke-rbac` or a
`helm template` sub-output).

## Contract extension (additive)

One new **optional** annotation on the source:

```yaml
metadata:
  annotations:
    replikate.brainchurts.com/sync: "team=web"          # existing: namespace selector
    replikate.brainchurts.com/target-clusters: "spoke-a,spoke-b"   # new; "*" = all registered
```

- **Absent** → today's behavior exactly: replicate within the local (hub)
  cluster only. No existing source changes behavior.
- **Present** → the namespace selector is evaluated **independently in each
  named target cluster**, and copies are written there. Whether the hub itself
  is also a target (include/exclude local) is controlled by a keyword such as
  `self` in the list, defaulting to *not* copying locally when `target-clusters`
  is set unless `self` is included. (Open question — see below.)

### New label

Copies gain an **origin-cluster** label alongside the existing origin-namespace /
origin-name labels:

```
replikate.brainchurts.com/origin-cluster: <hub-cluster-id>
```

This is required so cleanup (`deleteCopies`) and the same-name conflict guard
(`ownsCopy`) remain correct when copies from different source clusters could land
in the same target namespace. This is the one contract addition, and it's on the
*copies*, not the source, so it doesn't change how users annotate sources.

## Reconcile changes

`reconcileSource` gains an outer loop over target clusters:

```
for each target cluster T in resolve(target-clusters annotation):
    client_T = registry.clientFor(T)         // skip + event if unreachable
    namespaces_T = client_T.List(Namespaces) // apply selector + exclusions
    for ns in matched:
        upsertCopy(client_T, src, ns)          // stamps origin-cluster = hub id
    deleteCopies(client_T, src, keep=matched)  // per-cluster cleanup, origin-scoped
```

Key properties to preserve:

- **Partial failure is isolated.** An unreachable spoke logs, emits an event, and
  is retried on the next reconcile; it never blocks other spokes or the local
  path.
- **Ownership still holds.** `upsertCopy` / `ownsCopy` compare the full origin
  triple (cluster, namespace, name), so a spoke copy owned by a different source
  cluster is left alone with a `Conflict` event, exactly as intra-cluster.
- **Exclusions apply per cluster.** `--exclude-namespaces` protects system
  namespaces in every target cluster.

## Drift correction across clusters

Intra-cluster, drift is corrected near-instantly by watching managed copies. For
spokes this requires a **managed-copy informer per spoke** that maps a remote
edit/delete back to a reconcile of the hub source. That's the expensive part
(one cache/informer per spoke; memory scales with spoke count), so it's phased
separately. Until Phase 3, remote copies are corrected on the **periodic resync**
and on any source change — a documented, honest limitation of early cross-cluster
support.

## Phasing

| Phase | Deliverable | Notes |
|------|-------------|-------|
| **1. Cluster registry** | Discover spokes from labeled credential Secrets; build/cache clients; connectivity events + gauge; spoke RBAC manifests. | No replication behavior change yet. Independently testable. |
| **2. Cross-cluster fan-out (resync-only)** | `target-clusters` annotation; per-cluster fan-out, upsert, and cleanup; `origin-cluster` label; partial-failure isolation. | Delivers working cross-cluster replication. Remote drift via resync only. |
| **3. Remote drift correction** | Per-spoke managed-copy informer mapping back to the hub source. | The costly bit; makes remote copies self-heal like local ones. |
| **4. Webhook + metrics + packaging** | Validate `target-clusters` against the registry in the webhook; `cluster` label on metrics; Helm values to register spokes; docs + examples. | Polish and safety. |

Phases 1–2 alone are a shippable, useful cross-cluster feature.

## Testing strategy

- **Unit** (fake clients): a fake per-cluster client map exercises fan-out,
  per-cluster cleanup, origin-cluster ownership, and partial-failure isolation.
- **envtest**: two envtest control planes stood up in one test act as hub +
  spoke; assert a source in the hub populates the spoke and that ownership across
  clusters is respected.
- **Live smoke**: extend the kind smoke test to two kind clusters with a
  credential Secret wiring hub → spoke.

## Security considerations

- The hub becomes a high-value target: it holds write credentials to every
  spoke. Credential Secrets should be least-privilege (only the needed verbs on
  ConfigMaps/Secrets/Namespaces) and rotatable without a restart (registry
  re-reads on Secret change).
- Blast radius: a compromised hub can write into every spoke. Document scoping
  the spoke ServiceAccount tightly and consider a per-spoke namespace allowlist.
- The validating webhook (Phase 4) rejects a `target-clusters` that names an
  unregistered cluster, so typos fail at apply time rather than silently.

## Open questions

1. **Local inclusion semantics** — when `target-clusters` is set, is the hub a
   target by default, or only if `self`/the hub id is listed? Leaning: exclude
   local unless explicitly listed, so "replicate to other clusters" is the
   obvious reading.
2. **Cluster id source of truth** — credential-Secret name (proposed) vs. an
   explicit field inside the Secret. Name is simpler; a field allows renaming
   without recreating the Secret.
3. **Selector reuse** — reuse the same namespace selector for every target
   cluster (proposed) vs. per-cluster selectors. Start with shared; revisit if
   asked.
4. **Resync cadence** — is the existing periodic resync frequent enough to be an
   acceptable drift window for Phase 2, or does Phase 3 need to land together?
