package debug

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/rpc"
	"net/rpc/jsonrpc"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-delve/delve/service/api"

	dbgsvc "github.com/reliant-labs/forge/internal/debug"
)

// This file holds a scripted Delve rpc2 server and the stand-ins for the
// external programs forge debug drives (`dlv`, `go`, `docker`), so the whole
// command layer runs without a live debugger, a compiler or a container.
//
// The same fake serves two ways: in-process (a listener the test connects a
// session to), and as the `dlv` binary itself — a shell shim on PATH re-execs
// this test binary into TestHelper_dlv, which parses dlv's argv and serves the
// fake on its --listen address, exactly where forge's launch code expects dlv.

// Wire types: net/rpc wants exported argument types whose fields mirror the
// unexported ones in internal/debug/rpcclient.go.
type (
	FEmpty    struct{}
	FStateIn  struct{ NonBlocking bool }
	FStateOut struct{ State *api.DebuggerState }
	FCmdOut   struct{ State api.DebuggerState }
	FPidOut   struct{ Pid int }
	FDetachIn struct{ Kill bool }
	FCreateIn struct {
		Breakpoint          api.Breakpoint
		LocExpr             string
		SubstitutePathRules [][2]string
		Suspended           bool
	}
	FCreateOut struct{ Breakpoint api.Breakpoint }
	FClearIn   struct {
		Id   int //nolint:revive // delve's wire name
		Name string
	}
	FClearOut  struct{ Breakpoint *api.Breakpoint }
	FListBPIn  struct{ All bool }
	FListBPOut struct{ Breakpoints []*api.Breakpoint }
	FStackIn   struct {
		Id     int64 //nolint:revive // delve's wire name
		Depth  int
		Full   bool
		Defers bool
		Opts   api.StacktraceOptions
		Cfg    *api.LoadConfig
		Skip   int
	}
	FStackOut struct{ Locations []api.Stackframe }
	FGoIn     struct {
		Start   int
		Count   int
		Filters []api.ListGoroutinesFilter
		api.GoroutineGroupingOptions
		EvalScope *api.EvalScope
	}
	FGoOut struct {
		Goroutines    []*api.Goroutine
		Nextg         int
		Groups        []api.GoroutineGroup
		TooManyGroups bool
	}
	FVarsIn struct {
		Scope api.EvalScope
		Cfg   api.LoadConfig
	}
	FLocalsOut struct{ Variables []api.Variable }
	FArgsOut   struct{ Args []api.Variable }
	FEvalIn    struct {
		Scope api.EvalScope
		Expr  string
		Cfg   *api.LoadConfig
	}
	FEvalOut struct{ Variable *api.Variable }
)

// fakeDlvServer is a scripted RPCServer. Its exported-by-net/rpc methods are
// the Delve calls forge makes; every request is recorded.
type fakeDlvServer struct {
	mu sync.Mutex

	state      api.DebuggerState // what State answers
	stateErr   string            // State fails with this
	afterCmd   api.DebuggerState // what continue / next / step / stepout answer
	cmdErr     string            // those commands fail with this
	pid        int
	bps        []*api.Breakpoint
	failFiles  map[string]bool // CreateBreakpoint on these File values fails
	createErr  string          // CreateBreakpoint always fails with this
	listErr    string
	clearErr   string
	stack      []api.Stackframe
	goroutines []*api.Goroutine
	locals     []api.Variable
	args       []api.Variable
	eval       *api.Variable
	evalErr    string

	commands []string
	created  []FCreateIn
	cleared  []int
	detaches []bool
	scopes   []api.EvalScope
	depths   []int
	exprs    []string

	// onDetach runs after a Detach is answered (the subprocess fake exits).
	onDetach func(kill bool)
	// hang unblocks blocking State calls at teardown.
	hang chan struct{}
}

func (f *fakeDlvServer) SetApiVersion(_ api.SetAPIVersionIn, _ *api.SetAPIVersionOut) error { //nolint:revive // delve's method name
	return nil
}

func (f *fakeDlvServer) State(in FStateIn, out *FStateOut) error {
	f.mu.Lock()
	st, msg := f.state, f.stateErr
	f.mu.Unlock()
	if msg != "" {
		return fmt.Errorf("%s", msg)
	}
	// A blocking State on a running target returns only at its next stop.
	if !in.NonBlocking && st.Running {
		<-f.hang
	}
	out.State = &st
	return nil
}

