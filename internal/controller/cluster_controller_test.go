package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSameAPIHost(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"https://1.2.3.4:6443", "https://1.2.3.4:6443", true},
		{"https://1.2.3.4:6443/", "https://1.2.3.4:6443", true},
		{"https://1.2.3.4:6443", "https://5.6.7.8:6443", false},
		{"https://host:6443", "https://host:8443", false},
	}
	for _, tc := range cases {
		if got := sameAPIHost(tc.a, tc.b); got != tc.want {
			t.Errorf("sameAPIHost(%q,%q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestClusterCredential_RejectsSelfCluster verifies the guard: a credential
// whose kubeconfig server matches the hub's is refused, never registered, and
// raises a SelfCluster event.
func TestClusterCredential_RejectsSelfCluster(t *testing.T) {
	// testKubeconfig points at https://spoke.example:6443 — make that the hub.
	rec := record.NewFakeRecorder(10)
	reg := NewClusterRegistry(func(_ *rest.Config, _ string) (client.Client, error) {
		return fake.NewClientBuilder().Build(), nil
	})
	cred := credentialSecret("self", map[string][]byte{credentialKubeconfigKey: []byte(testKubeconfig)})
	cl := fake.NewClientBuilder().WithObjects(cred).Build()

	r := &ClusterCredentialReconciler{
		Client:    cl,
		Registry:  reg,
		Recorder:  rec,
		Namespace: "replikate-system",
		HubHost:   "https://spoke.example:6443", // == the credential's server
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "replikate-system", Name: "self"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if _, ok := reg.ClientFor("self"); ok {
		t.Error("a self-referential credential must not be registered")
	}
	if !hasEvent(rec, "SelfCluster") {
		t.Error("expected a SelfCluster event")
	}
}

// TestClusterCredential_RegistersDistinctSpoke is the positive counterpart: a
// credential whose server differs from the hub is registered normally.
func TestClusterCredential_RegistersDistinctSpoke(t *testing.T) {
	rec := record.NewFakeRecorder(10)
	reg := NewClusterRegistry(func(_ *rest.Config, _ string) (client.Client, error) {
		return fake.NewClientBuilder().Build(), nil // connectivity check passes
	})
	cred := credentialSecret("spoke-a", map[string][]byte{credentialKubeconfigKey: []byte(testKubeconfig)})
	cl := fake.NewClientBuilder().WithObjects(cred).Build()

	r := &ClusterCredentialReconciler{
		Client:    cl,
		Registry:  reg,
		Recorder:  rec,
		Namespace: "replikate-system",
		HubHost:   "https://different-hub:6443",
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "replikate-system", Name: "spoke-a"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, ok := reg.ClientFor("spoke-a"); !ok {
		t.Error("a distinct spoke credential should be registered")
	}
}
