package debug

import (
	"errors"
	"fmt"
	"net/rpc"
	"net/rpc/jsonrpc"
	"time"

	"github.com/go-delve/delve/service/api"
)

// rpcClient is a minimal Delve JSON-RPC client. It deliberately avoids
// importing delve's service/rpc2, which transitively pulls in
// service/debugger and pkg/proc/native — a package that does not build on
// windows/arm64 even though dlv itself runs there. Only service/api (pure
// types) is imported. The wire types below mirror the In/Out structs in
// delve v1.27.2 service/rpc2/server.go; the method names and call
// sequences mirror service/rpc2/client.go.
type rpcClient struct {
	client *rpc.Client
}

// newRPCClient dials addr and negotiates API version 2 (client.go newFromRPCClient).
func newRPCClient(addr string) (*rpcClient, error) {
	conn, err := jsonrpc.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dialing delve at %s: %w", addr, err)
	}
	c := &rpcClient{client: conn}
	if err := c.call("SetApiVersion", api.SetAPIVersionIn{APIVersion: 2}, &api.SetAPIVersionOut{}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("negotiating delve API version: %w", err)
	}
	return c, nil
}

func (c *rpcClient) call(method string, args, reply any) error {
	return c.client.Call("RPCServer."+method, args, reply)
}

// Wire types (server.go). Reply structs carry json tags naming the exact
// field names delve encodes (its own structs are untagged, so the wire name
// IS the Go field name): the tags change nothing on the wire, they declare
// that net/rpc/jsonrpc writes these fields by reflection.
type (
	processPidOut struct {
		Pid int `json:"Pid"`
	} // ProcessPidOut, server.go:34
	detachIn struct{ Kill bool }        // DetachIn, :56
	stateIn  struct{ NonBlocking bool } // StateIn, :105
	stateOut struct {                   // StateOut, :110
		State *api.DebuggerState `json:"State"`
	}
	commandOut struct { // CommandOut, :127
		State api.DebuggerState
	}
	stacktraceIn struct { // StacktraceIn, :194
		Id     int64 `json:"Id"` //nolint:revive // delve's wire name; ID would not decode
		Depth  int
		Full   bool
		Defers bool
		Opts   api.StacktraceOptions
		Cfg    *api.LoadConfig
		Skip   int
	}
	stacktraceOut struct {
		Locations []api.Stackframe `json:"Locations"`
	} // StacktraceOut, :204
	listBreakpointsIn  struct{ All bool } // :253
	listBreakpointsOut struct {
		Breakpoints []*api.Breakpoint `json:"Breakpoints"`
	}
	createBreakpointIn struct { // :267
		Breakpoint          api.Breakpoint
		LocExpr             string
		SubstitutePathRules [][2]string
		Suspended           bool
	}
	createBreakpointOut struct{ Breakpoint api.Breakpoint } // :275
	clearBreakpointIn   struct {                            // :332
		Id   int `json:"Id"` //nolint:revive // delve's wire name; ID would not decode
		Name string
	}
	clearBreakpointOut struct {
		Breakpoint *api.Breakpoint `json:"Breakpoint"`
	} // :337
	listLocalVarsIn struct { // :530
		Scope api.EvalScope
		Cfg   api.LoadConfig
	}
	listLocalVarsOut struct {
		Variables []api.Variable `json:"Variables"`
	}
	listFunctionArgsIn struct { // :549
		Scope api.EvalScope
		Cfg   api.LoadConfig
	}
	listFunctionArgsOut struct {
		Args []api.Variable `json:"Args"`
	}
	evalIn struct { // :568
		Scope api.EvalScope
		Expr  string
		Cfg   *api.LoadConfig
	}
	evalOut struct {
		Variable *api.Variable `json:"Variable"`
	}
	listGoroutinesIn struct { // :667
		Start   int
		Count   int
		Filters []api.ListGoroutinesFilter
		api.GoroutineGroupingOptions
		EvalScope *api.EvalScope
	}
	listGoroutinesOut struct { // :677
		Goroutines    []*api.Goroutine `json:"Goroutines"`
		Nextg         int              `json:"Nextg"`
		Groups        []api.GoroutineGroup
		TooManyGroups bool
	}
)