func (f *fakeDlvServer) Command(cmd api.DebuggerCommand, out *FCmdOut) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, cmd.Name)
	if cmd.Name == api.Halt {
		f.state.Running = false
		out.State = f.state
		return nil
	}
	if f.cmdErr != "" {
		return fmt.Errorf("%s", f.cmdErr)
	}
	out.State = f.afterCmd
	return nil
}

func (f *fakeDlvServer) ProcessPid(_ FEmpty, out *FPidOut) error {
	out.Pid = f.pid
	return nil
}

func (f *fakeDlvServer) CreateBreakpoint(in FCreateIn, out *FCreateOut) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, in)
	if f.createErr != "" {
		return fmt.Errorf("%s", f.createErr)
	}
	bp := in.Breakpoint
	if f.failFiles[bp.File] {
		return fmt.Errorf("could not find file %s", bp.File)
	}
	if in.LocExpr != "" {
		file, line, _ := strings.Cut(in.LocExpr, ":")
		bp.File = file
		_, _ = fmt.Sscanf(line, "%d", &bp.Line)
	}
	bp.ID = len(f.bps) + 1
	f.bps = append(f.bps, &bp)
	out.Breakpoint = bp
	return nil
}

func (f *fakeDlvServer) ClearBreakpoint(in FClearIn, out *FClearOut) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.clearErr != "" {
		return fmt.Errorf("%s", f.clearErr)
	}
	f.cleared = append(f.cleared, in.Id)
	f.bps = slices.DeleteFunc(f.bps, func(b *api.Breakpoint) bool { return b.ID == in.Id })
	out.Breakpoint = &api.Breakpoint{ID: in.Id}
	return nil
}

func (f *fakeDlvServer) ListBreakpoints(_ FListBPIn, out *FListBPOut) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != "" {
		return fmt.Errorf("%s", f.listErr)
	}
	out.Breakpoints = f.bps
	return nil
}

func (f *fakeDlvServer) Stacktrace(in FStackIn, out *FStackOut) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.depths = append(f.depths, in.Depth)
	out.Locations = f.stack
	return nil
}

func (f *fakeDlvServer) ListGoroutines(_ FGoIn, out *FGoOut) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	out.Goroutines = f.goroutines
	return nil
}

func (f *fakeDlvServer) ListLocalVars(in FVarsIn, out *FLocalsOut) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scopes = append(f.scopes, in.Scope)
	out.Variables = f.locals
	return nil
}

func (f *fakeDlvServer) ListFunctionArgs(in FVarsIn, out *FArgsOut) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scopes = append(f.scopes, in.Scope)
	out.Args = f.args
	return nil
}

func (f *fakeDlvServer) Eval(in FEvalIn, out *FEvalOut) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exprs = append(f.exprs, in.Expr)
	if f.evalErr != "" {
		return fmt.Errorf("%s", f.evalErr)
	}
	out.Variable = f.eval
	return nil
}

func (f *fakeDlvServer) Detach(in FDetachIn, _ *FEmpty) error {
	f.mu.Lock()
	f.detaches = append(f.detaches, in.Kill)
	cb := f.onDetach
	f.mu.Unlock()
	if cb != nil {
		go cb(in.Kill)
	}
	return nil
}

// snapshot returns copies of what the server recorded.
func (f *fakeDlvServer) snapshot() (commands []string, created []FCreateIn, cleared []int, detaches []bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.commands), slices.Clone(f.created), slices.Clone(f.cleared), slices.Clone(f.detaches)
}

func (f *fakeDlvServer) update(fn func(f *fakeDlvServer)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// serveOn serves f on l until it is closed.
func (f *fakeDlvServer) serveOn(l net.Listener) {
	srv := rpc.NewServer()
	if err := srv.RegisterName("RPCServer", f); err != nil {
		panic(err)
	}
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go srv.ServeCodec(jsonrpc.NewServerCodec(conn))
	}
}

