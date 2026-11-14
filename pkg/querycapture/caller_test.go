package querycapture

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRecorder_FailureWarnsOnceAndStops(t *testing.T) {
	var warn bytes.Buffer
	old := warnOut
	warnOut = &warn
	t.Cleanup(func() { warnOut = old })

	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(notADir)
	r.Record("SELECT 1")
	r.Record("SELECT 2")
	r.Record("SELECT 3")
	if n := strings.Count(warn.String(), "query capture"); n != 1 {
		t.Fatalf("want one warning, got %d: %q", n, warn.String())
	}
}

// frames feeds synthetic frames to callerOf/testName.
func frames(fs ...runtime.Frame) func() (runtime.Frame, bool) {
	return func() (runtime.Frame, bool) {
		if len(fs) == 0 {
			return runtime.Frame{}, false
		}
		f := fs[0]
		fs = fs[1:]
		return f, len(fs) > 0
	}
}

func TestCallerOf_SkipsTheDatabaseStackAndGeneratedCode(t *testing.T) {
	r := New(t.TempDir())
	dbStack := []runtime.Frame{
		{Function: "runtime.Callers", File: "/go/src/runtime/extern.go"},
		{Function: "github.com/reliant-labs/forge/pkg/querycapture.(*Recorder).Record", File: "/f/pkg/querycapture/recorder.go"},
		{Function: "github.com/reliant-labs/forge/pkg/orm.(*bindConn).QueryContext", File: "/f/pkg/orm/bind_conn.go"},
		{Function: "github.com/uptrace/bun.(*SelectQuery).Scan", File: "/m/bun/query_select.go"},
		{Function: "github.com/uptrace/bun/dialect/pgdialect.x", File: "/m/bun/dialect/pgdialect/x.go"},
		{Function: "github.com/reliant-labs/forge/pkg/crud.(*Repo[...]).Get", File: "/f/pkg/crud/repo.go"},
	}
	gen := runtime.Frame{Function: "example.com/app/internal/store.(*NoteStore).Get", File: "/app/internal/store/note_orm_gen.go", Line: 40}
	ext := runtime.Frame{Function: "example.com/app/internal/db.(*Repo).NoteByID", File: "/app/internal/db/note_repo_ext.go", Line: 12}
	test := runtime.Frame{Function: "example.com/app/internal/db_test.TestNoteByID.func2", File: "/app/internal/db/note_test.go", Line: 7}

	caller, generated, ok := r.callerOf(frames(append(dbStack, gen, ext, test)...))
	if !ok || caller.Function != ext.Function || caller.Line != 12 || generated == nil || generated.Function != gen.Function {
		t.Errorf("want the repo extension as caller via the generated store, got %+v via %+v", caller, generated)
	}

	caller, generated, ok = r.callerOf(frames(append(dbStack, gen)...))
	if !ok || caller.Function != gen.Function || generated != nil {
		t.Errorf("only generated code above the stack: want it as the caller, got %+v via %+v", caller, generated)
	}

	// A _test package of a database-stack package is a caller.
	ormTest := runtime.Frame{Function: "github.com/reliant-labs/forge/pkg/orm_test.TestCapture", File: "/f/pkg/orm/capture_pg_test.go"}
	if caller, _, _ = r.callerOf(frames(append(dbStack, ormTest)...)); caller.Function != ormTest.Function {
		t.Errorf("want pkg/orm_test as the caller, got %+v", caller)
	}

	if _, _, ok = r.callerOf(frames(dbStack...)); ok {
		t.Error("nothing outside the stack: want ok=false")
	}

	if got := testName(frames(ext, test)); got != "TestNoteByID" {
		t.Errorf("testName = %q, want TestNoteByID", got)
	}
	if got := testName(frames(ext)); got != "" {
		t.Errorf("testName with no test frame = %q", got)
	}
}

func TestTrimmedModuleFile(t *testing.T) {
	mod, rel, ok := trimmedModuleFile("github.com/reliant-labs/forge/pkg/orm/client.go")
	if !ok || mod != "github.com/reliant-labs/forge" || rel != "pkg/orm/client.go" {
		t.Errorf("main module: got %q %q %v", mod, rel, ok)
	}
	if _, _, ok := trimmedModuleFile("example.invalid/x/y.go"); ok {
		t.Error("a module this binary does not have: want ok=false")
	}
	if _, _, ok := trimmedModuleFile("github.com/reliant-labs/forgery/x.go"); ok {
		t.Error("a path that only shares a prefix with a module: want ok=false")
	}
}
