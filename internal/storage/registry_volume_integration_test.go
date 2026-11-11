//go:build integration

package storage

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestMaintenanceMountsSurviveRegistryRemoval is H1 against a real Docker
// daemon. The registry is created the way k3d creates one — /var/lib/registry
// is an ANONYMOUS volume — and a --rm helper is started with the mounts
// RegistryGC uses. The registry container is then removed while the helper
// runs, which is the window in which `--rm --volumes-from` used to delete the
// image store when the helper exited.
func TestMaintenanceMountsSurviveRegistryRemoval(t *testing.T) {
	ctx := context.Background()
	name := fmt.Sprintf("k3d-forge-storage-volume-test-%d", time.Now().UnixNano())
	helper := name + "-forge-retention"
	run := func(args ...string) string {
		t.Helper()
		b, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	run("run", "-d", "--name", name, "registry:2")
	r := Runner{Policy: DefaultPolicy(), Command: Exec}
	info, err := r.inspect(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	var volume string
	for _, m := range info.Mounts {
		if m.Type == "volume" && m.Destination == "/var/lib/registry" {
			volume = m.Name
		}
	}
	if volume == "" {
		t.Fatalf("registry:2 has no /var/lib/registry volume: %+v", info.Mounts)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", helper, name).Run()
		_ = exec.Command("docker", "volume", "rm", volume).Run()
	})
	mounts, err := maintenanceMounts(info)
	if err != nil {
		t.Fatal(err)
	}
	run(append(append([]string{"run", "-d", "--rm", "--pull=never", "--name", helper}, mounts...), info.Image)...)
	// The registry goes away mid-pass; the helper is now the volume's only user.
	run("rm", "-f", name)
	run("stop", helper)
	for i := 0; i < 50; i++ {
		if exec.Command("docker", "inspect", helper).Run() != nil {
			break // --rm has finished removing the helper
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := exec.Command("docker", "volume", "inspect", volume).Run(); err != nil {
		t.Fatalf("the registry's data volume %s was removed with the --rm helper: %v", volume, err)
	}
}
