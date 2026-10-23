package debug

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/go-delve/delve/service/api"
)

var defaultLoadConfig = api.LoadConfig{
	FollowPointers:     true,
	MaxVariableRecurse: 1,
	MaxStringLen:       256,
	MaxArrayValues:     64,
	MaxStructFields:    -1,
}

// DelveDebugger drives a running Delve server via its rpc2 client. It is the
// package's concrete debugger control surface (breakpoints, execution control,
// inspection, lifecycle); one implementation today, so callers hold the
// concrete *DelveDebugger rather than a single-impl interface.
type DelveDebugger struct {
	client *rpcClient
	addr   string
	cmd    *exec.Cmd // the dlv process we started (nil if we just connected)
	pid    int       // the debugged TARGET process PID
	dlvPID int       // the dlv server process PID forge spawned (0 if we just connected)
}

// NewDelveDebugger returns a new, unconnected DelveDebugger.
func NewDelveDebugger() *DelveDebugger {
	return &DelveDebugger{}
}

// Start launches dlv exec in headless mode for the given binary and connects.
// If listenPort > 0, it is used as the debugger listen port; otherwise a free port is chosen.
func (d *DelveDebugger) Start(ctx context.Context, binary string, args []string, listenPort int) error {
	return d.StartWithEnv(ctx, binary, args, nil, listenPort)
}

