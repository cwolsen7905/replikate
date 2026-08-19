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
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// k8sClient is a direct (uncached) client used for strong reads in assertions.
// The manager is started once for the whole package in TestMain — controller
// names are process-global (they register metrics), so a manager-per-test would
// collide on the second start.
var (
	k8sClient    client.Client // hub, direct
	spokeClient  client.Client // spoke, direct (a genuinely separate API server)
	testEnv      *envtest.Environment
	spokeEnv     *envtest.Environment
	testRegistry *ClusterRegistry
)

// credentialNS is the namespace the cluster-credential reconciler watches in
// the integration tests.
const credentialNS = "replikate-system"

func TestMain(m *testing.M) {
	testEnv = &envtest.Environment{}
	cfg, err := testEnv.Start()
	if err != nil {
		panic("start hub envtest (is KUBEBUILDER_ASSETS set?): " + err.Error())
	}
	// A second, genuinely separate control plane acts as the spoke cluster.
	spokeEnv = &envtest.Environment{}
	spokeCfg, err := spokeEnv.Start()
	if err != nil {
		panic("start spoke envtest: " + err.Error())
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Metrics:        metricsserver.Options{BindAddress: "0"},
		LeaderElection: false,
	})
	if err != nil {
		panic("new manager: " + err.Error())
	}
	_ = clientgoscheme.AddToScheme(mgr.GetScheme())

	testRegistry = NewClusterRegistry(func(c *rest.Config, _ string) (client.Client, error) {
		return client.New(c, client.Options{Scheme: mgr.GetScheme()})
	})

	syncer := &Syncer{
		Client:            mgr.GetClient(),
		Keys:              NewKeys(DefaultDomain),
		Recorder:          mgr.GetEventRecorderFor("replikate"),
		ExcludeNamespaces: NamespaceSet("kube-system,kube-public,kube-node-lease"),
		Registry:          testRegistry, // cross-cluster active; only annotated sources fan out
	}
	cmReady := make(chan event.GenericEvent, 64)
	secReady := make(chan event.GenericEvent, 64)
	if err := (&ConfigMapReconciler{Syncer: syncer, ClusterReady: cmReady}).SetupWithManager(mgr); err != nil {
		panic("setup configmap: " + err.Error())
	}
	if err := (&SecretReconciler{Syncer: syncer, ClusterReady: secReady}).SetupWithManager(mgr); err != nil {
		panic("setup secret: " + err.Error())
	}
	hubUID, err := ClusterUIDFromConfig(cfg, mgr.GetScheme()) // enable the self-cluster guard
	if err != nil {
		panic("read hub cluster UID: " + err.Error())
	}
	if err := (&ClusterCredentialReconciler{
		Client:        mgr.GetClient(),
		Registry:      testRegistry,
		Recorder:      mgr.GetEventRecorderFor("replikate-cluster"),
		Namespace:     credentialNS,
		HubClusterUID: hubUID,
		Notify:        []chan<- event.GenericEvent{cmReady, secReady},
	}).SetupWithManager(mgr); err != nil {
		panic("setup cluster-credential: " + err.Error())
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		panic("cache failed to sync")
	}

	k8sClient, err = client.New(cfg, client.Options{Scheme: mgr.GetScheme()})
	if err != nil {
		panic("hub client: " + err.Error())
	}
	spokeClient, err = client.New(spokeCfg, client.Options{Scheme: mgr.GetScheme()})
	if err != nil {
		panic("spoke client: " + err.Error())
	}

	// The hub needs the credential namespace; register the spoke directly so
	// fan-out tests have a ready target without racing the credential watch.
	if err := k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: credentialNS}}); err != nil {
		panic("create credential namespace: " + err.Error())
	}
	if _, err := testRegistry.Upsert("spoke", spokeCredential(spokeEnv)); err != nil {
		panic("register spoke: " + err.Error())
	}
	testRegistry.SetUp("spoke", true)

	code := m.Run()
	cancel()
	_ = testEnv.Stop()
	_ = spokeEnv.Stop()
	os.Exit(code)
}

