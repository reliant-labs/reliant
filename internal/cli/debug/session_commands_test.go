package debug

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-delve/delve/service/api"
	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/factory"
	dbgsvc "github.com/reliant-labs/forge/internal/debug"
)

// runCmd executes a subcommand with args and returns what it printed.
func runCmd(t *testing.T, cmd *cobra.Command, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	stdout, stderr = captureOutput(t, func() { err = cmd.Execute() })
	return stdout, stderr, err
}

var noFactory = &factory.Factory{}

func TestGroup_RegistersEverySubcommandAndRejectsUnknown(t *testing.T) {
	cmd := newCmd(noFactory)
	got := map[string]bool{}
	for _, c := range cmd.Commands() {
		got[c.Name()] = true
	}
	for _, want := range []string{"start", "break", "breakpoints", "clear", "continue", "step", "step-in", "step-out",
		"eval", "locals", "args", "stack", "goroutines", "stop", "connect"} {
		if !got[want] {
			t.Errorf("subcommand %q is not registered", want)
		}
	}
	_, _, err := runCmd(t, newCmd(noFactory), "contiune")
	if err == nil || !strings.Contains(err.Error(), `unknown command "contiune"`) || !strings.Contains(err.Error(), "continue") {
		t.Fatalf("err = %v, want an unknown-command error suggesting continue", err)
	}
}

func TestConnectWithSession_Failures(t *testing.T) {
	t.Run("no session says how to get one", func(t *testing.T) {
		inTempProject(t)
		_, err := connectToSession()
		if err == nil || !strings.Contains(err.Error(), "no active debug session") || !strings.Contains(err.Error(), "forge debug start") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an unreadable session file is a loading error", func(t *testing.T) {
		dir := inTempProject(t)
		writeFile(t, filepath.Join(dir, ".forge", "debug-session.json"), "{not json")
		_, err := connectToSession()
		if err == nil || !strings.Contains(err.Error(), "loading debug session") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a session whose dlv is gone names the address", func(t *testing.T) {
		inTempProject(t)
		addr := closedAddr(t)
		saveTestSession(t, &dbgsvc.SessionInfo{Type: "delve", Addr: addr})
		_, err := connectToSession()
		if err == nil || !strings.Contains(err.Error(), "connecting to debugger at "+addr) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestSetResumed_PersistsOnlyChanges(t *testing.T) {
	inTempProject(t)
	saveTestSession(t, &dbgsvc.SessionInfo{Type: "delve", Addr: "127.0.0.1:1"})
	s := loadTestSession(t)

	setResumed(s, true)
	if got := loadTestSession(t); !got.Resumed {
		t.Fatal("Resumed was not saved")
	}
	setResumed(nil, true) // a nil session is a no-op, not a panic

	markObserved()
	if got := loadTestSession(t); got.Resumed {
		t.Fatal("markObserved must record that the target's stop was seen")
	}

	// With no session file, markObserved has nothing to update and creates none.
	if err := debugSvc().ClearSession("."); err != nil {
		t.Fatal(err)
	}
	markObserved()
	if got := loadTestSession(t); got != nil {
		t.Fatalf("markObserved created a session: %+v", got)
	}
}

func TestBreak_FunctionSpecs(t *testing.T) {
	t.Run("a qualified function is set as given and recorded in the session", func(t *testing.T) {
		f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil)}
		withSession(t, f)
		out, _, err := runCmd(t, newBreakCmd(noFactory), "main.handleRequest", "--cond", "id > 5")
		if err != nil {
			t.Fatal(err)
		}
		_, created, _, _ := f.snapshot()
		if len(created) != 1 || created[0].Breakpoint.FunctionName != "main.handleRequest" || created[0].Breakpoint.Cond != "id > 5" {
			t.Fatalf("created = %+v, want one function breakpoint with the condition", created)
		}
		if !strings.Contains(out, "Breakpoint 1:") || !strings.Contains(out, "[cond: id > 5]") {
			t.Errorf("output = %q", out)
		}
		if got := loadTestSession(t).BreakpointIDs; !slices.Equal(got, []int{1}) {
			t.Errorf("session BreakpointIDs = %v, want [1] so connected stop can clear it", got)
		}
	})

	t.Run("a bare method name resolves to the handler package's *Service method", func(t *testing.T) {
		f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil)}
		dir := inTempProject(t)
		writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.22\n")
		writeFile(t, filepath.Join(dir, "internal", "handlers", "tasks", "rpc_create_task.go"),
			"package tasks\n\nfunc (s *Service) CreateTask() {}\n")
		saveTestSession(t, &dbgsvc.SessionInfo{Type: "delve", Addr: startFakeDlv(t, f)})
		if _, _, err := runCmd(t, newBreakCmd(noFactory), "CreateTask"); err != nil {
			t.Fatal(err)
		}
		_, created, _, _ := f.snapshot()
		if want := "example.com/app/internal/handlers/tasks.(*Service).CreateTask"; len(created) != 1 || created[0].Breakpoint.FunctionName != want {
			t.Fatalf("created = %+v, want function %q", created, want)
		}
	})

	t.Run("--func forces function resolution even for a file-looking name", func(t *testing.T) {
		f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil)}
		withSession(t, f)
		if _, _, err := runCmd(t, newBreakCmd(noFactory), "--func", "pkg/x.Handler"); err != nil {
			t.Fatal(err)
		}
		_, created, _, _ := f.snapshot()
		if len(created) != 1 || created[0].Breakpoint.FunctionName != "pkg/x.Handler" {
			t.Fatalf("created = %+v", created)
		}
	})

	t.Run("a delve refusal is wrapped with the spec", func(t *testing.T) {
		f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil), createErr: "Command failed: location \"nope\" not found"}
		withSession(t, f)
		_, _, err := runCmd(t, newBreakCmd(noFactory), "main.nope")
		if err == nil || !strings.Contains(err.Error(), `setting function breakpoint "main.nope"`) || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("err = %v", err)
		}
		if got := loadTestSession(t).BreakpointIDs; len(got) != 0 {
			t.Errorf("a failed breakpoint was recorded: %v", got)
		}
	})
}

