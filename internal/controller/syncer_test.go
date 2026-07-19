package controller

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var testKeys = NewKeys(DefaultDomain)

// newTestSyncer builds a Syncer backed by a fake client seeded with objs.
func newTestSyncer(objs ...client.Object) (*Syncer, *record.FakeRecorder) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	rec := record.NewFakeRecorder(100)
	index := func(o client.Object) []string {
		if testKeys.isSource(o) {
			return []string{sourceIndexTrue}
		}
		return nil
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithIndex(&corev1.ConfigMap{}, SourceIndexField, index).
		WithIndex(&corev1.Secret{}, SourceIndexField, index).
		WithObjects(objs...).Build()
	return &Syncer{Client: c, Keys: testKeys, Recorder: rec}, rec
}

func ns(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func sourceCM(name, namespace, sync string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: map[string]string{testKeys.SyncAnnotation: sync},
		},
		Data: data,
	}
}

// reconcileConfigMap drives the ConfigMap reconciler to a stable state (the
// first pass adds the finalizer and requeues, later passes do the sync).
func reconcileConfigMap(t *testing.T, s *Syncer, namespace, name string) {
	t.Helper()
	r := &ConfigMapReconciler{Syncer: s}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}
	for i := 0; i < 5; i++ {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
	}
}

func getCM(t *testing.T, s *Syncer, namespace, name string) (*corev1.ConfigMap, bool) {
	t.Helper()
	var cm corev1.ConfigMap
	err := s.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, &cm)
	if err != nil {
		return nil, false
	}
	return &cm, true
}

func TestReconcile_FanOutBySelector(t *testing.T) {
	s, _ := newTestSyncer(
		ns("default", nil),
		ns("web-a", map[string]string{"team": "web"}),
		ns("web-b", map[string]string{"team": "web"}),
		ns("other", map[string]string{"team": "db"}),
		sourceCM("cfg", "default", "team=web", map[string]string{"k": "v"}),
	)
	reconcileConfigMap(t, s, "default", "cfg")

	for _, want := range []string{"web-a", "web-b"} {
		cm, ok := getCM(t, s, want, "cfg")
		if !ok {
			t.Fatalf("expected copy in %s", want)
		}
		if cm.Data["k"] != "v" {
			t.Errorf("copy in %s has wrong data: %v", want, cm.Data)
		}
		if cm.Labels[testKeys.ManagedByLabel] != ManagedByValue {
			t.Errorf("copy in %s not marked managed", want)
		}
		if cm.Labels[testKeys.OriginNSLabel] != "default" || cm.Labels[testKeys.OriginNameLabel] != "cfg" {
			t.Errorf("copy in %s missing origin labels: %v", want, cm.Labels)
		}
	}
	if _, ok := getCM(t, s, "other", "cfg"); ok {
		t.Error("did not expect a copy in non-matching namespace 'other'")
	}
	if _, ok := getCM(t, s, "default", "cfg"); !ok {
		t.Error("source should still exist")
	}
}

func TestReconcile_AllNamespacesWhenSelectorEmpty(t *testing.T) {
	s, _ := newTestSyncer(
		ns("default", nil),
		ns("a", nil),
		ns("b", nil),
		sourceCM("cfg", "default", "", map[string]string{"k": "v"}),
	)
	reconcileConfigMap(t, s, "default", "cfg")

	for _, want := range []string{"a", "b"} {
		if _, ok := getCM(t, s, want, "cfg"); !ok {
			t.Errorf("expected copy in %s for empty selector", want)
		}
	}
}

func TestReconcile_AdoptsConfigSyncerCopy(t *testing.T) {
	// Pre-existing config-syncer copy with stale data and the kubed marker.
	stale := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "cfg",
			Namespace:   "web-a",
			Annotations: map[string]string{kubedOriginPrefix: `{"namespace":"default"}`},
		},
		Data: map[string]string{"k": "OLD"},
	}
	s, _ := newTestSyncer(
		ns("default", nil),
		ns("web-a", map[string]string{"team": "web"}),
		stale,
		sourceCM("cfg", "default", "team=web", map[string]string{"k": "NEW"}),
	)
	reconcileConfigMap(t, s, "default", "cfg")

	cm, ok := getCM(t, s, "web-a", "cfg")
	if !ok {
		t.Fatal("adopted copy should exist")
	}
	if cm.Data["k"] != "NEW" {
		t.Errorf("adopted copy not updated to source data: %v", cm.Data)
	}
	if cm.Labels[testKeys.ManagedByLabel] != ManagedByValue {
		t.Error("adopted copy should be relabeled as managed")
	}
	if _, has := cm.Annotations[kubedOriginPrefix]; has {
		t.Error("adopted copy should have the kubed origin annotation stripped")
	}
}

