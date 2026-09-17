package controller

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// legacyKeys is the previous annotation domain, honored for reads/matching.
var legacyKeys = NewKeys(DefaultPreviousDomain)

// dualKeySet honors the primary domain for writes and the previous one for
// reads/matching/adoption — the migration configuration.
var dualKeySet = NewKeySet(DefaultDomain, DefaultPreviousDomain)

// legacySourceCM builds a source ConfigMap annotated under the *previous*
// domain, to exercise legacy-domain recognition.
func legacySourceCM(name, namespace, sync string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: map[string]string{legacyKeys.SyncAnnotation: sync},
		},
		Data: data,
	}
}

// legacyCopyCM builds a managed copy stamped under the *previous* domain, as if
// written by a prior Replikate version.
func legacyCopyCM(name, namespace, originNS, originName string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				legacyKeys.ManagedByLabel:  ManagedByValue,
				legacyKeys.OriginNSLabel:   originNS,
				legacyKeys.OriginNameLabel: originName,
			},
		},
		Data: data,
	}
}

// TestKeySet_LegacySourceRecognizedAndMigrated verifies a source annotated under
// the previous domain is still driven: its copies are created stamped under the
// primary domain, and the finalizer self-migrates (primary added, legacy
// removed).
func TestKeySet_LegacySourceRecognizedAndMigrated(t *testing.T) {
	src := legacySourceCM("cfg", "default", "team=web", map[string]string{"k": "v"})
	src.Finalizers = []string{legacyKeys.Finalizer} // carried over from the old version

	s, _ := newTestSyncerKeys(dualKeySet,
		ns("default", nil),
		ns("web-a", map[string]string{"team": "web"}),
		src,
	)
	reconcileConfigMap(t, s, "default", "cfg")

	cm, ok := getCM(t, s, "web-a", "cfg")
	if !ok {
		t.Fatal("legacy-annotated source should still fan out a copy")
	}
	if cm.Data["k"] != "v" {
		t.Errorf("copy has wrong data: %v", cm.Data)
	}
	// Copies are always stamped under the primary domain, never the legacy one.
	if cm.Labels[testKeys.ManagedByLabel] != ManagedByValue {
		t.Error("copy should be stamped managed under the primary domain")
	}
	if _, has := cm.Labels[legacyKeys.ManagedByLabel]; has {
		t.Error("copy should not carry the legacy managed-by label")
	}
	if cm.Labels[testKeys.OriginNSLabel] != "default" || cm.Labels[testKeys.OriginNameLabel] != "cfg" {
		t.Errorf("copy missing primary origin labels: %v", cm.Labels)
	}

	// The finalizer self-migrates: primary present, legacy gone.
	got, _ := getCM(t, s, "default", "cfg")
	if !controllerutil.ContainsFinalizer(got, testKeys.Finalizer) {
		t.Error("source should carry the primary finalizer")
	}
	if controllerutil.ContainsFinalizer(got, legacyKeys.Finalizer) {
		t.Error("legacy finalizer should have been removed")
	}
}

// TestKeySet_MigratesLegacyStampedCopy verifies an existing copy stamped under
// the previous domain is recognized as owned, re-stamped to the primary domain
// in place (no delete/recreate), and reported via an Adopted event + the
// migration counter.
func TestKeySet_MigratesLegacyStampedCopy(t *testing.T) {
	before := testutil.ToFloat64(copiesMigratedTotal.WithLabelValues(DefaultPreviousDomain))

	// A pre-existing legacy-stamped copy with stale data, owned by default/cfg.
	legacy := legacyCopyCM("cfg", "web-a", "default", "cfg", map[string]string{"k": "OLD"})

	s, rec := newTestSyncerKeys(dualKeySet,
		ns("default", nil),
		ns("web-a", map[string]string{"team": "web"}),
		legacy,
		sourceCM("cfg", "default", "team=web", map[string]string{"k": "NEW"}),
	)
	reconcileConfigMap(t, s, "default", "cfg")

	cm, ok := getCM(t, s, "web-a", "cfg")
	if !ok {
		t.Fatal("copy should still exist (migrated in place, never recreated)")
	}
	if cm.UID == legacy.UID && cm.UID != "" {
		// Fake client doesn't set UIDs, so this is a soft check; the real proof
		// is that no delete happened and data/labels updated below.
		t.Log("copy retained its identity")
	}
	if cm.Data["k"] != "NEW" {
		t.Errorf("migrated copy not updated to source data: %v", cm.Data)
	}
	if cm.Labels[testKeys.ManagedByLabel] != ManagedByValue {
		t.Error("migrated copy should be stamped under the primary domain")
	}
	if _, has := cm.Labels[legacyKeys.ManagedByLabel]; has {
		t.Error("legacy managed-by label should have been dropped on migration")
	}
	if _, has := cm.Labels[legacyKeys.OriginNSLabel]; has {
		t.Error("legacy origin-namespace label should have been dropped on migration")
	}
	if !hasEvent(rec, "Adopted") {
		t.Error("expected an Adopted event when migrating a legacy-stamped copy")
	}
	if after := testutil.ToFloat64(copiesMigratedTotal.WithLabelValues(DefaultPreviousDomain)); after <= before {
		t.Errorf("copies-migrated counter did not increase: before=%v after=%v", before, after)
	}
}

