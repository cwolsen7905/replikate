package controller

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Syncer holds the shared replication logic used by both the ConfigMap and the
// Secret controllers.
type Syncer struct {
	client.Client
	Keys     Keys
	Recorder record.EventRecorder
	// ExcludeNamespaces is the set of namespaces that never receive copies,
	// regardless of a source's selector. It protects system namespaces by
	// default; a nil or empty set excludes nothing.
	ExcludeNamespaces map[string]bool
}

// DefaultExcludedNamespaces are the namespaces Replikate refuses to replicate
// into unless the operator overrides the exclusion list.
var DefaultExcludedNamespaces = []string{"kube-system", "kube-public", "kube-node-lease"}

// NamespaceSet parses a comma-separated namespace list into a lookup set,
// ignoring blank and whitespace-only entries. It returns nil for an empty list
// so the caller excludes nothing.
func NamespaceSet(csv string) map[string]bool {
	set := map[string]bool{}
	for _, ns := range strings.Split(csv, ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			set[ns] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// action describes what upsertCopy did to a single target, for event summaries.
type action int

const (
	actionNone action = iota
	actionCreated
	actionUpdated
	actionAdopted
)

// reconcileSource brings the set of managed copies for a single source object
// into the desired state. obj must already be fetched from the API server.
func (s *Syncer) reconcileSource(ctx context.Context, obj client.Object) (reconcile.Result, error) {
	l := log.FromContext(ctx)

	// Deletion in progress: remove all copies, then drop our finalizer.
	if !obj.GetDeletionTimestamp().IsZero() {
		return reconcile.Result{}, s.cleanupAndRemoveFinalizer(ctx, obj)
	}

	// Not (or no longer) a source: tear down any copies we still own.
	if !s.Keys.isSource(obj) {
		if controllerutil.ContainsFinalizer(obj, s.Keys.Finalizer) {
			return reconcile.Result{}, s.cleanupAndRemoveFinalizer(ctx, obj)
		}
		return reconcile.Result{}, nil
	}

	// Ensure our finalizer is present before we create any copies.
	if !controllerutil.ContainsFinalizer(obj, s.Keys.Finalizer) {
		controllerutil.AddFinalizer(obj, s.Keys.Finalizer)
		if err := s.Update(ctx, obj); err != nil {
			return reconcile.Result{}, client.IgnoreNotFound(err)
		}
		return reconcile.Result{}, nil // the update re-enqueues this object
	}

	// Parse the namespace selector (empty value => every namespace).
	selValue := obj.GetAnnotations()[s.Keys.SyncAnnotation]
	selector, err := labels.Parse(selValue)
	if err != nil {
		l.Error(err, "invalid namespace selector; skipping", "selector", selValue)
		s.Recorder.Eventf(obj, corev1.EventTypeWarning, "InvalidSelector",
			"Ignoring invalid sync selector %q: %v", selValue, err)
		return reconcile.Result{}, nil // user error: requeuing won't help
	}

	// Compute the set of target namespaces.
	var nsList corev1.NamespaceList
	if err := s.List(ctx, &nsList); err != nil {
		return reconcile.Result{}, err
	}
	targets := map[string]bool{}
	for i := range nsList.Items {
		ns := &nsList.Items[i]
		if ns.Name == obj.GetNamespace() || ns.DeletionTimestamp != nil {
			continue // never copy into the source's own or a terminating namespace
		}
		if s.ExcludeNamespaces[ns.Name] {
			continue // operator-excluded (system namespaces by default)
		}
		if selector.Matches(labels.Set(ns.Labels)) {
			targets[ns.Name] = true
		}
	}

	// Create, update, or adopt a copy in every target namespace.
	changed := 0
	for ns := range targets {
		act, err := s.upsertCopy(ctx, obj, ns)
		if err != nil {
			return reconcile.Result{}, err
		}
		if act != actionNone {
			changed++
			copyOperationsTotal.WithLabelValues(kindOf(obj), operationFor(act)).Inc()
		}
	}

	// Remove copies from namespaces that are no longer targets.
	deleted, err := s.deleteCopies(ctx, obj, targets)
	if err != nil {
		return reconcile.Result{}, err
	}

	if changed > 0 || deleted > 0 {
		s.Recorder.Eventf(obj, corev1.EventTypeNormal, "Replicated",
			"Reconciled %d target namespace(s): %d written, %d removed", len(targets), changed, deleted)
	}
	return reconcile.Result{}, nil
}

// cleanupAndRemoveFinalizer deletes every copy owned by obj and then removes the
// Replikate finalizer, allowing the source's own deletion to proceed.
func (s *Syncer) cleanupAndRemoveFinalizer(ctx context.Context, obj client.Object) error {
	if !controllerutil.ContainsFinalizer(obj, s.Keys.Finalizer) {
		return nil
	}
	if _, err := s.deleteCopies(ctx, obj, nil); err != nil {
		return err
	}
	controllerutil.RemoveFinalizer(obj, s.Keys.Finalizer)
	return client.IgnoreNotFound(s.Update(ctx, obj))
}

// upsertCopy creates, updates, or adopts the managed copy of src in namespace
// ns, and reports what it did. It refuses to overwrite an object it does not
// manage unless that object is an adoptable config-syncer copy.
func (s *Syncer) upsertCopy(ctx context.Context, src client.Object, ns string) (action, error) {
	l := log.FromContext(ctx)
	key := types.NamespacedName{Namespace: ns, Name: src.GetName()}

	existing := emptyLike(src)
	err := s.Get(ctx, key, existing)
	switch {
	case apierrors.IsNotFound(err):
		desired := emptyLike(src)
		desired.SetNamespace(ns)
		desired.SetName(src.GetName())
		s.Keys.applyCopyMeta(src, desired)
		copyContents(src, desired)
		l.Info("creating copy", "namespace", ns, "name", src.GetName())
		if err := s.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) {
			return actionNone, err
		}
		return actionCreated, nil
	case err != nil:
		return actionNone, err
	}

	managed := s.Keys.isManagedCopy(existing)
	if !managed && !isAdoptable(existing) {
		l.Info("refusing to overwrite unmanaged object", "namespace", ns, "name", src.GetName())
		s.Recorder.Eventf(src, corev1.EventTypeWarning, "Skipped",
			"Refusing to overwrite unmanaged object %s/%s", ns, src.GetName())
		return actionNone, nil
	}

	before := existing.DeepCopyObject().(client.Object)
	s.Keys.applyCopyMeta(src, existing)
	copyContents(src, existing)
	if managed && contentEqual(before, existing) {
		return actionNone, nil // already in the desired state; skip the write
	}
	if managed {
		l.Info("updating copy", "namespace", ns, "name", src.GetName())
	} else {
		l.Info("adopting copy", "namespace", ns, "name", src.GetName())
	}
	if err := s.Update(ctx, existing); err != nil {
		return actionNone, err
	}
	if managed {
		return actionUpdated, nil
	}
	return actionAdopted, nil
}

// deleteCopies removes managed copies of src and reports how many it deleted.
// When keep is non-nil, copies whose namespace is present in keep are retained;
// when keep is nil, all copies go.
func (s *Syncer) deleteCopies(ctx context.Context, src client.Object, keep map[string]bool) (int, error) {
	l := log.FromContext(ctx)
	list := emptyListLike(src)
	if err := s.List(ctx, list, client.MatchingLabels{
		s.Keys.ManagedByLabel:  ManagedByValue,
		s.Keys.OriginNSLabel:   src.GetNamespace(),
		s.Keys.OriginNameLabel: src.GetName(),
	}); err != nil {
		return 0, err
	}
	n := 0
	for _, c := range listItems(list) {
		if keep != nil && keep[c.GetNamespace()] {
			continue
		}
		l.Info("deleting copy", "namespace", c.GetNamespace(), "name", c.GetName())
		if err := s.Delete(ctx, c); err != nil && !apierrors.IsNotFound(err) {
			return n, err
		}
		copyOperationsTotal.WithLabelValues(kindOf(src), "deleted").Inc()
		n++
	}
	return n, nil
}

// SourceIndexField is the field-index name under which source objects are
// registered, so a namespace change can look up sources directly instead of
// scanning every ConfigMap/Secret in the cluster.
const SourceIndexField = "replikate.source"

// sourceIndexTrue is the index value stored for every source object.
const sourceIndexTrue = "true"

// indexSource is the field-indexer function: it indexes an object under
// SourceIndexField only when the object is a replication source.
func (s *Syncer) indexSource(obj client.Object) []string {
	if s.Keys.isSource(obj) {
		return []string{sourceIndexTrue}
	}
	return nil
}

// sourceRequests returns a reconcile request for every source object of the
// kind backing list, so a namespace change can re-drive all sources. It uses
// the SourceIndexField index to fetch only sources — O(sources), not O(all).
func (s *Syncer) sourceRequests(ctx context.Context, list client.ObjectList) []reconcile.Request {
	if err := s.List(ctx, list, client.MatchingFields{SourceIndexField: sourceIndexTrue}); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, o := range listItems(list) {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: o.GetNamespace(),
			Name:      o.GetName(),
		}})
	}
	return reqs
}

// mapCopyToSource maps a managed copy back to a reconcile request for its
// source, so that editing or deleting a copy re-drives the source and restores
// the copy — near-instant drift correction, rather than waiting for a resync.
func (s *Syncer) mapCopyToSource(_ context.Context, obj client.Object) []reconcile.Request {
	ls := obj.GetLabels()
	if ls[s.Keys.ManagedByLabel] != ManagedByValue {
		return nil
	}
	ns, name := ls[s.Keys.OriginNSLabel], ls[s.Keys.OriginNameLabel]
	if ns == "" || name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}}
}

// sourcePredicate limits the primary watch to objects that are sources or still
// carry our finalizer, so cleanup can run after the sync annotation is removed.
// It deliberately excludes managed copies, which prevents replication loops.
func (s *Syncer) sourcePredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(o client.Object) bool {
		return s.Keys.isSource(o) || controllerutil.ContainsFinalizer(o, s.Keys.Finalizer)
	})
}

// managedCopyPredicate limits the drift-correction watch to Replikate's copies.
func (s *Syncer) managedCopyPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(s.Keys.isManagedCopy)
}