func TestReconcile_RefusesUnmanagedObject(t *testing.T) {
	// A user's own object with the same name — must not be clobbered.
	own := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "web-a"},
		Data:       map[string]string{"k": "MINE"},
	}
	s, rec := newTestSyncer(
		ns("default", nil),
		ns("web-a", map[string]string{"team": "web"}),
		own,
		sourceCM("cfg", "default", "team=web", map[string]string{"k": "v"}),
	)
	reconcileConfigMap(t, s, "default", "cfg")

	cm, _ := getCM(t, s, "web-a", "cfg")
	if cm.Data["k"] != "MINE" {
		t.Errorf("unmanaged object was overwritten: %v", cm.Data)
	}
	if cm.Labels[testKeys.ManagedByLabel] == ManagedByValue {
		t.Error("unmanaged object should not be marked managed")
	}
	if !hasEvent(rec, "Skipped") {
		t.Error("expected a Skipped event when refusing to overwrite")
	}
}

func TestReconcile_RefusesCopyOwnedByAnotherSource(t *testing.T) {
	// A managed copy already owned by source other/cfg sits in web-a.
	owned := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cfg",
			Namespace: "web-a",
			Labels: map[string]string{
				testKeys.ManagedByLabel:  ManagedByValue,
				testKeys.OriginNSLabel:   "other",
				testKeys.OriginNameLabel: "cfg",
			},
		},
		Data: map[string]string{"k": "OWNED-BY-OTHER"},
	}
	// A second, same-named source in "default" also targets web-a.
	s, rec := newTestSyncer(
		ns("default", nil),
		ns("web-a", map[string]string{"team": "web"}),
		owned,
		sourceCM("cfg", "default", "team=web", map[string]string{"k": "MINE"}),
	)
	reconcileConfigMap(t, s, "default", "cfg")

	cm, _ := getCM(t, s, "web-a", "cfg")
	if cm.Data["k"] != "OWNED-BY-OTHER" {
		t.Errorf("copy owned by another source was overwritten: %v", cm.Data)
	}
	if cm.Labels[testKeys.OriginNSLabel] != "other" {
		t.Errorf("origin labels were rewritten by the losing source: %v", cm.Labels)
	}
	if !hasEvent(rec, "Conflict") {
		t.Error("expected a Conflict event when refusing to overwrite another source's copy")
	}
}

func TestReconcile_RemovesStaleCopyOnSelectorChange(t *testing.T) {
	s, _ := newTestSyncer(
		ns("default", nil),
		ns("web-a", map[string]string{"team": "web"}),
		sourceCM("cfg", "default", "team=web", map[string]string{"k": "v"}),
	)
	reconcileConfigMap(t, s, "default", "cfg")
	if _, ok := getCM(t, s, "web-a", "cfg"); !ok {
		t.Fatal("precondition: copy should exist in web-a")
	}

	// Narrow the selector so web-a no longer matches.
	src, _ := getCM(t, s, "default", "cfg")
	src.Annotations[testKeys.SyncAnnotation] = "team=none"
	if err := s.Update(context.Background(), src); err != nil {
		t.Fatalf("update source: %v", err)
	}
	reconcileConfigMap(t, s, "default", "cfg")

	if _, ok := getCM(t, s, "web-a", "cfg"); ok {
		t.Error("stale copy in web-a should have been removed after selector change")
	}
}

func TestReconcile_FinalizerCleanupOnDelete(t *testing.T) {
	s, _ := newTestSyncer(
		ns("default", nil),
		ns("web-a", map[string]string{"team": "web"}),
		sourceCM("cfg", "default", "team=web", map[string]string{"k": "v"}),
	)
	reconcileConfigMap(t, s, "default", "cfg")
	if _, ok := getCM(t, s, "web-a", "cfg"); !ok {
		t.Fatal("precondition: copy should exist")
	}

	// Deleting a finalized object marks it terminating; reconcile then cleans up.
	src, _ := getCM(t, s, "default", "cfg")
	if err := s.Delete(context.Background(), src); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	reconcileConfigMap(t, s, "default", "cfg")

	if _, ok := getCM(t, s, "web-a", "cfg"); ok {
		t.Error("copy should be deleted when the source is deleted")
	}
	if _, ok := getCM(t, s, "default", "cfg"); ok {
		t.Error("source should be gone once its finalizer is removed")
	}
}

func TestReconcile_SkipsExcludedNamespaces(t *testing.T) {
	s, _ := newTestSyncer(
		ns("default", nil),
		ns("web-a", map[string]string{"team": "web"}),
		ns("kube-system", map[string]string{"team": "web"}), // matches, but excluded
		sourceCM("cfg", "default", "team=web", map[string]string{"k": "v"}),
	)
	s.ExcludeNamespaces = map[string]bool{"kube-system": true}
	reconcileConfigMap(t, s, "default", "cfg")

	if _, ok := getCM(t, s, "web-a", "cfg"); !ok {
		t.Error("expected copy in non-excluded namespace web-a")
	}
	if _, ok := getCM(t, s, "kube-system", "cfg"); ok {
		t.Error("did not expect a copy in excluded namespace kube-system")
	}
}

