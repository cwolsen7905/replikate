package controller

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// CredentialLabel marks a Secret in the controller's namespace as a spoke
	// cluster credential. The Secret's name is the cluster id.
	CredentialLabel = "replikate.brainchurts.com/cluster-credential"

	// credentialKubeconfigKey is the Secret data key holding the spoke's
	// kubeconfig.
	credentialKubeconfigKey = "kubeconfig"

	// spokeRequestTimeout bounds every request to a spoke cluster, so a spoke
	// that accepts the connection but hangs can't stall a reconcile worker.
	spokeRequestTimeout = 10 * time.Second
)

// clusterConn is a single registered spoke: its API client and last-known
// reachability.
type clusterConn struct {
	client client.Client
	up     bool
}

// ClusterRegistry holds the set of spoke clusters Replikate can replicate into,
// keyed by cluster id. It is safe for concurrent use. Phase 1 only tracks the
// clusters and their reachability; no replication reads from it yet.
type ClusterRegistry struct {
	mu       sync.RWMutex
	clusters map[string]*clusterConn
	// newClient builds a client from a REST config; overridable in tests.
	newClient func(cfg *rest.Config, id string) (client.Client, error)
}

// NewClusterRegistry returns an empty registry that builds clients with build.
func NewClusterRegistry(build func(cfg *rest.Config, id string) (client.Client, error)) *ClusterRegistry {
	return &ClusterRegistry{
		clusters:  map[string]*clusterConn{},
		newClient: build,
	}
}

// ClientFor returns the client for cluster id and whether it is registered.
func (r *ClusterRegistry) ClientFor(id string) (client.Client, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.clusters[id]
	if !ok {
		return nil, false
	}
	return c.client, true
}

// IDs returns the registered cluster ids in sorted order.
func (r *ClusterRegistry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.clusters))
	for id := range r.clusters {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Upsert registers (or replaces) the cluster built from secret's kubeconfig and
// returns the built client. It does not mark the cluster up; the caller runs a
// connectivity check and calls SetUp.
func (r *ClusterRegistry) Upsert(id string, secret *corev1.Secret) (client.Client, error) {
	cfg, err := restConfigFromCredential(secret)
	if err != nil {
		return nil, err
	}
	c, err := r.newClient(cfg, id)
	if err != nil {
		return nil, fmt.Errorf("build client for cluster %q: %w", id, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clusters[id] = &clusterConn{client: c}
	return c, nil
}

// SetUp records the reachability of a registered cluster and updates the gauge.
func (r *ClusterRegistry) SetUp(id string, up bool) {
	r.mu.Lock()
	if c, ok := r.clusters[id]; ok {
		c.up = up
	}
	r.mu.Unlock()
	if up {
		clusterUp.WithLabelValues(id).Set(1)
	} else {
		clusterUp.WithLabelValues(id).Set(0)
	}
}

// Remove deregisters a cluster and clears its gauge.
func (r *ClusterRegistry) Remove(id string) {
	r.mu.Lock()
	delete(r.clusters, id)
	r.mu.Unlock()
	clusterUp.DeleteLabelValues(id)
}

// restConfigFromCredential parses a credential Secret's kubeconfig into a REST
// config.
func restConfigFromCredential(secret *corev1.Secret) (*rest.Config, error) {
	raw, ok := secret.Data[credentialKubeconfigKey]
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("credential Secret %s/%s has no %q data key",
			secret.Namespace, secret.Name, credentialKubeconfigKey)
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("parse kubeconfig from %s/%s: %w", secret.Namespace, secret.Name, err)
	}
	cfg.Timeout = spokeRequestTimeout
	return cfg, nil
}

// connectivityCheck performs a cheap read against a cluster to confirm the
// client can reach and authenticate to its API server.
func connectivityCheck(ctx context.Context, c client.Client) error {
	var ns corev1.NamespaceList
	return c.List(ctx, &ns, client.Limit(1))
}

// ClusterUIDFromConfig reads the kube-system namespace UID for the cluster
// described by cfg, using a one-off direct client. It identifies the hub so the
// credential reconciler can reject a spoke credential that resolves back to it.
func ClusterUIDFromConfig(cfg *rest.Config, scheme *runtime.Scheme) (string, error) {
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return "", err
	}
	return clusterUID(context.Background(), c)
}
