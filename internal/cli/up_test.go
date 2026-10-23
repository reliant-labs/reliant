package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/reliant-labs/forge/internal/hostlaunch"
)

// TestBuildHostServiceCmd covers the `forge env up` host launch over each
// runner: the argv is DERIVED from the workload's GoBuild + spec args by
// hostlaunch.BuildCmd (its own table pins every runner); this pins that the
// up path feeds it the workload's facts, and that no `server <name>` is
// invented.
func TestBuildHostServiceCmd(t *testing.T) {
	ctx := context.Background()
	withRunner := func(r string) func(*WorkloadEntity) {
		return func(w *WorkloadEntity) { w.Runtime.Host.Runner = r }
	}
	withArgs := func(a ...string) func(*WorkloadEntity) {
		return func(w *WorkloadEntity) { w.Spec.Args = a }
	}
	sharedBinary := func(w *WorkloadEntity) {
		w.Build = BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/myproj", OutputName: "myproj"}}
	}
	// The built binary as the host names it: hostlaunch adds .exe on Windows
	// (its own table pins that per OS); this test pins only that up feeds it.
	myprojBin := hostlaunch.ExeName(runtime.GOOS, "./bin/myproj")
	cases := []struct {
		name    string
		svc     WorkloadEntity
		want    []string
		wantErr string
	}{
		{
			name: "go-run runs the GoBuild cmd with the workload's args",
			svc:  hostWL("api", sharedBinary, withArgs("api")),
			want: []string{"go", "run", "./cmd/myproj", "api"},
		},
		{
			name: "empty runner is go-run",
			svc:  hostWL("api", sharedBinary, withRunner(""), withArgs("api")),
			want: []string{"go", "run", "./cmd/myproj", "api"},
		},
		{
			name: "air with custom config",
			svc: hostWL("api", withRunner("air"), func(w *WorkloadEntity) {
				w.Runtime.Host.AirConfig = ".air.custom.toml"
			}),
			want: []string{"air", "-c", ".air.custom.toml"},
		},
		{
			name: "binary runs the built output with args",
			svc:  hostWL("admin-server", sharedBinary, withRunner("binary"), withArgs("admin")),
			want: []string{myprojBin, "admin"},
		},
		{
			name: "delve runner custom port",
			svc: hostWL("api", sharedBinary, withRunner("delve"), withArgs("api"), func(w *WorkloadEntity) {
				w.Runtime.Host.DelvePort = 3030
			}),
			want: []string{"dlv", "exec", "--headless", "--listen=:3030", "--api-version=2", "--accept-multiclient", "--continue", myprojBin, "--", "api"},
		},
		{
			name: "an explicit spec.command runs verbatim with args",
			svc: hostWL("reliant-api", func(w *WorkloadEntity) {
				w.Build = BuildConfigEntity{}
				w.Spec.Command = []string{"go", "run", "./cmd/reliant"}
				w.Spec.Args = []string{"server", "api"}
			}),
			want: []string{"go", "run", "./cmd/reliant", "server", "api"},
		},
		{
			name: "no GoBuild and no command is an error",
			svc: hostWL("api", func(w *WorkloadEntity) {
				w.Build = BuildConfigEntity{}
			}),
			wantErr: "no Go build",
		},
		{
			name:    "unknown runner errors",
			svc:     hostWL("api", withRunner("tilt")),
			wantErr: `unknown host runner "tilt"`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd, _, err := buildHostServiceCmd(ctx, nil, c.svc, nil, "dev")
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("want err containing %q, got %v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if strings.Join(cmd.Args, " ") != strings.Join(c.want, " ") {
				t.Fatalf("args:\n got  %q\n want %q", cmd.Args, c.want)
			}
		})
	}
}

// TestEntitiesEmpty confirms the sanity check fires only when every
// declaration kind is absent.
func TestEntitiesEmpty(t *testing.T) {
	if !entitiesEmpty(nil) {
		t.Error("nil: want empty")
	}
	if !entitiesEmpty(&KCLEntities{}) {
		t.Error("zero value: want empty")
	}
	if entitiesEmpty(&KCLEntities{Workloads: []WorkloadEntity{hostWL("a")}}) {
		t.Error("one workload: want non-empty")
	}
	if entitiesEmpty(&KCLEntities{Frontends: []FrontendEntity{{Name: "web"}}}) {
		t.Error("one frontend: want non-empty")
	}
	if entitiesEmpty(&KCLEntities{Infra: []HostInfraEntity{{Name: "postgres"}}}) {
		t.Error("one infra entry: want non-empty")
	}
}

// TestUpStatePath covers the project-scoped
// $HOME/.cache/forge/up/<project-id>/<env>.pids path: the <project-id> segment
// (a hash of the canonical project dir) is what keeps two projects that bring
// up the same env name from sharing — and tearing down — each other's ledger.
// The suffix stays /<env>.pids so the per-env contract with `forge env down` is
// stable within a project.
func TestUpStatePath(t *testing.T) {
	projectID := projectIDForDir(t.TempDir())
	got, err := upStatePath(projectID, "dev")
	if err != nil {
		t.Fatalf("upStatePath: %v", err)
	}
	wantSuffix := filepath.FromSlash("/.cache/forge/up/" + projectID + "/dev.pids")
	if !strings.HasSuffix(got, wantSuffix) {
		t.Errorf("upStatePath: got %q, want suffix %q", got, wantSuffix)
	}
	// The ledger must live UNDER a project-id segment, never directly at
	// up/dev.pids — the old, cross-project-colliding shape.
	if strings.HasSuffix(got, filepath.FromSlash("/.cache/forge/up/dev.pids")) {
		t.Errorf("upStatePath is not project-scoped: %q", got)
	}
	// An unidentifiable project gets an ERROR, never a shared fallback path:
	// a lookup keyed on nothing would read (and tear down) whatever happened
	// to be recorded there.
	if _, err := upStatePath("", "dev"); err == nil {
		t.Error("upStatePath with no project id: want an error, got a path")
	}
}

