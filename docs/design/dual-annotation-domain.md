# Design note: dual annotation-domain support (non-breaking prefix migration)

> **Status:** Proposed · 2026-09-13 · branch `feat/dual-annotation-domain`.
> Motivated by moving the project under the `ubixsys` org and wanting the
> annotation/label key prefix to follow — **without breaking existing resources.**

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
- `--legacy-annotation-domains` — comma-separated (default
  `replikate.brainchurts.com`). Set empty to disable legacy honoring (the eventual
  end state).

`CredentialLabel` (`<domain>/cluster-credential`, currently hardcoded) must be
derived from the `KeySet` too and matched under primary-or-legacy.

## Rollout

Non-breaking: the cluster deploys the new image whenever; old annotations keep
working; operators re-annotate at leisure (or let reconciles migrate copies).
No coordinated re-annotation required.

## Remaining work (this is a plan; code + tests to follow)

1. Add `KeySet` + methods in `internal/controller/replikate.go`; derive
   `CredentialLabel` from it.
2. Rewire `syncer.go`, `webhook.go`, `cross_cluster.go`, `registry.go`,
   `cluster_controller.go`, `cmd/main.go` from `Keys` → `KeySet`.
3. Tests: a source/copy annotated under the *legacy* domain is still recognized,
   updated, and finalized; a reconcile re-stamps a legacy copy to the primary
   domain; with the legacy list empty, only the primary is honored. Extend the
   existing controller test suite.
4. Docs/examples/README swept to the new primary; note legacy is still honored.
5. Bump to a new **minor** (additive, non-breaking); the legacy-drop is the later
   **major**.
