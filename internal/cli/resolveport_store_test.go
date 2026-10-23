// File: internal/cli/resolveport_store_test.go
//
// Pins the DOCUMENTED behavior of `resolve_port` against its real behavior,
// so the two cannot drift apart again.
//
// The scaffolded deploy/kcl/dev/main.k tells the reader, at length:
//
//	plugin.resolve_port(name, preferred) is AVAILABILITY-CHECKED. It takes
//	`preferred` when free, steps to the next free port when not, and
//	remembers the answer (.forge/ports-dev.json) so it is stable from then
//	on.
//
// "Stable from then on" was true on exactly two code paths — `forge env up`
// and `forge env deploy`, the only callers that armed the store. Every other
// render (env config, env render, db reset's DSN reconciliation, status,
// doctor, build) ran with an unbound resolver and re-probed from scratch.
//
// That is worse than never persisting, because a re-probe's answer depends on
// whether the stack is UP. A running postgres answers on its port, so the
// availability probe calls it busy, so the render steps to the next port —
// forge walks off the very database it was asked to describe. The observed
// consequence: `forge env config dev` printed a DSN on one port while the
// stack was live on another, and `forge db reset` was handed a port nothing
// listens on, making it unreachable through the declared path.
//
// The tests below pin the claim itself: a recorded port is returned again,
// EVEN WHEN SOMETHING IS LISTENING ON IT, because "this port is busy" and
// "this port is busy because it is mine and it is running" are the same
// observation to a probe, and the store is the only thing that can tell them
// apart.

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/kclplugin"
)

