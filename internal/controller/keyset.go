package controller

import (
	"maps"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// KeySet is the annotation/label contract Replikate honors: a Primary domain
// used for all *writes*, plus zero or more Legacy domains honored only for
// *reads and matching*. It lets the key prefix move (e.g. from
// replikate.brainchurts.com to replikate.ubixsys.com) without a flag-day
// cutover: existing sources and copies stamped under a legacy domain keep being
// recognized, and every write self-migrates a copy to the primary domain, so a
// later major release can drop the legacy list once traffic has converged.
//
// See docs/design/dual-annotation-domain.md for the rationale (a hard cutover
// would orphan every copy the controller already manages — a data-safety
// hazard, not a rename).
type KeySet struct {
	// Primary is the domain used for writes: new copies are stamped with it, the
	// finalizer added is its finalizer, and the sync annotation written is its.
	Primary Keys
	// Legacy holds prior domains still honored for reads/matching/adoption. A
	// source or copy stamped under any of these is recognized and, on the next
	// write, migrated to Primary. Empty means the primary domain only.
	Legacy []Keys
}

// NewKeySet builds a KeySet from a primary domain and any number of legacy
// domains. Blank legacy entries, and any legacy entry equal to the primary, are
// dropped so the set never lists a domain twice.
func NewKeySet(primaryDomain string, legacyDomains ...string) KeySet {
	ks := KeySet{Primary: NewKeys(primaryDomain)}
	for _, d := range legacyDomains {
		if d = strings.TrimSpace(d); d == "" || d == primaryDomain {
			continue
		}
		ks.Legacy = append(ks.Legacy, NewKeys(d))
	}
	return ks
}

// each returns the primary domain's keys followed by every legacy domain's, so
// callers can look a resource up under the primary first and legacy after.
func (ks KeySet) each() []Keys {
	return append([]Keys{ks.Primary}, ks.Legacy...)
}

// keysFor returns the domain a managed copy is stamped under (the one whose
// managed-by label is set), preferring the primary. ok is false when the object
// is not a Replikate-managed copy under any domain.
func (ks KeySet) keysFor(copy client.Object) (k Keys, ok bool) {
	ls := copy.GetLabels()
	if ls[ks.Primary.ManagedByLabel] == ManagedByValue {
		return ks.Primary, true
	}
	for _, lk := range ks.Legacy {
		if ls[lk.ManagedByLabel] == ManagedByValue {
			return lk, true
		}
	}
	return Keys{}, false
}

// isSource reports whether obj carries a sync annotation under any domain.
func (ks KeySet) isSource(obj client.Object) bool {
	ann := obj.GetAnnotations()
	for _, k := range ks.each() {
		if _, ok := ann[k.SyncAnnotation]; ok {
			return true
		}
	}
	return false
}

// syncValue returns the sync-selector value and the annotation key it was found
// under (primary first, then legacy), so a source annotated under a legacy
// domain is still driven and the caller can report the exact key.
func (ks KeySet) syncValue(ann map[string]string) (key, value string, ok bool) {
	for _, k := range ks.each() {
		if v, found := ann[k.SyncAnnotation]; found {
			return k.SyncAnnotation, v, true
		}
	}
	return "", "", false
}

// targetClustersValue returns the target-clusters annotation value and whether
// it is present under any domain.
func (ks KeySet) targetClustersValue(ann map[string]string) (value string, ok bool) {
	for _, k := range ks.each() {
		if v, found := ann[k.TargetClustersAnnotation]; found {
			return v, true
		}
	}
	return "", false
}

// isManagedCopy reports whether obj is a Replikate-managed copy under any domain.
func (ks KeySet) isManagedCopy(obj client.Object) bool {
	_, ok := ks.keysFor(obj)
	return ok
}

// ownsCopy reports whether the managed copy belongs to src, matching its origin
// labels under whichever domain the copy is stamped.
func (ks KeySet) ownsCopy(copy, src client.Object, originCluster string) bool {
	k, ok := ks.keysFor(copy)
	if !ok {
		return false
	}
	return k.ownsCopy(copy, src, originCluster)
}

// ownerRef returns a copy's origin namespace/name/cluster labels (read under the
// domain it is stamped under), for conflict diagnostics.
func (ks KeySet) ownerRef(copy client.Object) (ns, name, cluster string) {
	k, ok := ks.keysFor(copy)
	if !ok {
		return "", "", ""
	}
	ls := copy.GetLabels()
	return ls[k.OriginNSLabel], ls[k.OriginNameLabel], ls[k.OriginClusterLabel]
}

// originCluster returns the origin-cluster label of a managed copy, read under
// the domain it is stamped under (empty for local or unstamped copies).
func (ks KeySet) originCluster(copy client.Object) string {
	k, ok := ks.keysFor(copy)
	if !ok {
		return ""
	}
	return copy.GetLabels()[k.OriginClusterLabel]
}

// copyOrigin returns a managed copy's source namespace and name (read under the
// domain it is stamped under). ok is false when the object is not a managed copy
// or is missing its origin labels.
func (ks KeySet) copyOrigin(obj client.Object) (ns, name string, ok bool) {
	k, found := ks.keysFor(obj)
	if !found {
		return "", "", false
	}
	ls := obj.GetLabels()
	ns, name = ls[k.OriginNSLabel], ls[k.OriginNameLabel]
	return ns, name, ns != "" && name != ""
}

// legacyDomainOf returns the legacy domain a copy is stamped under, or "" if it
// is stamped under the primary domain (or not managed). A non-empty result marks
// a copy that the next write will migrate to the primary domain.
func (ks KeySet) legacyDomainOf(copy client.Object) string {
	ls := copy.GetLabels()
	if ls[ks.Primary.ManagedByLabel] == ManagedByValue {
		return ""
	}
	for _, k := range ks.Legacy {
		if ls[k.ManagedByLabel] == ManagedByValue {
			return k.Domain
		}
	}
	return ""
}

// isSyncKey reports whether key is the sync annotation of any domain.
func (ks KeySet) isSyncKey(key string) bool {
	for _, k := range ks.each() {
		if key == k.SyncAnnotation {
			return true
		}
	}
	return false
}

// applyCopyMeta mirrors src's labels/annotations onto a copy and stamps the
// primary domain's managed-copy labels. Because the copy's labels are rebuilt
// from src's own labels plus the primary stamps, any legacy managed-by/origin/
// origin-cluster labels already on dst are dropped — so each reconcile
// self-migrates a copy to the primary domain. The sync annotation is stripped
// under *every* domain (a copy must never itself be a source), as is the
// last-applied annotation.
func (ks KeySet) applyCopyMeta(src, dst client.Object, originCluster string) {
	out := map[string]string{}
	maps.Copy(out, src.GetLabels())
	out[ks.Primary.ManagedByLabel] = ManagedByValue
	out[ks.Primary.OriginNSLabel] = src.GetNamespace()
	out[ks.Primary.OriginNameLabel] = src.GetName()
	if originCluster != "" {
		out[ks.Primary.OriginClusterLabel] = originCluster
	}
	dst.SetLabels(out)

	ann := map[string]string{}
	for key, v := range src.GetAnnotations() {
		if key == lastAppliedAnnotation || ks.isSyncKey(key) {
			continue
		}
		ann[key] = v
	}
	dst.SetAnnotations(ann)
}

// managedListOpts returns one label matcher per domain (primary, then legacy)
// selecting the managed copies of src. deleteCopies lists under each so it finds
// copies stamped under a legacy domain that have not yet been migrated.
func (ks KeySet) managedListOpts(src client.Object) []client.MatchingLabels {
	opts := make([]client.MatchingLabels, 0, len(ks.Legacy)+1)
	for _, k := range ks.each() {
		opts = append(opts, client.MatchingLabels{
			k.ManagedByLabel:  ManagedByValue,
			k.OriginNSLabel:   src.GetNamespace(),
			k.OriginNameLabel: src.GetName(),
		})
	}
	return opts
}

// hasFinalizer reports whether obj carries the finalizer of any domain.
func (ks KeySet) hasFinalizer(obj client.Object) bool {
	for _, k := range ks.each() {
		if controllerutil.ContainsFinalizer(obj, k.Finalizer) {
			return true
		}
	}
	return false
}

// reconcileFinalizer ensures the primary finalizer is present and removes any
// legacy finalizers, self-migrating the finalizer to the primary domain. It
// returns whether it changed obj (the caller persists and re-enqueues if so).
func (ks KeySet) reconcileFinalizer(obj client.Object) (changed bool) {
	if !controllerutil.ContainsFinalizer(obj, ks.Primary.Finalizer) {
		controllerutil.AddFinalizer(obj, ks.Primary.Finalizer)
		changed = true
	}
	for _, k := range ks.Legacy {
		if controllerutil.ContainsFinalizer(obj, k.Finalizer) {
			controllerutil.RemoveFinalizer(obj, k.Finalizer)
			changed = true
		}
	}
	return changed
}

// removeAllFinalizers strips the finalizer of every domain (primary and legacy),
// used during cleanup so a source stamped under a legacy domain still releases.
func (ks KeySet) removeAllFinalizers(obj client.Object) {
	for _, k := range ks.each() {
		controllerutil.RemoveFinalizer(obj, k.Finalizer)
	}
}

// hasCredentialLabel reports whether obj carries the cluster-credential label of
// any domain, so a spoke credential labeled under a legacy domain is still seen.
func (ks KeySet) hasCredentialLabel(obj client.Object) bool {
	ls := obj.GetLabels()
	for _, k := range ks.each() {
		if ls[k.CredentialLabel] != "" {
			return true
		}
	}
	return false
}
