package cli

import (
	"errors"
	"strings"
	"testing"
)

// Under a dev go.work bridge, generate used to run `go work sync`, which writes
// the WORKSPACE build list — including the bridged forge checkout's own
// requirements — into the project's go.mod. A project pinned to forge v0.1.17
// had gomega / protovalidate / go-sqlite3 "upgraded" by a generate that changed
// no code. The bridged tidy must only ever run module-local (GOWORK=off)
// commands, and must write nothing when the module is already tidy.
func TestTidyBridgedModule_CleanModuleWritesNothing(t *testing.T) {
	var calls []string
	run := func(dir string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return nil, nil // `tidy -diff` clean
	}
	if err := tidyBridgedModuleWith(run, "/proj", "root tidy"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ";") != "mod tidy -diff" {
		t.Fatalf("a tidy module must only be probed, got calls %q", calls)
	}
}

func TestTidyBridgedModule_StaleModuleTidiesModuleLocally(t *testing.T) {
	var calls []string
	run := func(dir string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(args) == 3 { // the -diff probe
			return []byte("diff current/go.mod tidy/go.mod"), errors.New("exit status 1")
		}
		return nil, nil
	}
	if err := tidyBridgedModuleWith(run, "/proj", "root tidy"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(calls, ";"); got != "mod tidy -diff;mod tidy" {
		t.Fatalf("calls = %q, want the probe then a module-local tidy", got)
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "work ") {
			t.Fatalf("the bridged tidy must never touch the workspace (`go %s`)", c)
		}
	}
}

// A module that needs unpublished forge API cannot tidy module-locally. That
// must not fail generate (the go.work-aware validate build is the gate), and
// must not fall back to anything that rewrites go.mod from the workspace.
func TestTidyBridgedModule_UnresolvableTidyIsNotFatal(t *testing.T) {
	run := func(dir string, args ...string) ([]byte, error) {
		return []byte("unknown revision"), errors.New("exit status 1")
	}
	if err := tidyBridgedModuleWith(run, "/proj", "gen/ tidy"); err != nil {
		t.Fatalf("an unresolvable bridged tidy must warn, not fail: %v", err)
	}
}
