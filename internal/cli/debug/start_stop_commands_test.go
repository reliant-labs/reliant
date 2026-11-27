package debug

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-delve/delve/service/api"

	"github.com/reliant-labs/forge/internal/cli/factory"
	dbgsvc "github.com/reliant-labs/forge/internal/debug"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// These tests run `forge debug start` and `stop` end to end against shims for
// dlv, go and docker (installTools): a real process is spawned, listens, and
// answers Delve's rpc2 from a scripted server, so launch and reap logic run
// for real while no debugger, compiler or container is needed.

func setDlvMode(t *testing.T, mode string) { t.Helper(); t.Setenv("FORGE_FAKE_DLV_MODE", mode) }

// killSessionDlv is cleanup for tests that start a fake dlv: whatever the
// test left behind is killed, so no server outlives it.
func killSessionDlv(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if s, _ := debugSvc().LoadSession("."); s != nil && s.DlvPID > 0 {
			if p, err := os.FindProcess(s.DlvPID); err == nil {
				_ = p.Kill()
			}
		}
	})
}

// shrinkLaunchTimers makes the settle and grace waits test-sized.
func shrinkLaunchTimers(t *testing.T) {
	t.Helper()
	oldSettle, oldGrace := launchSettle, dlvTermGrace
	launchSettle, dlvTermGrace = 20*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { launchSettle, dlvTermGrace = oldSettle, oldGrace })
}

// startProject is a project with one main package, cmd/app.
func startProject(t *testing.T) string {
	t.Helper()
	dir := inTempProject(t)
	writeMain(t, filepath.Join(dir, "cmd", "app", "main.go"))
	return dir
}

