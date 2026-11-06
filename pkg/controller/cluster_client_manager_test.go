package controller

import (
	"errors"
	"log/slog"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestClusterClientManager_Get_CachesAndReuses(t *testing.T) {
	scheme := runtime.NewScheme()
	mgr := NewClusterClientManager(scheme, slog.Default())

	calls := 0
	mgr.configBuilder = func(_ ClusterConfig) (*rest.Config, error) {
		calls++
		return &rest.Config{}, nil
	}
	mgr.clientFactory = func(_ *rest.Config, _ client.Options) (client.Client, error) {
		return fake.NewClientBuilder().WithScheme(scheme).Build(), nil
	}

	c1, err := mgr.Get(ClusterConfig{ID: "alpha"})
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	c2, err := mgr.Get(ClusterConfig{ID: "alpha"})
	if err != nil {
		t.Fatalf("Get (second) error: %v", err)
	}
	if c1 != c2 {
		t.Errorf("expected cached client, got two different clients")
	}
	if calls != 1 {
		t.Errorf("configBuilder called %d times, want 1", calls)
	}
}

func TestClusterClientManager_GetDefault_EmptyID(t *testing.T) {
	scheme := runtime.NewScheme()
	mgr := NewClusterClientManager(scheme, slog.Default())
	mgr.configBuilder = func(cfg ClusterConfig) (*rest.Config, error) {
		if cfg.ID != "" {
			t.Errorf("expected empty ID for GetDefault, got %q", cfg.ID)
		}
		return &rest.Config{}, nil
	}
	mgr.clientFactory = func(_ *rest.Config, _ client.Options) (client.Client, error) {
		return fake.NewClientBuilder().WithScheme(scheme).Build(), nil
	}
	if _, err := mgr.GetDefault(); err != nil {
		t.Fatalf("GetDefault: %v", err)
	}
}

func TestClusterClientManager_Refresh_EvictsCache(t *testing.T) {
	scheme := runtime.NewScheme()
	mgr := NewClusterClientManager(scheme, slog.Default())
	calls := 0
	mgr.configBuilder = func(_ ClusterConfig) (*rest.Config, error) {
		calls++
		return &rest.Config{}, nil
	}
	mgr.clientFactory = func(_ *rest.Config, _ client.Options) (client.Client, error) {
		return fake.NewClientBuilder().WithScheme(scheme).Build(), nil
	}

	if _, err := mgr.Get(ClusterConfig{ID: "alpha"}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := mgr.Refresh("alpha"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, err := mgr.Get(ClusterConfig{ID: "alpha"}); err != nil {
		t.Fatalf("Get after refresh: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected configBuilder called 2x (initial + after refresh), got %d", calls)
	}
}

func TestClusterClientManager_Get_BuildError(t *testing.T) {
	scheme := runtime.NewScheme()
	mgr := NewClusterClientManager(scheme, slog.Default())
	mgr.configBuilder = func(_ ClusterConfig) (*rest.Config, error) {
		return nil, errors.New("kubeconfig missing")
	}
	if _, err := mgr.Get(ClusterConfig{ID: "alpha"}); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestClusterClientManager_Get_ConcurrentSafe(t *testing.T) {
	scheme := runtime.NewScheme()
	mgr := NewClusterClientManager(scheme, slog.Default())
	mgr.configBuilder = func(_ ClusterConfig) (*rest.Config, error) {
		return &rest.Config{}, nil
	}
	mgr.clientFactory = func(_ *rest.Config, _ client.Options) (client.Client, error) {
		return fake.NewClientBuilder().WithScheme(scheme).Build(), nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := mgr.Get(ClusterConfig{ID: "alpha"}); err != nil {
				t.Errorf("concurrent Get: %v", err)
			}
		}()
	}
	wg.Wait()
}
