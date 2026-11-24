package querycapture_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/querycapture"
)

// recordFromHere is one call site: every call records from the same line.
func recordFromHere(r *querycapture.Recorder, query string) { r.Record(query) }

func TestRecorder_DedupsPerShapeAndCaller(t *testing.T) {
	dir := t.TempDir()
	r := querycapture.New(dir)
	t.Cleanup(func() { _ = r.Close() })

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Fifty goroutines, one shape (the literal differs), one caller.
			recordFromHere(r, "SELECT * FROM notes WHERE id = '"+strings.Repeat("x", i)+"'")
		}()
	}
	wg.Wait()
	for range 2 { // a second caller and shape, run twice
		r.Record("SELECT * FROM notes WHERE id = $1")
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) != 1 || !strings.HasPrefix(filepath.Base(files[0]), strconv.Itoa(os.Getpid())+"-") {
		t.Fatalf("want one <pid>-<random>.jsonl file, got %v", files)
	}
	c, err := querycapture.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Records != 2 || len(c.Shapes) != 2 {
		t.Fatalf("want 2 records in 2 shapes, got %d records, shapes %+v", c.Records, c.Shapes)
	}
	for _, s := range c.Shapes {
		if len(s.Callers) != 1 {
			t.Fatalf("shape %s: want 1 caller, got %+v", s.SQL, s.Callers)
		}
		cs := s.Callers[0]
		if cs.Caller.File != "pkg/querycapture/recorder_test.go" || cs.Caller.Module != "github.com/reliant-labs/forge" {
			t.Errorf("caller %+v: want the module-relative test file", cs.Caller)
		}
		if cs.Caller.Line == 0 || (cs.Caller.AbsFile != "" && !filepath.IsAbs(cs.Caller.AbsFile)) {
			t.Errorf("caller %+v: want a line, and an absolute path unless built -trimpath", cs.Caller)
		}
		// The goroutines' closure and the direct call both sit in the test.
		if len(cs.Tests) != 1 || cs.Tests[0] != "TestRecorder_DedupsPerShapeAndCaller" {
			t.Errorf("caller %+v: want the test name, got %v", cs.Caller, cs.Tests)
		}
		if s.Operation != "SELECT" || len(s.Tables) != 1 || s.Tables[0] != "notes" {
			t.Errorf("shape %+v: want SELECT on notes", s)
		}
		if strings.Contains(s.SQL, "$1") {
			if !strings.HasSuffix(cs.Caller.Function, ".TestRecorder_DedupsPerShapeAndCaller") {
				t.Errorf("function %q: want the test", cs.Caller.Function)
			}
		} else if !strings.HasSuffix(cs.Caller.Function, ".recordFromHere") {
			t.Errorf("function %q: want recordFromHere", cs.Caller.Function)
		}
	}
}

func TestRead_MergesFilesAndSkipsWhatItCannotRead(t *testing.T) {
	dir := t.TempDir()
	a, b := querycapture.New(dir), querycapture.New(dir) // two "processes"
	recordFromHere(a, "SELECT * FROM t WHERE a = $1")
	recordFromHere(b, "SELECT * FROM t WHERE a = $1")
	b.Record("DELETE FROM t WHERE a = $1")
	_ = a.Close()
	_ = b.Close()
	extra := `{"v":1,"fingerprint":"ffffffffffffffff","sql":"SELECT 1","caller":{"file":"x.go","line":3,"function":"m.F"},"test":"TestOther"}
{"v":99,"fingerprint":"eeeeeeeeeeeeeeee","sql":"SELECT 2","caller":{"file":"x.go","line":4,"function":"m.F"}}

{"v":1,"fingerprint":"dddd`
	if err := os.WriteFile(filepath.Join(dir, "1-handwritten.jsonl"), []byte(extra), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := querycapture.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Files != 3 || c.Records != 4 || c.Skipped != 2 || len(c.Shapes) != 3 {
		t.Fatalf("files %d records %d skipped %d shapes %d; want 3 4 2 3", c.Files, c.Records, c.Skipped, len(c.Shapes))
	}
	for i := 1; i < len(c.Shapes); i++ {
		if c.Shapes[i-1].Fingerprint > c.Shapes[i].Fingerprint {
			t.Fatalf("shapes not sorted by fingerprint")
		}
	}
	sel := querycapture.Normalize("SELECT * FROM t WHERE a = $1").Fingerprint
	for _, s := range c.Shapes {
		if s.Fingerprint != sel {
			continue
		}
		if s.Count != 2 || len(s.Callers) != 1 || s.Callers[0].Count != 2 {
			t.Fatalf("the shape both processes ran from one caller: %+v", s)
		}
		if got := s.Callers[0].Tests; len(got) != 1 || got[0] != "TestRead_MergesFilesAndSkipsWhatItCannotRead" {
			t.Errorf("tests %v: want the one test, deduplicated", got)
		}
		return
	}
	t.Fatalf("no shape %s in %+v", sel, c.Shapes)
}

func TestRead_MissingDirIsAnError(t *testing.T) {
	if _, err := querycapture.Read(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("want an error for a directory that does not exist")
	}
	c, err := querycapture.Read(t.TempDir())
	if err != nil || c.Records != 0 || c.Shapes == nil {
		t.Fatalf("an empty directory is an empty capture: %+v, %v", c, err)
	}
}

func TestRecorder_NilRecordsNothing(t *testing.T) {
	var r *querycapture.Recorder
	r.Record("SELECT 1")
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv(querycapture.EnvDir, "")
	if querycapture.FromEnv() != nil {
		t.Fatal("unset: want no recorder")
	}
	dir := t.TempDir()
	t.Setenv(querycapture.EnvDir, dir)
	a, b := querycapture.FromEnv(), querycapture.FromEnv()
	if a == nil || a != b {
		t.Fatalf("want one process recorder per directory, got %p %p", a, b)
	}
	t.Setenv(querycapture.EnvDir, t.TempDir())
	if c := querycapture.FromEnv(); c == a {
		t.Fatal("another directory: want another recorder")
	}
}
