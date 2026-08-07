package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// a minimal valid kubeconfig; the registry only needs it to parse into a REST
// config, not to connect (the fake builder ignores the config).
const testKubeconfig = `
apiVersion: v1
kind: Config
clusters:
- name: spoke
  cluster:
    server: https://spoke.example:6443
    insecure-skip-tls-verify: true
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
current-context: spoke
users:
- name: spoke
  user:
    token: abc
`

func credentialSecret(name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "replikate-system",
			Labels:    map[string]string{CredentialLabel: "true"},
		},
		Data: data,
	}
}

// fakeRegistry builds a registry whose clients are in-memory fake clients, so
// tests never touch the network.
func fakeRegistry() *ClusterRegistry {
	return NewClusterRegistry(func(_ *rest.Config, _ string) (client.Client, error) {
		return fake.NewClientBuilder().Build(), nil
	})
}

func TestRegistry_UpsertGetRemove(t *testing.T) {
	r := fakeRegistry()
	sec := credentialSecret("spoke-a", map[string][]byte{credentialKubeconfigKey: []byte(testKubeconfig)})

	if _, err := r.Upsert("spoke-a", sec); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, ok := r.ClientFor("spoke-a"); !ok {
		t.Error("expected spoke-a to be registered")
	}
	if got := r.IDs(); len(got) != 1 || got[0] != "spoke-a" {
		t.Errorf("IDs = %v, want [spoke-a]", got)
	}

	r.Remove("spoke-a")
	if _, ok := r.ClientFor("spoke-a"); ok {
		t.Error("spoke-a should be gone after Remove")
	}
	if len(r.IDs()) != 0 {
		t.Errorf("IDs should be empty, got %v", r.IDs())
	}
}

func TestRegistry_IDsSorted(t *testing.T) {
	r := fakeRegistry()
	for _, id := range []string{"c", "a", "b"} {
		if _, err := r.Upsert(id, credentialSecret(id, map[string][]byte{credentialKubeconfigKey: []byte(testKubeconfig)})); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
	}
	got := r.IDs()
	want := []string{"a", "b", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("IDs = %v, want %v", got, want)
		}
	}
}

func TestRegistry_RejectsBadCredential(t *testing.T) {
	r := fakeRegistry()

	if _, err := r.Upsert("no-key", credentialSecret("no-key", map[string][]byte{"other": []byte("x")})); err == nil {
		t.Error("expected error for a Secret with no kubeconfig key")
	}
	if _, err := r.Upsert("bad-yaml", credentialSecret("bad-yaml", map[string][]byte{credentialKubeconfigKey: []byte("not a kubeconfig")})); err == nil {
		t.Error("expected error for an unparseable kubeconfig")
	}
	if len(r.IDs()) != 0 {
		t.Errorf("no cluster should be registered after failures, got %v", r.IDs())
	}
}

func TestConnectivityCheck(t *testing.T) {
	// A fake client lists successfully, so the check passes.
	c := fake.NewClientBuilder().Build()
	if err := connectivityCheck(context.Background(), c); err != nil {
		t.Errorf("connectivity check against a working client should pass: %v", err)
	}
}