func TestUpLogPath_Sanitises(t *testing.T) {
	got, err := upLogPath("dev", "frontend:admin/web")
	if err != nil {
		t.Fatalf("upLogPath: %v", err)
	}
	// Colons and slashes in the NAME must be replaced so the file name is
	// safe. Only the base is checked: a Windows project dir carries a drive
	// colon (D:\a\...) that is not the name's to sanitise.
	if strings.Contains(filepath.Base(got), ":") {
		t.Errorf("upLogPath returned unsanitised path %q", got)
	}
	if !strings.HasSuffix(got, "frontend_admin_web.log") {
		t.Errorf("upLogPath: got %q, want frontend_admin_web.log suffix", got)
	}
}

// TestUpCmd_NoPortForwardSurface pins the phase-3 ingress refactor:
// `forge env up` no longer mentions port-forward in any user-facing string
// (Short / Long / Example / flag help). Reaching cluster services from
// the host is the Gateway API ingress path now (forge cluster urls); the
// orchestrator must not advertise a port-forward phase that doesn't
// exist.
func TestUpCmd_NoPortForwardSurface(t *testing.T) {
	cmd := newEnvUpCmd()
	surfaces := map[string]string{
		"Short": cmd.Short,
		"Long":  cmd.Long,
	}
	for label, s := range surfaces {
		if strings.Contains(strings.ToLower(s), "port-forward") {
			t.Errorf("%s mentions port-forward: %q", label, s)
		}
	}
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if strings.Contains(strings.ToLower(f.Usage), "port-forward") {
			t.Errorf("flag --%s usage mentions port-forward: %q", f.Name, f.Usage)
		}
	})
}

// TestBuildFrontendCmd_PortFromKCLOverridesParent pins Item 1: the
// frontend's KCL-declared port is force-injected into the child env,
// overriding any PORT bleeding in from the orchestrator's own
// os.Environ(). Before this fix a parent shell with `PORT=8080`
// exported for an unrelated service silently shifted the dev server's
// bind port out from under the user.
func TestBuildFrontendCmd_PortFromKCLOverridesParent(t *testing.T) {
	parent := []string{"PATH=/usr/bin", "PORT=8080", "EDITOR=vim"}
	fe := FrontendEntity{Name: "web", Path: "frontend", Port: 3000, EnvFile: "/does/not/exist"}
	cmd := buildFrontendCmd(context.Background(), fe, "dev", parent, nil, "")

	// PORT=3000 (KCL) must be present, PORT=8080 (parent) must NOT.
	hasKCLPort := false
	for _, kv := range cmd.Env {
		if kv == "PORT=3000" {
			hasKCLPort = true
		}
		if kv == "PORT=8080" {
			t.Errorf("parent PORT=8080 leaked into child env: %v", cmd.Env)
		}
	}
	if !hasKCLPort {
		t.Errorf("expected PORT=3000 from KCL; got env: %v", cmd.Env)
	}
	// Sanity: the rest of the parent env passed through.
	hasPath := false
	for _, kv := range cmd.Env {
		if kv == "PATH=/usr/bin" {
			hasPath = true
		}
	}
	if !hasPath {
		t.Errorf("expected parent PATH to survive; got env: %v", cmd.Env)
	}
}

// TestBuildFrontendCmd_KCLEnvVarsInjected confirms KCL-declared env_vars
// (e.g. a VITE_ADMIN_URL composed from forge.resolve_port) reach the dev
// process env, alongside the forced PORT and the passed-through shell.
func TestBuildFrontendCmd_KCLEnvVarsInjected(t *testing.T) {
	parent := []string{"PATH=/usr/bin", "SHELL_ONLY=keepme"}
	fe := FrontendEntity{
		Name: "reliant-web", Path: "web", Port: 3000, EnvFile: "/does/not/exist",
		EnvVars: []KCLEnvVar{
			{Name: "VITE_ADMIN_URL", Value: "http://localhost:3000/admin"},
			{Name: "VITE_API_URL", Value: "http://localhost:3090"},
		},
	}
	cmd := buildFrontendCmd(context.Background(), fe, "dev", parent, nil, "")

	got := map[string]string{}
	for _, kv := range cmd.Env {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			got[kv[:i]] = kv[i+1:]
		}
	}
	want := map[string]string{
		"VITE_ADMIN_URL": "http://localhost:3000/admin",
		"VITE_API_URL":   "http://localhost:3090",
		"PORT":           "3000",
		"SHELL_ONLY":     "keepme",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("env %s = %q, want %q (full: %v)", k, got[k], v, cmd.Env)
		}
	}
}

// TestBuildFrontendCmd_DeclaredEnvVarBeatsAmbientShell pins the precedence:
// a KCL-declared env_var WINS over a same-named variable that merely happens to
// be exported in the developer's shell. The inverse used to hold, which made
// every declaration advisory — the project renders one value and the process
// runs with another, silently. An override you typed is a decision; one you
// inherited is an accident, so the accident no longer wins by default.
func TestBuildFrontendCmd_DeclaredEnvVarBeatsAmbientShell(t *testing.T) {
	parent := []string{"VITE_ADMIN_URL=http://ambient"}
	fe := FrontendEntity{Name: "web", Path: "web", EnvFile: "/does/not/exist",
		EnvVars: []KCLEnvVar{{Name: "VITE_ADMIN_URL", Value: "http://kcl"}}}
	cmd := buildFrontendCmd(context.Background(), fe, "dev", parent, nil, "")
	found := false
	for _, kv := range cmd.Env {
		if kv == "VITE_ADMIN_URL=http://ambient" {
			t.Errorf("ambient shell value beat the declaration; env: %v", cmd.Env)
		}
		if kv == "VITE_ADMIN_URL=http://kcl" {
			found = true
		}
	}
	if !found {
		t.Errorf("declared value missing; env: %v", cmd.Env)
	}
}

// TestBuildFrontendCmd_ShellWinsWhenNamedExplicitly covers the escape hatch:
// naming the key in FORGE_ENV_OVERRIDE restores the old behaviour for it, so a
// deliberate ad-hoc override is still one command away.
func TestBuildFrontendCmd_ShellWinsWhenNamedExplicitly(t *testing.T) {
	parent := []string{
		"VITE_ADMIN_URL=http://ambient",
		hostlaunch.ShellWinsEnvVar + "=VITE_ADMIN_URL",
	}
	fe := FrontendEntity{Name: "web", Path: "web", EnvFile: "/does/not/exist",
		EnvVars: []KCLEnvVar{{Name: "VITE_ADMIN_URL", Value: "http://kcl"}}}
	cmd := buildFrontendCmd(context.Background(), fe, "dev", parent, nil, "")
	for _, kv := range cmd.Env {
		if kv == "VITE_ADMIN_URL=http://kcl" {
			t.Errorf("declared value won despite an explicit opt-out; env: %v", cmd.Env)
		}
	}
}