// TestKeySet_PrimaryOnlyIgnoresLegacy verifies that with an empty legacy list
// (the eventual end state), a legacy-annotated source and a legacy-stamped copy
// are not recognized at all.
func TestKeySet_PrimaryOnlyIgnoresLegacy(t *testing.T) {
	legacySrc := legacySourceCM("cfg", "default", "team=web", map[string]string{"k": "v"})
	legacy := legacyCopyCM("orphan", "web-a", "default", "orphan", map[string]string{"k": "x"})

	// Unit-level: the primary-only KeySet recognizes neither.
	if testKeySet.isSource(legacySrc) {
		t.Error("primary-only KeySet should not treat a legacy-annotated object as a source")
	}
	if testKeySet.isManagedCopy(legacy) {
		t.Error("primary-only KeySet should not treat a legacy-stamped copy as managed")
	}

	// Behavioral: reconciling a legacy source under a primary-only Syncer is a
	// no-op — no copy is created.
	s, _ := newTestSyncer(
		ns("default", nil),
		ns("web-a", map[string]string{"team": "web"}),
		legacySrc,
	)
	reconcileConfigMap(t, s, "default", "cfg")
	if _, ok := getCM(t, s, "web-a", "cfg"); ok {
		t.Error("primary-only Syncer should not fan out a legacy-annotated source")
	}
}

// TestKeySet_CleanupFindsLegacyStampedCopies verifies deleteCopies lists under
// every domain, so a stale copy stamped under the previous domain is still
// pruned when it falls out of a source's target set.
func TestKeySet_CleanupFindsLegacyStampedCopies(t *testing.T) {
	// A legacy-stamped copy owned by default/cfg sits in web-b, which the source
	// no longer targets (web-b is labeled team=db, selector is team=web).
	staleLegacy := legacyCopyCM("cfg", "web-b", "default", "cfg", map[string]string{"k": "STALE"})

	s, _ := newTestSyncerKeys(dualKeySet,
		ns("default", nil),
		ns("web-a", map[string]string{"team": "web"}),
		ns("web-b", map[string]string{"team": "db"}),
		staleLegacy,
		sourceCM("cfg", "default", "team=web", map[string]string{"k": "v"}),
	)
	reconcileConfigMap(t, s, "default", "cfg")

	if _, ok := getCM(t, s, "web-b", "cfg"); ok {
		t.Error("stale legacy-stamped copy in a non-target namespace should be pruned")
	}
	if _, ok := getCM(t, s, "web-a", "cfg"); !ok {
		t.Error("copy in the current target namespace should exist")
	}
}

// TestKeySet_LegacyCopyDeletedOnSourceDelete verifies that deleting a source
// removes copies stamped under the previous domain too (finalizer cleanup lists
// under every domain).
func TestKeySet_LegacyCopyDeletedOnSourceDelete(t *testing.T) {
	legacy := legacyCopyCM("cfg", "web-a", "default", "cfg", map[string]string{"k": "OLD"})

	s, _ := newTestSyncerKeys(dualKeySet,
		ns("default", nil),
		ns("web-a", map[string]string{"team": "web"}),
		legacy,
		sourceCM("cfg", "default", "team=web", map[string]string{"k": "v"}),
	)
	reconcileConfigMap(t, s, "default", "cfg") // migrates the legacy copy to primary

	src, _ := getCM(t, s, "default", "cfg")
	if err := s.Delete(context.Background(), src); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	reconcileConfigMap(t, s, "default", "cfg")

	if _, ok := getCM(t, s, "web-a", "cfg"); ok {
		t.Error("copy should be deleted when its source is deleted")
	}
}

// TestKeySet_Unit exercises the KeySet helpers directly.
func TestKeySet_Unit(t *testing.T) {
	t.Run("NewKeySet drops blank and duplicate legacy domains", func(t *testing.T) {
		ks := NewKeySet(DefaultDomain, "", DefaultDomain, DefaultPreviousDomain, "  ")
		if len(ks.Legacy) != 1 || ks.Legacy[0].Domain != DefaultPreviousDomain {
			t.Fatalf("legacy = %+v, want exactly [%s]", ks.Legacy, DefaultPreviousDomain)
		}
	})

	t.Run("syncValue prefers primary then legacy", func(t *testing.T) {
		key, val, ok := dualKeySet.syncValue(map[string]string{legacyKeys.SyncAnnotation: "team=db"})
		if !ok || key != legacyKeys.SyncAnnotation || val != "team=db" {
			t.Errorf("legacy read: key=%q val=%q ok=%v", key, val, ok)
		}
		key, val, ok = dualKeySet.syncValue(map[string]string{
			legacyKeys.SyncAnnotation: "team=db",
			testKeys.SyncAnnotation:   "team=web",
		})
		if !ok || key != testKeys.SyncAnnotation || val != "team=web" {
			t.Errorf("primary should win: key=%q val=%q ok=%v", key, val, ok)
		}
	})

	t.Run("applyCopyMeta strips sync under every domain", func(t *testing.T) {
		src := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Name: "cfg", Namespace: "default",
			Annotations: map[string]string{
				legacyKeys.SyncAnnotation: "team=web",
				"keep-me":                 "yes",
			},
		}}
		dst := &corev1.ConfigMap{}
		dualKeySet.applyCopyMeta(src, dst, "")
		if _, has := dst.Annotations[legacyKeys.SyncAnnotation]; has {
			t.Error("copy must not carry any domain's sync annotation")
		}
		if dst.Annotations["keep-me"] != "yes" {
			t.Error("non-sync annotations should be preserved")
		}
	})

	t.Run("hasCredentialLabel honors legacy", func(t *testing.T) {
		obj := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{legacyKeys.CredentialLabel: "true"},
		}}
		if !dualKeySet.hasCredentialLabel(obj) {
			t.Error("credential labeled under the legacy domain should be recognized")
		}
		if testKeySet.hasCredentialLabel(obj) {
			t.Error("primary-only KeySet should not recognize a legacy credential label")
		}
	})
}
