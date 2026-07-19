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
)

func init() {
	metrics.Registry.MustRegister(reconcileTotal, copyOperationsTotal)
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