// TestBuildFrontendCmd_PortZeroLeavesParentPortAlone confirms the
// fe.Port == 0 fallback (legacy projects whose KCL doesn't emit the
// port field): we don't force-inject "PORT=0" because that would crash
// the dev server. The parent's PORT (if any) is left untouched.
func TestBuildFrontendCmd_PortZeroLeavesParentPortAlone(t *testing.T) {
	parent := []string{"PORT=8080"}
	fe := FrontendEntity{Name: "web", Path: "frontend", Port: 0, EnvFile: "/does/not/exist"}
	cmd := buildFrontendCmd(context.Background(), fe, "dev", parent, nil, "")

	for _, kv := range cmd.Env {
		if kv == "PORT=0" {
			t.Errorf("PORT=0 must not be injected for fe.Port==0; got %v", cmd.Env)
		}
	}
	found := false
	for _, kv := range cmd.Env {
		if kv == "PORT=8080" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected parent PORT=8080 to pass through when fe.Port==0; got env: %v", cmd.Env)
	}
}

// TestUpNoDeployFlag pins Item 5: the --no-deploy flag is registered
// on `forge env up` with the expected help text. The actual short-circuit
// behaviour is read straight off opts.noDeploy in runUp (`if !opts.noDeploy`
// gate around the deploy phase) — this test guards the flag wiring so
// a future refactor can't quietly delete the surface.
func TestUpNoDeployFlag(t *testing.T) {
	cmd := newEnvUpCmd()
	flag := cmd.Flags().Lookup("no-deploy")
	if flag == nil {
		t.Fatal("--no-deploy flag missing from forge env up")
	}
	if flag.DefValue != "false" {
		t.Errorf("--no-deploy default: got %q, want false", flag.DefValue)
	}
	if !strings.Contains(flag.Usage, "cluster apply") {
		t.Errorf("--no-deploy usage: got %q, want a phrase mentioning 'cluster apply'", flag.Usage)
	}
	// Verify the flag actually parses into opts.noDeploy by exercising
	// the cobra parser. We can't easily run the RunE without a project,
	// but flag parse is enough to confirm the BoolVar wiring is intact.
	if err := cmd.ParseFlags([]string{"--no-deploy"}); err != nil {
		t.Fatalf("parse --no-deploy: %v", err)
	}
	got, err := cmd.Flags().GetBool("no-deploy")
	if err != nil {
		t.Fatalf("GetBool --no-deploy: %v", err)
	}
	if !got {
		t.Errorf("--no-deploy: parsed value got false, want true")
	}
}

func TestSummaryLogPath_MatchesUpLogPath(t *testing.T) {
	// The displayed path must be the tail of the file upLogPath writes,
	// so a printed path is exactly what `grep`/`tail` will find.
	full, err := upLogPath("dev", "frontend:admin/web")
	if err != nil {
		t.Fatalf("upLogPath: %v", err)
	}
	disp := summaryLogPath("dev", "frontend:admin/web")
	// The display path is slash-separated on every OS (see upLogDir); the
	// file it names is the native one.
	if !strings.HasSuffix(filepath.ToSlash(full), disp) {
		t.Errorf("summaryLogPath %q is not a suffix of upLogPath %q", disp, full)
	}
	if want := ".forge/logs/dev/frontend_admin_web.log"; disp != want {
		t.Errorf("summaryLogPath: got %q, want %q", disp, want)
	}
}

// TestWorkloadHostPorts pins where a host workload's bind ports come from:
// the runtime's listen_ports when declared (declared EMPTY = binds nothing),
// else the workload's spec.ports. There is no env-var heuristic any more — a
// *_PORT variable is not a bind port (TEMPORAL_PORT is a dependency address),
// and guessing from them blocked `up` on healthy infra.
func TestWorkloadHostPorts(t *testing.T) {
	eq := func(t *testing.T, got, want []int) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	}
	// spec.ports, http first for the summary port.
	w := hostWL("api", withPorts(8081, 3091))
	eq(t, w.HostPorts(), []int{8081, 3091})
	if got := w.HostPort(); got != 8081 {
		t.Errorf("HostPort = %d, want the http port 8081", got)
	}
	// listen_ports wins over spec.ports, deduped and bounds-checked.
	w = hostWL("api", withPorts(8080), withListenPorts(3091, 3091, 0, 70000, 9090))
	eq(t, w.HostPorts(), []int{3091, 9090})
	if got := w.HostPort(); got != 3091 {
		t.Errorf("HostPort = %d, want the first listen port 3091", got)
	}
	// Declared EMPTY listen_ports: binds nothing, even with spec.ports.
	w = hostWL("desktop", withPorts(8080), withListenPorts())
	eq(t, w.HostPorts(), nil)
	if got := w.HostPort(); got != 0 {
		t.Errorf("HostPort = %d, want 0 for a workload that binds nothing", got)
	}
	// *_PORT env vars are NOT bind ports.
	w = hostWL("reliant-api-server", withEnv("TEMPORAL_PORT", "7233", "PORT", "8080"))
	eq(t, w.HostPorts(), nil)
	// The per-run ephemeral allocation (listen_ports + LaunchEnv PORT) is
	// what HostEnv and HostPorts both read.
	w = hostWL("api")
	w.Runtime.Host.ListenPorts = &[]int{51234}
	w.Runtime.Host.LaunchEnv = map[string]string{"PORT": "51234"}
	eq(t, w.HostPorts(), []int{51234})
	if got := w.HostEnv()["PORT"]; got != "51234" {
		t.Errorf("HostEnv PORT = %q, want the launch overlay 51234", got)
	}
}

