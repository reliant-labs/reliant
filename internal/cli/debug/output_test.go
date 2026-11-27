package debug

import (
	"strings"
	"testing"

	dbgsvc "github.com/reliant-labs/forge/internal/debug"
)

func TestPrintStopState(t *testing.T) {
	state := &dbgsvc.StopState{
		File: "/app/handler.go", Line: 42, Function: "app.(*Service).Create", Reason: "breakpoint", GoroutineID: 7,
		Args:   []dbgsvc.Variable{{Name: "req", Type: "*Request", Value: "0xc000", Children: []dbgsvc.Variable{{Name: "Title", Type: "string", Value: `"hi"`}}}},
		Locals: []dbgsvc.Variable{{Name: "n", Type: "int", Value: "3"}},
	}

	t.Run("text names the location, reason, goroutine, args and locals", func(t *testing.T) {
		out, _ := captureOutput(t, func() { printStopState(state, false) })
		for _, want := range []string{
			"Stopped at /app/handler.go:42 (app.(*Service).Create)\n",
			"Reason: breakpoint | Goroutine 7\n",
			"\nArguments:\n",
			"  req                  *Request                       0xc000\n",
			"    Title                string                         \"hi\"\n", // a child is indented one level deeper
			"\nLocals:\n",
			"  n                    int                            3\n",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("no goroutine, args or locals leaves those parts out", func(t *testing.T) {
		out, _ := captureOutput(t, func() {
			printStopState(&dbgsvc.StopState{File: "f.go", Line: 1, Function: "main.main", Reason: "step"}, false)
		})
		if out != "Stopped at f.go:1 (main.main)\nReason: step\n" {
			t.Errorf("output = %q", out)
		}
	})

	t.Run("json is the state as the wire shape", func(t *testing.T) {
		out, _ := captureOutput(t, func() { printStopState(state, true) })
		var got dbgsvc.StopState
		jsonInto(t, out, &got)
		if got.File != state.File || got.Line != 42 || got.GoroutineID != 7 || len(got.Args) != 1 || got.Args[0].Children[0].Name != "Title" {
			t.Errorf("decoded = %+v", got)
		}
	})
}

func TestPrintBreakpoint(t *testing.T) {
	bp := dbgsvc.BreakpointInfo{ID: 3, File: "/app/h.go", Line: 9, FunctionName: "app.h", Condition: "id > 5", HitCount: 2}

	out, _ := captureOutput(t, func() { printBreakpoint(bp, false) })
	if want := "Breakpoint 3: /app/h.go:9 (app.h)  hits=2 [cond: id > 5]\n"; out != want {
		t.Errorf("full breakpoint = %q, want %q", out, want)
	}

	out, _ = captureOutput(t, func() { printBreakpoint(dbgsvc.BreakpointInfo{ID: 1, File: "x.go", Line: 2}, false) })
	if want := "Breakpoint 1: x.go:2  hits=0\n"; out != want {
		t.Errorf("minimal breakpoint = %q, want %q", out, want)
	}

	out, _ = captureOutput(t, func() { printBreakpoint(bp, true) })
	var got dbgsvc.BreakpointInfo
	jsonInto(t, out, &got)
	if got != bp {
		t.Errorf("json round trip = %+v, want %+v", got, bp)
	}
}

func TestPrintVariables(t *testing.T) {
	vars := []dbgsvc.Variable{{Name: "a", Type: "int", Value: "1"}, {Name: "b", Type: "string", Value: `"x"`}}

	out, _ := captureOutput(t, func() { printVariables(nil, false) })
	if out != "(none)\n" {
		t.Errorf("empty list = %q, want (none)", out)
	}
	out, _ = captureOutput(t, func() { printVariables(vars, false) })
	if !strings.Contains(out, "a                    int                            1\n") || !strings.Contains(out, "b                    string") {
		t.Errorf("text = %q", out)
	}
	out, _ = captureOutput(t, func() { printVariables(vars, true) })
	var got []dbgsvc.Variable
	jsonInto(t, out, &got)
	if len(got) != 2 || got[1].Value != `"x"` {
		t.Errorf("json = %+v", got)
	}
}

func TestPrintStacktrace(t *testing.T) {
	frames := []dbgsvc.StackFrame{{Function: "main.f", File: "/a.go", Line: 3}, {Function: "main.main", File: "/b.go", Line: 9}}
	out, _ := captureOutput(t, func() { printStacktrace(frames, false) })
	if want := "#0   main.f\n     /a.go:3\n#1   main.main\n     /b.go:9\n"; out != want {
		t.Errorf("text = %q, want %q", out, want)
	}
	out, _ = captureOutput(t, func() { printStacktrace(frames, true) })
	var got []dbgsvc.StackFrame
	jsonInto(t, out, &got)
	if len(got) != 2 || got[0].Function != "main.f" {
		t.Errorf("json = %+v", got)
	}
}

func TestPrintGoroutines(t *testing.T) {
	gs := []dbgsvc.GoroutineInfo{{ID: 1, Status: "running", Function: "main.main", CurrentFile: "/m.go", CurrentLine: 5}}
	out, _ := captureOutput(t, func() { printGoroutines(gs, false) })
	for _, want := range []string{"ID       STATUS       FUNCTION", "LOCATION", "1        running      main.main", "/m.go:5"} {
		if !strings.Contains(out, want) {
			t.Errorf("text lacks %q:\n%s", want, out)
		}
	}
	out, _ = captureOutput(t, func() { printGoroutines(gs, true) })
	var got []dbgsvc.GoroutineInfo
	jsonInto(t, out, &got)
	if len(got) != 1 || got[0].CurrentLine != 5 {
		t.Errorf("json = %+v", got)
	}
}

func TestPrintRunState(t *testing.T) {
	out, _ := captureOutput(t, func() { printRunState(true, "at its entry point") })
	if !strings.Contains(out, "Target is HALTED (at its entry point)") || !strings.Contains(out, "'forge debug continue' to run it") {
		t.Errorf("halted = %q", out)
	}
	out, _ = captureOutput(t, func() { printRunState(false, "ignored") })
	if !strings.Contains(out, "Target is RUNNING.") || strings.Contains(out, "ignored") {
		t.Errorf("running = %q", out)
	}
}

func TestTailFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/log"
	writeFile(t, path, "a\nb\nc\nd\n")
	for _, tc := range []struct {
		n    int
		want string
	}{{2, "c\nd"}, {4, "a\nb\nc\nd"}, {10, "a\nb\nc\nd"}} {
		if got := tailFile(path, tc.n); got != tc.want {
			t.Errorf("tailFile(n=%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
	if got := tailFile(dir+"/missing", 3); got != "" {
		t.Errorf("an unreadable file must tail to nothing, got %q", got)
	}
}

func TestComposeArgsPinsFileAndProjectDir(t *testing.T) {
	got := strings.Join(composeArgs("port", "app-debug", "2345"), " ")
	if want := "compose -f docker-compose.yml --project-directory . port app-debug 2345"; got != want {
		t.Errorf("composeArgs = %q, want %q", got, want)
	}
}
