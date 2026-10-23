package debug

import (
	"fmt"
	"net/rpc"
	"net/rpc/jsonrpc"

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

// Detach detaches (optionally killing the target) and closes the connection.
func (c *rpcClient) Detach(kill bool) error {
	defer func() { _ = c.client.Close() }()
	return c.call("Detach", detachIn{kill}, &struct{}{})
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
func (c *rpcClient) Halt() (*api.DebuggerState, error)    { return c.command(api.Halt) }

// Continue resumes the target, delivering each stop on the returned channel.
// Like delve's client it keeps resuming through tracepoint-only stops and
// closes the channel at the first real stop, exit, or error.
func (c *rpcClient) Continue() <-chan *api.DebuggerState {
	ch := make(chan *api.DebuggerState)
	go func() {
		for {
			var out commandOut
			err := c.call("Command", &api.DebuggerCommand{Name: api.Continue}, &out)
			state := out.State
			if err != nil {
				state.Err = err
			}
			if state.Exited {
				state.Err = fmt.Errorf("process %d has exited with status %d", c.ProcessPid(), state.ExitStatus)
			}
			ch <- &state
			if err != nil || state.Exited {
				close(ch)
				return
			}

			isBreakpoint, isTracepoint := false, true
			for i := range state.Threads {
				if bp := state.Threads[i].Breakpoint; bp != nil {
					isBreakpoint = true
					isTracepoint = isTracepoint && (bp.Tracepoint || bp.TraceReturn)
				}
			}
			if !isBreakpoint || !isTracepoint {
				close(ch)
				return
			}
		}
	}()
	return ch
}

func (c *rpcClient) CreateBreakpoint(bp *api.Breakpoint) (*api.Breakpoint, error) {
	var out createBreakpointOut
	err := c.call("CreateBreakpoint", createBreakpointIn{Breakpoint: *bp}, &out)
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
