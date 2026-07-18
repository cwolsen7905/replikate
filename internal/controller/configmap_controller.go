package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// ConfigMapReconciler replicates annotated ConfigMaps across namespaces.
type ConfigMapReconciler struct {
	*Syncer
}

// Reconcile fetches the ConfigMap named by req and drives it to desired state.
func (r *ConfigMapReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var cm corev1.ConfigMap
	if err := r.Get(ctx, req.NamespacedName, &cm); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return r.reconcileSource(ctx, &cm)
}

// SetupWithManager wires the controller: it reconciles source ConfigMaps and
// re-drives all sources whenever a namespace changes.
func (r *ConfigMapReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}, builder.WithPredicates(sourcePredicate())).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(
			func(ctx context.Context, _ client.Object) []reconcile.Request {
				return r.sourceRequests(ctx, &corev1.ConfigMapList{})
			})).
		Named("configmap").
		Complete(r)
}

// sourcePredicate limits reconciliation to objects that are sources or still
// carry our finalizer, so cleanup can run after the sync annotation is removed.
// It deliberately excludes managed copies, which prevents replication loops.
func sourcePredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(o client.Object) bool {
		return isSource(o) || controllerutil.ContainsFinalizer(o, Finalizer)
	})
}