// writePortStore records name->port in a project's .forge/ports-<env>.json,
// the way a previous `forge env up` would have.
func writePortStore(t *testing.T, projectDir, env string, ports map[string]int) string {
	t.Helper()
	path := filepath.Join(projectDir, ".forge", "host-ports.json")
	scoped := map[string]int{}
	for name, port := range ports {
		scoped[filepath.Clean(projectDir)+":"+env+":"+name] = port
	}
	ports = scoped
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(ports, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// listenOn occupies a port for the duration of the test and returns it,
// standing in for "this project's own postgres is up and answering".
func listenOn(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot bind a loopback port in this environment: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	// Accept and drop, so the probe's dial succeeds (portAnswering) rather
	// than hanging on a full backlog.
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// writeResolvePortEnv writes a deploy/kcl/<env>/main.k that resolves one port
// through the real plugin seam, so the test exercises the SAME entry point
// every forge command uses (renderKCLRaw), not just the resolver in isolation.
func writeResolvePortEnv(t *testing.T, projectDir, env, role string, preferred int) {
	t.Helper()
	kdir := filepath.Join(projectDir, "deploy", "kcl", env)
	if err := os.MkdirAll(kdir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf("import kcl_plugin.forge\nport = forge.resolve_port(%q, %d)\n", role, preferred)
	if err := os.WriteFile(filepath.Join(kdir, "main.k"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// renderPortThroughCLI renders the env through renderKCLRaw — the single seam
// every forge render goes through — and returns the resolved port.
func renderPortThroughCLI(t *testing.T, projectDir, env string) int {
	t.Helper()
	out, err := renderKCLRaw(context.Background(), projectDir, env)
	if err != nil {
		t.Fatalf("render %s: %v", env, err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", out, err)
	}
	p, ok := m["port"].(float64)
	if !ok {
		t.Fatalf("no port in render output: %s", out)
	}
	return int(p)
}

// TestResolvePortHonorsStoreOnEveryRender is the regression-lock for the
// oscillating dev DSN. An ORDINARY render — not `env up`, not `env deploy` —
// must return the port the store recorded.
//
// Before the fix this returned the preferred port instead, because only the
// two launch paths armed the store.
func TestResolvePortHonorsStoreOnEveryRender(t *testing.T) {
	resetResolverForTest(t)
	projectDir := t.TempDir()
	const role, preferred = "proj-dev-postgres", 5432

	// A previous `forge env up` stepped off a busy 5432 and recorded 5480.
	writePortStore(t, projectDir, "dev", map[string]int{role: 5480})
	writeResolvePortEnv(t, projectDir, "dev", role, preferred)

	got := renderPortThroughCLI(t, projectDir, "dev")
	if got != 5480 {
		t.Fatalf("render resolved %d, want the RECORDED 5480 — an ordinary render "+
			"ignored .forge/ports-dev.json, so the DSN it reports is not the one "+
			"the stack was launched on", got)
	}
}

// TestResolvePortStableWhileItsOwnServiceIsRunning is the exact failure the
// user hit, and the one a probe alone can never get right: the port is busy
// BECAUSE this project's database is serving on it. Stepping off it walks
// away from the database forge was asked about.
//
// Availability-checking is still correct for a port with NO record. This
// pins that a RECORDED port is returned unconditionally.
func TestResolvePortStableWhileItsOwnServiceIsRunning(t *testing.T) {
	resetResolverForTest(t)
	projectDir := t.TempDir()
	const role = "proj-dev-postgres"

	live := listenOn(t) // stands in for the project's own running postgres
	writePortStore(t, projectDir, "dev", map[string]int{role: live})
	writeResolvePortEnv(t, projectDir, "dev", role, live)

	got := renderPortThroughCLI(t, projectDir, "dev")
	if got != live {
		t.Fatalf("render stepped from %d to %d while that port's own service was "+
			"answering — forge walked off its own database; the DSN now names a "+
			"port nothing listens on and `forge db reset` is unreachable", live, got)
	}

	// And twice in a row, because "stable from then on" is the claim.
	resetResolverForTest(t)
	if again := renderPortThroughCLI(t, projectDir, "dev"); again != live {
		t.Fatalf("second render drifted to %d (want %d) — the port oscillates "+
			"between runs", again, live)
	}
}

// TestReadOnlyRenderDoesNotCreatePortStore pins the other half of the
// documented split, and the reason the read path is read-ONLY.
//
// A reporting command must not mint machine-local state, and it especially
// must not RECORD a port it just resolved while the real service was up — that
// would pin the wrong answer forever, which is the oscillation made permanent.
// Writing stays with the launch paths (`env up` / `env deploy`), which resolve
// while the port is genuinely free.
func TestReadOnlyRenderDoesNotCreatePortStore(t *testing.T) {
	resetResolverForTest(t)
	projectDir := t.TempDir()
	const role, preferred = "proj-dev-api", 8085
	writeResolvePortEnv(t, projectDir, "dev", role, preferred)

	renderPortThroughCLI(t, projectDir, "dev")

	path := filepath.Join(projectDir, ".forge", "ports-dev.json")
	if _, err := os.Stat(path); err == nil {
		data, _ := os.ReadFile(path)
		t.Errorf("a read-only render created %s (%s) — writing belongs to the "+
			"launch paths, which resolve while the port is actually free", path, data)
	}
}

// TestLaunchPathStillWritesPortStore pins that arming the WRITABLE store
// still persists, and that the read-only arming in the render seam does not
// downgrade it. Without this, the fix for the read path could silently make
// the write path stop recording — and nothing would ever be remembered.
func TestLaunchPathStillWritesPortStore(t *testing.T) {
	resetResolverForTest(t)
	projectDir := t.TempDir()
	const role, preferred = "proj-dev-api", 8085
	writeResolvePortEnv(t, projectDir, "dev", role, preferred)

	// What activateDevStack does on `forge env up`.
	path := filepath.Join(projectDir, ".forge", "ports-dev.json")
	kclplugin.UsePortStore(path)

	got := renderPortThroughCLI(t, projectDir, "dev")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("launch path did not write the port store: %v — the KCL doc "+
			"promises resolve_port remembers its answer there", err)
	}
	var stored map[string]int
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored[role] != got {
		t.Errorf("store has %s=%d but the render resolved %d", role, stored[role], got)
	}
}

// resetResolverForTest returns the process-global resolver to its unbound,
// in-memory default, so one test's arming cannot leak into the next. The
// resolver is process-global by design (ports must stay stable across the
// several renders one command performs), which makes this explicit reset the
// test-side cost of that design.
func resetResolverForTest(t *testing.T) {
	t.Helper()
	kclplugin.ResetDefaultResolverForTest()
	t.Cleanup(kclplugin.ResetDefaultResolverForTest)
}