func TestBreak_FileLineSpecs(t *testing.T) {
	t.Run("the absolute spelling is tried first, then the module-relative one", func(t *testing.T) {
		dir := inTempProject(t)
		// Resolve symlinks so the expected absolute path matches os.Getwd.
		wd, _ := os.Getwd()
		writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.22\n")
		abs := filepath.Join(wd, "internal", "h", "rpc.go")
		f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil), failFiles: map[string]bool{abs: true}}
		saveTestSession(t, &dbgsvc.SessionInfo{Type: "delve", Addr: startFakeDlv(t, f)})

		out, _, err := runCmd(t, newBreakCmd(noFactory), "internal/h/rpc.go:42", "--json")
		if err != nil {
			t.Fatal(err)
		}
		_, created, _, _ := f.snapshot()
		if len(created) != 2 || created[0].Breakpoint.File != abs || created[1].Breakpoint.File != "example.com/app/internal/h/rpc.go" ||
			created[0].Breakpoint.Line != 42 {
			t.Fatalf("spellings tried = %+v, want absolute then module-relative at line 42", created)
		}
		var bp dbgsvc.BreakpointInfo
		jsonInto(t, out, &bp)
		if bp.ID != 1 || bp.Line != 42 {
			t.Errorf("json breakpoint = %+v", bp)
		}
	})

	t.Run("when every file spelling fails, delve's suffix-matching location is the last resort", func(t *testing.T) {
		dir := inTempProject(t)
		wd, _ := os.Getwd()
		_ = dir
		f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil), failFiles: map[string]bool{filepath.Join(wd, "rpc.go"): true}}
		saveTestSession(t, &dbgsvc.SessionInfo{Type: "delve", Addr: startFakeDlv(t, f)})
		if _, _, err := runCmd(t, newBreakCmd(noFactory), "rpc.go:7"); err != nil {
			t.Fatal(err)
		}
		_, created, _, _ := f.snapshot()
		if last := created[len(created)-1]; last.LocExpr != "rpc.go:7" {
			t.Fatalf("last attempt = %+v, want LocExpr rpc.go:7", last)
		}
	})

	t.Run("a refusal names the spec", func(t *testing.T) {
		f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil), createErr: "could not find statement"}
		withSession(t, f)
		_, _, err := runCmd(t, newBreakCmd(noFactory), "x.go:3")
		if err == nil || !strings.Contains(err.Error(), "setting breakpoint at x.go:3") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestBreak_NeedsASpecAndASession(t *testing.T) {
	inTempProject(t)
	if _, _, err := runCmd(t, newBreakCmd(noFactory)); err == nil || !strings.Contains(err.Error(), "provide a file:line or function") {
		t.Fatalf("no spec: err = %v", err)
	}
	if _, _, err := runCmd(t, newBreakCmd(noFactory), "main.f"); err == nil || !strings.Contains(err.Error(), "no active debug session") {
		t.Fatalf("no session: err = %v", err)
	}
}

