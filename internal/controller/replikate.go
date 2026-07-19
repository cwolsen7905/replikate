// Package controller implements Replikate's replication logic: it watches
// ConfigMaps and Secrets annotated as sources and keeps managed copies of them
// in the namespaces selected by the source.
package controller

import (
	"bytes"
	"maps"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DefaultDomain is the annotation/label key prefix used when none is configured.
const DefaultDomain = "replikate.brainchurts.com"

const (
	// ManagedByValue is the value of the managed-by label on every copy.
	ManagedByValue = "replikate"

	// lastAppliedAnnotation is stripped from copies; it is meaningless there.
	lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

	// kubedOriginPrefix is the annotation/label key prefix that AppsCode's
	// config-syncer (kubed) stamps on the copies it manages. Replikate treats
	// objects carrying it as adoptable, so a config-syncer -> Replikate
	// migration can take over existing copies in place instead of refusing
	// them (which would risk deleting live data during the cutover).
	kubedOriginPrefix = "kubed.appscode.com/origin"
)

// Keys holds the annotation and label keys Replikate uses, all derived from a
// configurable domain prefix so the controller can be reused under any domain.
type Keys struct {
	// Domain is the key prefix, e.g. "replikate.brainchurts.com".
	Domain string
	// SyncAnnotation marks a ConfigMap/Secret as a replication source; its
	// value is a namespace label selector (empty means all namespaces).
	SyncAnnotation string
	// Finalizer lets Replikate clean up copies before a source is deleted.
	Finalizer string
	// ManagedByLabel marks an object as a Replikate-managed copy.
	ManagedByLabel string
	// OriginNSLabel / OriginNameLabel record the source's namespace and name.
	OriginNSLabel   string
	OriginNameLabel string
}

// NewKeys derives the annotation/label keys from a domain prefix.
func NewKeys(domain string) Keys {
	return Keys{
		Domain:          domain,
		SyncAnnotation:  domain + "/sync",
		Finalizer:       domain + "/finalizer",
		ManagedByLabel:  domain + "/managed-by",
		OriginNSLabel:   domain + "/origin-namespace",
		OriginNameLabel: domain + "/origin-name",
	}
}

// isSource reports whether obj is annotated as a replication source.
func (k Keys) isSource(obj client.Object) bool {
	_, ok := obj.GetAnnotations()[k.SyncAnnotation]
	return ok
}

// isManagedCopy reports whether obj is one of Replikate's managed copies.
func (k Keys) isManagedCopy(obj client.Object) bool {
	return obj.GetLabels()[k.ManagedByLabel] == ManagedByValue
}

// ownsCopy reports whether the managed copy's origin labels point back at src,
// i.e. src is the source this copy belongs to. Used to keep two same-named
// sources in different namespaces from overwriting each other's copies.
func (k Keys) ownsCopy(copy, src client.Object) bool {
	ls := copy.GetLabels()
	return ls[k.OriginNSLabel] == src.GetNamespace() && ls[k.OriginNameLabel] == src.GetName()
}

// applyCopyMeta mirrors the source's labels and annotations onto a copy (minus
// Replikate's own keys) and stamps the managed-copy labels used for lookups.
func (k Keys) applyCopyMeta(src, dst client.Object) {
	out := map[string]string{}
	maps.Copy(out, src.GetLabels())
	out[k.ManagedByLabel] = ManagedByValue
	out[k.OriginNSLabel] = src.GetNamespace()
	out[k.OriginNameLabel] = src.GetName()
	dst.SetLabels(out)

	ann := map[string]string{}
	for key, v := range src.GetAnnotations() {
		if key == k.SyncAnnotation || key == lastAppliedAnnotation {
			continue
		}
		ann[key] = v
	}
	dst.SetAnnotations(ann)
}

// isAdoptable reports whether obj is an existing replicated copy that Replikate
// may safely take over — currently, any copy previously managed by AppsCode's
// config-syncer, identified by its origin annotation or origin labels.
func isAdoptable(obj client.Object) bool {
	if _, ok := obj.GetAnnotations()[kubedOriginPrefix]; ok {
		return true
	}
	for k := range obj.GetLabels() {
		if len(k) >= len(kubedOriginPrefix) && k[:len(kubedOriginPrefix)] == kubedOriginPrefix {
			return true
		}
	}
	return false
}

// emptyLike returns a new empty object of the same concrete kind as obj.
func emptyLike(obj client.Object) client.Object {
	switch obj.(type) {
	case *corev1.ConfigMap:
		return &corev1.ConfigMap{}
	case *corev1.Secret:
		return &corev1.Secret{}
	default:
		return nil
	}
}

// emptyListLike returns a new empty list matching the kind of obj.
func emptyListLike(obj client.Object) client.ObjectList {
	switch obj.(type) {
	case *corev1.ConfigMap:
		return &corev1.ConfigMapList{}
	case *corev1.Secret:
		return &corev1.SecretList{}
	default:
		return nil
	}
}

// listItems returns the elements of a ConfigMapList or SecretList as objects.
func listItems(list client.ObjectList) []client.Object {
	switch l := list.(type) {
	case *corev1.ConfigMapList:
		out := make([]client.Object, len(l.Items))
		for i := range l.Items {
			out[i] = &l.Items[i]
		}
		return out
	case *corev1.SecretList:
		out := make([]client.Object, len(l.Items))
		for i := range l.Items {
			out[i] = &l.Items[i]
		}
		return out
	default:
		return nil
	}
}

// copyContents copies the payload from src into dst. Both must be the same
// concrete kind (ConfigMap or Secret).
func copyContents(src, dst client.Object) {
	switch s := src.(type) {
	case *corev1.ConfigMap:
		d := dst.(*corev1.ConfigMap)
		d.Data = s.Data
		d.BinaryData = s.BinaryData
	case *corev1.Secret:
		d := dst.(*corev1.Secret)
		d.Data = s.Data
		d.Type = s.Type
	}
}

// contentEqual reports whether two copies already carry identical managed
// content — labels, annotations, and payload — so a write can be skipped.
func contentEqual(a, b client.Object) bool {
	if !maps.Equal(a.GetLabels(), b.GetLabels()) || !maps.Equal(a.GetAnnotations(), b.GetAnnotations()) {
		return false
	}
	switch x := a.(type) {
	case *corev1.ConfigMap:
		y := b.(*corev1.ConfigMap)
		return maps.Equal(x.Data, y.Data) && byteMapEqual(x.BinaryData, y.BinaryData)
	case *corev1.Secret:
		y := b.(*corev1.Secret)
		return x.Type == y.Type && byteMapEqual(x.Data, y.Data)
	}
	return false
}

// byteMapEqual compares two map[string][]byte values for equality.
func byteMapEqual(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || !bytes.Equal(v, w) {
			return false
		}
	}
	return true
}