func TestPersistUsesCapturedPid(t *testing.T) {
	// Regression: `forge env up --background` Release()s the child (resetting
	// cmd.Process.Pid to -1), so persist() must use the PID captured at
	// Start, and skip entries with no usable PID rather than writing -1/0.
	env := "test-persist-pidcapture"
	t.Setenv("HOME", t.TempDir())
	statePath, err := upStatePath(testProj, env)
	if err != nil {
		t.Fatal(err)
	}

	p := newProcRegistry(testProj, t.TempDir(), env)
	p.processes = []*managedProcess{
		{name: "svc-a", pid: 4242, cmd: &exec.Cmd{}}, // detached: captured pid, no live handle
		{name: "svc-b", pid: 0, cmd: &exec.Cmd{}},    // never captured: must be skipped
	}
	p.persist()

	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "svc-a\t4242\n") {
		t.Errorf("captured pid not persisted; got %q", got)
	}
	if strings.Contains(got, "svc-b") {
		t.Errorf("entry without a pid should be skipped; got %q", got)
	}
	if strings.Contains(got, "-1") {
		t.Errorf("a -1 pid leaked into the state file; got %q", got)
	}
}

func TestProcPID_PrefersCaptured(t *testing.T) {
	// Captured pid wins (survives Release on the detach path).
	if got := procPID(&managedProcess{pid: 4242, cmd: &exec.Cmd{}}); got != 4242 {
		t.Errorf("captured pid: got %d, want 4242", got)
	}
	// No captured pid, no live process → 0 (callers skip, never signal).
	if got := procPID(&managedProcess{cmd: &exec.Cmd{}}); got != 0 {
		t.Errorf("unset: got %d, want 0", got)
	}
}

func TestSignalProcessGroup_NonPositiveIsNoop(t *testing.T) {
	// A 0/-1 pid must never fan a signal out (negative pid = whole group).
	if err := signalProcessGroup(0, syscall.SIGTERM); err != nil {
		t.Errorf("pid 0: %v", err)
	}
	if err := signalProcessGroup(-1, syscall.SIGTERM); err != nil {
		t.Errorf("pid -1: %v", err)
	}
}

func TestInTargetSet(t *testing.T) {
	// Empty filter matches everything (default).
	if !inTargetSet(nil, "admin-server") {
		t.Error("empty filter should match everything")
	}
	// Non-empty: only named entries match.
	targets := []string{"admin-server", "reliant-web"}
	if !inTargetSet(targets, "admin-server") {
		t.Error("named target should match")
	}
	if inTargetSet(targets, "workspace-proxy") {
		t.Error("unnamed target should not match")
	}
}

// TestPortInUse asserts the real-socket probe: a held listener reads as
// in-use, a port nothing binds reads as free. The post-close flip is
// best-effort (ephemeral reuse can be racy) — we assert the true case
// against a held listener and the false case against a never-bound port.
func TestPortInUse(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	held := ln.Addr().(*net.TCPAddr).Port

	if !portInUse(held) {
		t.Errorf("portInUse(%d) = false; want true (listener is held)", held)
	}

	// Find a port that is (almost certainly) free: bind, capture, release.
	free, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free: %v", err)
	}
	freePort := free.Addr().(*net.TCPAddr).Port
	_ = free.Close()
	if portInUse(freePort) {
		t.Errorf("portInUse(%d) = true; want false (port was released)", freePort)
	}
}

// TestConflictingPorts exercises the pure collection logic with an
// injected probe — no real sockets. It covers --target scoping, the
// host-service inline-PORT skip, the frontend feature gate, and the
// fe.Port==0 skip.
func TestConflictingPorts(t *testing.T) {
	entities := &KCLEntities{
		Workloads: []WorkloadEntity{
			hostWL("admin-server", withListenPorts(8090)),
			// Multi-port host workload: three distinct declared bind ports.
			hostWL("api", withListenPorts(8081, 3091, 6060)),
			hostWL("noport"),
			clusterWL("cluster-svc", "k3d-x", "ns", withPorts(9999)),
		},
		Frontends: []FrontendEntity{
			{Name: "reliant-web", Port: 3000},
			{Name: "noportfe", Port: 0},
		},
	}
	// busy treats these ports as in-use.
	busy := func(ports ...int) func(int) bool {
		set := map[int]bool{}
		for _, p := range ports {
			set[p] = true
		}
		return func(p int) bool { return set[p] }
	}

	names := func(cs []portConflict) []string {
		out := make([]string, len(cs))
		for i, c := range cs {
			out[i] = c.name
		}
		return out
	}

	t.Run("both host and frontend ports busy", func(t *testing.T) {
		got := conflictingPorts(entities, nil, true, busy(8090, 3000))
		if g := names(got); len(g) != 2 || g[0] != "admin-server" || g[1] != "reliant-web" {
			t.Fatalf("got %v; want [admin-server reliant-web]", g)
		}
	})

	t.Run("nothing busy → no conflicts", func(t *testing.T) {
		if got := conflictingPorts(entities, nil, true, busy()); len(got) != 0 {
			t.Fatalf("got %v; want none", names(got))
		}
	})

	t.Run("target scopes the check", func(t *testing.T) {
		// admin-server is up, but we only target reliant-web → allowed.
		got := conflictingPorts(entities, []string{"reliant-web"}, true, busy(8090))
		if len(got) != 0 {
			t.Fatalf("got %v; want none (admin-server not in target set)", names(got))
		}
		// Now reliant-web's own port is busy → it should flag.
		got = conflictingPorts(entities, []string{"reliant-web"}, true, busy(3000))
		if g := names(got); len(g) != 1 || g[0] != "reliant-web" {
			t.Fatalf("got %v; want [reliant-web]", g)
		}
	})

	t.Run("frontend gate off skips frontends", func(t *testing.T) {
		got := conflictingPorts(entities, nil, false, busy(8090, 3000))
		if g := names(got); len(g) != 1 || g[0] != "admin-server" {
			t.Fatalf("got %v; want [admin-server] (frontends gated off)", g)
		}
	})

	t.Run("host service without inline PORT is skipped", func(t *testing.T) {
		// Even if some port the probe would call true for, noport declares
		// no PORT so it never contributes a conflict.
		got := conflictingPorts(entities, []string{"noport"}, true, func(int) bool { return true })
		if len(got) != 0 {
			t.Fatalf("got %v; want none (noport declares no PORT)", names(got))
		}
	})

	t.Run("multi-port service surfaces EVERY busy port", func(t *testing.T) {
		// Gap A: api binds :8081, :3091, :6060. A conflict on any one of them
		// must be caught — previously only the first was probed, so a stale
		// stack squatting :3091 slipped past and a second stack launched.
		got := conflictingPorts(entities, []string{"api"}, true, busy(3091))
		if g := names(got); len(g) != 1 || g[0] != "api" || got[0].port != 3091 {
			t.Fatalf("got %v (ports %v); want one api conflict on :3091", g, ports(got))
		}
		// Two of its three ports busy → two conflicts, both named api.
		got = conflictingPorts(entities, []string{"api"}, true, busy(8081, 6060))
		if len(got) != 2 || got[0].port != 8081 || got[1].port != 6060 {
			t.Fatalf("got ports %v; want [8081 6060] both on api", ports(got))
		}
	})
}

