package debug

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
	pid    int // the debugged TARGET process PID
	dlvPID int // the dlv server process PID forge spawned (0 if we just connected)
}

// NewDelveDebugger returns a new, unconnected DelveDebugger.
func NewDelveDebugger() *DelveDebugger {
	return &DelveDebugger{}
}

// ErrStillRunning reports that the target did not stop within the time a
// caller was willing to wait. It is not a failure of the target: the process
// keeps running, and the next stop is still delivered to whoever asks.
var ErrStillRunning = errors.New("target still running")

// dlvReadyTimeout bounds how long a launch waits for dlv to start listening.
// dlv loads the binary's DWARF before it binds, which takes seconds for a
// large server binary. A var so tests can shrink it.
var dlvReadyTimeout = 30 * time.Second

// rpcTimeout bounds the lifecycle RPCs (halt, detach) so a wedged dlv cannot
// hang `forge debug stop` forever; the caller falls back to signals.
var rpcTimeout = 15 * time.Second

// LaunchOptions configures Launch.
type LaunchOptions struct {
	// Binary is the debug-built executable dlv runs.
	Binary string
	// Args are passed to the target after `--`.
	Args []string
	// Env is the target's complete environment (dlv hands its own to the
	// debuggee). nil inherits forge's environment unchanged.
	Env []string
	// ListenPort is dlv's port on 127.0.0.1; 0 picks a free one. dlv never
	// listens beyond loopback: its API executes arbitrary code in the target.
	ListenPort int
	// OutputPath receives dlv's and the target's stdout/stderr, appended.
	// Empty discards them.
	OutputPath string
	// StopOnStart leaves the target halted at its entry point. The default
	// lets it run (dlv --continue), so the service serves while breakpoints
	// are set against it.
	StopOnStart bool
}

// AttachOptions configures Attach.
type AttachOptions struct {
	PID int
	// ListenPort is dlv's port on 127.0.0.1; 0 picks a free one.
	ListenPort int
	// OutputPath receives dlv's own output. Empty discards it.
	OutputPath string
	// StopOnStart leaves the attached process halted. The default resumes it
	// at once (dlv --continue): a live server that is attached and then
	// silently frozen stops answering everyone.
	StopOnStart bool
}

// Start launches dlv exec in headless mode for the given binary, halted at
// the entry point. If listenPort > 0, it is used as the debugger listen
// port; otherwise a free port is chosen.
func (d *DelveDebugger) Start(ctx context.Context, binary string, args []string, listenPort int) error {
	return d.StartWithEnv(ctx, binary, args, nil, listenPort)
}

// StartWithEnv is Start with extra environment variables layered onto the
// debugged process's environment (os.Environ() + extraEnv, extraEnv wins).
func (d *DelveDebugger) StartWithEnv(ctx context.Context, binary string, args []string, extraEnv []string, listenPort int) error {
	var env []string
	if len(extraEnv) > 0 {
		env = append(os.Environ(), extraEnv...)
	}
	return d.Launch(ctx, LaunchOptions{Binary: binary, Args: args, Env: env, ListenPort: listenPort, StopOnStart: true})
}

