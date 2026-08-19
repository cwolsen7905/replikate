package controller

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
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
	// HubClusterUID is the UID of the hub's own kube-system namespace — a stable
	// per-cluster identity. A credential that resolves to the same UID is
	// rejected: replicating into the hub-as-a-spoke would make the controller
	// treat its own local copies as remote and delete them. Unlike an API-host
	// string compare, this is immune to the same cluster being reached via a
	// different URL (external LB, IP vs DNS). Empty disables the check.
	HubClusterUID string
	// Notify are channels (one per source kind) signalled with the credential
	// after a spoke is registered, so the source controllers can fan out to it
	// immediately rather than waiting for a requeue.
	Notify []chan<- event.GenericEvent
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

	// Build the client but do NOT register it yet: it must pass reachability and
	// the self-cluster check first, so a bad or self-referential credential is
	// never briefly live in the registry (a source reconcile on another worker
	// could otherwise fan out to it).
	c, err := r.Registry.BuildClient(id, &secret)
	if err != nil {
		// A malformed credential is a user error; requeuing won't help until the
		// Secret changes, which re-triggers us. Record it and stop.
		l.Error(err, "invalid cluster credential", "cluster", id)
		r.Registry.Remove(id)
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

	// Self-cluster check (see HubClusterUID). Fail closed: if the candidate's
	// identity can't be read we refuse to register it rather than risk a
	// self-spoke, and surface it so the misconfiguration is visible.
	if r.HubClusterUID != "" {
		uid, uerr := clusterUID(ctx, c)
		if uerr != nil {
			r.Registry.Remove(id)
			l.Error(uerr, "could not verify cluster identity; refusing to register", "cluster", id)
			r.Recorder.Event(&secret, corev1.EventTypeWarning, "IdentityUnverified",
				"Refusing to register: could not read cluster identity: "+uerr.Error())
			return ctrl.Result{RequeueAfter: clusterRecheckInterval}, nil
		}
		if uid == r.HubClusterUID {
			r.Registry.Remove(id)
			l.Info("refusing self-referential cluster credential", "cluster", id, "clusterUID", uid)
			r.Recorder.Event(&secret, corev1.EventTypeWarning, "SelfCluster",
				"Refusing credential that resolves to the hub's own cluster")
			return ctrl.Result{}, nil
		}
	}

	// Vetted: register it.
	r.Registry.Store(id, c)
	r.Registry.SetUp(id, true)
	l.Info("registered spoke cluster", "cluster", id)
	r.Recorder.Event(&secret, corev1.EventTypeNormal, "ClusterConnected", "Registered spoke cluster "+id)

	// Nudge the source controllers to fan out to this spoke now that it's live.
	// Non-blocking: the periodic recheck below re-notifies if a buffer is full.
	// This fires on every successful reconcile, including the periodic rechecks,
	// so cross-cluster sources are also re-driven roughly every recheck interval
	// — an intentional, lightweight remote resync until real remote drift
	// correction lands.
	for _, ch := range r.Notify {
		select {
		case ch <- event.GenericEvent{Object: secret.DeepCopy()}:
		default:
		}
	}
	// Re-check periodically so a spoke that goes down flips the gauge (and a
	// dropped notification is retried) even without a Secret change.
	return ctrl.Result{RequeueAfter: clusterRecheckInterval}, nil
}

// clusterUID returns the UID of the cluster's kube-system namespace, a stable
// identifier for the cluster behind c.
func clusterUID(ctx context.Context, c client.Client) (string, error) {
	var ns corev1.Namespace
	if err := c.Get(ctx, types.NamespacedName{Name: "kube-system"}, &ns); err != nil {
		return "", err
	}
	return string(ns.UID), nil
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