// ProcessPid returns the PID of the debugged target (0 on error).
func (c *rpcClient) ProcessPid() int {
	out := new(processPidOut)
	_ = c.call("ProcessPid", struct{}{}, out)
	return out.Pid
}

// errRPCTimeout is a bounded call that did not return in time.
var errRPCTimeout = errors.New("timed out waiting for delve")

// await waits for an in-flight call until deadline (nil = forever). On
// deadline it returns ErrStillRunning; the call stays pending on the
// connection, and the server finishes it regardless.
func await(call *rpc.Call, deadline <-chan time.Time) error {
	select {
	case <-call.Done:
		return call.Error
	case <-deadline:
		return ErrStillRunning
	}
}

// callTimeout is call with a bound: lifecycle RPCs must not hang stop.
func (c *rpcClient) callTimeout(method string, args, reply any, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	err := await(c.client.Go("RPCServer."+method, args, reply, nil), t.C)
	if errors.Is(err, ErrStillRunning) {
		return fmt.Errorf("%s: %w after %s", method, errRPCTimeout, d)
	}
	return err
}

// Close closes the connection.
func (c *rpcClient) Close() error { return c.client.Close() }

// detachTimeout detaches (optionally killing the target) and closes the
// connection. With kill false Delve removes its breakpoints first.
func (c *rpcClient) detachTimeout(kill bool, d time.Duration) error {
	defer func() { _ = c.client.Close() }()
	return c.callTimeout("Detach", detachIn{kill}, &struct{}{}, d)
}

// stateNonBlockingTimeout is GetStateNonBlocking with a bound. NonBlocking
// only spares the wait for Delve's target lock; the transport still waits for
// a reply, which a wedged server never sends.
func (c *rpcClient) stateNonBlockingTimeout(d time.Duration) (*api.DebuggerState, error) {
	var out stateOut
	if err := c.callTimeout("State", stateIn{NonBlocking: true}, &out, d); err != nil {
		// A timed-out call may still write out later: never read it.
		return nil, err
	}
	return out.State, nil
}

// haltTimeout stops a running target; it returns once the target is halted.
func (c *rpcClient) haltTimeout(d time.Duration) (*api.DebuggerState, error) {
	var out commandOut
	err := c.callTimeout("Command", api.DebuggerCommand{Name: api.Halt}, &out, d)
	return &out.State, err
}

// listBreakpointsTimeout is ListBreakpoints with a bound: release's cleanup
// must not hang on a server that answered State and then stalled.
func (c *rpcClient) listBreakpointsTimeout(d time.Duration) ([]*api.Breakpoint, error) {
	var out listBreakpointsOut
	if err := c.callTimeout("ListBreakpoints", listBreakpointsIn{false}, &out, d); err != nil {
		// A timed-out call may still write out later: never read it.
		return nil, err
	}
	return out.Breakpoints, nil
}

// clearBreakpointTimeout is ClearBreakpoint with a bound, for release.
func (c *rpcClient) clearBreakpointTimeout(id int, d time.Duration) error {
	return c.callTimeout("ClearBreakpoint", clearBreakpointIn{Id: id}, new(clearBreakpointOut), d)
}

// resume sends a Continue without waiting for its reply: the request is
// written before Go returns, and a multi-client dlv keeps running the target
// after this connection closes (dlv's own --continue does the same).
func (c *rpcClient) resume() error {
	call := c.client.Go("RPCServer.Command", &api.DebuggerCommand{Name: api.Continue}, new(commandOut), make(chan *rpc.Call, 1))
	select {
	case <-call.Done:
		// Only a send failure completes this fast; a real Continue returns
		// at the next stop.
		return call.Error
	default:
		return nil
	}
}

// continueCmd resumes the target and waits, until deadline, for it to stop.
func (c *rpcClient) continueCmd(deadline <-chan time.Time) (*api.DebuggerState, error) {
	var out commandOut
	if err := await(c.client.Go("RPCServer.Command", &api.DebuggerCommand{Name: api.Continue}, &out, nil), deadline); err != nil {
		return nil, err
	}
	return &out.State, nil
}

