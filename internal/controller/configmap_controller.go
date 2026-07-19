package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
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
	res, err := r.reconcileSource(ctx, &cm)
	observeReconcile("configmap", err)
	return res, err
}

// SetupWithManager wires the controller. It reconciles source ConfigMaps,
// re-drives all sources when a namespace changes, and re-drives the source when
// one of its managed copies is edited or deleted (drift correction).
func (r *ConfigMapReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &corev1.ConfigMap{}, SourceIndexField, r.indexSource); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}, builder.WithPredicates(r.sourcePredicate())).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(
			func(ctx context.Context, _ client.Object) []reconcile.Request {
				return r.sourceRequests(ctx, &corev1.ConfigMapList{})
			})).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.mapCopyToSource),
			builder.WithPredicates(r.managedCopyPredicate())).
		Named("configmap").
		Complete(r)
}