func TestBreakpointsAndClear(t *testing.T) {
	f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil)}
	withSession(t, f)
	cmdList := func(args ...string) (string, error) {
		out, _, err := runCmd(t, newBreakpointsCmd(noFactory), args...)
		return out, err
	}

	out, err := cmdList()
	if err != nil || out != "No breakpoints set.\n" {
		t.Fatalf("empty list = (%q, %v)", out, err)
	}

	for _, spec := range []string{"main.a", "main.b"} {
		if _, _, err := runCmd(t, newBreakCmd(noFactory), spec); err != nil {
			t.Fatal(err)
		}
	}
	out, err = cmdList()
	if err != nil || !strings.Contains(out, "Breakpoint 1:") || !strings.Contains(out, "Breakpoint 2:") {
		t.Fatalf("list = (%q, %v)", out, err)
	}
	out, _ = cmdList("--json")
	var got []dbgsvc.BreakpointInfo
	jsonInto(t, out, &got)
	if len(got) != 2 || got[1].FunctionName != "main.b" {
		t.Errorf("json list = %+v", got)
	}

	t.Run("clear removes it on the server and from the session", func(t *testing.T) {
		out, _, err := runCmd(t, newClearCmd(noFactory), "1")
		if err != nil || out != "Breakpoint 1 cleared.\n" {
			t.Fatalf("clear = (%q, %v)", out, err)
		}
		if _, _, cleared, _ := f.snapshot(); !slices.Equal(cleared, []int{1}) {
			t.Errorf("server cleared %v", cleared)
		}
		if ids := loadTestSession(t).BreakpointIDs; !slices.Equal(ids, []int{2}) {
			t.Errorf("session BreakpointIDs = %v, want [2]", ids)
		}
	})
	t.Run("clear --json", func(t *testing.T) {
		out, _, err := runCmd(t, newClearCmd(noFactory), "2", "--json")
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		jsonInto(t, out, &got)
		if got["id"] != float64(2) || got["cleared"] != true {
			t.Errorf("json = %v", got)
		}
	})
	t.Run("a non-numeric id is rejected before connecting", func(t *testing.T) {
		_, _, err := runCmd(t, newClearCmd(noFactory), "abc")
		if err == nil || !strings.Contains(err.Error(), `invalid breakpoint ID "abc"`) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a server refusal is wrapped", func(t *testing.T) {
		f.update(func(f *fakeDlvServer) { f.clearErr = "no breakpoint with id 9" })
		_, _, err := runCmd(t, newClearCmd(noFactory), "9")
		if err == nil || !strings.Contains(err.Error(), "clearing breakpoint") || !strings.Contains(err.Error(), "id 9") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("list failure is wrapped", func(t *testing.T) {
		f.update(func(f *fakeDlvServer) { f.listErr = "boom" })
		_, err := cmdList()
		if err == nil || !strings.Contains(err.Error(), "listing breakpoints") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestContinue(t *testing.T) {
	hit := atBreakpoint("/app/h.go", 30, "app.h")

	t.Run("a halted target is resumed and its next stop reported", func(t *testing.T) {
		f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil), afterCmd: hit}
		withSession(t, f, func(s *dbgsvc.SessionInfo) { s.Resumed = true })
		// Resumed=true → wait only (never resume past an unobserved stop).
		f.update(func(f *fakeDlvServer) { f.state = hit })
		out, _, err := runCmd(t, newContinueCmd(noFactory))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "Stopped at /app/h.go:30 (app.h)") || !strings.Contains(out, "Reason: breakpoint") {
			t.Errorf("output = %q", out)
		}
		if cmds, _, _, _ := f.snapshot(); len(cmds) != 0 {
			t.Errorf("waiting for an unobserved stop sent %v: it resumed past it", cmds)
		}
		if loadTestSession(t).Resumed {
			t.Error("a reported stop must clear Resumed")
		}
	})

	t.Run("without Resumed it resumes to the next stop", func(t *testing.T) {
		f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil), afterCmd: hit}
		withSession(t, f)
		out, _, err := runCmd(t, newContinueCmd(noFactory), "--json")
		if err != nil {
			t.Fatal(err)
		}
		if cmds, _, _, _ := f.snapshot(); !slices.Equal(cmds, []string{api.Continue}) {
			t.Errorf("commands = %v, want one continue", cmds)
		}
		var st dbgsvc.StopState
		jsonInto(t, out, &st)
		if st.Function != "app.h" || st.Line != 30 {
			t.Errorf("json stop = %+v", st)
		}
	})

	t.Run("--no-wait resumes and returns, leaving the session marked resumed", func(t *testing.T) {
		f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil)}
		withSession(t, f)
		out, _, err := runCmd(t, newContinueCmd(noFactory), "--no-wait")
		if err != nil || !strings.Contains(out, "Target resumed (running)") {
			t.Fatalf("out=%q err=%v", out, err)
		}
		waitFor(t, "the continue to reach dlv", func() bool { cmds, _, _, _ := f.snapshot(); return slices.Contains(cmds, api.Continue) })
		if !loadTestSession(t).Resumed {
			t.Error("--no-wait must mark the session resumed")
		}
	})

	t.Run("--no-wait --json", func(t *testing.T) {
		f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil)}
		withSession(t, f)
		out, _, err := runCmd(t, newContinueCmd(noFactory), "--no-wait", "--json")
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		jsonInto(t, out, &got)
		if got["running"] != true {
			t.Errorf("json = %v", got)
		}
	})

	t.Run("an exited target is reported as such", func(t *testing.T) {
		f := &fakeDlvServer{stateErr: "Process 99 has exited with status 2"}
		withSession(t, f)
		_, _, err := runCmd(t, newContinueCmd(noFactory))
		if err == nil || !strings.Contains(err.Error(), "target has exited") || !strings.Contains(err.Error(), "status 2") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a state read failure is returned", func(t *testing.T) {
		f := &fakeDlvServer{stateErr: "connection reset"}
		withSession(t, f)
		_, _, err := runCmd(t, newContinueCmd(noFactory))
		if err == nil || !strings.Contains(err.Error(), "connection reset") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a bounded wait on a target that never stops times out and says it is still running", func(t *testing.T) {
		f := &fakeDlvServer{state: runningState()}
		withSession(t, f, func(s *dbgsvc.SessionInfo) { s.Resumed = true })
		_, _, err := runCmd(t, newContinueCmd(noFactory), "--timeout", "100ms")
		if err == nil || !strings.Contains(err.Error(), "no stop within 100ms") || !strings.Contains(err.Error(), "still running") {
			t.Fatalf("err = %v", err)
		}
		if !loadTestSession(t).Resumed {
			t.Error("a timed-out continue must leave the session marked resumed")
		}
	})

	t.Run("a target that exits while continuing is an error", func(t *testing.T) {
		f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil), afterCmd: exitedState(3)}
		withSession(t, f)
		_, _, err := runCmd(t, newContinueCmd(noFactory))
		if err == nil || !strings.Contains(err.Error(), "continuing:") || !strings.Contains(err.Error(), "exited with status 3") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a refused continue is wrapped", func(t *testing.T) {
		f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil), cmdErr: "cannot continue"}
		withSession(t, f)
		_, _, err := runCmd(t, newContinueCmd(noFactory))
		if err == nil || !strings.Contains(err.Error(), "continuing: cannot continue") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("without a session", func(t *testing.T) {
		inTempProject(t)
		if _, _, err := runCmd(t, newContinueCmd(noFactory)); err == nil || !strings.Contains(err.Error(), "no active debug session") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestSteps(t *testing.T) {
	next := stoppedAt("/app/h.go", 31, "app.h", nil)
	cases := []struct {
		name    string
		newCmd  func(*factory.Factory) *cobra.Command
		command string
		errWrap string
	}{
		{"step over", newStepCmd, api.Next, "stepping over"},
		{"step in", newStepInCmd, api.Step, "stepping in"},
		{"step out", newStepOutCmd, api.StepOut, "stepping out"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeDlvServer{state: stoppedAt("/app/h.go", 30, "app.h", nil), afterCmd: next}
			withSession(t, f, func(s *dbgsvc.SessionInfo) { s.Resumed = true })

			out, _, err := runCmd(t, tc.newCmd(noFactory))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "Stopped at /app/h.go:31 (app.h)") || !strings.Contains(out, "Reason: step") {
				t.Errorf("output = %q", out)
			}
			if cmds, _, _, _ := f.snapshot(); !slices.Equal(cmds, []string{tc.command}) {
				t.Errorf("commands = %v, want [%s]", cmds, tc.command)
			}
			if loadTestSession(t).Resumed {
				t.Error("a step observes the stop: Resumed must be cleared")
			}

			out, _, err = runCmd(t, tc.newCmd(noFactory), "--json")
			if err != nil {
				t.Fatal(err)
			}
			var st dbgsvc.StopState
			jsonInto(t, out, &st)
			if st.Line != 31 {
				t.Errorf("json line = %d", st.Line)
			}

			f.update(func(f *fakeDlvServer) { f.cmdErr = "cannot step" })
			if _, _, err = runCmd(t, tc.newCmd(noFactory)); err == nil || !strings.Contains(err.Error(), tc.errWrap+": cannot step") {
				t.Errorf("err = %v, want %q wrapping the refusal", err, tc.errWrap)
			}
		})
	}

	t.Run("a step on a running target halts it first", func(t *testing.T) {
		f := &fakeDlvServer{state: runningState(), afterCmd: next}
		withSession(t, f)
		if _, _, err := runCmd(t, newStepCmd(noFactory)); err != nil {
			t.Fatal(err)
		}
		if cmds, _, _, _ := f.snapshot(); !slices.Equal(cmds, []string{api.Halt, api.Next}) {
			t.Errorf("commands = %v, want halt then next", cmds)
		}
	})

	t.Run("without a session", func(t *testing.T) {
		inTempProject(t)
		for _, c := range []func(*factory.Factory) *cobra.Command{newStepCmd, newStepInCmd, newStepOutCmd} {
			if _, _, err := runCmd(t, c(noFactory)); err == nil || !strings.Contains(err.Error(), "no active debug session") {
				t.Errorf("err = %v", err)
			}
		}
	})
}

