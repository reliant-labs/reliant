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
// The injection must not cause a crash of its own, and must hit only the
// first container. It used to `docker exec … pidof k3s` every 200ms from the
// moment the container existed, and kill whatever container had that name.
// In CI that crashed BOTH attempts (k3s "exited during boot on both
// attempts", the second with exit 137, this test's SIGKILL): an exec landing
// while k3d's cgroup-v2 entrypoint script evacuates the root cgroup puts a
// process back in it, the script's write to cgroup.subtree_control fails
// with EBUSY, and the entrypoint exits 1 before k3s starts (its output goes
// to /var/log/k3d-entrypoints_*.log, not to docker logs — which is why that
// first crash printed no log lines). forge recreated the cluster, as it
// should, and the poller, still waiting for its k3s, then killed the
// recreated one. Now it execs nothing until k3s has logged (the entrypoint
// scripts are done by then), and only into the first container, by ID.
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
	type injection struct {
		injected bool
		err      error
	}
	killed := make(chan injection, 1)
	go func() {
		ok, err := crashFirstServerBoot(ctx, "k3d-"+name+"-server-0")
		killed <- injection{ok, err}
	}()
	err := createK3dCluster(ctx, name, []string{"cluster", "create", name,
		"--servers", "1", "--agents", "0", "--no-lb", "--wait", "--timeout", "300s"})
	inj := <-killed
	if inj.err != nil {
		t.Fatalf("could not crash the first boot, so this proves nothing: %v", inj.err)
	}
	if !inj.injected {
		t.Log("the first server container crashed on its own before the injection; forge must ride out that crash all the same")
	}
	if err != nil {
		t.Fatalf("createK3dCluster did not ride out a server crash during boot: %v", err)
	}
	if got := crashes.Load(); got != 1 {
		t.Fatalf("saw %d boot crashes, want exactly one", got)
	}
	out, err := exec.Command("docker", "inspect", "--format", "{{.RestartCount}} {{.State.Status}}", "k3d-"+name+"-server-0").Output()
	if err != nil || strings.TrimSpace(string(out)) != "0 running" {
		t.Fatalf("the recreated server is not a fresh, running container: %q %v", out, err)
	}
}

// crashFirstServerBoot kills the entrypoint of the FIRST container named
// container once k3s has started in it — a process dying under k3d's
// readiness wait, with Docker left to restart the container. injected is
// false, with no error, when that container crashed or went away on its own
// first (forge then has a real boot crash to ride out).
//
// Nothing runs inside the container before k3s's first log line: k3d's
// entrypoint scripts log to a file, k3s to stdout, so a line in `docker logs`
// means the scripts — the cgroup-v2 evacuation among them — are done. Every
// command names the container by the ID it had when it first appeared, so a
// container forge recreated under the same name is never touched.
func crashFirstServerBoot(ctx context.Context, container string) (injected bool, err error) {
	var id string
	for id == "" {
		if ctx.Err() != nil {
			return false, fmt.Errorf("%s never appeared: %w", container, ctx.Err())
		}
		if out, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.Id}}", container).Output(); err == nil {
			id = strings.TrimSpace(string(out))
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	for ctx.Err() == nil {
		state, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.RestartCount}} {{.State.Status}}", id).Output()
		if err != nil {
			return false, nil // removed: forge already rolled it back
		}
		if f := strings.Fields(string(state)); len(f) == 2 && (f[0] != "0" || f[1] == "restarting" || f[1] == "exited") {
			return false, nil // crashed on its own before k3s started
		}
		if logs, _ := exec.CommandContext(ctx, "docker", "logs", "--tail", "1", id).CombinedOutput(); len(strings.TrimSpace(string(logs))) > 0 {
			// docker-init (PID 1) runs k3d's entrypoint, which runs k3s. With
			// the entrypoint gone, docker-init exits 137 and Docker restarts
			// the container.
			script := `for p in $(cat /proc/1/task/1/children); do if [ "$(cat /proc/$p/comm)" = "k3d-entrypoint." ]; then kill -9 "$p" && echo "killed $p"; fi; done`
			out, err := exec.CommandContext(ctx, "docker", "exec", id, "sh", "-c", script).CombinedOutput()
			if err != nil || !strings.Contains(string(out), "killed") {
				return false, fmt.Errorf("kill the entrypoint of %s (%s): %v\n%s", container, id, err, out)
			}
			return true, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false, fmt.Errorf("%s never started k3s: %w", container, ctx.Err())
}