func TestNamespaceSet(t *testing.T) {
	got := NamespaceSet(" kube-system, kube-public ,,kube-node-lease ")
	want := map[string]bool{"kube-system": true, "kube-public": true, "kube-node-lease": true}
	if len(got) != len(want) {
		t.Fatalf("parsed %v, want %v", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing %q in parsed set %v", k, got)
		}
	}
	if NamespaceSet("") != nil || NamespaceSet("  , ") != nil {
		t.Error("empty/blank input should yield a nil set")
	}
}

func TestReconcile_RestoresDriftedCopy(t *testing.T) {
	s, _ := newTestSyncer(
		ns("default", nil),
		ns("web-a", map[string]string{"team": "web"}),
		sourceCM("cfg", "default", "team=web", map[string]string{"k": "v"}),
	)
	reconcileConfigMap(t, s, "default", "cfg")

	// Hand-edit a managed copy; the next reconcile of the source restores it.
	copy, ok := getCM(t, s, "web-a", "cfg")
	if !ok {
		t.Fatal("precondition: copy should exist")
	}
	copy.Data["k"] = "TAMPERED"
	if err := s.Update(context.Background(), copy); err != nil {
		t.Fatalf("edit copy: %v", err)
	}
	reconcileConfigMap(t, s, "default", "cfg")
	if restored, _ := getCM(t, s, "web-a", "cfg"); restored.Data["k"] != "v" {
		t.Errorf("edited copy not restored to source data: %v", restored.Data)
	}

	// Delete a managed copy; the next reconcile of the source recreates it.
	copy, _ = getCM(t, s, "web-a", "cfg")
	if err := s.Delete(context.Background(), copy); err != nil {
		t.Fatalf("delete copy: %v", err)
	}
	if _, ok := getCM(t, s, "web-a", "cfg"); ok {
		t.Fatal("precondition: copy should be gone after delete")
	}
	reconcileConfigMap(t, s, "default", "cfg")
	if _, ok := getCM(t, s, "web-a", "cfg"); !ok {
		t.Error("deleted copy should have been recreated")
	}
}

func TestSourceRequests_ReturnsOnlySources(t *testing.T) {
	s, _ := newTestSyncer(
		ns("default", nil),
		sourceCM("src-a", "default", "team=web", map[string]string{"k": "v"}),
		sourceCM("src-b", "team-ns", "", map[string]string{"k": "v"}),
		// A plain ConfigMap with no sync annotation must not be indexed as a source.
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "default"}},
	)

	reqs := s.sourceRequests(context.Background(), &corev1.ConfigMapList{})
	if len(reqs) != 2 {
		t.Fatalf("expected 2 source requests, got %d: %v", len(reqs), reqs)
	}
	got := map[string]bool{}
	for _, r := range reqs {
		got[r.Namespace+"/"+r.Name] = true
	}
	if !got["default/src-a"] || !got["team-ns/src-b"] {
		t.Errorf("missing expected sources: %v", got)
	}
	if got["default/plain"] {
		t.Error("non-source ConfigMap should not appear in source requests")
	}
}

func TestMapCopyToSource(t *testing.T) {
	s, _ := newTestSyncer()

	managed := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      "cfg",
		Namespace: "web-a",
		Labels: map[string]string{
			testKeys.ManagedByLabel:  ManagedByValue,
			testKeys.OriginNSLabel:   "default",
			testKeys.OriginNameLabel: "cfg",
		},
	}}
	reqs := s.mapCopyToSource(context.Background(), managed)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request for a managed copy, got %d", len(reqs))
	}
	if reqs[0].Namespace != "default" || reqs[0].Name != "cfg" {
		t.Errorf("mapped to wrong source: %v", reqs[0].NamespacedName)
	}

	// A copy without the managed-by label maps to nothing (avoids loops).
	unmanaged := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "web-a"}}
	if reqs := s.mapCopyToSource(context.Background(), unmanaged); len(reqs) != 0 {
		t.Errorf("expected no requests for an unmanaged object, got %d", len(reqs))
	}
}

func TestReconcile_RecordsMetrics(t *testing.T) {
	created := testutil.ToFloat64(copyOperationsTotal.WithLabelValues("configmap", "created"))
	success := testutil.ToFloat64(reconcileTotal.WithLabelValues("configmap", "success"))

	s, _ := newTestSyncer(
		ns("default", nil),
		ns("web-a", map[string]string{"team": "web"}),
		ns("web-b", map[string]string{"team": "web"}),
		sourceCM("cfg", "default", "team=web", map[string]string{"k": "v"}),
	)
	r := &ConfigMapReconciler{Syncer: s}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "cfg"}}
	for i := 0; i < 5; i++ {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
	}

	if got := testutil.ToFloat64(copyOperationsTotal.WithLabelValues("configmap", "created")) - created; got != 2 {
		t.Errorf("expected 2 create operations recorded, got %v", got)
	}
	if got := testutil.ToFloat64(reconcileTotal.WithLabelValues("configmap", "success")) - success; got < 1 {
		t.Errorf("expected successful reconciles recorded, got %v", got)
	}
}

func hasEvent(rec *record.FakeRecorder, reason string) bool {
	for {
		select {
		case e := <-rec.Events:
			if containsSubstr(e, reason) {
				return true
			}
		default:
			return false
		}
	}
}

func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
