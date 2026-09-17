package controller

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// DefaultDomainCountInterval is how often the domain-copy gauge is refreshed.
const DefaultDomainCountInterval = time.Minute

// DomainCounter is a manager Runnable that periodically counts managed copies
// per annotation domain and publishes them as the replikate_copies_by_domain
// gauge. It is the completion signal for a domain migration: an operator watches
// a legacy domain's count fall to 0 before dropping it from the legacy list.
//
// It reads from the manager's cache (the copies are already watched by the
// ConfigMap/Secret controllers), and runs only on the leader so a multi-replica
// deployment publishes one consistent series.
type DomainCounter struct {
	client.Client
	Keys     KeySet
	Interval time.Duration
}

// NeedLeaderElection makes the counter run only on the elected leader.
func (d *DomainCounter) NeedLeaderElection() bool { return true }

// Start runs the count loop until ctx is cancelled.
func (d *DomainCounter) Start(ctx context.Context) error {
	interval := d.Interval
	if interval <= 0 {
		interval = DefaultDomainCountInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	d.count(ctx) // publish once at startup rather than waiting a full interval
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			d.count(ctx)
		}
	}
}

// count lists managed copies under each domain and sets the gauge. List errors
// are logged and skipped: a stale gauge is better than a crash, and the next
// tick recovers. A copy carries exactly one domain's managed-by label (writes
// rebuild the label set from scratch), so the per-domain lists never overlap.
func (d *DomainCounter) count(ctx context.Context) {
	l := log.FromContext(ctx)
	for _, k := range d.Keys.each() {
		sel := client.MatchingLabels{k.ManagedByLabel: ManagedByValue}

		var cms corev1.ConfigMapList
		if err := d.List(ctx, &cms, sel); err != nil {
			l.Error(err, "counting copies by domain failed", "domain", k.Domain, "kind", "configmap")
		} else {
			copiesByDomain.WithLabelValues(k.Domain, "configmap").Set(float64(len(cms.Items)))
		}

		var secs corev1.SecretList
		if err := d.List(ctx, &secs, sel); err != nil {
			l.Error(err, "counting copies by domain failed", "domain", k.Domain, "kind", "secret")
		} else {
			copiesByDomain.WithLabelValues(k.Domain, "secret").Set(float64(len(secs.Items)))
		}
	}
}
