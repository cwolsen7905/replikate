//go:build integration

// Package controller integration tests run the real manager (watches, cache,
// field indexer) against a live API server via envtest. They are gated behind
// the `integration` build tag and require KUBEBUILDER_ASSETS to point at the
// control-plane binaries — run them with `make test-integration`.
package controller

import (
	"context"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// k8sClient is a direct (uncached) client used for strong reads in assertions.
// The manager is started once for the whole package in TestMain — controller
// names are process-global (they register metrics), so a manager-per-test would
// collide on the second start.
var k8sClient client.Client

func TestMain(m *testing.M) {
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		panic("start envtest (is KUBEBUILDER_ASSETS set?): " + err.Error())
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Metrics:        metricsserver.Options{BindAddress: "0"},
		LeaderElection: false,
	})
	if err != nil {
		panic("new manager: " + err.Error())
	}
	_ = clientgoscheme.AddToScheme(mgr.GetScheme())

	syncer := &Syncer{
		Client:            mgr.GetClient(),
		Keys:              NewKeys(DefaultDomain),
		Recorder:          mgr.GetEventRecorderFor("replikate"),
		ExcludeNamespaces: NamespaceSet("kube-system,kube-public,kube-node-lease"),
	}
	if err := (&ConfigMapReconciler{Syncer: syncer}).SetupWithManager(mgr); err != nil {
		panic("setup configmap: " + err.Error())
	}
	if err := (&SecretReconciler{Syncer: syncer}).SetupWithManager(mgr); err != nil {
		panic("setup secret: " + err.Error())
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		panic("cache failed to sync")
	}

	k8sClient, err = client.New(cfg, client.Options{Scheme: mgr.GetScheme()})
	if err != nil {
		panic("direct client: " + err.Error())
	}

	code := m.Run()
	cancel()
	_ = env.Stop()
	os.Exit(code)
}

func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", msg)
}

func mkNamespace(t *testing.T, k8s client.Client, name string, labels map[string]string) {
	t.Helper()
	err := k8s.Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
	})
	if err != nil {
		t.Fatalf("create namespace %s: %v", name, err)
	}
}

func copyExists(k8s client.Client, namespace, name string) bool {
	var cm corev1.ConfigMap
	return k8s.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, &cm) == nil
}

// TestIntegration_FanOutDriftAndNamespaceAdd exercises the real reconcile loop
// end to end: selector fan-out, drift restore, and — via the field indexer —
// fan-out to a namespace created after the source.
func TestIntegration_FanOutDriftAndNamespaceAdd(t *testing.T) {
	k8s := k8sClient
	ctx := context.Background()

	mkNamespace(t, k8s, "web-1", map[string]string{"team": "web"})

	// Create a source; it should fan out to the matching namespace.
	src := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "app-config",
			Namespace:   "default",
			Annotations: map[string]string{NewKeys(DefaultDomain).SyncAnnotation: "team=web"},
		},
		Data: map[string]string{"k": "v1"},
	}
	if err := k8s.Create(ctx, src); err != nil {
		t.Fatalf("create source: %v", err)
	}
	eventually(t, func() bool { return copyExists(k8s, "web-1", "app-config") },
		"copy created in web-1")

	// Drift: delete the copy; the managed-copy watch should restore it.
	var copy corev1.ConfigMap
	_ = k8s.Get(ctx, types.NamespacedName{Namespace: "web-1", Name: "app-config"}, &copy)
	if err := k8s.Delete(ctx, &copy); err != nil {
		t.Fatalf("delete copy: %v", err)
	}
	eventually(t, func() bool { return copyExists(k8s, "web-1", "app-config") },
		"deleted copy restored in web-1")

	// A namespace created after the source must be populated — this is the
	// path the field index serves (namespace event -> source lookup).
	mkNamespace(t, k8s, "web-2", map[string]string{"team": "web"})
	eventually(t, func() bool { return copyExists(k8s, "web-2", "app-config") },
		"copy fanned out to newly created web-2")

	// A non-matching namespace must never receive a copy.
	mkNamespace(t, k8s, "db-1", map[string]string{"team": "db"})
	time.Sleep(1 * time.Second)
	if copyExists(k8s, "db-1", "app-config") {
		t.Error("copy should not exist in non-matching namespace db-1")
	}
}

// TestIntegration_SameNameSourcesDoNotFight verifies the conflict guard against
// a live API server: two same-named sources in different namespaces that both
// target one namespace must not overwrite each other's copy in a loop.
func TestIntegration_SameNameSourcesDoNotFight(t *testing.T) {
	k8s := k8sClient
	ctx := context.Background()
	keys := NewKeys(DefaultDomain)

	mkNamespace(t, k8s, "owner-ns", nil)
	mkNamespace(t, k8s, "rival-ns", nil)
	mkNamespace(t, k8s, "shared-target", map[string]string{"tier": "shared"})

	newSource := func(namespace, val string) *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "creds",
				Namespace:   namespace,
				Annotations: map[string]string{keys.SyncAnnotation: "tier=shared"},
			},
			Data: map[string]string{"who": val},
		}
	}

	// First source wins ownership of the copy in shared-target.
	if err := k8s.Create(ctx, newSource("owner-ns", "owner")); err != nil {
		t.Fatalf("create owner source: %v", err)
	}
	eventually(t, func() bool { return copyExists(k8s, "shared-target", "creds") },
		"copy created by the first source")

	// Second, same-named source appears and also targets shared-target.
	if err := k8s.Create(ctx, newSource("rival-ns", "rival")); err != nil {
		t.Fatalf("create rival source: %v", err)
	}

	// Give both reconcile loops ample time to run, then assert the copy still
	// belongs to the first source and was never flipped to the rival.
	time.Sleep(2 * time.Second)
	var copy corev1.ConfigMap
	if err := k8s.Get(ctx, types.NamespacedName{Namespace: "shared-target", Name: "creds"}, &copy); err != nil {
		t.Fatalf("get copy: %v", err)
	}
	if copy.Data["who"] != "owner" {
		t.Errorf("copy content was clobbered by the rival source: %v", copy.Data)
	}
	if copy.Labels[keys.OriginNSLabel] != "owner-ns" {
		t.Errorf("copy ownership flipped to the rival: origin-namespace=%q",
			copy.Labels[keys.OriginNSLabel])
	}
}
