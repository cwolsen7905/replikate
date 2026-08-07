package controller

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// clusterRecheckInterval is how often a registered spoke is re-checked for
// reachability, so a cluster that goes down flips its gauge even without a
// change to its credential Secret.
const clusterRecheckInterval = 2 * time.Minute

// ClusterCredentialReconciler watches credential Secrets in the controller's
// namespace and keeps the ClusterRegistry in sync: a labeled Secret registers
// (or refreshes) a spoke cluster, its removal deregisters it. It runs a cheap
// connectivity check per credential and records reachability. This is Phase 1
// of cross-cluster support — it populates the registry but nothing replicates
// across clusters yet.
type ClusterCredentialReconciler struct {
	client.Client
	Registry  *ClusterRegistry
	Recorder  record.EventRecorder
	Namespace string // the namespace credential Secrets live in
}

// Reconcile registers, refreshes, or deregisters the spoke cluster named by the
// credential Secret in req.
func (r *ClusterCredentialReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	id := req.Name

	var secret corev1.Secret
	if err := r.Get(ctx, req.NamespacedName, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			// Credential removed: drop the cluster from the registry.
			r.Registry.Remove(id)
			l.Info("deregistered spoke cluster", "cluster", id)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	c, err := r.Registry.Upsert(id, &secret)
	if err != nil {
		// A malformed credential is a user error; requeuing won't help until the
		// Secret changes, which re-triggers us. Record it and stop.
		l.Error(err, "invalid cluster credential", "cluster", id)
		r.Registry.SetUp(id, false)
		r.Recorder.Event(&secret, corev1.EventTypeWarning, "InvalidCredential", err.Error())
		return ctrl.Result{}, nil
	}

	if err := connectivityCheck(ctx, c); err != nil {
		r.Registry.SetUp(id, false)
		l.Error(err, "spoke cluster unreachable", "cluster", id)
		r.Recorder.Event(&secret, corev1.EventTypeWarning, "ClusterUnreachable", err.Error())
		// Retry: connectivity is transient, unlike a malformed credential.
		return ctrl.Result{RequeueAfter: clusterRecheckInterval}, nil
	}

	r.Registry.SetUp(id, true)
	l.Info("registered spoke cluster", "cluster", id)
	r.Recorder.Event(&secret, corev1.EventTypeNormal, "ClusterConnected", "Registered spoke cluster "+id)
	// Re-check periodically so a spoke that goes down flips the gauge even
	// without a Secret change.
	return ctrl.Result{RequeueAfter: clusterRecheckInterval}, nil
}

// SetupWithManager wires the controller to watch only labeled credential Secrets
// in the configured namespace.
func (r *ClusterCredentialReconciler) SetupWithManager(mgr ctrl.Manager) error {
	inNamespace := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetNamespace() == r.Namespace && o.GetLabels()[CredentialLabel] != ""
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}, builder.WithPredicates(inNamespace)).
		Named("cluster-credential").
		Complete(r)
}
