package debug

import (
	"slices"
	"testing"

	"github.com/go-delve/delve/service/api"
)

// TestDlvServerArgs pins the shape of every forge-launched dlv server: it
// listens on loopback only (its API runs arbitrary code in the target) and
// accepts many clients (each `forge debug` call connects and leaves).
func TestDlvServerArgs(t *testing.T) {
	t.Parallel()
	got := dlvServerArgs("exec", "127.0.0.1:4000", "/bin/app")
	want := []string{"exec", "--headless", "--listen=127.0.0.1:4000", "--api-version=2", "--accept-multiclient", "/bin/app"}
	if !slices.Equal(got, want) {
		t.Fatalf("dlvServerArgs = %v, want %v", got, want)
	}
}

func TestStopBreakpoints(t *testing.T) {
	t.Parallel()
	bp := &api.Breakpoint{ID: 1}
	trace := &api.Breakpoint{ID: 2, Tracepoint: true}
	cases := []struct {
		name               string
		state              api.DebuggerState
		wantBP, wantTraceO bool
	}{
		{"manual halt", api.DebuggerState{Threads: []*api.Thread{{}}}, false, false},
		{"breakpoint", api.DebuggerState{Threads: []*api.Thread{{Breakpoint: bp}}}, true, false},
		{"tracepoint only", api.DebuggerState{Threads: []*api.Thread{{Breakpoint: trace}}}, true, true},
		{"tracepoint and breakpoint", api.DebuggerState{Threads: []*api.Thread{{Breakpoint: trace}, {Breakpoint: bp}}}, true, false},
		{"current thread only", api.DebuggerState{CurrentThread: &api.Thread{Breakpoint: bp}}, true, false},
	}
	for _, tc := range cases {
		gotBP, gotTrace := stopBreakpoints(&tc.state)
		if gotBP != tc.wantBP || gotTrace != tc.wantTraceO {
			t.Errorf("%s: stopBreakpoints = (%v, %v), want (%v, %v)", tc.name, gotBP, gotTrace, tc.wantBP, tc.wantTraceO)
		}
	}
}