// waitState waits, until deadline, for a running target to stop: a blocking
// State call returns only once Delve's target lock is free.
func (c *rpcClient) waitState(deadline <-chan time.Time) (*api.DebuggerState, error) {
	var out stateOut
	if err := await(c.client.Go("RPCServer.State", stateIn{NonBlocking: false}, &out, nil), deadline); err != nil {
		return nil, err
	}
	if out.State == nil {
		return nil, errors.New("delve returned no state")
	}
	return out.State, nil
}

// Disconnect closes the connection; with cont it first fires an async Continue.
func (c *rpcClient) Disconnect(cont bool) error {
	if cont {
		out := new(commandOut)
		c.client.Go("RPCServer.Command", &api.DebuggerCommand{Name: api.Continue}, out, nil)
	}
	return c.client.Close()
}

func (c *rpcClient) GetState() (*api.DebuggerState, error) {
	var out stateOut
	err := c.call("State", stateIn{NonBlocking: false}, &out)
	return out.State, err
}

func (c *rpcClient) GetStateNonBlocking() (*api.DebuggerState, error) {
	var out stateOut
	err := c.call("State", stateIn{NonBlocking: true}, &out)
	return out.State, err
}

func (c *rpcClient) command(name string) (*api.DebuggerState, error) {
	var out commandOut
	err := c.call("Command", api.DebuggerCommand{Name: name}, &out)
	return &out.State, err
}

func (c *rpcClient) Next() (*api.DebuggerState, error)    { return c.command(api.Next) }
func (c *rpcClient) Step() (*api.DebuggerState, error)    { return c.command(api.Step) }
func (c *rpcClient) StepOut() (*api.DebuggerState, error) { return c.command(api.StepOut) }

func (c *rpcClient) CreateBreakpoint(bp *api.Breakpoint) (*api.Breakpoint, error) {
	return c.createBreakpoint(createBreakpointIn{Breakpoint: *bp})
}

// createBreakpoint is CreateBreakpoint with the full request: LocExpr lets
// Delve resolve a location expression (`file.go:42` by path suffix).
func (c *rpcClient) createBreakpoint(in createBreakpointIn) (*api.Breakpoint, error) {
	var out createBreakpointOut
	err := c.call("CreateBreakpoint", in, &out)
	return &out.Breakpoint, err
}

func (c *rpcClient) ClearBreakpoint(id int) (*api.Breakpoint, error) {
	var out clearBreakpointOut
	err := c.call("ClearBreakpoint", clearBreakpointIn{Id: id}, &out)
	return out.Breakpoint, err
}

func (c *rpcClient) ListBreakpoints(all bool) ([]*api.Breakpoint, error) {
	var out listBreakpointsOut
	err := c.call("ListBreakpoints", listBreakpointsIn{all}, &out)
	return out.Breakpoints, err
}

func (c *rpcClient) Stacktrace(goroutineID int64, depth, skip int, opts api.StacktraceOptions, cfg *api.LoadConfig) ([]api.Stackframe, error) {
	var out stacktraceOut
	err := c.call("Stacktrace", stacktraceIn{Id: goroutineID, Depth: depth, Opts: opts, Cfg: cfg, Skip: skip}, &out)
	return out.Locations, err
}

func (c *rpcClient) ListGoroutines(start, count int) ([]*api.Goroutine, int, error) {
	var out listGoroutinesOut
	err := c.call("ListGoroutines", listGoroutinesIn{Start: start, Count: count}, &out)
	return out.Goroutines, out.Nextg, err
}

func (c *rpcClient) ListLocalVariables(scope api.EvalScope, cfg api.LoadConfig) ([]api.Variable, error) {
	var out listLocalVarsOut
	err := c.call("ListLocalVars", listLocalVarsIn{scope, cfg}, &out)
	return out.Variables, err
}

func (c *rpcClient) ListFunctionArgs(scope api.EvalScope, cfg api.LoadConfig) ([]api.Variable, error) {
	var out listFunctionArgsOut
	err := c.call("ListFunctionArgs", listFunctionArgsIn{scope, cfg}, &out)
	return out.Args, err
}

func (c *rpcClient) EvalVariable(scope api.EvalScope, expr string, cfg api.LoadConfig) (*api.Variable, error) {
	var out evalOut
	err := c.call("Eval", evalIn{scope, expr, &cfg}, &out)
	return out.Variable, err
}
