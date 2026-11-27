package operatorkit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
)

// useFakeCluster points ctrl.GetConfig at an in-process API server that
// answers every request with 404, so Run gets a REACHABLE-looking cluster
// without any real one. It returns the log output Run writes.
func useFakeCluster(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "kubeconfig")
	cfg := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: fake
  cluster:
    server: %s
contexts:
- name: fake
  context: {cluster: fake, user: fake}
current-context: fake
users:
- name: fake
  user: {token: not-a-real-token}
`, srv.URL)
	if err := os.WriteFile(kubeconfig, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kubeconfig)
	t.Setenv("HOME", dir)
	t.Setenv("KUBERNETES_SERVICE_HOST", "")

	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

func skipIfInCluster(t *testing.T) {
	t.Helper()
	if runningInCluster() {
		t.Skip("this test process runs inside a Kubernetes pod, so the out-of-cluster path cannot be exercised")
	}
}

func TestRun_OutOfClusterWithoutLeaseNamespaceDisablesOperators(t *testing.T) {
	skipIfInCluster(t)
	logger, logs := useFakeCluster(t)

	setupCalled := false
	err := Run(context.Background(), logger, Options{LeaderElectionID: "test-leader"}, []Controller{{
		Name:             "scaler",
		SetupWithManager: func(ctrl.Manager) error { setupCalled = true; return nil },
	}})
	if err != nil {
		t.Fatalf("Run = %v; a reachable cluster without a lease namespace must degrade to nil, not die in NewManager", err)
	}
	if setupCalled {
		t.Error("a controller was set up although operators are disabled")
	}
	if !strings.Contains(logs.String(), "operators disabled: not running in-cluster") {
		t.Errorf("log %q does not say why operators are disabled", logs.String())
	}
}

func TestRun_NoClusterWarnsAndNamesTheCause(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KUBECONFIG", filepath.Join(dir, "no-kubeconfig"))
	t.Setenv("HOME", dir)
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	var logs bytes.Buffer

	err := Run(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), Options{LeaderElectionID: "x"}, nil)
	if err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if !strings.Contains(logs.String(), "operators disabled: no Kubernetes cluster reachable") {
		t.Errorf("log %q missing the no-cluster warning", logs.String())
	}
}

func TestRun_ControllerErrorsAreWrappedWithTheirName(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name        string
		controllers []Controller
		want        string
	}{
		{
			name: "scheme registration failure",
			controllers: []Controller{{
				Name:        "scaler",
				AddToScheme: func(*runtime.Scheme) error { return boom },
			}},
			want: "adding scaler scheme: boom",
		},
		{
			name: "setup failure",
			controllers: []Controller{
				{Name: "first", SetupWithManager: func(ctrl.Manager) error { return nil }},
				{Name: "second", SetupWithManager: func(ctrl.Manager) error { return boom }},
			},
			want: `setting up controller "second": boom`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, logs := useFakeCluster(t)
			err := Run(context.Background(), logger, Options{
				LeaderElectionID:        "test-leader",
				LeaderElectionNamespace: "default",
			}, tt.controllers)
			if !errors.Is(err, boom) {
				t.Fatalf("Run = %v, want an error wrapping boom", err)
			}
			if err.Error() != tt.want {
				t.Errorf("Run error = %q, want %q", err.Error(), tt.want)
			}
			if tt.name == "setup failure" && !strings.Contains(logs.String(), "controller=first") {
				t.Errorf("the first controller's registration was not logged: %q", logs.String())
			}
		})
	}
}

func TestRun_RejectsAnInvalidHealthProbeAddress(t *testing.T) {
	logger, _ := useFakeCluster(t)
	err := Run(context.Background(), logger, Options{
		LeaderElectionID:        "test-leader",
		LeaderElectionNamespace: "default",
		HealthProbeBindAddress:  "not-an-address",
	}, nil)
	// The manager binds the probe listener when it starts, so the failure
	// must surface from Run as an error rather than being swallowed.
	if err == nil {
		t.Fatal("Run with an unusable probe address returned nil")
	}
}

func TestRun_StartsTheManagerAndStopsOnCancel(t *testing.T) {
	logger, logs := useFakeCluster(t)
	ctx, cancel := context.WithCancel(context.Background())

	registered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, logger, Options{
			LeaderElectionID:        "test-leader",
			LeaderElectionNamespace: "default",
			HealthProbeBindAddress:  "127.0.0.1:0",
			LeaseDuration:           3 * time.Second,
			RenewDeadline:           2 * time.Second,
			RetryPeriod:             500 * time.Millisecond,
		}, []Controller{{
			Name:        "scaler",
			AddToScheme: func(*runtime.Scheme) error { return nil },
			SetupWithManager: func(ctrl.Manager) error {
				close(registered)
				return nil
			},
		}})
	}()

	select {
	case <-registered:
	case err := <-done:
		t.Fatalf("Run returned %v before the controller was set up", err)
	case <-time.After(30 * time.Second):
		t.Fatal("controller was never set up")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
	for _, want := range []string{"registered operator controller", "controller=scaler", "starting controller manager"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log %q missing %q", logs.String(), want)
		}
	}
}
