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
// reconcile or blocking the local path, which has already succeeded. It reports
// whether any spoke failed, so the caller can requeue and retry sooner than the
// next natural reconcile.
func (s *Syncer) reconcileRemote(ctx context.Context, src client.Object) (failed bool) {
	l := log.FromContext(ctx)
	targets := NamespaceSet(src.GetAnnotations()[s.Keys.TargetClustersAnnotation])

	// Warn about targeted clusters that aren't registered, so a typo or a
	// missing credential is visible rather than silent. Treated as a failure so
	// the source is requeued: this self-heals once the spoke's credential is
	// added (there is no credential->source watch yet — a pass-3 item that would
	// let us stop polling here). The cost of a genuine typo is a repeating event.
	for id := range targets {
		if _, ok := s.Registry.ClientFor(id); !ok {
			s.Recorder.Eventf(src, corev1.EventTypeWarning, "UnknownCluster",
				"target-clusters names unregistered cluster %q", id)
			failed = true
		}
	}

	// Walk every registered spoke: write into the targeted ones, prune the rest.
	for _, id := range s.Registry.IDs() {
		cl, ok := s.Registry.ClientFor(id)
		if !ok {
			continue
		}
		if targets[id] {
			act, err := s.upsertCopy(ctx, cl, src, src.GetNamespace(), s.HubClusterUID)
			if err != nil {
				l.Error(err, "cross-cluster copy failed", "cluster", id)
				s.Recorder.Eventf(src, corev1.EventTypeWarning, "RemoteError",
					"Replicating to cluster %q failed: %v", id, err)
				failed = true
				continue
			}
			if act != actionNone {
				remoteCopyOperationsTotal.WithLabelValues(id, operationFor(act)).Inc()
				s.Recorder.Eventf(src, corev1.EventTypeNormal, "RemoteReplicated",
					"Replicated to cluster %q", id)
			}
		} else if n, err := s.deleteCopies(ctx, cl, src, nil, s.HubClusterUID); err != nil {
			l.Error(err, "cross-cluster cleanup failed", "cluster", id)
			s.Recorder.Eventf(src, corev1.EventTypeWarning, "RemoteError",
				"Removing copies from cluster %q failed: %v", id, err)
			failed = true
		} else if n > 0 {
			remoteCopyOperationsTotal.WithLabelValues(id, "deleted").Add(float64(n))
		}
	}
	return failed
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
		if n, err := s.deleteCopies(ctx, cl, src, nil, s.HubClusterUID); err != nil {
			l.Error(err, "cross-cluster cleanup on delete failed", "cluster", id)
			s.Recorder.Eventf(src, corev1.EventTypeWarning, "RemoteError",
				"Removing copies from cluster %q on delete failed: %v", id, err)
		} else if n > 0 {
			remoteCopyOperationsTotal.WithLabelValues(id, "deleted").Add(float64(n))
		}
	}
	return nil
}