func TestInspection(t *testing.T) {
	newFake := func() *fakeDlvServer {
		return &fakeDlvServer{
			state:  stoppedAt("/app/h.go", 30, "app.h", nil),
			locals: []api.Variable{intVar("n", "3"), {Name: "req", Type: "*Req", Value: "0xc0", Children: []api.Variable{intVar("ID", "9")}}},
			args:   []api.Variable{{Name: "ctx", Type: "context.Context", Value: "ctx"}},
			eval:   &api.Variable{Name: "len(items)", Type: "int", Value: "4"},
			stack: []api.Stackframe{
				{Location: api.Location{File: "/app/h.go", Line: 30, Function: &api.Function{Name_: "app.h"}}, Arguments: []api.Variable{intVar("a", "1")}},
				{Location: api.Location{File: "/app/main.go", Line: 8, Function: &api.Function{Name_: "main.main"}}},
			},
			goroutines: []*api.Goroutine{
				{ID: 1, Status: 4, CurrentLoc: api.Location{File: "/rt/proc.go", Line: 1, Function: &api.Function{Name_: "runtime.gopark"}}},
				{ID: 2, Status: 2, UserCurrentLoc: api.Location{File: "/app/w.go", Line: 12, Function: &api.Function{Name_: "app.work"}}, CurrentLoc: api.Location{File: "/rt/x.go", Line: 1}},
				{ID: 3, Status: 99},
			},
		}
	}

	t.Run("eval prints the variable, in scope of the current goroutine", func(t *testing.T) {
		f := newFake()
		withSession(t, f, func(s *dbgsvc.SessionInfo) { s.Resumed = true })
		out, _, err := runCmd(t, newEvalCmd(noFactory), "len(items)")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "len(items)") || !strings.Contains(out, "int") || !strings.Contains(out, "4") {
			t.Errorf("output = %q", out)
		}
		f.mu.Lock()
		exprs := slices.Clone(f.exprs)
		f.mu.Unlock()
		if !slices.Equal(exprs, []string{"len(items)"}) {
			t.Errorf("evaluated %v", exprs)
		}
		if loadTestSession(t).Resumed {
			t.Error("inspecting a halted target observes its stop: Resumed must be cleared")
		}
		out, _, err = runCmd(t, newEvalCmd(noFactory), "len(items)", "--json")
		if err != nil {
			t.Fatal(err)
		}
		var v dbgsvc.Variable
		jsonInto(t, out, &v)
		if v.Value != "4" {
			t.Errorf("json = %+v", v)
		}
	})

	t.Run("eval error is wrapped and leaves Resumed alone", func(t *testing.T) {
		f := newFake()
		f.evalErr = "could not find symbol value for nope"
		withSession(t, f, func(s *dbgsvc.SessionInfo) { s.Resumed = true })
		_, _, err := runCmd(t, newEvalCmd(noFactory), "nope")
		if err == nil || !strings.Contains(err.Error(), "evaluating expression: could not find symbol") {
			t.Fatalf("err = %v", err)
		}
		if !loadTestSession(t).Resumed {
			t.Error("a failed inspection must not claim the stop was observed")
		}
	})

	t.Run("locals and args use the selected goroutine's scope", func(t *testing.T) {
		f := newFake()
		withSession(t, f)
		out, _, err := runCmd(t, newLocalsCmd(noFactory))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"n  ", "req", "*Req", "  ID"} { // the child is indented
			if !strings.Contains(out, want) {
				t.Errorf("locals lack %q:\n%s", want, out)
			}
		}
		out, _, err = runCmd(t, newArgsCmd(noFactory), "--json")
		if err != nil {
			t.Fatal(err)
		}
		var vars []dbgsvc.Variable
		jsonInto(t, out, &vars)
		if len(vars) != 1 || vars[0].Name != "ctx" {
			t.Errorf("args json = %+v", vars)
		}
		f.mu.Lock()
		scopes := slices.Clone(f.scopes)
		f.mu.Unlock()
		if len(scopes) != 2 || scopes[0].GoroutineID != 7 || scopes[1].GoroutineID != 7 {
			t.Errorf("scopes = %+v, want the selected goroutine 7 both times", scopes)
		}
	})

	t.Run("locals with none says (none)", func(t *testing.T) {
		f := newFake()
		f.locals = nil
		withSession(t, f)
		out, _, err := runCmd(t, newLocalsCmd(noFactory))
		if err != nil || out != "(none)\n" {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})

	t.Run("stack passes --depth and prints frames", func(t *testing.T) {
		f := newFake()
		withSession(t, f)
		out, _, err := runCmd(t, newStackCmd(noFactory), "--depth", "7")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "#0   app.h\n     /app/h.go:30\n") || !strings.Contains(out, "#1   main.main") {
			t.Errorf("stack = %q", out)
		}
		f.mu.Lock()
		depths := slices.Clone(f.depths)
		f.mu.Unlock()
		if !slices.Equal(depths, []int{7}) {
			t.Errorf("depth requested = %v, want [7]", depths)
		}
		out, _, _ = runCmd(t, newStackCmd(noFactory), "--json")
		var frames []dbgsvc.StackFrame
		jsonInto(t, out, &frames)
		if len(frames) != 2 || len(frames[0].Args) != 1 {
			t.Errorf("stack json = %+v", frames)
		}
	})

	t.Run("goroutines prefer the user location and name unknown statuses", func(t *testing.T) {
		f := newFake()
		withSession(t, f)
		out, _, err := runCmd(t, newGoroutinesCmd(noFactory))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"waiting", "runtime.gopark", "running", "app.work", "/app/w.go:12", "unknown(99)"} {
			if !strings.Contains(out, want) {
				t.Errorf("goroutines lack %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "/rt/x.go") {
			t.Errorf("a goroutine with a user location must not show its runtime one:\n%s", out)
		}
		out, _, _ = runCmd(t, newGoroutinesCmd(noFactory), "--json")
		var gs []dbgsvc.GoroutineInfo
		jsonInto(t, out, &gs)
		if len(gs) != 3 || gs[0].Status != "waiting" {
			t.Errorf("goroutines json = %+v", gs)
		}
	})

	t.Run("inspecting a running target halts it first", func(t *testing.T) {
		f := newFake()
		f.state = runningState()
		withSession(t, f)
		if _, _, err := runCmd(t, newStackCmd(noFactory)); err != nil {
			t.Fatal(err)
		}
		if cmds, _, _, _ := f.snapshot(); !slices.Equal(cmds, []string{api.Halt}) {
			t.Errorf("commands = %v, want a halt", cmds)
		}
	})

	t.Run("without a session every inspection says so", func(t *testing.T) {
		inTempProject(t)
		for name, c := range map[string]*cobra.Command{
			"locals": newLocalsCmd(noFactory), "args": newArgsCmd(noFactory), "stack": newStackCmd(noFactory),
			"goroutines": newGoroutinesCmd(noFactory), "breakpoints": newBreakpointsCmd(noFactory),
		} {
			if _, _, err := runCmd(t, c); err == nil || !strings.Contains(err.Error(), "no active debug session") {
				t.Errorf("%s: err = %v", name, err)
			}
		}
		if _, _, err := runCmd(t, newEvalCmd(noFactory), "x"); err == nil || !strings.Contains(err.Error(), "no active debug session") {
			t.Errorf("eval: err = %v", err)
		}
	})
}