// startFakeDlv serves f on a loopback port for the life of the test and
// returns its address.
func startFakeDlv(t *testing.T, f *fakeDlvServer) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	f.hang = make(chan struct{})
	go f.serveOn(l)
	t.Cleanup(func() {
		close(f.hang)
		_ = l.Close()
	})
	return l.Addr().String()
}

// closedAddr is a loopback address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// ---- states and variables --------------------------------------------------

func stoppedAt(file string, line int, fn string, bp *api.Breakpoint) api.DebuggerState {
	th := &api.Thread{ID: 1, File: file, Line: line, Function: &api.Function{Name_: fn}, Breakpoint: bp}
	return api.DebuggerState{
		CurrentThread:     th,
		SelectedGoroutine: &api.Goroutine{ID: 7},
		Threads:           []*api.Thread{th},
	}
}

func atBreakpoint(file string, line int, fn string) api.DebuggerState {
	return stoppedAt(file, line, fn, &api.Breakpoint{ID: 1, FunctionName: fn})
}

func runningState() api.DebuggerState { return api.DebuggerState{Running: true} }

func exitedState(code int) api.DebuggerState {
	return api.DebuggerState{Exited: true, ExitStatus: code}
}

func intVar(name, value string) api.Variable {
	return api.Variable{Name: name, Type: "int", Value: value}
}

// ---- sessions ---------------------------------------------------------------

// inTempProject changes into a fresh directory for the test (t.Chdir: no
// parallel tests) and returns it.
func inTempProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

// withSession serves f, saves a delve session for it in a fresh project
// directory, and returns the saved session.
func withSession(t *testing.T, f *fakeDlvServer, mutate ...func(*dbgsvc.SessionInfo)) *dbgsvc.SessionInfo {
	t.Helper()
	inTempProject(t)
	s := &dbgsvc.SessionInfo{Type: "delve", Addr: startFakeDlv(t, f), PID: 4242, Owned: true, Started: time.Now()}
	for _, m := range mutate {
		m(s)
	}
	saveTestSession(t, s)
	return s
}

func saveTestSession(t *testing.T, s *dbgsvc.SessionInfo) {
	t.Helper()
	if err := debugSvc().SaveSession(".", s); err != nil {
		t.Fatal(err)
	}
}

func loadTestSession(t *testing.T) *dbgsvc.SessionInfo {
	t.Helper()
	s, err := debugSvc().LoadSession(".")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// ---- output capture ---------------------------------------------------------

// captureOutput runs fn with os.Stdout and os.Stderr redirected, as the
// commands print with fmt directly.
func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	or, ow, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	er, ew, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var outB, errB []byte
	wg.Add(2)
	go func() { defer wg.Done(); outB, _ = io.ReadAll(or) }()
	go func() { defer wg.Done(); errB, _ = io.ReadAll(er) }()
	os.Stdout, os.Stderr = ow, ew
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
	fn()
	os.Stdout, os.Stderr = oldOut, oldErr
	_ = ow.Close()
	_ = ew.Close()
	wg.Wait()
	return string(outB), string(errB)
}

// ---- stand-ins for external programs ---------------------------------------