func runStart(t *testing.T, f *factory.Factory, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runCmd(t, newStartCmd(f), args...)
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func TestStart_ServiceBuildsWithDebugFlagsAndLaunchesUnderDlv(t *testing.T) {
	testkit.Slow(t, "spawns a dlv shim process per launch")
	shrinkLaunchTimers(t)
	logs := installTools(t)
	dir := startProject(t)
	killSessionDlv(t)

	out, _, err := runStart(t, noFactory, "app", "--no-env")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// The build: debug flags, trimpath forced off, one importable main package.
	goCalls := logs.read(t, logs.goTool)
	if len(goCalls) != 1 {
		t.Fatalf("go invoked %d times: %v", len(goCalls), goCalls)
	}
	wantBuild := "build -gcflags=all=-N -l -trimpath=false -o " + filepath.Join(".forge", "debug", "app") + " ./cmd/app"
	if goCalls[0] != wantBuild {
		t.Errorf("go argv = %q, want %q", goCalls[0], wantBuild)
	}

	// The launch: a headless multi-client exec server on loopback.
	s := loadTestSession(t)
	if s == nil {
		t.Fatal("no session saved")
	}
	wantBin := filepath.Join(evalSymlinks(t, dir), ".forge", "debug", "app")
	if s.Type != "delve" || !s.Owned || s.Resumed || s.Docker || s.Connected || s.Binary != wantBin || s.DlvPID <= 0 || s.PID != 4194999 {
		t.Errorf("session = %+v, want an owned, halted delve session of %s", s, wantBin)
	}
	if !strings.HasPrefix(s.Addr, "127.0.0.1:") {
		t.Errorf("Addr = %q, want a loopback address", s.Addr)
	}
	dlvCalls := logs.read(t, logs.dlv)
	if len(dlvCalls) != 1 || !strings.HasPrefix(dlvCalls[0], "exec --headless --listen="+s.Addr+" --api-version=2 --accept-multiclient ") ||
		!strings.HasSuffix(dlvCalls[0], wantBin) {
		t.Errorf("dlv argv = %v", dlvCalls)
	}
	if wantLog := filepath.Join(".forge", "logs", "dev", "debug_app.log"); s.LogPath != wantLog {
		t.Errorf("LogPath = %q, want %q", s.LogPath, wantLog)
	}

	for _, want := range []string{"Building ./cmd/app with debug flags...", "Debug session started for app", "Delve listening at " + s.Addr,
		"Program output: " + s.LogPath, "Target is HALTED (at its entry point)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Configuration:") {
		t.Errorf("--no-env must not claim a workload:\n%s", out)
	}
}

func evalSymlinks(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestStart_RunLetsItServeAndJSONIsTheSession(t *testing.T) {
	testkit.Slow(t, "spawns a dlv shim process per launch")
	shrinkLaunchTimers(t)
	setDlvMode(t, "running")
	installTools(t)
	startProject(t)
	killSessionDlv(t)

	appPort := freeTCPPort(t)
	out, _, err := runStart(t, noFactory, "./cmd/app", "--no-env", "--run", "--json", "--app-port", strconv.Itoa(appPort))
	if err != nil {
		t.Fatalf("start --run: %v", err)
	}
	var got dbgsvc.SessionInfo
	jsonInto(t, out, &got)
	if !got.Resumed || !got.Owned || got.Addr == "" {
		t.Errorf("json session = %+v, want an owned, resumed one", got)
	}
	if saved := loadTestSession(t); saved == nil || saved.Addr != got.Addr || !saved.Resumed {
		t.Errorf("saved session = %+v", saved)
	}
}

func TestStart_RunReportsAServiceThatDiesOnBoot(t *testing.T) {
	testkit.Slow(t, "spawns a dlv shim process per launch")
	shrinkLaunchTimers(t)
	setDlvMode(t, "exited")
	installTools(t)
	startProject(t)
	killSessionDlv(t)

	_, _, err := runStart(t, noFactory, "app", "--no-env", "--run")
	if err == nil {
		t.Fatal("a service that exited during startup was reported as started")
	}
	for _, want := range []string{"the service exited during startup", "has exited with status 3", "panic: config: DATABASE_URL is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	if s := loadTestSession(t); s != nil {
		t.Errorf("a failed launch left a session behind: %+v", s)
	}
}

func TestStart_AppPortIsBoundBeforeBuilding(t *testing.T) {
	logs := installTools(t)
	startProject(t)

	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	defer func() { _ = l.Close() }()
	port := l.Addr().(*net.TCPAddr).Port

	_, _, err = runStart(t, noFactory, "app", "--no-env", "--app-port", strconv.Itoa(port))
	if err == nil || !strings.Contains(err.Error(), "--app-port "+strconv.Itoa(port)+" is in use") {
		t.Fatalf("err = %v, want the busy --app-port named", err)
	}
	if calls := logs.read(t, logs.goTool); len(calls) != 0 {
		t.Errorf("the build ran although the port was taken: %v", calls)
	}
}

func TestStart_Failures(t *testing.T) {
	t.Run("no service and no --attach", func(t *testing.T) {
		installTools(t)
		startProject(t)
		_, _, err := runStart(t, noFactory)
		if err == nil || !strings.Contains(err.Error(), "provide a service name or path, or use --attach") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a name with no main package says to pass a path", func(t *testing.T) {
		installTools(t)
		inTempProject(t)
		_, _, err := runStart(t, noFactory, "ghost", "--no-env")
		if err == nil || !strings.Contains(err.Error(), `service "ghost" not found in project config and no main package resolved; provide a path instead`) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a failing go build is reported and nothing is launched", func(t *testing.T) {
		logs := installTools(t)
		t.Setenv("FORGE_FAKE_GO_FAIL", "cmd/app/main.go:3: undefined: nope")
		startProject(t)
		_, stderr, err := runStart(t, noFactory, "app", "--no-env")
		if err == nil || !strings.Contains(err.Error(), "building debug binary") {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(stderr, "undefined: nope") {
			t.Errorf("the compiler's output was swallowed: %q", stderr)
		}
		if calls := logs.read(t, logs.dlv); len(calls) != 0 {
			t.Errorf("dlv ran after a failed build: %v", calls)
		}
	})

	t.Run("an unusable dlv is reported with its own message", func(t *testing.T) {
		shrinkLaunchTimers(t)
		installTools(t)
		setDlvMode(t, "refuse")
		startProject(t)
		_, _, err := runStart(t, noFactory, "app", "--no-env")
		if err == nil || !strings.Contains(err.Error(), "starting Delve") || !strings.Contains(err.Error(), "operation not permitted") {
			t.Fatalf("err = %v, want dlv's own message under 'starting Delve'", err)
		}
		if s := loadTestSession(t); s != nil {
			t.Errorf("session saved for a launch that failed: %+v", s)
		}
	})

	t.Run("an explicit --env without deploy/kcl is an error before building", func(t *testing.T) {
		logs := installTools(t)
		startProject(t)
		f := &factory.Factory{Debug: factory.DebugAPI{
			WorkloadNames: func(context.Context, string, string) ([]string, error) { return nil, nil },
			WorkloadEnv:   func(context.Context, string, string, string, []string) ([]string, error) { return nil, nil },
		}}
		_, _, err := runStart(t, f, "app", "--env", "staging")
		if err == nil || !strings.Contains(err.Error(), `env "staging": deploy/kcl/staging/ not found`) {
			t.Fatalf("err = %v", err)
		}
		if calls := logs.read(t, logs.goTool); len(calls) != 0 {
			t.Errorf("the build ran before the configuration was resolved: %v", calls)
		}
	})
}

func TestStart_ServiceLaunchesWithItsWorkloadEnvironment(t *testing.T) {
	testkit.Slow(t, "spawns a dlv shim process per launch")
	shrinkLaunchTimers(t)
	logs := installTools(t)
	dir := startProject(t)
	killSessionDlv(t)
	if err := os.MkdirAll(filepath.Join(dir, "deploy", "kcl", "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	port := freeTCPPort(t)
	var gotWorkload string
	f := &factory.Factory{Debug: factory.DebugAPI{
		WorkloadNames: func(context.Context, string, string) ([]string, error) { return []string{"api"}, nil },
		WorkloadEnv: func(_ context.Context, _, _, workload string, base []string) ([]string, error) {
			gotWorkload = workload
			return append(base, "PORT="+strconv.Itoa(port)), nil
		},
	}}
	out, _, err := runStart(t, f, "app")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if gotWorkload != "api" {
		t.Errorf("workload = %q, want api", gotWorkload)
	}
	for _, want := range []string{`Configuration: workload "api" of env "dev"`, "Service PORT: " + strconv.Itoa(port)} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if len(logs.read(t, logs.dlv)) != 1 {
		t.Error("dlv was not launched")
	}
}

func TestStart_AttachRecordsAnUnownedSession(t *testing.T) {
	testkit.Slow(t, "spawns a dlv shim process per launch")
	logs := installTools(t)
	inTempProject(t)
	killSessionDlv(t)

	out, _, err := runStart(t, noFactory, "--attach", "4242")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	s := loadTestSession(t)
	if s == nil || s.Owned || s.Resumed || s.PID != 4242 || s.Binary != "pid:4242" || s.DlvPID <= 0 {
		t.Fatalf("session = %+v, want an unowned, halted attach of pid 4242", s)
	}
	dlvCalls := logs.read(t, logs.dlv)
	if len(dlvCalls) != 1 || !strings.HasPrefix(dlvCalls[0], "attach --headless --listen="+s.Addr) || !strings.HasSuffix(dlvCalls[0], " 4242") {
		t.Errorf("dlv argv = %v", dlvCalls)
	}
	for _, want := range []string{"Attached to PID 4242 (forge does not own this process; 'stop' will detach, not kill)", "Delve listening at " + s.Addr, "Target is HALTED (where attach found it)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(".forge", "debug", "dlv-attach-4242.log")); err != nil {
		t.Errorf("dlv's log was not kept: %v", err)
	}
}

func TestStart_AttachRunAndJSON(t *testing.T) {
	testkit.Slow(t, "spawns a dlv shim process per launch")
	setDlvMode(t, "running")
	installTools(t)
	inTempProject(t)
	killSessionDlv(t)

	out, _, err := runStart(t, noFactory, "--attach", "31337", "--run", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got dbgsvc.SessionInfo
	jsonInto(t, out, &got)
	if got.PID != 31337 || got.Owned || !got.Resumed {
		t.Errorf("session = %+v, want an unowned resumed attach", got)
	}
}

func TestStart_AttachRefusedNamesThePID(t *testing.T) {
	testkit.Slow(t, "spawns a dlv shim process per launch")
	installTools(t)
	setDlvMode(t, "refuse")
	inTempProject(t)
	_, _, err := runStart(t, noFactory, "--attach", "4242")
	if err == nil || !strings.Contains(err.Error(), "attaching to PID 4242") || !strings.Contains(err.Error(), "operation not permitted") {
		t.Fatalf("err = %v", err)
	}
	if s := loadTestSession(t); s != nil {
		t.Errorf("a refused attach saved a session: %+v", s)
	}
}

func TestStart_Docker(t *testing.T) {
	t.Run("starts app-debug, finds its published Delve port and records an owned docker session", func(t *testing.T) {
		logs := installTools(t)
		inTempProject(t)
		f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil)}
		addr := startFakeDlv(t, f)
		_, port, _ := net.SplitHostPort(addr)
		t.Setenv("FORGE_FAKE_DOCKER_PORT", port)

		out, _, err := runStart(t, noFactory, "--docker")
		if err != nil {
			t.Fatalf("start --docker: %v", err)
		}
		s := loadTestSession(t)
		if s == nil || !s.Docker || !s.Owned || s.Addr != addr {
			t.Fatalf("session = %+v, want an owned docker session at %s", s, addr)
		}
		calls := logs.read(t, logs.docker)
		if len(calls) < 2 || !strings.HasSuffix(calls[0], "--profile debug up -d app-debug") ||
			!strings.HasPrefix(calls[0], "compose -f docker-compose.yml --project-directory . ") ||
			!strings.HasSuffix(calls[1], "port app-debug 2345") {
			t.Errorf("docker calls = %v", calls)
		}
		for _, want := range []string{"Waiting for debug container...", "Delve listening at " + addr, "Docker debug session started"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("a container that fails to start is reported", func(t *testing.T) {
		installTools(t)
		inTempProject(t)
		t.Setenv("FORGE_FAKE_DOCKER_FAIL", "1")
		_, _, err := runStart(t, noFactory, "--docker")
		if err == nil || !strings.Contains(err.Error(), "starting debug container") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a Delve that never answers times out naming the compose logs", func(t *testing.T) {
		installTools(t)
		inTempProject(t)
		old := dockerReadyTimeout
		dockerReadyTimeout = 700 * time.Millisecond
		t.Cleanup(func() { dockerReadyTimeout = old })
		_, _, err := runStart(t, noFactory, "--docker")
		if err == nil || !strings.Contains(err.Error(), "delve in app-debug not reachable after 700ms") || !strings.Contains(err.Error(), "docker compose logs app-debug") {
			t.Fatalf("err = %v", err)
		}
		if s := loadTestSession(t); s != nil {
			t.Errorf("session saved for an unreachable container: %+v", s)
		}
	})

	t.Run("a cancelled context stops the wait", func(t *testing.T) {
		installTools(t)
		inTempProject(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := waitForDockerDelve(ctx, time.Minute)
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("err = %v, want the cancellation", err)
		}
	})
}

// ---- stop ------------------------------------------------------------------

func runStopCmd(t *testing.T) (stdout, stderr string, err error) {
	t.Helper()
	return runCmd(t, newStopCmd(noFactory))
}

func TestStop_OwnedSessionKillsTheTarget(t *testing.T) {
	f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil)}
	withSession(t, f)
	out, _, err := runStopCmd(t)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, detaches := f.snapshot(); !slices.Equal(detaches, []bool{true}) {
		t.Errorf("detach(kill) calls = %v, want one with kill=true", detaches)
	}
	if !strings.Contains(out, "Debug session stopped (debugged process killed).") {
		t.Errorf("output = %q", out)
	}
	if s := loadTestSession(t); s != nil {
		t.Errorf("session survived stop: %+v", s)
	}
}

func TestStop_AttachedSessionDetachesAndNeverKills(t *testing.T) {
	f := &fakeDlvServer{state: runningState()}
	sleeper := startSleeper(t)
	withSession(t, f, func(s *dbgsvc.SessionInfo) { s.Owned = false; s.PID = sleeper.Process.Pid })
	out, _, err := runStopCmd(t)
	if err != nil {
		t.Fatal(err)
	}
	cmds, _, _, detaches := f.snapshot()
	if !slices.Equal(detaches, []bool{false}) {
		t.Errorf("detach(kill) calls = %v, want one with kill=false", detaches)
	}
	if !slices.Contains(cmds, "halt") {
		t.Errorf("commands = %v: a running target must be halted before detach", cmds)
	}
	if !strings.Contains(out, "detached; breakpoints removed, target process left running") {
		t.Errorf("output = %q", out)
	}
	if !sleeperAlive(sleeper) {
		t.Errorf("the attached target was killed: %v", err)
	}
}

func TestStop_UnreachableDlvFallsBackToSignalsButStillRespectsOwnership(t *testing.T) {
	t.Run("owned: the target is killed directly", func(t *testing.T) {
		sleeper := startSleeper(t)
		inTempProject(t)
		saveTestSession(t, &dbgsvc.SessionInfo{Type: "delve", Addr: closedAddr(t), PID: sleeper.Process.Pid, Owned: true})
		out, _, err := runStopCmd(t)
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the owned target to be killed", func() bool { return reaped(sleeper) })
		if !strings.Contains(out, "debugged process killed") {
			t.Errorf("output = %q", out)
		}
	})

	t.Run("attached: the target is left alone", func(t *testing.T) {
		sleeper := startSleeper(t)
		inTempProject(t)
		saveTestSession(t, &dbgsvc.SessionInfo{Type: "delve", Addr: closedAddr(t), PID: sleeper.Process.Pid, Owned: false})
		out, _, err := runStopCmd(t)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "target process left running") {
			t.Errorf("output = %q", out)
		}
		if !sleeperAlive(sleeper) {
			t.Error("an attached target was killed when dlv was unreachable")
		}
		if s := loadTestSession(t); s != nil {
			t.Errorf("session survived stop: %+v", s)
		}
	})

	t.Run("no session at all still succeeds", func(t *testing.T) {
		inTempProject(t)
		out, _, err := runStopCmd(t)
		if err != nil || !strings.Contains(out, "Debug session stopped (detached") {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})
}

func TestStop_WedgedDlvFallsBackToKillingAnOwnedTarget(t *testing.T) {
	// dlv answers the connection but its State errors: Stop fails, the target
	// is signalled.
	f := &fakeDlvServer{stateErr: "wedged"}
	sleeper := startSleeper(t)
	withSession(t, f, func(s *dbgsvc.SessionInfo) { s.PID = sleeper.Process.Pid })
	_, stderr, err := runStopCmd(t)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "Warning: error stopping debugger: wedged; killing pid "+strconv.Itoa(sleeper.Process.Pid)) {
		t.Errorf("stderr = %q", stderr)
	}
	waitFor(t, "the owned target to be killed", func() bool { return reaped(sleeper) })
}

func TestStop_DetachRPCFailureIsWarnedNotFatal(t *testing.T) {
	f := &fakeDlvServer{stateErr: "wedged"}
	withSession(t, f, func(s *dbgsvc.SessionInfo) { s.Owned = false })
	out, stderr, err := runStopCmd(t)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "Warning: detach RPC failed") || !strings.Contains(stderr, "SIGTERM") {
		t.Errorf("stderr = %q", stderr)
	}
	if !strings.Contains(out, "target process left running") {
		t.Errorf("output = %q", out)
	}
}

func TestStop_NeverKillsAProcessHostingThisCommand(t *testing.T) {
	// The debugged process "is" this very process (an ancestor-or-self of the
	// command): killing it would end the session running `forge debug stop`.
	f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil)}
	withSession(t, f, func(s *dbgsvc.SessionInfo) { s.PID = os.Getpid() })
	out, stderr, err := runStopCmd(t)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "skipped killing pid "+strconv.Itoa(os.Getpid())) || !strings.Contains(stderr, "detaching instead") {
		t.Errorf("stderr = %q", stderr)
	}
	if _, _, _, detaches := f.snapshot(); !slices.Equal(detaches, []bool{false}) {
		t.Errorf("detach(kill) calls = %v, want a plain detach", detaches)
	}
	if !strings.Contains(out, "target process left running") {
		t.Errorf("output = %q", out)
	}
}