func ports(cs []portConflict) []int {
	out := make([]int, len(cs))
	for i, c := range cs {
		out[i] = c.port
	}
	return out
}

// TestCollectUpServices verifies the summary/services row collection:
// host services + EVERY declared frontend are surfaced (multiple
// frontends never collapse to one), ports/URLs resolve from the KCL
// conventions, the health probe is applied, and --target / the frontend
// gate scope the set.
func TestCollectUpServices(t *testing.T) {
	entities := &KCLEntities{
		Workloads: []WorkloadEntity{
			hostWL("admin-server", withListenPorts(8090)),
			hostWL("noport"),
			clusterWL("cluster-svc", "k3d-x", "ns", withPorts(9999)),
		},
		Frontends: []FrontendEntity{
			{Name: "admin-web", Port: 3000},
			{Name: "reliant-web", Port: 3001},
		},
	}
	// admin-server (:8090) and reliant-web (:3001) are listening; admin-web
	// (:3000) is not (still booting).
	probe := func(p int) bool { return p == 8090 || p == 3001 }

	rows := collectUpServices(entities, "dev", nil, true, probe)

	// Order: host services first, then ALL frontends. cluster-svc is dropped
	// (not host-mode).
	var got []string
	for _, r := range rows {
		got = append(got, r.Kind+":"+r.Name)
	}
	want := []string{"host:admin-server", "host:noport", "frontend:admin-web", "frontend:reliant-web"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rows: got %v, want %v", got, want)
	}

	byName := map[string]upServiceRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	if r := byName["admin-server"]; r.Port != 8090 || r.URL != "http://localhost:8090" || !r.Listening {
		t.Errorf("admin-server: got %+v; want port 8090, url set, listening", r)
	}
	if r := byName["noport"]; r.Port != 0 || r.URL != "" || r.Listening {
		t.Errorf("noport: got %+v; want no port/url and not listening", r)
	}
	if r := byName["admin-web"]; r.Port != 3000 || r.Listening {
		t.Errorf("admin-web: got %+v; want port 3000, not listening", r)
	}
	if r := byName["reliant-web"]; r.Port != 3001 || !r.Listening {
		t.Errorf("reliant-web: got %+v; want port 3001, listening", r)
	}
	// Both frontends present — never collapsed to one.
	feCount := 0
	for _, r := range rows {
		if r.Kind == "frontend" {
			feCount++
		}
	}
	if feCount != 2 {
		t.Errorf("frontend rows: got %d, want 2 (all frontends surfaced)", feCount)
	}

	// Frontend gate off drops frontends entirely.
	rows = collectUpServices(entities, "dev", nil, false, probe)
	for _, r := range rows {
		if r.Kind == "frontend" {
			t.Errorf("frontend %q listed with gate off", r.Name)
		}
	}

	// --target scopes to one service.
	rows = collectUpServices(entities, "dev", []string{"reliant-web"}, true, probe)
	if len(rows) != 1 || rows[0].Name != "reliant-web" {
		t.Errorf("target scope: got %v; want [reliant-web]", rows)
	}
}

// TestRenderUpSummary formats the block to a buffer and asserts it lists
// every service with its URL, health word, and log path — the greppable
// contract an agent relies on. It also prints the block so the test log
// shows the real output shape.
func TestRenderUpSummary(t *testing.T) {
	rows := []upServiceRow{
		{Name: "admin-server", Kind: "host", URL: "http://localhost:8090", Port: 8090, Log: ".forge/logs/dev/admin-server.log", Listening: true, PID: 4242, Owned: true},
		{Name: "worker", Kind: "host", Log: ".forge/logs/dev/worker.log"},
		{Name: "admin-web", Kind: "frontend", URL: "http://localhost:3000", Port: 3000, Log: ".forge/logs/dev/frontend_admin-web.log", Listening: true, PID: 5151, Owned: false},
		{Name: "reliant-web", Kind: "frontend", URL: "http://localhost:3001", Port: 3001, Log: ".forge/logs/dev/frontend_reliant-web.log", Listening: false},
	}
	var b bytes.Buffer
	renderUpSummary(&b, "dev", rows, "down", true, []string{"Ctrl-C to stop."}, nil)
	out := b.String()
	t.Logf("\n%s", out)

	for _, want := range []string{
		"admin-server", "http://localhost:8090", "up (pid 4242)",
		"admin-web", "up (pid 5151, not forge-owned)",
		"reliant-web", "http://localhost:3001", "down",
		"worker", "(no port declared)",
		".forge/logs/dev/frontend_reliant-web.log",
		"Logs   .forge/logs/dev/",
		"forge cluster urls",
		"forge env status dev",
		"Host services", "Frontends",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q\n---\n%s", want, out)
		}
	}
}