// StartWithEnv is Start with extra environment variables layered onto the
// debugged process's environment (os.Environ() + extraEnv, extraEnv wins).
// Used to inject the OTEL_SERVICE_NAME / PORT a forge service binary needs to
// actually serve.
func (d *DelveDebugger) StartWithEnv(ctx context.Context, binary string, args []string, extraEnv []string, listenPort int) error {
	port := listenPort
	if port <= 0 {
		var err error
		port, err = freePort(ctx)
		if err != nil {
			return fmt.Errorf("finding free port: %w", err)
		}
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	dlvArgs := []string{
		"exec", "--headless",
		"--listen=" + addr,
		"--api-version=2",
		"--accept-multiclient",
		binary,
	}
	if len(args) > 0 {
		dlvArgs = append(dlvArgs, "--")
		dlvArgs = append(dlvArgs, args...)
	}

	d.cmd = exec.CommandContext(ctx, "dlv", dlvArgs...)
	// dlv exec inherits its environment to the debuggee. Layer extraEnv
	// (OTEL_SERVICE_NAME / PORT / ...) on top of the current env, last-wins.
	if len(extraEnv) > 0 {
		d.cmd.Env = append(os.Environ(), extraEnv...)
	}
	// Start dlv in its own process group and detach IO so it survives
	// after the parent (forge) exits.
	detachProcess(d.cmd)
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("opening %s: %w", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()
	d.cmd.Stdin = devNull
	d.cmd.Stdout = devNull
	d.cmd.Stderr = devNull
	if err := d.cmd.Start(); err != nil {
		return fmt.Errorf("starting dlv: %w", err)
	}

	if err := waitForServer(ctx, addr, 5*time.Second); err != nil {
		_ = d.cmd.Process.Kill()
		return fmt.Errorf("waiting for dlv: %w", err)
	}

	d.addr = addr
	d.dlvPID = d.cmd.Process.Pid
	// The target is the dlv-launched debuggee; query its PID so callers can
	// kill the right process on stop (the dlv server PID is tracked
	// separately in d.dlvPID for reaping).
	if c, err := newRPCClient(addr); err == nil {
		d.pid = c.ProcessPid()
		_ = c.Disconnect(false)
	}
	// Release the process handle so the Go runtime does not kill dlv
	// when the parent (forge) process exits.
	_ = d.cmd.Process.Release()
	d.cmd = nil
	return nil
}

// StartAttach launches dlv attach in headless mode for the given PID.
func (d *DelveDebugger) StartAttach(ctx context.Context, pid int) error {
	port, err := freePort(ctx)
	if err != nil {
		return fmt.Errorf("finding free port: %w", err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	dlvArgs := []string{
		"attach", "--headless",
		"--listen=" + addr,
		"--api-version=2",
		"--accept-multiclient",
		strconv.Itoa(pid),
	}

	d.cmd = exec.CommandContext(ctx, "dlv", dlvArgs...)
	// Detach IO and run dlv in its own process group so it survives after
	// forge exits — the session is reconnected by later `forge debug`
	// invocations, exactly like Start.
	detachProcess(d.cmd)
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("opening %s: %w", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()
	d.cmd.Stdin = devNull
	d.cmd.Stdout = devNull
	d.cmd.Stderr = devNull
	if err := d.cmd.Start(); err != nil {
		return fmt.Errorf("starting dlv attach: %w", err)
	}

	if err := waitForServer(ctx, addr, 5*time.Second); err != nil {
		_ = d.cmd.Process.Kill()
		return fmt.Errorf("waiting for dlv: %w", err)
	}

	d.addr = addr
	d.pid = pid
	d.dlvPID = d.cmd.Process.Pid
	// Release the dlv handle so the Go runtime does not reap dlv when forge
	// exits. dlv was launched with `attach` (no --continue), so the target
	// is suspended; the dlv server PID is tracked for reaping on stop.
	_ = d.cmd.Process.Release()
	d.cmd = nil
	return nil
}

// Connect connects to an already-running Delve instance at addr.
// It applies a timeout so the caller isn't blocked indefinitely when
// Delve's RPC handler is stuck (e.g. due to stale CLOSE_WAIT connections).
func (d *DelveDebugger) Connect(addr string) error {
	type result struct {
		client *rpcClient
		pid    int
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := newRPCClient(addr)
		if err != nil {
			ch <- result{err: err}
			return
		}
		ch <- result{client: c, pid: c.ProcessPid()}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		d.client = r.client
		d.addr = addr
		d.pid = r.pid
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("timeout connecting to debugger at %s (server may be busy)", addr)
	}
}

// Addr returns the listen address of the Delve server.
func (d *DelveDebugger) Addr() string { return d.addr }

// PID returns the PID of the debugged (target) process.
func (d *DelveDebugger) PID() int { return d.pid }

// DlvPID returns the PID of the dlv server process forge spawned, or 0 when
// this debugger merely connected to an existing dlv. Tracked so `stop` can
// reap the dlv server even after the session has been reconnected from disk.
func (d *DelveDebugger) DlvPID() int { return d.dlvPID }

// ---------------------------------------------------------------------------
// Breakpoints
// ---------------------------------------------------------------------------

// SetBreakpoint sets a source-line breakpoint with an optional condition.
func (d *DelveDebugger) SetBreakpoint(file string, line int, condition string) (*BreakpointInfo, error) {
	bp, err := d.client.CreateBreakpoint(&api.Breakpoint{
		File: file,
		Line: line,
		Cond: condition,
	})
	if err != nil {
		return nil, err
	}
	return apiBreakpointToInfo(bp), nil
}

// SetFunctionBreakpoint sets a breakpoint on a function entry by name.
func (d *DelveDebugger) SetFunctionBreakpoint(funcName string, condition string) (*BreakpointInfo, error) {
	bp, err := d.client.CreateBreakpoint(&api.Breakpoint{
		FunctionName: funcName,
		Cond:         condition,
	})
	if err != nil {
		return nil, err
	}
	return apiBreakpointToInfo(bp), nil
}

// ListBreakpoints returns every breakpoint currently set in the debugger.
func (d *DelveDebugger) ListBreakpoints() ([]BreakpointInfo, error) {
	bps, err := d.client.ListBreakpoints(false)
	if err != nil {
		return nil, err
	}
	out := make([]BreakpointInfo, len(bps))
	for i, bp := range bps {
		out[i] = *apiBreakpointToInfo(bp)
	}
	return out, nil
}

// ClearBreakpoint removes the breakpoint with the given ID.
func (d *DelveDebugger) ClearBreakpoint(id int) error {
	_, err := d.client.ClearBreakpoint(id)
	return err
}

// ---------------------------------------------------------------------------
// Execution control
// ---------------------------------------------------------------------------

// Continue resumes execution until the next breakpoint or program exit.
func (d *DelveDebugger) Continue() (*StopState, error) {
	ch := d.client.Continue()
	for state := range ch {
		if state == nil {
			continue
		}
		if state.Err != nil {
			return nil, state.Err
		}
		return stateToStopState(state), nil
	}
	return nil, fmt.Errorf("continue: channel closed without state")
}

// StepOver advances to the next source line in the current function.
func (d *DelveDebugger) StepOver() (*StopState, error) {
	if err := d.ensureHalted(); err != nil {
		return nil, err
	}
	state, err := d.client.Next()
	if err != nil {
		return nil, err
	}
	return stateToStopState(state), nil
}

// StepInto steps into the call on the current line.
func (d *DelveDebugger) StepInto() (*StopState, error) {
	if err := d.ensureHalted(); err != nil {
		return nil, err
	}
	state, err := d.client.Step()
	if err != nil {
		return nil, err
	}
	return stateToStopState(state), nil
}

// StepOut runs until the current function returns.
func (d *DelveDebugger) StepOut() (*StopState, error) {
	if err := d.ensureHalted(); err != nil {
		return nil, err
	}
	state, err := d.client.StepOut()
	if err != nil {
		return nil, err
	}
	return stateToStopState(state), nil
}

// ---------------------------------------------------------------------------
// Inspection
// ---------------------------------------------------------------------------

// Eval evaluates an expression in the current goroutine's scope.
func (d *DelveDebugger) Eval(expr string) (*Variable, error) {
	scope := api.EvalScope{GoroutineID: -1}
	v, err := d.client.EvalVariable(scope, expr, defaultLoadConfig)
	if err != nil {
		return nil, err
	}
	out := apiVarToVariable(v)
	return &out, nil
}

// Locals returns the local variables of the current stack frame.
func (d *DelveDebugger) Locals() ([]Variable, error) {
	scope, err := d.currentScope()
	if err != nil {
		return nil, fmt.Errorf("getting current scope: %w", err)
	}
	vars, err := d.client.ListLocalVariables(scope, defaultLoadConfig)
	if err != nil {
		return nil, err
	}
	return apiVarsToVariables(vars), nil
}

// Args returns the function arguments of the current stack frame.
func (d *DelveDebugger) Args() ([]Variable, error) {
	scope, err := d.currentScope()
	if err != nil {
		return nil, fmt.Errorf("getting current scope: %w", err)
	}
	vars, err := d.client.ListFunctionArgs(scope, defaultLoadConfig)
	if err != nil {
		return nil, err
	}
	return apiVarsToVariables(vars), nil
}

// Stacktrace returns up to depth stack frames for the current goroutine.
func (d *DelveDebugger) Stacktrace(depth int) ([]StackFrame, error) {
	if err := d.ensureHalted(); err != nil {
		return nil, err
	}
	frames, err := d.client.Stacktrace(-1, depth, 0, api.StacktraceOptions(0), &defaultLoadConfig)
	if err != nil {
		return nil, err
	}
	out := make([]StackFrame, len(frames))
	for i, f := range frames {
		out[i] = StackFrame{
			Function: funcName(f.Function),
			File:     f.File,
			Line:     f.Line,
			Args:     apiVarsToVariables(f.Arguments),
			Locals:   apiVarsToVariables(f.Locals),
		}
	}
	return out, nil
}

// Goroutines lists every goroutine known to the debugger.
func (d *DelveDebugger) Goroutines() ([]GoroutineInfo, error) {
	// ListGoroutines walks runtime.allgs in target memory, which is only
	// readable when the target is HALTED. Against a running target (the normal
	// state right after `forge debug start --attach`, before any breakpoint is
	// hit) the call either blocks forever or fails with Delve's
	// "could not find goroutine array" — the goroutine cache hasn't been
	// primed because the process never stopped. Halt first, exactly as dlv's
	// own terminal does, so the listing is always against a stopped target.
	if err := d.ensureHalted(); err != nil {
		return nil, err
	}
	goroutines, _, err := d.client.ListGoroutines(0, 0)
	if err != nil {
		return nil, err
	}
	out := make([]GoroutineInfo, len(goroutines))
	for i, g := range goroutines {
		loc := g.UserCurrentLoc
		if loc.File == "" {
			loc = g.CurrentLoc
		}
		out[i] = GoroutineInfo{
			ID:          g.ID,
			Status:      goroutineStatus(g.Status),
			Function:    funcName(loc.Function),
			CurrentFile: loc.File,
			CurrentLine: loc.Line,
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Stop detaches and kills the debugger process.
func (d *DelveDebugger) Stop() error {
	if d.client != nil {
		_ = d.client.Detach(true)
		d.client = nil
	}
	if d.cmd != nil && d.cmd.Process != nil {
		_ = d.cmd.Process.Kill()
		_ = d.cmd.Wait()
		d.cmd = nil
	}
	return nil
}

// Disconnect closes the RPC connection without killing the debugee.
func (d *DelveDebugger) Disconnect() {
	if d.client != nil {
		_ = d.client.Disconnect(false) // false = don't kill the debugged process
		d.client = nil
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ensureHalted guarantees the target is stopped before an operation that
// reads target memory or steps execution. Inspection (goroutines, locals,
// stacktrace) and stepping all require a HALTED target; on a running target
// the underlying Delve RPCs either block on the next (never-arriving) stop or
// fail because the goroutine cache was never primed. We probe state without
// blocking, and Halt() only when actually running so an already-stopped
// target is untouched. Halting is idempotent and safe — it leaves an attached
// target suspended exactly as a breakpoint hit would, and `stop` still
// detaches without killing.
func (d *DelveDebugger) ensureHalted() error {
	state, err := d.client.GetStateNonBlocking()
	if err != nil {
		return err
	}
	if state == nil || !state.Running {
		return nil
	}
	if _, err := d.client.Halt(); err != nil {
		return fmt.Errorf("halting target: %w", err)
	}
	return nil
}

// currentScope returns an EvalScope for the currently selected goroutine.
func (d *DelveDebugger) currentScope() (api.EvalScope, error) {
	if err := d.ensureHalted(); err != nil {
		return api.EvalScope{}, err
	}
	state, err := d.client.GetState()
	if err != nil {
		return api.EvalScope{}, err
	}
	goroutineID := int64(-1)
	if state.SelectedGoroutine != nil {
		goroutineID = state.SelectedGoroutine.ID
	}
	return api.EvalScope{GoroutineID: goroutineID}, nil
}

func stateToStopState(state *api.DebuggerState) *StopState {
	ss := &StopState{}

	if state.CurrentThread != nil {
		ss.File = state.CurrentThread.File
		ss.Line = state.CurrentThread.Line
		if state.CurrentThread.Function != nil {
			ss.Function = state.CurrentThread.Function.Name()
		}
		if state.CurrentThread.Breakpoint != nil {
			ss.Reason = "breakpoint"
		}
	}

	if state.SelectedGoroutine != nil {
		ss.GoroutineID = state.SelectedGoroutine.ID
	}

	if ss.Reason == "" {
		ss.Reason = "step"
	}

	return ss
}

func apiBreakpointToInfo(bp *api.Breakpoint) *BreakpointInfo {
	return &BreakpointInfo{
		ID:           bp.ID,
		File:         bp.File,
		Line:         bp.Line,
		FunctionName: bp.FunctionName,
		Condition:    bp.Cond,
		HitCount:     bp.TotalHitCount,
	}
}

func apiVarToVariable(v *api.Variable) Variable {
	out := Variable{
		Name:  v.Name,
		Type:  v.Type,
		Value: v.Value,
	}
	if len(v.Children) > 0 {
		out.Children = make([]Variable, len(v.Children))
		for i := range v.Children {
			out.Children[i] = apiVarToVariable(&v.Children[i])
		}
	}
	return out
}

func apiVarsToVariables(vars []api.Variable) []Variable {
	if len(vars) == 0 {
		return nil
	}
	out := make([]Variable, len(vars))
	for i := range vars {
		out[i] = apiVarToVariable(&vars[i])
	}
	return out
}

func funcName(fn *api.Function) string {
	if fn == nil {
		return ""
	}
	return fn.Name()
}

func goroutineStatus(status uint64) string {
	switch status {
	case 0:
		return "idle"
	case 1:
		return "runnable"
	case 2:
		return "running"
	case 3:
		return "syscall"
	case 4:
		return "waiting"
	case 6:
		return "dead"
	default:
		return fmt.Sprintf("unknown(%d)", status)
	}
}

func freePort(ctx context.Context) (int, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}

func waitForServer(ctx context.Context, addr string, timeout time.Duration) error {
	dialer := net.Dialer{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for dlv at %s", addr)
}
