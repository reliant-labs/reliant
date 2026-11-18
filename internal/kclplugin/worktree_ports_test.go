package kclplugin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func TestPortReservationSubprocess(t *testing.T) {
	path := os.Getenv("FORGE_PORT_TEST_STORE")
	if path != "" {
		r := NewPersistentPortResolver(path)
		r.available = func(int) bool { return true }
		if _, err := r.Resolve(os.Getenv("FORGE_PORT_TEST_NAME"), 18180); err != nil {
			t.Fatal(err)
		}
		return
	}
	if testing.Short() {
		t.Skip("real subprocesses; runs in task test")
	}
	path = filepath.Join(t.TempDir(), "ports.json")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var cmds []*exec.Cmd
	for i := range 8 {
		cmd := exec.Command(exe, "-test.run=^TestPortReservationSubprocess$")
		cmd.Env = append(os.Environ(), "FORGE_PORT_TEST_STORE="+path, fmt.Sprintf("FORGE_PORT_TEST_NAME=worktree-%d", i))
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
	}
	for _, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	r := NewReadOnlyPortResolver(path)
	seen := map[int]bool{}
	for i := range 8 {
		p, err := r.Resolve(fmt.Sprintf("worktree-%d", i), 18180)
		if err != nil || seen[p] {
			t.Fatalf("cross-process collision: %d %v", p, err)
		}
		seen[p] = true
	}
}

func TestFixedBlocksAndResolvedPortsShareReservations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ports.json")
	r := NewPersistentPortResolver(path)
	r.available = func(int) bool { return true }
	if err := r.reserveFixed(8080, "feature", 8180); err != nil {
		t.Fatal(err)
	}
	hyperdx, err := r.Resolve("main-hyperdx", 8180)
	if err != nil || hyperdx == 8180 {
		t.Fatalf("resolver ignored block reservation: %d %v", hyperdx, err)
	}
	if err := r.reserveFixed(8080, "other", hyperdx); err == nil {
		t.Fatal("fixed port silently adopted another role's reservation")
	}
}

// Independent resolver instances model different forge processes, before any
// service binds. Merely probing sockets cannot distinguish these reservations.
func TestPersistentResolversReserveAcrossProcessesAndRoles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ports.json")
	const n = 16
	ports := make(chan int, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := NewPersistentPortResolver(path)
			r.available = func(int) bool { return true }
			p, err := r.Resolve(string(rune('a'+i)), 18180)
			if err != nil {
				errs <- err
				return
			}
			ports <- p
		}()
	}
	wg.Wait()
	close(ports)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	seen := map[int]bool{}
	for p := range ports {
		if seen[p] {
			t.Errorf("two independent allocators reserved %d", p)
		}
		seen[p] = true
	}
	if len(seen) != n {
		t.Fatalf("got %d distinct ports, want %d", len(seen), n)
	}
	r := NewReadOnlyPortResolver(path)
	for i := range n {
		p, err := r.Resolve(string(rune('a'+i)), 18180)
		if err != nil || !seen[p] {
			t.Fatalf("reservation lost: %d %v", p, err)
		}
	}
}

func TestWorktreesResolveNonintersectingIdpHyperdxOtlpSets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ports.json")
	seen := map[int]string{}
	for _, wt := range []string{"main", "feature"} {
		r := NewPersistentPortResolver(path)
		r.available = func(int) bool { return true }
		r.scope = wt
		for _, role := range []struct {
			name      string
			preferred int
		}{{"postgres", 5432}, {"api", 8085}, {"idp", 8080}, {"hyperdx", 8180}, {"otlp-http", 4318}, {"otlp-grpc", 4317}, {"profiles", 4328}} {
			name := "project-dev-" + role.name
			p, err := r.Resolve(name, role.preferred)
			if err != nil {
				t.Fatal(err)
			}
			if prev, ok := seen[p]; ok {
				t.Errorf("%s intersects %s at %d", name, prev, p)
			}
			seen[p] = name
		}
	}
}