// TestRunUpServices_EndToEnd drives the `forge env status` code path all
// the way through: render KCL (via the fixture override) → collect rows →
// probe a real listener → enrich ownership → format. It binds a live
// socket on the host service's port so the health snapshot reports it
// "up", and leaves the two frontends unbound so they report "down" — and
// asserts BOTH frontends appear (never collapsed). Covers the text table
// and the --json contract.
func TestRunUpServices_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	// A real listener on a free port so the probe reports the host service up.
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	// Serve HTTP on it like the host service it stands in for. A bare
	// accept-and-ignore socket made the App Health runtime check wait out its
	// 3s client timeout on both runUpServices calls and print a red
	// "healthz failed" — nothing asserts on it, but it read as the failure.
	srv := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	dir := t.TempDir()
	writeForgeYAML(t, dir, `name: demo
module_path: github.com/example/demo
version: "0.1.0"
binary: shared
`)
	fixture := fmt.Sprintf(`{
  "output": {
    "frontends": [
      {
        "name": "admin-web",
        "path": "web/admin",
        "port": 3100
      },
      {
        "name": "reliant-web",
        "path": "web/reliant",
        "port": 3101
      }
    ],
    "workloads": [
      {
        "name": "admin-server",
        "kind": "service",
        "runtime": {
          "type": "host",
          "runner": "go-run",
          "listen_ports": [%d]
        },
        "spec": {
          "kind": "service"
        }
      },
      {
        "name": "cluster-only",
        "kind": "service",
        "runtime": {
          "type": "cluster",
          "cluster": "k3d-demo"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`, port)
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, fixture))
	t.Chdir(dir)

	// Text output.
	out := captureStdout(t, func() {
		if err := runUpServices(context.Background(), "dev", false, "", false); err != nil {
			t.Fatalf("runUpServices: %v", err)
		}
	})
	t.Logf("\n%s", out)
	for _, want := range []string{
		"admin-server", fmt.Sprintf("http://localhost:%d", port),
		"admin-web", "http://localhost:3100",
		"reliant-web", "http://localhost:3101", // BOTH frontends surfaced
		"down", // an unbound frontend port
		"forge env status dev",
		".forge/logs/dev/",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "cluster-only") {
		t.Errorf("cluster service leaked into host/frontend table:\n%s", out)
	}

	// JSON output.
	jsonOut := captureStdout(t, func() {
		if err := runUpServices(context.Background(), "dev", true, "", false); err != nil {
			t.Fatalf("runUpServices json: %v", err)
		}
	})
	var rep upServicesReport
	if err := json.Unmarshal([]byte(jsonOut), &rep); err != nil {
		t.Fatalf("parse json %q: %v", jsonOut, err)
	}
	if rep.Env != "dev" {
		t.Errorf("json env: got %q, want dev", rep.Env)
	}
	byName := map[string]upServiceRow{}
	for _, r := range rep.Services {
		byName[r.Name] = r
	}
	if len(byName) != 3 { // 1 host + 2 frontends (cluster excluded)
		t.Fatalf("json services: got %d (%v), want 3", len(byName), byName)
	}
	if r := byName["admin-server"]; r.Port != port || !r.Listening || r.PID <= 0 {
		t.Errorf("admin-server json: got %+v; want port %d, listening, pid>0", r, port)
	}
	if _, present := byName["admin-web"]; !present {
		t.Error("admin-web missing from json")
	}
	if r, present := byName["reliant-web"]; !present || r.Listening {
		t.Errorf("reliant-web json: got %+v (present=%v); want present and not listening", r, present)
	}
}

// listeningSet builds an injected `listening` probe for the readiness tests.
func listeningSet(ports ...int) func(int) bool {
	set := map[int]bool{}
	for _, p := range ports {
		set[p] = true
	}
	return func(p int) bool { return set[p] }
}

// TestClassifyPortReadiness is the Gap-B core: after launch, tell OUR OWN
// marked child apart from a foreign/stale holder and from nothing-listening.
// listening / resolvePID / procFacts are all injected — no real sockets or
// lsof.
func TestClassifyPortReadiness(t *testing.T) {
	f := fakeFacts{
		env: map[int][]string{
			100: marker("dev", "api"), // our child for env=dev
			200: {"PATH=/usr/bin"},    // foreign: no marker
		},
		ppid: map[int]int{100: 1, 200: 1},
	}
	resolve := func(port int) int {
		switch port {
		case 8081:
			return 100 // ours
		case 3091:
			return 200 // foreign
		case 7000:
			return 0 // listening but holder unresolvable
		default:
			return 0
		}
	}

	// our-child-bound.
	if got := classifyPortReadiness(8081, testProj, "dev", listeningSet(8081, 3091, 7000), resolve, f); got != portReadyOurs {
		t.Errorf("our child bound: got %v, want portReadyOurs", got)
	}
	// foreign-holds-port (the stale-holder trap).
	if got := classifyPortReadiness(3091, testProj, "dev", listeningSet(8081, 3091, 7000), resolve, f); got != portReadyForeign {
		t.Errorf("foreign holder: got %v, want portReadyForeign", got)
	}
	// nothing-listening (silent bind failure).
	if got := classifyPortReadiness(9999, testProj, "dev", listeningSet(8081, 3091, 7000), resolve, f); got != portReadyNobody {
		t.Errorf("nothing listening: got %v, want portReadyNobody", got)
	}
	// Listening but holder unresolvable (no lsof / Windows) → degrade to
	// "ours" rather than a FALSE foreign that would fail a healthy run.
	if got := classifyPortReadiness(7000, testProj, "dev", listeningSet(7000), resolve, f); got != portReadyOurs {
		t.Errorf("unresolvable holder: got %v, want portReadyOurs (graceful degrade)", got)
	}
	// A marker for a DIFFERENT env is foreign to us (mirrors the reclaim
	// guard's per-env ownership).
	f2 := fakeFacts{env: map[int][]string{300: marker("staging", "api")}, ppid: map[int]int{300: 1}}
	if got := classifyPortReadiness(8081, testProj, "dev", listeningSet(8081), func(int) int { return 300 }, f2); got != portReadyForeign {
		t.Errorf("wrong-env marker: got %v, want portReadyForeign", got)
	}
	// A marker for a DIFFERENT project (same env name) is likewise foreign —
	// the post-launch readiness gate must not paint another project's stack as
	// "ours" on our port.
	f3 := fakeFacts{env: map[int][]string{400: markerProj("projB", "dev", "api")}, ppid: map[int]int{400: 1}}
	if got := classifyPortReadiness(8081, testProj, "dev", listeningSet(8081), func(int) int { return 400 }, f3); got != portReadyForeign {
		t.Errorf("different-project marker: got %v, want portReadyForeign", got)
	}
}