func TestConnect(t *testing.T) {
	t.Run("a halted server becomes a connected, unowned session", func(t *testing.T) {
		f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil), pid: 555}
		inTempProject(t)
		addr := startFakeDlv(t, f)
		out, _, err := runCmd(t, newConnectCmd(noFactory), addr)
		if err != nil {
			t.Fatal(err)
		}
		s := loadTestSession(t)
		if s == nil || s.Addr != addr || s.PID != 555 || !s.Connected || s.Owned || s.Resumed || s.Binary != "pid:555" {
			t.Fatalf("session = %+v", s)
		}
		for _, want := range []string{"Connected to dlv at " + addr, "target pid 555", "Target is HALTED (as found)"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("a running server is marked resumed and its pid is read by halting it", func(t *testing.T) {
		f := &fakeDlvServer{state: runningState(), pid: 777}
		inTempProject(t)
		addr := startFakeDlv(t, f)
		out, _, err := runCmd(t, newConnectCmd(noFactory), addr, "--json")
		if err != nil {
			t.Fatal(err)
		}
		var s dbgsvc.SessionInfo
		jsonInto(t, out, &s)
		if s.PID != 777 || !s.Resumed || !s.Connected {
			t.Fatalf("session = %+v", s)
		}
	})

	t.Run(":port and bare port address forms are normalised", func(t *testing.T) {
		for in, want := range map[string]string{":2345": "127.0.0.1:2345", "2345": "127.0.0.1:2345", "10.0.0.5:9": "10.0.0.5:9", "localhost:1": "localhost:1"} {
			if got := normalizeAddr(in); got != want {
				t.Errorf("normalizeAddr(%q) = %q, want %q", in, got, want)
			}
		}
	})

	t.Run("a repeated connect to the same server keeps the breakpoints it set", func(t *testing.T) {
		f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil), pid: 555}
		inTempProject(t)
		addr := startFakeDlv(t, f)
		saveTestSession(t, &dbgsvc.SessionInfo{Type: "delve", Addr: addr, PID: 555, Connected: true, BreakpointIDs: []int{4, 6}})
		if _, _, err := runCmd(t, newConnectCmd(noFactory), addr); err != nil {
			t.Fatal(err)
		}
		if got := loadTestSession(t).BreakpointIDs; !slices.Equal(got, []int{4, 6}) {
			t.Errorf("BreakpointIDs = %v, want [4 6]", got)
		}
	})

	t.Run("it refuses to replace a session forge started", func(t *testing.T) {
		f := &fakeDlvServer{state: stoppedAt("/a.go", 1, "main.main", nil)}
		inTempProject(t)
		addr := startFakeDlv(t, f)
		saveTestSession(t, &dbgsvc.SessionInfo{Type: "delve", Addr: "127.0.0.1:1", Owned: true})
		_, _, err := runCmd(t, newConnectCmd(noFactory), addr)
		if err == nil || !strings.Contains(err.Error(), "a debug session is already active (dlv at 127.0.0.1:1)") || !strings.Contains(err.Error(), "forge debug stop") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a server serving an exited process is refused", func(t *testing.T) {
		f := &fakeDlvServer{stateErr: "Process 9 has exited with status 1"}
		inTempProject(t)
		_, _, err := runCmd(t, newConnectCmd(noFactory), startFakeDlv(t, f))
		if err == nil || !strings.Contains(err.Error(), "serving an exited process") {
			t.Fatalf("err = %v", err)
		}
		if loadTestSession(t) != nil {
			t.Error("a refused connect must not save a session")
		}
	})

	t.Run("an unreadable target state is wrapped", func(t *testing.T) {
		f := &fakeDlvServer{stateErr: "internal error"}
		inTempProject(t)
		_, _, err := runCmd(t, newConnectCmd(noFactory), startFakeDlv(t, f))
		if err == nil || !strings.Contains(err.Error(), "reading target state: internal error") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("nothing listening", func(t *testing.T) {
		inTempProject(t)
		addr := closedAddr(t)
		_, _, err := runCmd(t, newConnectCmd(noFactory), addr)
		if err == nil || !strings.Contains(err.Error(), "connecting to dlv at "+addr) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("exactly one address is required", func(t *testing.T) {
		if _, _, err := runCmd(t, newConnectCmd(noFactory)); err == nil {
			t.Fatal("connect with no address must fail")
		}
	})
}

func TestReadModulePath(t *testing.T) {
	dir := t.TempDir()
	if got := readModulePath(dir); got != "" {
		t.Errorf("no go.mod: got %q", got)
	}
	writeFile(t, filepath.Join(dir, "go.mod"), "// header\nmodule example.com/svc\n\ngo 1.22\n")
	if got := readModulePath(dir); got != "example.com/svc" {
		t.Errorf("got %q", got)
	}
	writeFile(t, filepath.Join(dir, "go.mod"), "go 1.22\n")
	if got := readModulePath(dir); got != "" {
		t.Errorf("go.mod without a module line: got %q", got)
	}
}

func TestResolveShortFuncName_BothReceiverSpellings(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "internal", "handlers", "billing", "svc.go"), "package billing\n\nfunc (s *service) Charge() {}\n")
	writeFile(t, filepath.Join(dir, "internal", "handlers", "README.md"), "not a dir entry")
	withWD(t, dir, func() {
		if got, want := resolveShortFuncName("m", "Charge"), "m/internal/handlers/billing.(*Service).Charge"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	withWD(t, t.TempDir(), func() {
		if got := resolveShortFuncName("m", "Charge"); got != "" {
			t.Errorf("no internal/handlers: got %q", got)
		}
	})
}