// spokeCredential builds a credential Secret carrying a kubeconfig for env.
func spokeCredential(env *envtest.Environment) *corev1.Secret {
	user, err := env.AddUser(envtest.User{Name: "replikate-spoke", Groups: []string{"system:masters"}}, nil)
	if err != nil {
		panic("add spoke user: " + err.Error())
	}
	kubeconfig, err := user.KubeConfig()
	if err != nil {
		panic("render spoke kubeconfig: " + err.Error())
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "spoke", Namespace: credentialNS,
			Labels: map[string]string{CredentialLabel: "true"}},
		Data: map[string][]byte{credentialKubeconfigKey: kubeconfig},
	}
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

// TestIntegration_ClusterRegistry drives the credential reconciler against a
// real second control plane: a credential Secret for the spoke is discovered
// and registered, its client passes the connectivity check, and removing the
// Secret deregisters it.
func TestIntegration_ClusterRegistry(t *testing.T) {
	ctx := context.Background()

	cred := spokeCredential(spokeEnv)
	cred.Name = "spoke-reg" // distinct id so it doesn't clash with the pre-registered "spoke"
	cred.Labels = map[string]string{CredentialLabel: "true"}
	if err := k8sClient.Create(ctx, cred); err != nil {
		t.Fatalf("create credential secret: %v", err)
	}

	eventually(t, func() bool {
		_, ok := testRegistry.ClientFor("spoke-reg")
		return ok
	}, "spoke-reg registered in the cluster registry")

	c, _ := testRegistry.ClientFor("spoke-reg")
	if err := connectivityCheck(ctx, c); err != nil {
		t.Errorf("registered spoke client failed connectivity check: %v", err)
	}

	if err := k8sClient.Delete(ctx, cred); err != nil {
		t.Fatalf("delete credential secret: %v", err)
	}
	eventually(t, func() bool {
		_, ok := testRegistry.ClientFor("spoke-reg")
		return !ok
	}, "spoke-reg deregistered after credential removal")
}

// TestIntegration_CrossClusterFanOut is the release gate: a source in the hub
// with target-clusters is replicated into a genuinely separate spoke cluster,
// pruned when the target is dropped, and cleaned up when the source is deleted.
func TestIntegration_CrossClusterFanOut(t *testing.T) {
	ctx := context.Background()
	keys := NewKeys(DefaultDomain)

	// The copy lands in the source's namespace on the spoke, so it must exist
	// there (Phase 2 doesn't create remote namespaces).
	for _, c := range []client.Client{k8sClient, spokeClient} {
		if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "xc"}}); err != nil {
			t.Fatalf("create namespace xc: %v", err)
		}
	}

	src := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shared",
			Namespace: "xc",
			Annotations: map[string]string{
				keys.SyncAnnotation:           "", // no local peers created in this test
				keys.TargetClustersAnnotation: "spoke",
			},
		},
		Data: map[string]string{"k": "v"},
	}
	if err := k8sClient.Create(ctx, src); err != nil {
		t.Fatalf("create source: %v", err)
	}

	// The copy appears in the SPOKE cluster.
	spokeHas := func() bool {
		var cm corev1.ConfigMap
		return spokeClient.Get(ctx, types.NamespacedName{Namespace: "xc", Name: "shared"}, &cm) == nil
	}
	eventually(t, spokeHas, "copy replicated into the spoke cluster")

	// Drop the target → the remote copy is pruned.
	got := &corev1.ConfigMap{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "xc", Name: "shared"}, got); err != nil {
		t.Fatalf("get source: %v", err)
	}
	got.Annotations[keys.TargetClustersAnnotation] = ""
	if err := k8sClient.Update(ctx, got); err != nil {
		t.Fatalf("update source: %v", err)
	}
	eventually(t, func() bool { return !spokeHas() }, "remote copy pruned after target dropped")

	// Re-target, then delete the source → remote copy removed via the finalizer.
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "xc", Name: "shared"}, got); err != nil {
		t.Fatalf("get source: %v", err)
	}
	got.Annotations[keys.TargetClustersAnnotation] = "spoke"
	if err := k8sClient.Update(ctx, got); err != nil {
		t.Fatalf("re-target: %v", err)
	}
	eventually(t, spokeHas, "copy re-replicated after re-targeting")

	if err := k8sClient.Delete(ctx, got); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	eventually(t, func() bool { return !spokeHas() }, "remote copy removed when source deleted")
}
