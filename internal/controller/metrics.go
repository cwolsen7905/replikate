package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Prometheus metrics, registered with controller-runtime's registry so they are
// exposed on the manager's existing metrics endpoint.
var (
	reconcileTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "replikate_reconcile_total",
		Help: "Total source reconciles, labeled by kind and result (success or error).",
	}, []string{"kind", "result"})

	copyOperationsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "replikate_copy_operations_total",
		Help: "Total copy operations, labeled by kind and operation (created, updated, adopted, deleted).",
	}, []string{"kind", "operation"})

	clusterUp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "replikate_cluster_up",
		Help: "Whether a registered spoke cluster is currently reachable (1) or not (0), labeled by cluster id.",
	}, []string{"cluster"})

	remoteCopyOperationsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "replikate_remote_copy_operations_total",
		Help: "Total copy operations performed on spoke clusters, labeled by cluster and operation (created, updated, adopted, deleted).",
	}, []string{"cluster", "operation"})

	// copiesMigratedTotal counts copies re-stamped from a legacy annotation
	// domain to the primary one, labeled by the previous domain — a counter of
	// adoption events during a domain migration.
	copiesMigratedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "replikate_copies_migrated_total",
		Help: "Total managed copies migrated from a previous annotation domain to the primary, labeled by the previous domain.",
	}, []string{"from_domain"})

	// copiesByDomain reports how many managed copies currently carry each
	// annotation domain's stamp, labeled by domain and kind — the completion
	// signal for a domain migration (watch a legacy domain fall to 0).
	copiesByDomain = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "replikate_copies_by_domain",
		Help: "Managed copies currently stamped under each annotation domain, labeled by domain and kind.",
	}, []string{"domain", "kind"})
)

func init() {
	metrics.Registry.MustRegister(reconcileTotal, copyOperationsTotal, clusterUp,
		remoteCopyOperationsTotal, copiesMigratedTotal, copiesByDomain)
}

// kindOf returns the metric label for obj's kind.
func kindOf(obj client.Object) string {
	switch obj.(type) {
	case *corev1.ConfigMap:
		return "configmap"
	case *corev1.Secret:
		return "secret"
	default:
		return "unknown"
	}
}

// observeReconcile records the outcome of one source reconcile.
func observeReconcile(kind string, err error) {
	result := "success"
	if err != nil {
		result = "error"
	}
	reconcileTotal.WithLabelValues(kind, result).Inc()
}

// operationFor maps an internal action to its copy-operation metric label,
// returning "" for actionNone (nothing to record).
func operationFor(act action) string {
	switch act {
	case actionCreated:
		return "created"
	case actionUpdated:
		return "updated"
	case actionAdopted:
		return "adopted"
	default:
		return ""
	}
}