func TestStop_ConnectedSessionReleasesOnlyItsBreakpoints(t *testing.T) {
	t.Run("clears the session's breakpoints, resumes, keeps the server", func(t *testing.T) {
		f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil), bps: bpsWithIDs(-1, 1, 2, 3)}
		withSession(t, f, func(s *dbgsvc.SessionInfo) { s.Owned = false; s.Connected = true; s.BreakpointIDs = []int{2, 9} })
		out, _, err := runStopCmd(t)
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the resume to reach dlv", func() bool { cmds, _, _, _ := f.snapshot(); return slices.Contains(cmds, "continue") })
		if _, _, cleared, detaches := f.snapshot(); !slices.Equal(cleared, []int{2}) || len(detaches) != 0 {
			t.Errorf("cleared=%v detaches=%v, want only breakpoint 2 cleared and no detach", cleared, detaches)
		}
		if !strings.Contains(out, "breakpoints cleared, target resumed; the dlv server keeps running") {
			t.Errorf("output = %q", out)
		}
		if s := loadTestSession(t); s != nil {
			t.Errorf("session survived stop: %+v", s)
		}
	})

	t.Run("a failed release keeps the session so stop can be retried", func(t *testing.T) {
		f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil), listErr: "boom"}
		withSession(t, f, func(s *dbgsvc.SessionInfo) { s.Owned = false; s.Connected = true })
		_, _, err := runStopCmd(t)
		if err == nil || !strings.Contains(err.Error(), "releasing the dlv server at") || !strings.Contains(err.Error(), "retry 'forge debug stop'") {
			t.Fatalf("err = %v", err)
		}
		if s := loadTestSession(t); s == nil {
			t.Error("the session was dropped although its breakpoints were not cleared")
		}
	})

	t.Run("an unreachable server keeps the session too", func(t *testing.T) {
		inTempProject(t)
		addr := closedAddr(t)
		saveTestSession(t, &dbgsvc.SessionInfo{Type: "delve", Addr: addr, Connected: true})
		_, _, err := runStopCmd(t)
		if err == nil || !strings.Contains(err.Error(), "cannot reach the dlv server at "+addr) || !strings.Contains(err.Error(), "session kept") {
			t.Fatalf("err = %v", err)
		}
		if s := loadTestSession(t); s == nil {
			t.Error("the session was dropped although its breakpoints were not cleared")
		}
	})
}

