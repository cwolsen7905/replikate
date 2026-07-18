package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Syncer holds the shared replication logic used by both the ConfigMap and the
// Secret controllers.
type Syncer struct {
	client.Client
}

// reconcileSource brings the set of managed copies for a single source object
// into the desired state. obj must already be fetched from the API server.
func (s *Syncer) reconcileSource(ctx context.Context, obj client.Object) (reconcile.Result, error) {
	l := log.FromContext(ctx)

	// Deletion in progress: remove all copies, then drop our finalizer.
	if !obj.GetDeletionTimestamp().IsZero() {
		return reconcile.Result{}, s.cleanupAndRemoveFinalizer(ctx, obj)
	}

	// Not (or no longer) a source: tear down any copies we still own.
	if !isSource(obj) {
		if controllerutil.ContainsFinalizer(obj, Finalizer) {
			return reconcile.Result{}, s.cleanupAndRemoveFinalizer(ctx, obj)
		}
		return reconcile.Result{}, nil
	}

	// Ensure our finalizer is present before we create any copies.
	if !controllerutil.ContainsFinalizer(obj, Finalizer) {
		controllerutil.AddFinalizer(obj, Finalizer)
		if err := s.Update(ctx, obj); err != nil {
			return reconcile.Result{}, client.IgnoreNotFound(err)
		}
		return reconcile.Result{}, nil // the update re-enqueues this object
	}

	// Parse the namespace selector (empty value => every namespace).
	selValue := obj.GetAnnotations()[SyncAnnotation]
	selector, err := labels.Parse(selValue)
	if err != nil {
		l.Error(err, "invalid namespace selector; skipping", "selector", selValue)
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
		if selector.Matches(labels.Set(ns.Labels)) {
			targets[ns.Name] = true
		}
	}

	// Create or update a copy in every target namespace.
	for ns := range targets {
		if err := s.upsertCopy(ctx, obj, ns); err != nil {
			return reconcile.Result{}, err
		}
	}

	// Remove copies from namespaces that are no longer targets.
	return reconcile.Result{}, s.deleteCopies(ctx, obj, targets)
}

// cleanupAndRemoveFinalizer deletes every copy owned by obj and then removes the
// Replikate finalizer, allowing the source's own deletion to proceed.
func (s *Syncer) cleanupAndRemoveFinalizer(ctx context.Context, obj client.Object) error {
	if !controllerutil.ContainsFinalizer(obj, Finalizer) {
		return nil
	}
	if err := s.deleteCopies(ctx, obj, nil); err != nil {
		return err
	}
	controllerutil.RemoveFinalizer(obj, Finalizer)
	return client.IgnoreNotFound(s.Update(ctx, obj))
}

// upsertCopy creates or updates the managed copy of src in namespace ns. It
// refuses to overwrite an object it does not manage.
func (s *Syncer) upsertCopy(ctx context.Context, src client.Object, ns string) error {
	l := log.FromContext(ctx)
	key := types.NamespacedName{Namespace: ns, Name: src.GetName()}

	existing := emptyLike(src)
	err := s.Get(ctx, key, existing)
	switch {
	case apierrors.IsNotFound(err):
		desired := emptyLike(src)
		desired.SetNamespace(ns)
		desired.SetName(src.GetName())
		applyCopyMeta(src, desired)
		copyContents(src, desired)
		if err := s.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		return nil
	case err != nil:
		return err
	}

	// The target already exists. Update it if it is one of our copies, or adopt
	// it if it is a copy left behind by config-syncer; otherwise leave it alone
	// so we never clobber an object a user created themselves.
	if existing.GetLabels()[ManagedByLabel] != ManagedByValue {
		if !isAdoptable(existing) {
			l.Info("refusing to overwrite unmanaged object", "namespace", ns, "name", src.GetName())
			return nil
		}
		l.Info("adopting existing replicated copy", "namespace", ns, "name", src.GetName())
	}
	applyCopyMeta(src, existing)
	copyContents(src, existing)
	return s.Update(ctx, existing)
}

// deleteCopies removes managed copies of src. When keep is non-nil, copies whose
// namespace is present in keep are retained; when keep is nil, all copies go.
func (s *Syncer) deleteCopies(ctx context.Context, src client.Object, keep map[string]bool) error {
	list := emptyListLike(src)
	if err := s.List(ctx, list, client.MatchingLabels{
		ManagedByLabel:  ManagedByValue,
		OriginNSLabel:   src.GetNamespace(),
		OriginNameLabel: src.GetName(),
	}); err != nil {
		return err
	}
	for _, c := range listItems(list) {
		if keep != nil && keep[c.GetNamespace()] {
			continue
		}
		if err := s.Delete(ctx, c); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// applyCopyMeta mirrors the source's labels and annotations onto a copy (minus
// Replikate's own keys) and stamps the managed-copy labels used for lookups.
func applyCopyMeta(src, dst client.Object) {
	out := map[string]string{}
	for k, v := range src.GetLabels() {
		out[k] = v
	}
	out[ManagedByLabel] = ManagedByValue
	out[OriginNSLabel] = src.GetNamespace()
	out[OriginNameLabel] = src.GetName()
	dst.SetLabels(out)

	ann := map[string]string{}
	for k, v := range src.GetAnnotations() {
		if k == SyncAnnotation || k == lastAppliedAnnotation {
			continue
		}
		ann[k] = v
	}
	dst.SetAnnotations(ann)
}

// sourceRequests lists every source object of the kind backing list and returns
// a reconcile request for each, so a namespace change can re-drive all sources.
func (s *Syncer) sourceRequests(ctx context.Context, list client.ObjectList) []reconcile.Request {
	if err := s.List(ctx, list); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, o := range listItems(list) {
		if isSource(o) {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: o.GetNamespace(),
				Name:      o.GetName(),
			}})
		}
	}
	return reqs
}