// Launch runs opts.Binary under a headless dlv exec server that outlives
// forge, and records the server's address and PIDs.
func (d *DelveDebugger) Launch(ctx context.Context, opts LaunchOptions) error {
	port, err := listenPortOrFree(ctx, opts.ListenPort)
	if err != nil {
		return err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	dlvArgs := dlvServerArgs("exec", addr, opts.Binary)
	if len(opts.Args) > 0 {
		dlvArgs = append(dlvArgs, "--")
		dlvArgs = append(dlvArgs, opts.Args...)
	}
	if err := d.spawn(ctx, dlvArgs, opts.Env, opts.OutputPath, addr); err != nil {
		return err
	}
	// The target is the dlv-launched debuggee; its PID is what stop kills
	// (the dlv server PID is tracked separately in d.dlvPID for reaping).
	return d.afterSpawn(opts.StopOnStart, 0)
}

// afterSpawn records the target PID while the target is still halted —
// Delve's ProcessPid takes the target lock, which a running target holds —
// then resumes it unless stopOnStart. Resuming here rather than with
// dlv --continue is what makes the PID read race-free.
func (d *DelveDebugger) afterSpawn(stopOnStart bool, knownPID int) error {
	c, err := newRPCClient(d.addr)
	if err != nil {
		return fmt.Errorf("connecting to dlv: %w", err)
	}
	defer func() { _ = c.Close() }()
	d.pid = knownPID
	if d.pid == 0 {
		d.pid = c.ProcessPid()
	}
	if stopOnStart {
		return nil
	}
	if err := c.resume(); err != nil {
		return fmt.Errorf("resuming target: %w", err)
	}
	return nil
}

// StartAttach attaches a headless dlv to pid, leaving it halted.
func (d *DelveDebugger) StartAttach(ctx context.Context, pid int) error {
	return d.Attach(ctx, AttachOptions{PID: pid, StopOnStart: true})
}

// Attach attaches a headless dlv server, which outlives forge, to opts.PID.
func (d *DelveDebugger) Attach(ctx context.Context, opts AttachOptions) error {
	port, err := listenPortOrFree(ctx, opts.ListenPort)
	if err != nil {
		return err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	dlvArgs := dlvServerArgs("attach", addr, strconv.Itoa(opts.PID))
	if err := d.spawn(ctx, dlvArgs, nil, opts.OutputPath, addr); err != nil {
		if hint := ptraceScopeHint(); hint != "" {
			return fmt.Errorf("%w\n%s", err, hint)
		}
		return err
	}
	return d.afterSpawn(opts.StopOnStart, opts.PID)
}

// dlvServerArgs is the argv (after `dlv`) of a headless, multi-client API v2
// server on addr. Multi-client is what lets each `forge debug` invocation
// connect, act and leave without ending the session. The target starts
// halted; afterSpawn resumes it.
func dlvServerArgs(verb, addr, target string) []string {
	return []string{verb, "--headless", "--listen=" + addr, "--api-version=2", "--accept-multiclient", target}
}

func listenPortOrFree(ctx context.Context, port int) (int, error) {
	if port > 0 {
		return port, nil
	}
	p, err := freePort(ctx)
	if err != nil {
		return 0, fmt.Errorf("finding free port: %w", err)
	}
	return p, nil
}

// spawn starts dlv detached (own session, IO not tied to forge) so it
// survives forge's exit, waits for it to listen on addr, and releases the
// handle. Later `forge debug` invocations reconnect to it by address.
func (d *DelveDebugger) spawn(ctx context.Context, dlvArgs, env []string, outputPath, addr string) error {
	// Not CommandContext: the server must outlive this command's context.
	cmd := exec.Command("dlv", dlvArgs...) // #nosec G204 -- argv is built by forge, not a shell
	cmd.Env = env
	detachProcess(cmd)
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("opening %s: %w", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()
	cmd.Stdin = devNull
	cmd.Stdout, cmd.Stderr = devNull, devNull
	if outputPath != "" {
		if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
			return fmt.Errorf("creating log dir: %w", err)
		}
		f, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("opening log %s: %w", outputPath, err)
		}
		defer func() { _ = f.Close() }()
		cmd.Stdout, cmd.Stderr = f, f
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting dlv %s: %w", dlvArgs[0], err)
	}
	// A dlv that cannot start (attach refused, binary unreadable) exits at
	// once; waiting out dlvReadyTimeout for it to listen would only hide
	// why. The waiter also reaps dlv if it exits while forge still runs;
	// forge exiting leaves dlv, in its own session, running.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	if err := waitForServer(ctx, addr, dlvReadyTimeout, exited); err != nil {
		if !errors.Is(err, errDlvExited) {
			_ = cmd.Process.Kill()
			<-exited
		}
		if outputPath != "" {
			return fmt.Errorf("waiting for dlv: %w (dlv output: %s)%s", err, outputPath, dlvOutputTail(outputPath))
		}
		return fmt.Errorf("waiting for dlv: %w", err)
	}
	d.addr = addr
	d.dlvPID = cmd.Process.Pid
	return nil
}

