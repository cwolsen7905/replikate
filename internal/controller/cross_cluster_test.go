package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// registryWith builds a ClusterRegistry backed by the given in-memory spoke
// clients, all marked reachable.
func registryWith(spokes map[string]client.Client) *ClusterRegistry {
	reg := NewClusterRegistry(func(_ *rest.Config, id string) (client.Client, error) {
		return spokes[id], nil
	})
	for id := range spokes {
		_, _ = reg.Upsert(id, credentialSecret(id, map[string][]byte{credentialKubeconfigKey: []byte(testKubeconfig)}))
		reg.SetUp(id, true)
	}
	return reg
}

func newSpoke() client.Client { return fake.NewClientBuilder().Build() }

func sourceWithTargets(name, namespace, targets string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				testKeys.SyncAnnotation:           "", // valid source; no local peers in these tests
				testKeys.TargetClustersAnnotation: targets,
			},
		},
		Data: data,
	}
}

func remoteCopy(t *testing.T, cl client.Client, namespace, name string) (*corev1.ConfigMap, bool) {
	t.Helper()
	var cm corev1.ConfigMap
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, &cm); err != nil {
		return nil, false
	}
	return &cm, true
}

func TestReconcile_CrossClusterFanOut(t *testing.T) {
	spokeA, spokeB := newSpoke(), newSpoke()
	s, _ := newTestSyncer(
		ns("default", nil),
		sourceWithTargets("cfg", "default", "spoke-a", map[string]string{"k": "v"}),
	)
	s.Registry = registryWith(map[string]client.Client{"spoke-a": spokeA, "spoke-b": spokeB})
	reconcileConfigMap(t, s, "default", "cfg")

	// A copy lands in spoke-a, in the SOURCE namespace, with the source data.
	cm, ok := remoteCopy(t, spokeA, "default", "cfg")
	if !ok {
		t.Fatal("expected a copy in spoke-a/default")
	}
	if cm.Data["k"] != "v" {
		t.Errorf("remote copy has wrong data: %v", cm.Data)
	}
	if cm.Labels[testKeys.ManagedByLabel] != ManagedByValue {
		t.Error("remote copy not marked managed")
	}
	// spoke-b was not targeted → no copy.
	if _, ok := remoteCopy(t, spokeB, "default", "cfg"); ok {
		t.Error("did not expect a copy in the un-targeted spoke-b")
	}
}

func TestReconcile_CrossClusterPrunesDelistedCluster(t *testing.T) {
	spokeA, spokeB := newSpoke(), newSpoke()
	s, _ := newTestSyncer(
		ns("default", nil),
		sourceWithTargets("cfg", "default", "spoke-a,spoke-b", map[string]string{"k": "v"}),
	)
	s.Registry = registryWith(map[string]client.Client{"spoke-a": spokeA, "spoke-b": spokeB})
	reconcileConfigMap(t, s, "default", "cfg")
	if _, ok := remoteCopy(t, spokeB, "default", "cfg"); !ok {
		t.Fatal("precondition: spoke-b should have a copy")
	}

	// Drop spoke-b from the target list; its copy must be pruned.
	src, _ := getCM(t, s, "default", "cfg")
	src.Annotations[testKeys.TargetClustersAnnotation] = "spoke-a"
	if err := s.Update(context.Background(), src); err != nil {
		t.Fatalf("update source: %v", err)
	}
	reconcileConfigMap(t, s, "default", "cfg")

	if _, ok := remoteCopy(t, spokeB, "default", "cfg"); ok {
		t.Error("de-listed spoke-b should have had its copy pruned")
	}
	if _, ok := remoteCopy(t, spokeA, "default", "cfg"); !ok {
		t.Error("still-targeted spoke-a should keep its copy")
	}
}

func TestReconcile_CrossClusterCleanupOnDelete(t *testing.T) {
	spokeA := newSpoke()
	s, _ := newTestSyncer(
		ns("default", nil),
		sourceWithTargets("cfg", "default", "spoke-a", map[string]string{"k": "v"}),
	)
	s.Registry = registryWith(map[string]client.Client{"spoke-a": spokeA})
	reconcileConfigMap(t, s, "default", "cfg")
	if _, ok := remoteCopy(t, spokeA, "default", "cfg"); !ok {
		t.Fatal("precondition: spoke-a should have a copy")
	}

	src, _ := getCM(t, s, "default", "cfg")
	if err := s.Delete(context.Background(), src); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	reconcileConfigMap(t, s, "default", "cfg")

	if _, ok := remoteCopy(t, spokeA, "default", "cfg"); ok {
		t.Error("remote copy should be removed when the source is deleted")
	}
}

func TestReconcile_CrossClusterSkippedWithoutAnnotation(t *testing.T) {
	// A leftover managed copy of this source sits in the spoke.
	spoke := newSpoke()
	pre := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      "cfg",
		Namespace: "default",
		Labels: map[string]string{
			testKeys.ManagedByLabel:  ManagedByValue,
			testKeys.OriginNSLabel:   "default",
			testKeys.OriginNameLabel: "cfg",
		},
	}}
	if err := spoke.Create(context.Background(), pre); err != nil {
		t.Fatalf("seed spoke copy: %v", err)
	}

	// The source has NO target-clusters annotation, so the remote walk must be
	// skipped entirely — the spoke copy is left untouched, not pruned.
	s, _ := newTestSyncer(ns("default", nil), sourceCM("cfg", "default", "", map[string]string{"k": "v"}))
	s.Registry = registryWith(map[string]client.Client{"spoke-a": spoke})
	reconcileConfigMap(t, s, "default", "cfg")

	if _, ok := remoteCopy(t, spoke, "default", "cfg"); !ok {
		t.Error("a source without target-clusters must not touch spokes (leftover copy was pruned)")
	}
}

func TestReconcile_CrossClusterUnknownClusterEvent(t *testing.T) {
	s, rec := newTestSyncer(
		ns("default", nil),
		sourceWithTargets("cfg", "default", "ghost", map[string]string{"k": "v"}),
	)
	s.Registry = registryWith(map[string]client.Client{"spoke-a": newSpoke()})
	reconcileConfigMap(t, s, "default", "cfg")

	if !hasEvent(rec, "UnknownCluster") {
		t.Error("expected an UnknownCluster event for a target that isn't registered")
	}
}
