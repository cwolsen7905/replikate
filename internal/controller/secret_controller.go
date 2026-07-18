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

// SecretReconciler replicates annotated Secrets across namespaces.
type SecretReconciler struct {
	*Syncer
}

// Reconcile fetches the Secret named by req and drives it to desired state.
func (r *SecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var sec corev1.Secret
	if err := r.Get(ctx, req.NamespacedName, &sec); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return r.reconcileSource(ctx, &sec)
}

// SetupWithManager wires the controller. It reconciles source Secrets,
// re-drives all sources when a namespace changes, and re-drives the source when
// one of its managed copies is edited or deleted (drift correction).
func (r *SecretReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}, builder.WithPredicates(r.sourcePredicate())).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(
			func(ctx context.Context, _ client.Object) []reconcile.Request {
				return r.sourceRequests(ctx, &corev1.SecretList{})
			})).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.mapCopyToSource),
			builder.WithPredicates(r.managedCopyPredicate())).
		Named("secret").
		Complete(r)
}
