package controller

import (
	"fmt"
	"log/slog"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const defaultClusterKey = "default"

// ClusterConfig describes a remote Kubernetes cluster the operator can
// schedule workloads onto. The zero value (empty ID, empty
// KubeconfigPath) means "use the manager's in-cluster config" and is
// what GetDefaultClient passes.
type ClusterConfig struct {
	// ID is a stable identifier for the cluster. Used as the cache
	// key. Empty means default / in-cluster.
	ID string

	// KubeconfigPath, when non-empty, is loaded via clientcmd to
	// build the rest config. Empty means in-cluster.
	KubeconfigPath string

	// Context, when non-empty, overrides the current-context in the
	// loaded kubeconfig.
	Context string
}

// ClusterClientManager creates and caches controller-runtime clients
// for multiple Kubernetes clusters. Safe for concurrent use.
//
// Lifted from
// control-plane-next/operators/workspace_controller/cluster_client_manager.go
// where it was the production implementation; the only change here is
// that ClusterConfig is now a parameter type the library owns rather
// than a project-level config struct.
type ClusterClientManager struct {
	mu      sync.RWMutex
	clients map[string]client.Client
	scheme  *runtime.Scheme
	logger  *slog.Logger

	// configBuilder is overridable in tests; production callers
	// leave it nil and the manager falls back to ctrl.GetConfigOrDie /
	// clientcmd. The signature accepts the ClusterConfig and returns
	// a *rest.Config or an error.
	configBuilder func(ClusterConfig) (*rest.Config, error)

	// clientFactory is overridable in tests; production callers
	// leave it nil and the manager falls back to client.New.
	clientFactory func(*rest.Config, client.Options) (client.Client, error)
}

// NewClusterClientManager returns a ClusterClientManager that uses the
// provided scheme when building clients for remote clusters. logger
// may be nil (defaults to slog.Default).
func NewClusterClientManager(scheme *runtime.Scheme, logger *slog.Logger) *ClusterClientManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &ClusterClientManager{
		clients: make(map[string]client.Client),
		scheme:  scheme,
		logger:  logger.With("component", "cluster-client-manager"),
	}
}

// Get returns a cached client for the given cluster config. On first
// access for a given cluster ID the client is created and cached.
func (m *ClusterClientManager) Get(cfg ClusterConfig) (client.Client, error) {
	key := cfg.ID
	if key == "" {
		key = defaultClusterKey
	}

	m.mu.RLock()
	if c, ok := m.clients[key]; ok {
		m.mu.RUnlock()
		return c, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.clients[key]; ok {
		return c, nil
	}

	c, err := m.buildClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("building client for cluster %q: %w", key, err)
	}
	m.clients[key] = c
	m.logger.Info("cached new cluster client", "cluster", key)
	return c, nil
}

// GetDefault returns the in-cluster client (empty ClusterConfig).
func (m *ClusterClientManager) GetDefault() (client.Client, error) {
	return m.Get(ClusterConfig{})
}

// Refresh evicts the cached client for clusterID, forcing the next
// Get() to rebuild. Returns nil even if no entry was cached — the
// caller's intent is "next access should be fresh", and the empty-
// cache case satisfies that trivially.
func (m *ClusterClientManager) Refresh(clusterID string) error {
	if clusterID == "" {
		clusterID = defaultClusterKey
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.clients, clusterID)
	m.logger.Info("evicted cluster client", "cluster", clusterID)
	return nil
}

// buildClient constructs a controller-runtime client for the given
// cluster configuration. An empty KubeconfigPath uses the in-cluster
// config.
func (m *ClusterClientManager) buildClient(cfg ClusterConfig) (client.Client, error) {
	build := m.configBuilder
	if build == nil {
		build = m.defaultConfigBuilder
	}
	rc, err := build(cfg)
	if err != nil {
		return nil, err
	}
	factory := m.clientFactory
	if factory == nil {
		factory = client.New
	}
	return factory(rc, client.Options{Scheme: m.scheme})
}

func (m *ClusterClientManager) defaultConfigBuilder(cfg ClusterConfig) (*rest.Config, error) {
	if cfg.KubeconfigPath != "" {
		loadingRules := &clientcmd.ClientConfigLoadingRules{
			ExplicitPath: cfg.KubeconfigPath,
		}
		overrides := &clientcmd.ConfigOverrides{}
		if cfg.Context != "" {
			overrides.CurrentContext = cfg.Context
		}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
		rc, err := kubeConfig.ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("loading kubeconfig from %q: %w", cfg.KubeconfigPath, err)
		}
		return rc, nil
	}
	// In-cluster / default config. ctrl.GetConfigOrDie panics when
	// no config can be loaded; tests must inject configBuilder
	// before calling Get with an empty path.
	return ctrl.GetConfigOrDie(), nil
}
