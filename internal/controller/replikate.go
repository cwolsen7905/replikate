// Package controller implements Replikate's replication logic: it watches
// ConfigMaps and Secrets annotated as sources and keeps managed copies of them
// in the namespaces selected by the source.
package controller

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Domain is the annotation/label key prefix owned by Replikate.
const Domain = "replikate.brainchurts.com"

const (
	// SyncAnnotation, when present on a ConfigMap or Secret, marks it as a
	// replication source. Its value is a label selector matched against
	// namespace labels; an empty value means "all namespaces".
	SyncAnnotation = Domain + "/sync"

	// Finalizer lets Replikate clean up copies before a source is deleted.
	Finalizer = Domain + "/finalizer"

	// ManagedByLabel marks an object as a Replikate-managed copy.
	ManagedByLabel = Domain + "/managed-by"
	// OriginNSLabel records the source object's namespace on each copy.
	OriginNSLabel = Domain + "/origin-namespace"
	// OriginNameLabel records the source object's name on each copy.
	OriginNameLabel = Domain + "/origin-name"

	// ManagedByValue is the value of ManagedByLabel on managed copies.
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

// isAdoptable reports whether obj is an existing replicated copy that Replikate
// may safely take over — currently, any copy previously managed by AppsCode's
// config-syncer, identified by its origin annotation or origin labels.
func isAdoptable(obj client.Object) bool {
	if _, ok := obj.GetAnnotations()[kubedOriginPrefix]; ok {
		return true
	}
	for k := range obj.GetLabels() {
		if strings.HasPrefix(k, kubedOriginPrefix) {
			return true
		}
	}
	return false
}

// isSource reports whether obj is annotated as a replication source.
func isSource(obj client.Object) bool {
	_, ok := obj.GetAnnotations()[SyncAnnotation]
	return ok
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
