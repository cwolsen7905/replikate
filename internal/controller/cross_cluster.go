package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// reconcileRemote replicates src into the spoke clusters named by its
// target-clusters annotation — one copy in src's own namespace per cluster
// (config-syncer-style: no remote selector fan-out) — and removes copies from
// spokes it no longer targets. It is best-effort per spoke: an unreachable or
// misconfigured cluster is reported via an event and skipped, never failing the
// reconcile or blocking the local path, which has already succeeded.
func (s *Syncer) reconcileRemote(ctx context.Context, src client.Object) error {
	l := log.FromContext(ctx)
	targets := NamespaceSet(src.GetAnnotations()[s.Keys.TargetClustersAnnotation])

	// Warn about targeted clusters that aren't registered, so a typo or a
	// missing credential is visible rather than silent.
	for id := range targets {
		if _, ok := s.Registry.ClientFor(id); !ok {
			s.Recorder.Eventf(src, corev1.EventTypeWarning, "UnknownCluster",
				"target-clusters names unregistered cluster %q", id)
		}
	}

	// Walk every registered spoke: write into the targeted ones, prune the rest.
	for _, id := range s.Registry.IDs() {
		cl, ok := s.Registry.ClientFor(id)
		if !ok {
			continue
		}
		if targets[id] {
			act, err := s.upsertCopy(ctx, cl, src, src.GetNamespace())
			if err != nil {
				l.Error(err, "cross-cluster copy failed", "cluster", id)
				s.Recorder.Eventf(src, corev1.EventTypeWarning, "RemoteError",
					"Replicating to cluster %q failed: %v", id, err)
				continue
			}
			if act != actionNone {
				copyOperationsTotal.WithLabelValues(kindOf(src), operationFor(act)).Inc()
				s.Recorder.Eventf(src, corev1.EventTypeNormal, "RemoteReplicated",
					"Replicated to cluster %q", id)
			}
		} else if _, err := s.deleteCopies(ctx, cl, src, nil); err != nil {
			l.Error(err, "cross-cluster cleanup failed", "cluster", id)
			s.Recorder.Eventf(src, corev1.EventTypeWarning, "RemoteError",
				"Removing copies from cluster %q failed: %v", id, err)
		}
	}
	return nil
}

// deleteRemoteCopies removes every copy of src from all registered spokes, used
// when the source is deleted or its annotation dropped. Best-effort: a spoke
// that can't be reached is reported and skipped rather than blocking the
// source's own deletion (its copies are left for the next reconcile once the
// spoke returns).
func (s *Syncer) deleteRemoteCopies(ctx context.Context, src client.Object) error {
	l := log.FromContext(ctx)
	for _, id := range s.Registry.IDs() {
		cl, ok := s.Registry.ClientFor(id)
		if !ok {
			continue
		}
		if _, err := s.deleteCopies(ctx, cl, src, nil); err != nil {
			l.Error(err, "cross-cluster cleanup on delete failed", "cluster", id)
			s.Recorder.Eventf(src, corev1.EventTypeWarning, "RemoteError",
				"Removing copies from cluster %q on delete failed: %v", id, err)
		}
	}
	return nil
}