// errDlvExited reports that dlv exited before it listened.
var errDlvExited = errors.New("dlv exited before it listened")

// dlvOutputTailBytes bounds how much of dlv's log a launch error quotes:
// enough for dlv's own error line, not a whole DWARF-loading transcript.
const dlvOutputTailBytes = 2048

// dlvOutputTail returns the end of dlv's log as an indented block to append
// to an error, or "" when there is nothing to show.
func dlvOutputTail(path string) string {
	b, err := os.ReadFile(path) // #nosec G304 -- forge's own log path
	if err != nil {
		return ""
	}
	if len(b) > dlvOutputTailBytes {
		b = b[len(b)-dlvOutputTailBytes:]
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return ""
	}
	return ":\n  " + strings.ReplaceAll(text, "\n", "\n  ")
}

// ptraceScopePath is Linux Yama's ptrace policy. A var so tests can point it
// at a file of their own.
var ptraceScopePath = "/proc/sys/kernel/yama/ptrace_scope"

// ptraceScopeHint explains a failed attach on a host whose Yama policy lets a
// process trace only its own descendants (Ubuntu's default, 1, and GitHub's
// runners): dlv is not the target's ancestor, so the kernel refuses it. It
// returns "" where Yama is absent or allows the attach (0).
func ptraceScopeHint() string {
	b, err := os.ReadFile(ptraceScopePath)
	if err != nil {
		return ""
	}
	scope := strings.TrimSpace(string(b))
	if scope == "" || scope == "0" {
		return ""
	}
	return fmt.Sprintf("kernel.yama.ptrace_scope is %s, so dlv may trace only processes it started. "+
		"Allow the attach with `sudo sysctl -w kernel.yama.ptrace_scope=0`, or debug a build forge starts itself (`forge debug start <workload>`).", scope)
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
		// ProcessPid takes Delve's target lock, which a running target
		// holds until its next stop: ask only a halted target. Callers that
		// need the PID of a running one use ResolvePID.
		pid := 0
		if st, err := c.GetStateNonBlocking(); err == nil && st != nil && !st.Running {
			pid = c.ProcessPid()
		}
		ch <- result{client: c, pid: pid}
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

// ResolvePID returns the target's PID, halting a running target for the
// length of the query.
func (d *DelveDebugger) ResolvePID() (int, error) {
	if d.pid > 0 {
		return d.pid, nil
	}
	err := d.withHalted(func() error {
		d.pid = d.client.ProcessPid()
		return nil
	})
	return d.pid, err
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

// Every breakpoint RPC takes Delve's target lock, which a running target
// holds until it next stops — so against a running target each one would
// block until some request happened to hit a breakpoint. They run under
// withHalted instead: halt, act, resume.

// SetBreakpoint sets a source-line breakpoint with an optional condition.
func (d *DelveDebugger) SetBreakpoint(file string, line int, condition string) (*BreakpointInfo, error) {
	return d.SetLineBreakpoint([]string{file}, "", line, condition)
}

// SetLineBreakpoint sets a source-line breakpoint, trying each spelling of
// the file in files in order, then — when locExpr is set — Delve's location
// expression `locExpr:line`, which matches a path by suffix. The spellings
// exist because a binary records file names as it was built: absolute
// without -trimpath, `<module>/<rel>` with it (air and the Reliant image
// build with GOFLAGS=-trimpath). The first error is returned when all fail.
func (d *DelveDebugger) SetLineBreakpoint(files []string, locExpr string, line int, condition string) (*BreakpointInfo, error) {
	var out *BreakpointInfo
	err := d.withHalted(func() error {
		var first error
		try := func(in createBreakpointIn) bool {
			bp, err := d.client.createBreakpoint(in)
			if err == nil {
				out = apiBreakpointToInfo(bp)
				return true
			}
			if first == nil {
				first = err
			}
			return false
		}
		for _, f := range files {
			if f != "" && try(createBreakpointIn{Breakpoint: api.Breakpoint{File: f, Line: line, Cond: condition}}) {
				return nil
			}
		}
		if locExpr != "" && try(createBreakpointIn{Breakpoint: api.Breakpoint{Cond: condition}, LocExpr: fmt.Sprintf("%s:%d", locExpr, line)}) {
			return nil
		}
		return first
	})
	return out, err
}

// SetFunctionBreakpoint sets a breakpoint on a function entry by name.
func (d *DelveDebugger) SetFunctionBreakpoint(funcName string, condition string) (*BreakpointInfo, error) {
	var out *BreakpointInfo
	err := d.withHalted(func() error {
		bp, err := d.client.CreateBreakpoint(&api.Breakpoint{FunctionName: funcName, Cond: condition})
		if err != nil {
			return err
		}
		out = apiBreakpointToInfo(bp)
		return nil
	})
	return out, err
}

// ListBreakpoints returns every breakpoint currently set in the debugger.
func (d *DelveDebugger) ListBreakpoints() ([]BreakpointInfo, error) {
	var out []BreakpointInfo
	err := d.withHalted(func() error {
		bps, err := d.client.ListBreakpoints(false)
		if err != nil {
			return err
		}
		out = make([]BreakpointInfo, len(bps))
		for i, bp := range bps {
			out[i] = *apiBreakpointToInfo(bp)
		}
		return nil
	})
	return out, err
}

// ClearBreakpoint removes the breakpoint with the given ID.
func (d *DelveDebugger) ClearBreakpoint(id int) error {
	return d.withHalted(func() error {
		_, err := d.client.ClearBreakpoint(id)
		return err
	})
}

// withHalted runs fn with the target halted, resuming it afterwards if it
// was running. A halted target is left halted.
func (d *DelveDebugger) withHalted(fn func() error) error {
	st, err := d.client.GetStateNonBlocking()
	if err != nil {
		return err
	}
	running := st != nil && st.Running
	if running {
		if _, err := d.client.haltTimeout(rpcTimeout); err != nil {
			return fmt.Errorf("halting target: %w", err)
		}
	}
	ferr := fn()
	if running {
		if err := d.client.resume(); err != nil && ferr == nil {
			ferr = fmt.Errorf("resuming target: %w", err)
		}
	}
	return ferr
}

// ---------------------------------------------------------------------------
// Execution control
// ---------------------------------------------------------------------------

// Continue resumes execution until the next breakpoint or program exit,
// waiting as long as that takes.
func (d *DelveDebugger) Continue() (*StopState, error) { return d.ContinueFor(0) }

// stopSettle is how long ContinueFor waits after a stop with no breakpoint
// before deciding the target really stopped: another client's halt-act-resume
// (a `forge debug break` against a running target) halts it only for the
// length of one RPC. A var so tests can shrink it.
var stopSettle = 300 * time.Millisecond

// ContinueFor waits up to timeout (0 = forever) for the target's next stop.
// A halted target is resumed first; a running one — resumed by an earlier
// command, or by dlv --continue at launch — is only waited on, never
// resumed again, so a stop already on its way is not skipped. When the
// timeout elapses it returns ErrStillRunning and the target keeps running.
func (d *DelveDebugger) ContinueFor(timeout time.Duration) (*StopState, error) {
	return d.waitForStop(timeout, false)
}

// WaitForStop is ContinueFor for a target a caller resumed earlier without
// observing its next stop: it never resumes the target, so a halted target's
// current stop IS the stop it reports. ContinueFor decides from a state read
// whether to resume, and a stop landing just before that read would be
// resumed past.
func (d *DelveDebugger) WaitForStop(timeout time.Duration) (*StopState, error) {
	return d.waitForStop(timeout, true)
}

func (d *DelveDebugger) waitForStop(timeout time.Duration, resumed bool) (*StopState, error) {
	var deadline <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		deadline = t.C
	}
	// One client for the whole wait: Stop/Detach on another goroutine nil
	// d.client, and a closed client fails its calls rather than panicking.
	c := d.client
	// A blocking State returns at once for a halted target, so a resumed
	// session's wait reads its current stop the same way it waits for one.
	running := resumed
	if !running {
		st, err := c.GetStateNonBlocking()
		if err != nil {
			return nil, err
		}
		running = st != nil && st.Running
	}
	for {
		var (
			state *api.DebuggerState
			err   error
		)
		if running {
			state, err = c.waitState(deadline)
		} else {
			state, err = c.continueCmd(deadline)
		}
		if errors.Is(err, ErrStillRunning) {
			return nil, err
		}
		if err != nil {
			return nil, err
		}
		if state.Exited {
			return nil, fmt.Errorf("process %d has exited with status %d", d.pid, state.ExitStatus)
		}
		bp, tracepointOnly := stopBreakpoints(state)
		switch {
		case bp && tracepointOnly:
			// Tracepoints log and continue, as delve's own client does.
			running = false
			continue
		case bp:
			return stateToStopState(state), nil
		}
		// No breakpoint: a manual halt. If it was someone's halt-act-resume,
		// the target is running again a moment later — keep waiting.
		time.Sleep(stopSettle)
		if ns, err := c.GetStateNonBlocking(); err == nil && ns != nil && ns.Running {
			running = true
			continue
		}
		ss := stateToStopState(state)
		ss.Reason = "halted"
		return ss, nil
	}
}

// Resume lets a halted target run on without waiting for its next stop.
// A running target is left alone.
func (d *DelveDebugger) Resume() error {
	st, err := d.client.GetStateNonBlocking()
	if err != nil {
		return err
	}
	if st != nil && st.Running {
		return nil
	}
	return d.client.resume()
}

// TargetState is the debugged process's execution state.
type TargetState int

const (
	// TargetHalted is a stopped target (entry point, breakpoint, or a halt).
	TargetHalted TargetState = iota
	// TargetRunning is an executing target.
	TargetRunning
	// TargetExited is a target whose process is gone; dlv still serves it.
	TargetExited
)

// State reports the target's execution state without blocking. For an
// exited target the error carries Delve's exit message.
func (d *DelveDebugger) State() (TargetState, error) {
	st, err := d.client.GetStateNonBlocking()
	if err != nil {
		if strings.Contains(err.Error(), "has exited") {
			return TargetExited, err
		}
		return TargetHalted, err
	}
	if st != nil && st.Exited {
		return TargetExited, fmt.Errorf("process %d has exited with status %d", d.pid, st.ExitStatus)
	}
	if st != nil && st.Running {
		return TargetRunning, nil
	}
	return TargetHalted, nil
}

// CurrentStop describes where a halted target is stopped.
func (d *DelveDebugger) CurrentStop() (*StopState, error) {
	st, err := d.client.GetState()
	if err != nil {
		return nil, err
	}
	return stateToStopState(st), nil
}

// stopBreakpoints reports whether any thread stopped on a breakpoint, and
// whether every such breakpoint is a tracepoint.
func stopBreakpoints(state *api.DebuggerState) (bp, tracepointOnly bool) {
	tracepointOnly = true
	for i := range state.Threads {
		if b := state.Threads[i].Breakpoint; b != nil {
			bp = true
			tracepointOnly = tracepointOnly && (b.Tracepoint || b.TraceReturn)
		}
	}
	if !bp && state.CurrentThread != nil && state.CurrentThread.Breakpoint != nil {
		b := state.CurrentThread.Breakpoint
		return true, b.Tracepoint || b.TraceReturn
	}
	return bp, bp && tracepointOnly
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

// Stop ends a session forge launched: it kills the debuggee and the dlv
// server exits with it. Detach takes Delve's target lock, so a running
// target is halted first — otherwise the detach would wait for a breakpoint.
func (d *DelveDebugger) Stop() error {
	if d.client == nil {
		return nil
	}
	defer func() { d.client = nil }()
	if err := d.haltIfRunning(); err != nil {
		_ = d.client.Close()
		return err
	}
	return d.client.detachTimeout(true, rpcTimeout)
}

// Detach ends an ATTACH session and leaves the target running: Delve
// removes every breakpoint it inserted, releases the process, and the dlv
// server exits. Only closing the connection, or killing dlv, would leave
// int3 instructions in the live process, which then dies of SIGTRAP on the
// next hit.
func (d *DelveDebugger) Detach() error {
	if d.client == nil {
		return errors.New("not connected")
	}
	defer func() { d.client = nil }()
	if err := d.haltIfRunning(); err != nil {
		_ = d.client.Close()
		return err
	}
	return d.client.detachTimeout(false, rpcTimeout)
}

// Release hands a dlv server forge did not start back as it found it: the
// breakpoints in ids (the ones this session created) cleared, the target
// running, the server still up. Every other breakpoint — set before forge
// connected, or by another client of the multi-client server — is left.
func (d *DelveDebugger) Release(ids []int) error {
	if d.client == nil {
		return errors.New("not connected")
	}
	defer d.Disconnect()
	if err := d.haltIfRunning(); err != nil {
		return err
	}
	// Every cleanup RPC is bounded like the state read and the halt: a server
	// that answers State and then stalls must not hang stop. On a timeout the
	// deferred Disconnect closes the connection and the error goes back to
	// stop, which keeps the session so the release can be retried.
	bps, err := d.client.listBreakpointsTimeout(rpcTimeout)
	if err != nil {
		return fmt.Errorf("listing breakpoints: %w", err)
	}
	for _, bp := range bps {
		// Negative IDs are Delve's own (unrecovered panic, fatal throw). An
		// id the user already cleared is simply not listed.
		if bp.ID > 0 && slices.Contains(ids, bp.ID) {
			if err := d.client.clearBreakpointTimeout(bp.ID, rpcTimeout); err != nil {
				return fmt.Errorf("clearing breakpoint %d: %w", bp.ID, err)
			}
		}
	}
	return d.client.resume()
}

// Disconnect closes the RPC connection without killing the debugee.
func (d *DelveDebugger) Disconnect() {
	if d.client != nil {
		_ = d.client.Disconnect(false) // false = don't kill the debugged process
		d.client = nil
	}
}

// haltIfRunning bounds every RPC it makes, the state read included: a dlv
// that accepts connections but never answers must not hang `forge debug
// stop` short of its signal fallback.
func (d *DelveDebugger) haltIfRunning() error {
	st, err := d.client.stateNonBlockingTimeout(rpcTimeout)
	if err != nil {
		if strings.Contains(err.Error(), "has exited") {
			return nil
		}
		return err
	}
	if st != nil && st.Running {
		if _, err := d.client.haltTimeout(rpcTimeout); err != nil {
			return fmt.Errorf("halting target: %w", err)
		}
	}
	return nil
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
	if _, err := d.client.haltTimeout(rpcTimeout); err != nil {
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

// waitForServer polls addr until it accepts a connection, the timeout
// passes, or exited delivers (dlv is gone, so nothing will ever listen).
func waitForServer(ctx context.Context, addr string, timeout time.Duration, exited <-chan error) error {
	dialer := net.Dialer{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case werr := <-exited:
			if werr != nil {
				return fmt.Errorf("%w (%v)", errDlvExited, werr)
			}
			return errDlvExited
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("timeout waiting for dlv at %s", addr)
}
