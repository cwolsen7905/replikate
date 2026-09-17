# Design note: dual annotation-domain support (non-breaking prefix migration)

> **Status:** Implemented · 2026-09-16 · shipped in 1.3.0 (branch
> `feat/dual-annotation-domain`). Motivated by moving the project under the
> `ubixsys` org and wanting the annotation/label key prefix to follow —
> **without breaking existing resources.**
>
> Implementation: `KeySet` in `internal/controller/keyset.go`; the primary/legacy
> split is threaded through `syncer.go`, `cross_cluster.go`, `webhook.go`,
> `registry.go`, `cluster_controller.go`, and `cmd/main.go`. The
> `replikate_copies_by_domain` gauge is published by `DomainCounter`
> (`internal/controller/domain_counter.go`). Tests: `keyset_test.go`.

## Goal

Move the default annotation/label key prefix from `replikate.brainchurts.com` to
`replikate.ubixsys.com` **without a flag-day cutover.** Replikate should honor
**both** prefixes during a transition, write the new one, and let the old one be
dropped in a future major release.

## Why not a hard cutover

The prefix is a *contract with every annotated resource*, not just a string:
- Sources are marked `<domain>/sync` (and `<domain>/target-clusters`).
- Managed copies are stamped `<domain>/managed-by`, `<domain>/origin-namespace`,
  `<domain>/origin-name`, `<domain>/origin-cluster`, and carry a `<domain>/finalizer`.

Flipping the default to the new domain in one release would make the controller
stop recognizing every existing source **and every copy it already manages** —
it would orphan or duplicate live Secret/ConfigMap copies and mis-handle
finalizers. That is a data-safety hazard, not a rename.

## Design

Introduce a **`KeySet`**: a `Primary Keys` (the new domain, used for **writes**)
plus `Legacy []Keys` (old domains, honored on **reads/matching**). The current
matching is already centralized on `Keys` methods, so the change is contained:

| Concern | Behavior |
| --- | --- |
| Source detection (`isSource`) | true if `<sync>` present under **primary or any legacy** |
| Selector value (`sync`, `target-clusters`) | read from whichever domain carries it (primary first, then legacy) |
| Copy recognition (`isManagedCopy`, `ownsCopy`) | match `managed-by`/origin labels under **primary or any legacy** — this is the data-safety-critical part: the controller must keep recognizing its *existing* copies |
| Finalizer | **add** the primary finalizer; **detect/remove** under primary **or any legacy** |
| Writes (`applyCopyMeta`) | stamp **primary** labels; strip the `sync` annotation under **all** domains; and remove legacy `managed-by`/origin labels so each reconcile **self-migrates** a copy to the new domain |

Net effect: existing resources keep working untouched; new/updated copies converge
to `replikate.ubixsys.com`; once traffic has fully migrated, a later major release
drops the legacy list.

## Config

- `--annotation-domain` — primary (default becomes `replikate.ubixsys.com`).
- `--previous-annotation-domain` / chart `previousAnnotationDomain` — the prior
  domain still honored for reads/matching/adoption (default
  `replikate.brainchurts.com`). Set empty to disable — the eventual end state.

## Data-safety requirements (from the cluster side)

The live cluster has **93 managed copies** (incl. production TLS secrets behind
ingresses) and 13 sources under the previous domain. A flag flip without adoption
would make all 93 fail `ownsCopy`, hit the "refusing to overwrite unmanaged" path,
and stop being updated — **including on cert renewal** — expiring silently weeks
later (exactly the incident that started the cluster's audit). So adoption is
mandatory, and:

- **In-place relabel only — never delete-then-recreate.** These are live secrets;
  a copy is adopted and its metadata rewritten to the primary domain on the next
  reconcile, never recreated.
- **Emit an `Adopted` event + log line per copy**, so 93 copies converging is
  observable, not inferred.
- `isAdoptable()` today recognizes only AppsCode config-syncer copies; previous-
  Replikate-domain adoption is a distinct path.

`CredentialLabel` (`<domain>/cluster-credential`, currently hardcoded) must be
derived from the `KeySet` too and matched under primary-or-legacy.

## Rollout

Non-breaking: the cluster deploys the new image whenever; old annotations keep
working; operators re-annotate at leisure (or let reconciles migrate copies).
No coordinated re-annotation required.

## Status of the work

Done (shipped in 1.3.0):

1. ✅ `KeySet` + methods in `internal/controller/keyset.go`; `CredentialLabel`
   derived from it (added to `Keys`).
2. ✅ Rewired `syncer.go`, `webhook.go`, `cross_cluster.go`, `registry.go`,
   `cluster_controller.go`, `cmd/main.go` from `Keys` → `KeySet`.
3. ✅ Tests (`keyset_test.go`): a source/copy annotated under the legacy domain is
   recognized, migrated, and finalized; a reconcile re-stamps a legacy copy to the
   primary domain and emits `Adopted`; a stale legacy-stamped copy is pruned; with
   the legacy list empty only the primary is honored.
4. ✅ Cluster-side refinements: convergence documented (self-migrates on the next
   source reconcile — source change, manager resync, or restart) and a completion
   signal added (`replikate_copies_by_domain` gauge via `DomainCounter`, plus
   `replikate_copies_migrated_total` counter).
5. ✅ Bumped to a new **minor** (1.3.0, additive/non-breaking). The legacy-drop
   (default `previousAnnotationDomain` → `""`, remove the honoring path) is the
   later **major**.

Follow-ups (not blocking the cluster deploy):

- Sweep README/examples prose to the new primary domain, noting the previous one
  is still honored.
- ubixsys-web docs refresh for Replikate (release convention).