// TestEvalHostReadiness checks the whole-service snapshot: every declared
// bind port of a multi-port host service is classified, non-host / no-port
// services contribute nothing, --target scopes the set, and the failure list
// + error name the offending ports with the right foreign-vs-nobody wording.
func TestEvalHostReadiness(t *testing.T) {
	entities := &KCLEntities{
		Workloads: []WorkloadEntity{
			hostWL("api", withListenPorts(8081, 3091, 6060)),
			hostWL("noport"),
			clusterWL("cluster-svc", "k3d-x", "ns", withPorts(9999)),
		},
	}
	// :8081 bound by our child (100, marked dev); :3091 held by a foreign
	// process (200, unmarked); :6060 nothing listening.
	f := fakeFacts{
		env:  map[int][]string{100: marker("dev", "api"), 200: {"PATH=/x"}},
		ppid: map[int]int{100: 1, 200: 1},
	}
	listening := listeningSet(8081, 3091)
	resolve := func(p int) int {
		switch p {
		case 8081:
			return 100
		case 3091:
			return 200
		default:
			return 0
		}
	}

	rs := evalHostReadiness(entities, testProj, "dev", nil, listening, resolve, f)
	if len(rs) != 3 { // one per api port; noport + cluster-svc contribute none
		t.Fatalf("got %d results, want 3 (one per api bind port): %+v", len(rs), rs)
	}
	byPort := map[int]portReadyState{}
	for _, r := range rs {
		if r.name != "api" {
			t.Errorf("unexpected service in readiness set: %q", r.name)
		}
		byPort[r.port] = r.state
	}
	if byPort[8081] != portReadyOurs {
		t.Errorf(":8081 = %v, want ours", byPort[8081])
	}
	if byPort[3091] != portReadyForeign {
		t.Errorf(":3091 = %v, want foreign", byPort[3091])
	}
	if byPort[6060] != portReadyNobody {
		t.Errorf(":6060 = %v, want nobody", byPort[6060])
	}

	// Only the not-ours ports keep the gate waiting / name the failure.
	unready := hostReadyUnready(rs)
	if len(unready) != 2 {
		t.Fatalf("unready = %d, want 2 (the foreign + the nobody)", len(unready))
	}
	msg := hostReadyError("dev", unready, nil).Error()
	for _, want := range []string{"api", "3091", "6060", "forge env down dev"} {
		if !strings.Contains(msg, want) {
			t.Errorf("readiness error missing %q:\n%s", want, msg)
		}
	}
	if !strings.Contains(msg, "stale/foreign") || !strings.Contains(msg, "compilation or startup") {
		t.Errorf("readiness error should distinguish foreign vs nobody:\n%s", msg)
	}
	// :8081 (ours) must NOT appear as a failure.
	if strings.Contains(msg, "8081") {
		t.Errorf("readiness error names an already-bound port:\n%s", msg)
	}

	// All ports ours → nothing unready (the healthy pass).
	allOurs := evalHostReadiness(entities, testProj, "dev", nil, func(int) bool { return true },
		func(int) int { return 100 }, f)
	if u := hostReadyUnready(allOurs); len(u) != 0 {
		t.Errorf("all-ours: want no unready, got %+v", u)
	}

	// --target scopes the gate: targeting only the no-port service yields
	// nothing to wait on.
	if got := evalHostReadiness(entities, testProj, "dev", []string{"noport"}, listening, resolve, f); len(got) != 0 {
		t.Errorf("target=noport: want 0 results, got %+v", got)
	}
}

// TestWaitHostServicesReady_NoPortsIsInstantPass pins the fast path: when no
// host service declares a bind port there is nothing to gate, so the wrapper
// returns nil immediately without any real socket/lsof work or polling.
func TestWaitHostServicesReady_NoPortsIsInstantPass(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{
		hostWL("noport"),
		clusterWL("cluster-svc", "k3d-x", "ns", withPorts(9999)),
	}}
	done := make(chan error, 1)
	go func() {
		done <- waitHostServicesReady(context.Background(), e, testProj, "dev", nil, hostReadyTimeout, hostReadyPoll, nil)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("no declared ports: want nil, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitHostServicesReady did not return promptly when no ports are declared")
	}
}

