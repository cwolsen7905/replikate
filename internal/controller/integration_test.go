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
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var testCfg *rest.Config

func TestMain(m *testing.M) {
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		panic("start envtest (is KUBEBUILDER_ASSETS set?): " + err.Error())
	}
	testCfg = cfg
	code := m.Run()
	_ = env.Stop()
	os.Exit(code)
}

// startManager wires both reconcilers onto a manager and runs it until the
// returned cancel func is called. It returns a direct (uncached) client for
// strong reads in assertions.
func startManager(t *testing.T) (client.Client, context.CancelFunc) {
	t.Helper()
	mgr, err := ctrl.NewManager(testCfg, ctrl.Options{
		Metrics:        metricsserver.Options{BindAddress: "0"},
		LeaderElection: false,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	_ = clientgoscheme.AddToScheme(mgr.GetScheme())

	syncer := &Syncer{
		Client:            mgr.GetClient(),
		Keys:              NewKeys(DefaultDomain),
		Recorder:          mgr.GetEventRecorderFor("replikate"),
		ExcludeNamespaces: NamespaceSet("kube-system,kube-public,kube-node-lease"),
	}
	if err := (&ConfigMapReconciler{Syncer: syncer}).SetupWithManager(mgr); err != nil {
		t.Fatalf("setup configmap: %v", err)
	}
	if err := (&SecretReconciler{Syncer: syncer}).SetupWithManager(mgr); err != nil {
		t.Fatalf("setup secret: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := mgr.Start(ctx); err != nil {
			// Start returns non-nil on abnormal exit; cancel path returns nil.
			t.Logf("manager exited: %v", err)
		}
	}()
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		cancel()
		t.Fatal("cache failed to sync")
	}

	k8s, err := client.New(testCfg, client.Options{Scheme: mgr.GetScheme()})
	if err != nil {
		cancel()
		t.Fatalf("direct client: %v", err)
	}
	return k8s, cancel
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
	k8s, cancel := startManager(t)
	defer cancel()
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