func bpsWithIDs(ids ...int) []*api.Breakpoint {
	out := make([]*api.Breakpoint, len(ids))
	for i, id := range ids {
		out[i] = &api.Breakpoint{ID: id}
	}
	return out
}

func TestStop_DockerSessionStopsTheContainer(t *testing.T) {
	t.Run("reachable", func(t *testing.T) {
		logs := installTools(t)
		f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil)}
		withSession(t, f, func(s *dbgsvc.SessionInfo) { s.Docker = true })
		out, _, err := runStopCmd(t)
		if err != nil {
			t.Fatal(err)
		}
		calls := logs.read(t, logs.docker)
		if len(calls) != 1 || !strings.HasSuffix(calls[0], "--profile debug stop app-debug") {
			t.Errorf("docker calls = %v", calls)
		}
		if !strings.Contains(out, "Docker debug session stopped.") {
			t.Errorf("output = %q", out)
		}
	})
	t.Run("delve already gone", func(t *testing.T) {
		logs := installTools(t)
		inTempProject(t)
		saveTestSession(t, &dbgsvc.SessionInfo{Type: "delve", Addr: closedAddr(t), Docker: true, Owned: true})
		if _, _, err := runStopCmd(t); err != nil {
			t.Fatal(err)
		}
		if calls := logs.read(t, logs.docker); len(calls) != 1 {
			t.Errorf("docker calls = %v, want the container stopped regardless", calls)
		}
	})
}

