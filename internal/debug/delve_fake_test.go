package debug

import (
	"net"
	"net/rpc"
	"net/rpc/jsonrpc"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/go-delve/delve/service/api"
)

// fakeDlv is a scripted Delve rpc2 server: just enough of RPCServer for the
// lifecycle and wait paths, with every request recorded. It lets a test hold
// the target in exactly the state a race would leave it in, deterministically.
type fakeDlv struct {
	mu         sync.Mutex
	state      api.DebuggerState // what State answers
	stateHangs bool              // State never answers (a wedged dlv)
	listHangs  bool              // ListBreakpoints never answers
	clearHangs bool              // ClearBreakpoint never answers
	bps        []*api.Breakpoint
	commands   []string
	cleared    []int
	hang       chan struct{}
}

// Wire types: net/rpc wants exported argument types; the fields mirror the
// unexported ones in rpcclient.go.
type (
	FakeStateIn  struct{ NonBlocking bool }
	FakeStateOut struct{ State *api.DebuggerState }
	FakeCmdOut   struct{ State api.DebuggerState }
	FakeListIn   struct{ All bool }
	FakeListOut  struct{ Breakpoints []*api.Breakpoint }
	FakeClearIn  struct {
		Id   int //nolint:revive // delve's wire name
		Name string
	}
	FakeClearOut struct{ Breakpoint *api.Breakpoint }
	FakeDetachIn struct{ Kill bool }
	FakeEmpty    struct{}
)

func (f *fakeDlv) SetApiVersion(_ api.SetAPIVersionIn, _ *api.SetAPIVersionOut) error { //nolint:revive // delve's method name
	return nil
}

func (f *fakeDlv) State(_ FakeStateIn, out *FakeStateOut) error {
	f.mu.Lock()
	hangs := f.stateHangs
	st := f.state
	f.mu.Unlock()
	if hangs {
		<-f.hang
	}
	out.State = &st
	return nil
}

func (f *fakeDlv) Command(cmd api.DebuggerCommand, out *FakeCmdOut) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, cmd.Name)
	// A continue runs straight to a breakpoint; a halt leaves it halted.
	out.State = f.state
	return nil
}

func (f *fakeDlv) ListBreakpoints(_ FakeListIn, out *FakeListOut) error {
	f.mu.Lock()
	hangs := f.listHangs
	f.mu.Unlock()
	if hangs {
		<-f.hang
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out.Breakpoints = f.bps
	return nil
}

func (f *fakeDlv) ClearBreakpoint(in FakeClearIn, out *FakeClearOut) error {
	f.mu.Lock()
	hangs := f.clearHangs
	f.mu.Unlock()
	if hangs {
		<-f.hang
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, in.Id)
	out.Breakpoint = &api.Breakpoint{ID: in.Id}
	return nil
}

func (f *fakeDlv) Detach(_ FakeDetachIn, _ *FakeEmpty) error { return nil }

func (f *fakeDlv) recorded() (commands []string, cleared []int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.commands), slices.Clone(f.cleared)
}

// serveFake starts f on loopback and returns a DelveDebugger connected to
// it. Connect itself is bypassed: it reads State, which a hanging fake
// never answers.
func serveFake(t *testing.T, f *fakeDlv) *DelveDebugger {
	t.Helper()
	f.hang = make(chan struct{})
	srv := rpc.NewServer()
	if err := srv.RegisterName("RPCServer", f); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go srv.ServeCodec(jsonrpc.NewServerCodec(conn))
		}
	}()
	t.Cleanup(func() {
		close(f.hang)
		_ = l.Close()
	})
	c, err := newRPCClient(l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &DelveDebugger{client: c, addr: l.Addr().String()}
}

// haltedAtBreakpoint is a target stopped on user breakpoint 1.
func haltedAtBreakpoint() api.DebuggerState {
	bp := &api.Breakpoint{ID: 1, FunctionName: "main.tick"}
	th := &api.Thread{ID: 1, Function: &api.Function{Name_: "main.tick"}, Breakpoint: bp}
	return api.DebuggerState{CurrentThread: th, Threads: []*api.Thread{th}}
}

// shrinkRPCTimeout bounds lifecycle RPCs to d for one test.
func shrinkRPCTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := rpcTimeout
	rpcTimeout = d
	t.Cleanup(func() { rpcTimeout = old })
}