// withDevRunDefaults injects the dev-run env defaults (ENVIRONMENT=development,
// AUTO_MIGRATE=true) at the lowest precedence for a dev env, and passes the
// project config through untouched for non-dev — so `forge env up` boots a
// migrated schema in dev while production stays unchanged (turnkey dev migrate).
//
// ENVIRONMENT carries the CORS story: it is what enables the backend's CORS
// layer (serverkit.Config.CORSEnabled) and selects the origin-reflecting dev
// policy. `forge env up` therefore synthesizes NO CORS_ORIGINS of its own — it
// used to derive one from the declared frontend ports purely to trip
// serverkit's old "origins non-empty" gate, a value the generated factory's
// dev branch then discarded. That derivation could not work in general
// anyway: resolveEphemeralFrontendPorts hands a portless frontend a
// kernel-assigned port, so there is often no origin to name in advance.
func TestWithDevRunDefaults(t *testing.T) {
	// Non-dev: passthrough, no dev-run vars injected. A deployed posture owns
	// its own CORS allow-list and gets no layer without one.
	base := map[string]string{"LOG_LEVEL": "info"}
	got := withDevRunDefaults(base, false)
	if _, ok := got["AUTO_MIGRATE"]; ok {
		t.Fatalf("non-dev env must not receive AUTO_MIGRATE; got %v", got)
	}
	if _, ok := got["CORS_ORIGINS"]; ok {
		t.Fatalf("non-dev env must not receive a CORS_ORIGINS default; got %v", got)
	}
	if got["ENVIRONMENT"] == "development" {
		t.Fatalf("non-dev env must not be forced to development; got %v", got)
	}

	// Dev: the pair injected as defaults, project config preserved.
	got = withDevRunDefaults(map[string]string{"LOG_LEVEL": "debug"}, true)
	if got["ENVIRONMENT"] != "development" {
		t.Fatalf("dev env must default ENVIRONMENT=development; got %q", got["ENVIRONMENT"])
	}
	if got["AUTO_MIGRATE"] != "true" {
		t.Fatalf("dev env must default AUTO_MIGRATE=true; got %q", got["AUTO_MIGRATE"])
	}
	if got["LOG_LEVEL"] != "debug" {
		t.Fatalf("existing project config must be preserved; got %v", got)
	}

	// No CORS_ORIGINS is ever synthesized. The dev posture does not need one,
	// and inventing an allow-list would be a second, competing answer to a
	// question ENVIRONMENT already answers.
	if v, ok := got["CORS_ORIGINS"]; ok {
		t.Fatalf("dev run must not synthesize CORS_ORIGINS; got %q", v)
	}

	// An empty projected value must not shadow a dev default: the KCL
	// projection is total, so a field no config.k pinned arrives as "". For
	// ENVIRONMENT that absence winning would silently demote the run to a
	// deployed posture and take dev CORS down with it.
	got = withDevRunDefaults(map[string]string{"ENVIRONMENT": "", "AUTO_MIGRATE": ""}, true)
	if got["ENVIRONMENT"] != "development" {
		t.Fatalf("an unpinned \"\" must not shadow the ENVIRONMENT dev default; got %q", got["ENVIRONMENT"])
	}
	if got["AUTO_MIGRATE"] != "true" {
		t.Fatalf("an unpinned \"\" must not shadow the AUTO_MIGRATE dev default; got %q", got["AUTO_MIGRATE"])
	}

	// Precedence: an explicit project-config value overrides the dev-run
	// default (the dev-run defaults are the LOWEST layer).
	got = withDevRunDefaults(map[string]string{
		"ENVIRONMENT":  "staging-like",
		"AUTO_MIGRATE": "false",
		"CORS_ORIGINS": "https://app.example.com",
	}, true)
	if got["ENVIRONMENT"] != "staging-like" {
		t.Fatalf("project config must override the dev-run default; got %q", got["ENVIRONMENT"])
	}
	if got["AUTO_MIGRATE"] != "false" {
		t.Fatalf("project config must override the AUTO_MIGRATE default; got %q", got["AUTO_MIGRATE"])
	}
	if got["CORS_ORIGINS"] != "https://app.example.com" {
		t.Fatalf("an explicitly configured CORS_ORIGINS must survive; got %q", got["CORS_ORIGINS"])
	}
}

// THE BANNER IS WHERE PEOPLE LOOK. A stack whose identity halves disagree
// comes up entirely green — every process running, every port bound — and
// then 401s every authenticated RPC. doctor has always been able to say so,
// but nothing prompts a run of it at the moment the stack starts, so the
// diagnosis sat in a command nobody was told to type.
func TestAuthParityTrailer(t *testing.T) {
	t.Run("warns when only the frontend names an issuer", func(t *testing.T) {
		dir := t.TempDir()
		writeParityConfigK(t, dir, `app_config: config_gen.AppConfig = {
    environment = "development"
}

web_config: frontend_config_gen.WebConfig = {
    oidc_issuer = "http://localhost:8080"
}
`)
		got := authParityTrailerIn(dir, "dev")
		if !strings.Contains(got, "Auth:") {
			t.Errorf("no warning for a half-wired stack, got %q", got)
		}
		if !strings.Contains(got, "forge doctor") {
			t.Errorf("the warning must say where the detail is, got %q", got)
		}
	})

	t.Run("silent when both halves agree", func(t *testing.T) {
		dir := t.TempDir()
		writeParityConfigK(t, dir, `app_config: config_gen.AppConfig = {
    jwt_issuer = "http://localhost:8080"
}

web_config: frontend_config_gen.WebConfig = {
    oidc_issuer = "http://localhost:8080"
}
`)
		if got := authParityTrailerIn(dir, "dev"); got != "" {
			t.Errorf("a correctly-wired stack must print nothing, got %q", got)
		}
	})

	t.Run("silent when neither half names an issuer", func(t *testing.T) {
		// The scaffold's default before any IdP exists. No key material is
		// the correct closed-and-bootable state, not a misconfiguration.
		dir := t.TempDir()
		writeParityConfigK(t, dir, "app_config: config_gen.AppConfig = {\n    environment = \"development\"\n}\n")
		if got := authParityTrailerIn(dir, "dev"); got != "" {
			t.Errorf("an identity-less project must print nothing, got %q", got)
		}
	})
}

func writeParityConfigK(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, "deploy", "kcl", "dev")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.k"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ENVIRONMENT=development is defaulted for air workloads at the LOWEST layer:
// project config, KCL env and the shell all beat it, and non-air runners are
// untouched.
func TestWithAirRunDefaultsPrecedence(t *testing.T) {
	layer := func(shell []string, projectCfg, kclEnv map[string]string, runner string) string {
		cfg := withAirRunDefaults(projectCfg, runner)
		env := hostlaunch.LayerHostEnv(shell, cfg, nil, kclEnv)
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, "ENVIRONMENT="); ok {
				return v
			}
		}
		return "<unset>"
	}
	if got := layer(nil, nil, nil, "air"); got != "development" {
		t.Errorf("air default = %q, want development", got)
	}
	if got := layer(nil, map[string]string{"ENVIRONMENT": ""}, nil, "air"); got != "development" {
		t.Errorf("empty projected value must not shadow the default; got %q", got)
	}
	if got := layer(nil, map[string]string{"ENVIRONMENT": "staging"}, nil, "air"); got != "staging" {
		t.Errorf("project config must win; got %q", got)
	}
	if got := layer(nil, nil, map[string]string{"ENVIRONMENT": "test"}, "air"); got != "test" {
		t.Errorf("KCL env must win; got %q", got)
	}
	// Same policy as every other host env var: the shell wins only when it
	// opts in via FORGE_ENV_OVERRIDE (hostlaunch.ShellWinsEnvVar).
	if got := layer([]string{"ENVIRONMENT=ci", hostlaunch.ShellWinsEnvVar + "=ENVIRONMENT"}, nil, nil, "air"); got != "ci" {
		t.Errorf("opted-in shell must win; got %q", got)
	}
	if got := layer(nil, nil, nil, "binary"); got != "<unset>" {
		t.Errorf("non-air runner must not get the default; got %q", got)
	}
}