// installTools puts shell shims for dlv, go and docker first on PATH. They
// record their argv in files named by FORGE_FAKE_*_LOG env vars (set here) so
// tests can assert what forge asked for.
func installTools(t *testing.T) (logs toolLogs) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the tool shims are shell scripts")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	logDir := t.TempDir()
	logs = toolLogs{
		dlv:    filepath.Join(logDir, "dlv.args"),
		goTool: filepath.Join(logDir, "go.args"),
		docker: filepath.Join(logDir, "docker.args"),
	}
	scripts := map[string]string{
		// dlv re-execs this test binary into TestHelper_dlv.
		"dlv": "#!/bin/sh\nFORGE_FAKE_DLV=1 exec '" + exe + "' -test.run='^TestHelper_dlv$' -- \"$@\"\n",
		// go build -o <out> ...: writes an executable file at <out>.
		"go": `#!/bin/sh
echo "$@" >> "$FORGE_FAKE_GO_LOG"
if [ -n "$FORGE_FAKE_GO_FAIL" ]; then echo "$FORGE_FAKE_GO_FAIL" >&2; exit 2; fi
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then out="$2"; fi
  shift
done
printf '#!/bin/sh\n' > "$out"
chmod +x "$out"
`,
		// docker compose ...: logs argv; "port" answers the published address.
		"docker": `#!/bin/sh
echo "$@" >> "$FORGE_FAKE_DOCKER_LOG"
case "$*" in
  *" port "*) [ -n "$FORGE_FAKE_DOCKER_PORT" ] && echo "0.0.0.0:$FORGE_FAKE_DOCKER_PORT"; exit 0 ;;
  *"up -d"*) [ -n "$FORGE_FAKE_DOCKER_FAIL" ] && exit 1 ;;
esac
exit 0
`,
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil { // #nosec G306 -- an executable test fixture
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FORGE_FAKE_DLV_LOG", logs.dlv)
	t.Setenv("FORGE_FAKE_GO_LOG", logs.goTool)
	t.Setenv("FORGE_FAKE_DOCKER_LOG", logs.docker)
	return logs
}

type toolLogs struct{ dlv, goTool, docker string }

// read returns the recorded invocations of a tool, one per line.
func (toolLogs) read(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// TestHelper_dlv is not a test: it is the `dlv` program of installTools. It
// serves a fakeDlvServer on the --listen address of the dlv argv after "--",
// and records that argv. FORGE_FAKE_DLV_MODE scripts it:
//
//	halted (default)  a target halted at its entry point
//	running           a running target
//	exited            a target whose process has exited, with output on stderr
//	refuse            dlv exits 1 at once with an error (an attach the kernel refuses)
//	ignore-term       as halted, but SIGTERM does nothing (only SIGKILL stops it)
func TestHelper_dlv(t *testing.T) {
	if os.Getenv("FORGE_FAKE_DLV") != "1" {
		return
	}
	var argv []string
	for i, a := range os.Args {
		if a == "--" {
			argv = os.Args[i+1:]
			break
		}
	}
	if log := os.Getenv("FORGE_FAKE_DLV_LOG"); log != "" {
		f, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			_, _ = fmt.Fprintln(f, strings.Join(argv, " "))
			_ = f.Close()
		}
	}
	var listen string
	for _, a := range argv {
		if v, ok := strings.CutPrefix(a, "--listen="); ok {
			listen = v
		}
	}
	mode := os.Getenv("FORGE_FAKE_DLV_MODE")
	if mode == "refuse" {
		fmt.Fprintln(os.Stderr, "could not attach to pid 4242: operation not permitted")
		os.Exit(1)
	}
	f := &fakeDlvServer{pid: 4194999, hang: make(chan struct{})}
	switch mode {
	case "running":
		f.state = runningState()
	case "exited":
		f.stateErr = "Process 4194999 has exited with status 3"
		fmt.Fprintln(os.Stderr, "panic: config: DATABASE_URL is required")
	default:
		f.state = stoppedAt("/app/main.go", 10, "main.main", nil)
	}
	if mode == "ignore-term" {
		signal.Ignore(syscall.SIGTERM)
	}
	if mode != "ignore-term" { // that dlv has to be killed: it never exits by itself
		f.onDetach = func(bool) {
			time.Sleep(50 * time.Millisecond)
			os.Exit(0)
		}
	}
	l, err := net.Listen("tcp", listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Exit(1)
	}
	f.serveOn(l)
	os.Exit(0)
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// jsonInto decodes the JSON the command printed.
func jsonInto(t *testing.T, out string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("output is not the expected JSON: %v\n%s", err, out)
	}
}

// startSleeper starts a long sleep child and returns it, killed at cleanup,
// as a stand-in for a debugged process forge may or may not kill.
func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})
	sleeperDone[cmd] = done
	return cmd
}

var sleeperDone = map[*exec.Cmd]chan struct{}{}

// reaped reports whether the sleeper has exited.
func reaped(cmd *exec.Cmd) bool {
	select {
	case <-sleeperDone[cmd]:
		return true
	default:
		return false
	}
}

// sleeperAlive reports whether the sleeper is still running. Where signal 0
// probes a process (everywhere but Windows, whose os.Process.Signal supports
// only Kill) it must succeed as well as the process being unreaped.
func sleeperAlive(cmd *exec.Cmd) bool {
	if reaped(cmd) {
		return false
	}
	return runtime.GOOS == "windows" || cmd.Process.Signal(syscall.Signal(0)) == nil
}