// A real dlv process is asked to exit with SIGTERM and killed only when it
// does not; a dlv that is no longer this session's is never signalled.
func TestStop_ReapsTheDlvServerForgeStarted(t *testing.T) {
	testkit.Slow(t, "spawns a dlv shim process and waits for its exit")

	startAttached := func(t *testing.T) *dbgsvc.SessionInfo {
		t.Helper()
		shrinkLaunchTimers(t)
		installTools(t)
		inTempProject(t)
		killSessionDlv(t)
		if _, _, err := runStart(t, noFactory, "--attach", "4242"); err != nil {
			t.Fatalf("attach: %v", err)
		}
		return loadTestSession(t)
	}
	dlvAlive := func(s *dbgsvc.SessionInfo) bool {
		p, err := os.FindProcess(s.DlvPID)
		return err == nil && p.Signal(syscall.Signal(0)) == nil
	}

	t.Run("a dlv that exits on detach is gone and the session cleared", func(t *testing.T) {
		s := startAttached(t)
		if !dlvAlive(s) {
			t.Fatal("the fake dlv did not start")
		}
		_, stderr, err := runStopCmd(t)
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "dlv to exit", func() bool { return !dlvAlive(s) })
		if strings.Contains(stderr, "did not exit") {
			t.Errorf("stderr = %q", stderr)
		}
		if loadTestSession(t) != nil {
			t.Error("session survived stop")
		}
	})

	t.Run("a dlv that ignores SIGTERM is killed, with a warning that breakpoints may remain", func(t *testing.T) {
		setDlvMode(t, "ignore-term")
		s := startAttached(t)
		_, stderr, err := runStopCmd(t)
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "dlv to be killed", func() bool { return !dlvAlive(s) })
		if !strings.Contains(stderr, "did not exit on SIGTERM and was killed; breakpoints may remain in pid 4242") {
			t.Errorf("stderr = %q", stderr)
		}
	})

	t.Run("a recycled dlv pid that is no dlv for the session's address is not signalled", func(t *testing.T) {
		sleeper := startSleeper(t)
		reapDlv(context.Background(), &dbgsvc.SessionInfo{DlvPID: sleeper.Process.Pid, Addr: "127.0.0.1:1", PID: 1})
		if !sleeperAlive(sleeper) {
			t.Error("reapDlv signalled a process that is not the session's dlv")
		}
	})

	t.Run("a nil session reaps nothing", func(t *testing.T) {
		reapDlv(context.Background(), nil)
	})
}

func TestStopLaunched_TearsDownAFailedLaunch(t *testing.T) {
	testkit.Slow(t, "spawns a dlv shim process and waits for its exit")
	shrinkLaunchTimers(t)
	installTools(t)
	inTempProject(t)
	killSessionDlv(t)
	if _, _, err := runStart(t, noFactory, "--attach", "4242"); err != nil {
		t.Fatal(err)
	}
	s := loadTestSession(t)
	stopLaunched(context.Background(), s)
	waitFor(t, "the launched dlv to be torn down", func() bool {
		p, err := os.FindProcess(s.DlvPID)
		return err != nil || p.Signal(syscall.Signal(0)) != nil
	})
}