// TestStopAndDetachBoundedWhenStateNeverAnswers: a dlv that accepts
// connections but never answers State must not hang stop. Every lifecycle
// RPC is bounded — the initial state read included — so Stop and Detach
// return an error and runStop reaches its signal fallback.
func TestStopAndDetachBoundedWhenStateNeverAnswers(t *testing.T) {
	shrinkRPCTimeout(t, 200*time.Millisecond)
	for name, op := range map[string]func(*DelveDebugger) error{
		"Stop":    (*DelveDebugger).Stop,
		"Detach":  (*DelveDebugger).Detach,
		"Release": func(d *DelveDebugger) error { return d.Release([]int{1}) },
	} {
		t.Run(name, func(t *testing.T) {
			d := serveFake(t, &fakeDlv{stateHangs: true})
			done := make(chan error, 1)
			go func() { done <- op(d) }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatalf("%s against a dlv that never answers State returned nil", name)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%s hung on a dlv that never answers State", name)
			}
		})
	}
}

// TestReleaseBoundedWhenCleanupStalls: a dlv that answers State and then
// stalls ListBreakpoints, or ClearBreakpoint, must not hang stop either.
// Release returns an error (stop keeps the session for a retry) and sends no
// Continue: a target forge could not clean up is not resumed into breakpoints
// it still holds.
func TestReleaseBoundedWhenCleanupStalls(t *testing.T) {
	shrinkRPCTimeout(t, 200*time.Millisecond)
	for name, f := range map[string]*fakeDlv{
		"ListBreakpoints": {listHangs: true, bps: []*api.Breakpoint{{ID: 1}}},
		"ClearBreakpoint": {clearHangs: true, bps: []*api.Breakpoint{{ID: 1}}},
	} {
		t.Run(name, func(t *testing.T) {
			d := serveFake(t, f)
			done := make(chan error, 1)
			go func() { done <- d.Release([]int{1}) }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatalf("Release against a dlv whose %s never answers returned nil", name)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("Release hung on a dlv whose %s never answers", name)
			}
			if cmds, _ := f.recorded(); slices.Contains(cmds, api.Continue) {
				t.Fatalf("Release resumed the target after a failed cleanup: %v", cmds)
			}
		})
	}
}

// TestWaitForStopNeverResumesAHaltedTarget pins the unobserved-stop race:
// a resumed target that reaches its breakpoint before the wait reads its
// state is halted when the wait starts. WaitForStop reports that stop and
// sends no Continue; ContinueFor, for a target nobody resumed, does resume.
func TestWaitForStopNeverResumesAHaltedTarget(t *testing.T) {
	f := &fakeDlv{state: haltedAtBreakpoint()}
	d := serveFake(t, f)
	stop, err := d.WaitForStop(5 * time.Second)
	if err != nil {
		t.Fatalf("WaitForStop: %v", err)
	}
	if stop.Reason != "breakpoint" || stop.Function != "main.tick" {
		t.Fatalf("WaitForStop = %+v, want the pending breakpoint stop in main.tick", stop)
	}
	if cmds, _ := f.recorded(); len(cmds) != 0 {
		t.Fatalf("WaitForStop sent %v: it resumed past the unobserved stop", cmds)
	}

	if _, err := d.ContinueFor(5 * time.Second); err != nil {
		t.Fatalf("ContinueFor: %v", err)
	}
	if cmds, _ := f.recorded(); !slices.Equal(cmds, []string{api.Continue}) {
		t.Fatalf("ContinueFor of a halted target sent %v, want one continue", cmds)
	}
}

// TestReleaseClearsOnlySessionBreakpoints: a connected dlv is multi-client,
// so release clears the breakpoints this session created and leaves every
// other one — set before forge connected, or by another client — and
// Delve's own (negative IDs).
func TestReleaseClearsOnlySessionBreakpoints(t *testing.T) {
	f := &fakeDlv{bps: []*api.Breakpoint{{ID: -1}, {ID: 1}, {ID: 2}, {ID: 3}}}
	d := serveFake(t, f)
	if err := d.Release([]int{2, 7}); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, cleared := f.recorded(); !slices.Equal(cleared, []int{2}) {
		t.Fatalf("Release cleared %v, want only the session's [2]", cleared)
	}
}
