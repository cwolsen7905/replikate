package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// reconcileCredentialWithNotify drives the reconciler once with a Notify channel
// and returns it, so tests can assert whether a registration notification fired.
func reconcileCredentialWithNotify(t *testing.T, id, hubUID string, spokeClient client.Client) chan event.GenericEvent {
	t.Helper()
	reg := NewClusterRegistry(func(_ *rest.Config, _ string) (client.Client, error) {
		return spokeClient, nil
	})
	cred := credentialSecret(id, map[string][]byte{credentialKubeconfigKey: []byte(testKubeconfig)})
	hub := fake.NewClientBuilder().WithObjects(cred).Build()
	notify := make(chan event.GenericEvent, 1)
	r := &ClusterCredentialReconciler{
		Client:        hub,
		Registry:      reg,
		Recorder:      record.NewFakeRecorder(10),
		Namespace:     "replikate-system",
		HubClusterUID: hubUID,
		Notify:        []chan<- event.GenericEvent{notify},
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "replikate-system", Name: id},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return notify
}

func TestClusterCredential_NotifiesOnRegister(t *testing.T) {
	spoke := fake.NewClientBuilder().WithObjects(kubeSystem("spoke-uid")).Build()
	notify := reconcileCredentialWithNotify(t, "spoke-a", "hub-uid", spoke)
	select {
	case ev := <-notify:
		if ev.Object.GetName() != "spoke-a" {
			t.Errorf("notification named %q, want spoke-a", ev.Object.GetName())
		}
	default:
		t.Error("expected a registration notification")
	}
}

func TestClusterCredential_NoNotifyOnSelfCluster(t *testing.T) {
	spoke := fake.NewClientBuilder().WithObjects(kubeSystem("hub-uid")).Build() // == hub
	notify := reconcileCredentialWithNotify(t, "self", "hub-uid", spoke)
	select {
	case <-notify:
		t.Error("a rejected self-cluster must not notify the source controllers")
	default:
	}
}

// kubeSystem returns a kube-system namespace object carrying uid, used to give a
// fake cluster a stable identity.
func kubeSystem(uid string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID(uid)}}
}

func TestClusterUID(t *testing.T) {
	c := fake.NewClientBuilder().WithObjects(kubeSystem("abc-123")).Build()
	got, err := clusterUID(context.Background(), c)
	if err != nil {
		t.Fatalf("clusterUID: %v", err)
	}
	if got != "abc-123" {
		t.Errorf("clusterUID = %q, want abc-123", got)
	}
}

// reconcileCredential drives the credential reconciler once for id, with the
// registry building spokeClient for it.
func reconcileCredential(t *testing.T, id, hubUID string, spokeClient client.Client) (*ClusterRegistry, *record.FakeRecorder) {
	t.Helper()
	rec := record.NewFakeRecorder(10)
	reg := NewClusterRegistry(func(_ *rest.Config, _ string) (client.Client, error) {
		return spokeClient, nil
	})
	cred := credentialSecret(id, map[string][]byte{credentialKubeconfigKey: []byte(testKubeconfig)})
	hub := fake.NewClientBuilder().WithObjects(cred).Build()

	r := &ClusterCredentialReconciler{
		Client:        hub,
		Registry:      reg,
		Recorder:      rec,
		Namespace:     "replikate-system",
		HubClusterUID: hubUID,
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "replikate-system", Name: id},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return reg, rec
}

// TestClusterCredential_RejectsSelfCluster: a credential whose cluster UID
// matches the hub's is refused, never registered, and raises a SelfCluster event.
func TestClusterCredential_RejectsSelfCluster(t *testing.T) {
	spoke := fake.NewClientBuilder().WithObjects(kubeSystem("hub-uid")).Build()
	reg, rec := reconcileCredential(t, "self", "hub-uid", spoke)

	if _, ok := reg.ClientFor("self"); ok {
		t.Error("a self-referential credential must not be registered")
	}
	if !hasEvent(rec, "SelfCluster") {
		t.Error("expected a SelfCluster event")
	}
}

// TestClusterCredential_RegistersDistinctSpoke: a credential whose cluster UID
// differs from the hub's registers normally.
func TestClusterCredential_RegistersDistinctSpoke(t *testing.T) {
	spoke := fake.NewClientBuilder().WithObjects(kubeSystem("spoke-uid")).Build()
	reg, _ := reconcileCredential(t, "spoke-a", "hub-uid", spoke)

	if _, ok := reg.ClientFor("spoke-a"); !ok {
		t.Error("a distinct spoke credential should be registered")
	}
}

// TestClusterCredential_UnverifiableIdentityRejected: with the guard enabled,
// a spoke whose identity can't be read (no kube-system) is refused rather than
// registered on faith, and the reason is surfaced as an event.
func TestClusterCredential_UnverifiableIdentityRejected(t *testing.T) {
	spoke := fake.NewClientBuilder().Build() // no kube-system → clusterUID errors
	reg, rec := reconcileCredential(t, "opaque", "hub-uid", spoke)

	if _, ok := reg.ClientFor("opaque"); ok {
		t.Error("a spoke whose identity can't be verified must not be registered")
	}
	if !hasEvent(rec, "IdentityUnverified") {
		t.Error("expected an IdentityUnverified event")
	}
}
