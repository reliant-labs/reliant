//go:build e2e

package cli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestE2EK3dCreateRidesOutAServerCrashDuringBoot reproduces, on purpose, the
// one failure TestE2ERouteToOnHostWorkloadReachesHostProcess ever had in CI:
//
//	Node k3d-…-server-0 failed to get ready: error waiting for log line
//	`k3s is up and running` … stopped returning log lines: node … is
//	running=true in status=restarting
//
// The server container's process dies a moment into boot; Docker restarts it
// (restart policy unless-stopped), but k3d's readiness wait follows one log
// stream and gives up when it ends. Killing the container's entrypoint while
// k3d waits produces exactly that message, every time. forge's create must
// see the restarted server, delete the half-made cluster, and create it once
// more.
//
// Not parallel: it points KUBECONFIG at a temp file so the merge and the
// delete never touch the developer's kubeconfig.
func TestE2EK3dCreateRidesOutAServerCrashDuringBoot(t *testing.T) {
	requireTool(t, "k3d", "docker")
	t.Setenv("KUBECONFIG", t.TempDir()+"/kubeconfig")
	name := fmt.Sprintf("forge-bootcrash-%d", time.Now().UnixNano()%1_000_000)
	t.Cleanup(func() {
		if out, err := exec.Command("k3d", "cluster", "delete", name).CombinedOutput(); err != nil {
			t.Logf("cleanup: k3d cluster delete %s: %v\n%s", name, err, out)
		}
	})

	var crashes atomic.Int32
	prev := k3dServerBootCrashFn
	t.Cleanup(func() { k3dServerBootCrashFn = prev })
	k3dServerBootCrashFn = func(ctx context.Context, n string) string {
		reason := prev(ctx, n)
		if reason != "" {
			crashes.Add(1)
		}
		return reason
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	killed := make(chan error, 1)
	go func() { killed <- crashFirstServerBoot(ctx, "k3d-"+name+"-server-0") }()

	err := createK3dCluster(ctx, name, []string{"cluster", "create", name,
		"--servers", "1", "--agents", "0", "--no-lb", "--wait", "--timeout", "300s"})
	if kerr := <-killed; kerr != nil {
		t.Fatalf("could not crash the first boot, so this proves nothing: %v", kerr)
	}
	if err != nil {
		t.Fatalf("createK3dCluster did not ride out a server crash during boot: %v", err)
	}
	if got := crashes.Load(); got != 1 {
		t.Fatalf("saw %d boot crashes, want exactly the one this test caused", got)
	}
	out, err := exec.Command("docker", "inspect", "--format", "{{.RestartCount}} {{.State.Status}}", "k3d-"+name+"-server-0").Output()
	if err != nil || strings.TrimSpace(string(out)) != "0 running" {
		t.Fatalf("the recreated server is not a fresh, running container: %q %v", out, err)
	}
}

// crashFirstServerBoot waits for the server's k3s to start, then kills the
// container's entrypoint — a process dying under k3d's readiness wait, with
// Docker left to restart the container.
func crashFirstServerBoot(ctx context.Context, container string) error {
	for ctx.Err() == nil {
		if exec.CommandContext(ctx, "docker", "exec", container, "pidof", "k3s").Run() == nil {
			// k3s and the entrypoint that started it (k3s's parent): with
			// both gone, docker-init (PID 1) exits with an error and Docker
			// restarts the container.
			script := `k=$(pidof k3s); p=$(awk '/^PPid:/{print $2}' /proc/$k/status); kill -9 $k $p`
			if out, err := exec.CommandContext(ctx, "docker", "exec", container, "sh", "-c", script).CombinedOutput(); err != nil {
				return fmt.Errorf("kill the entrypoint of %s: %v\n%s", container, err, out)
			}
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("%s never started k3s: %w", container, ctx.Err())
}
