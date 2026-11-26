package querycapture

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestNormalize_LexerLiteralForms(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"unicode-escape string", `SELECT U&'d\0061t\+000061' FROM t`, `SELECT U&'d\0061t\+000061' FROM t`},
		{"bit string", `SELECT B'1010', X'1F', N'abc' FROM t`, `SELECT B'1010', X'1F', N'abc' FROM t`},
		{"escaped doubled quote", `SELECT 'it''s' FROM t`, `SELECT 'it''s' FROM t`},
		{"unterminated string runs to the end", `SELECT 'oops FROM t`, `SELECT 'oops FROM t`},
		{"unterminated quoted identifier runs to the end", `SELECT "oops FROM t`, `SELECT "oops FROM t`},
		{"escape string keeps a backslash-quote inside", `SELECT E'a\'b', 1 FROM t`, `SELECT E'a\'b', 1 FROM t`},
		{"plain string does not honour a backslash", `SELECT 'a\', 1 FROM t`, `SELECT 'a\', 1 FROM t`},
		{"hex literal with separators", `SELECT 0xFF_FF, 0b10, 0o17 FROM t`, `SELECT 0xFF_FF, 0b10, 0o17 FROM t`},
		{"leading-dot and exponent numbers", `SELECT .5e-3, 1_000.25E+2, 3e FROM t`, `SELECT .5e-3, 1_000.25E+2, 3e FROM t`},
		{"dollar tag unterminated runs to the end", `SELECT $tag$ never closed`, `SELECT $tag$ never closed`},
		{"lone dollar is punctuation", `SELECT $ FROM t`, `SELECT $ FROM t`},
		{"dollar followed by a non-identifier byte", `SELECT $-1 FROM t`, `SELECT $-1 FROM t`},
		{"line comment to end of input", "SELECT 1 -- done", "SELECT 1"},
		{"unterminated block comment", "SELECT 1 /* never closed", "SELECT 1"},
		{"cast operator stays one token", `SELECT $1::int, a :b FROM t`, `SELECT $1::int, a :b FROM t`},
		{"multiple trailing semicolons dropped", "SELECT 1;;  ;", "SELECT 1"},
		{"non-ASCII identifier", `SELECT naïve FROM café`, `SELECT naïve FROM café`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Normalize(tc.in).SQL; got != tc.want {
				t.Errorf("Normalize(%q).SQL\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalize_LiteralsAreMaskedInTheFingerprint(t *testing.T) {
	same := [][2]string{
		{`SELECT 0x1F FROM t`, `SELECT 7 FROM t`},
		{`SELECT E'a\'b' FROM t`, `SELECT 'z' FROM t`},
		{`SELECT $q$x$q$ FROM t`, `SELECT 'y' FROM t`},
		{`SELECT U&'d' FROM t`, `SELECT 'y' FROM t`},
		{`SELECT .5e-3 FROM t`, `SELECT 2 FROM t`},
	}
	for _, p := range same {
		if a, b := Normalize(p[0]).Fingerprint, Normalize(p[1]).Fingerprint; a != b {
			t.Errorf("want one fingerprint for %q (%s) and %q (%s)", p[0], a, p[1], b)
		}
	}
}

func TestNormalize_CollapsingLeavesWhatItCannotReadAlone(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"negative and signed constants collapse", `SELECT 1 FROM t WHERE id IN (-1, +2, NULL, TRUE)`, `SELECT 1 FROM t WHERE id IN (-1)`},
		{"parenthesised constants collapse", `SELECT 1 FROM t WHERE id IN ((1), (2))`, `SELECT 1 FROM t WHERE id IN ((1))`},
		{"an ARRAY constructor is not a constant", `SELECT 1 FROM t WHERE id IN (ARRAY[1], ARRAY[2])`, `SELECT 1 FROM t WHERE id IN (ARRAY[1], ARRAY[2])`},
		{"a quoted identifier is not a constant", `SELECT 1 FROM t WHERE x IN ("a", "b")`, `SELECT 1 FROM t WHERE x IN ("a", "b")`},
		{"an operator other than a sign is not constant", `SELECT 1 FROM t WHERE x IN (1 * 2, 3)`, `SELECT 1 FROM t WHERE x IN (1 * 2, 3)`},
		{"an empty element is not constant", `SELECT 1 FROM t WHERE x IN (1,,2)`, `SELECT 1 FROM t WHERE x IN (1,,2)`},
		{"a lone constant is already one element", `SELECT 1 FROM t WHERE x IN (1)`, `SELECT 1 FROM t WHERE x IN (1)`},
		{"an unclosed IN list is untouched", `SELECT 1 FROM t WHERE x IN (1, 2`, `SELECT 1 FROM t WHERE x IN (1, 2`},
		{"an unclosed VALUES list is untouched", `INSERT INTO t VALUES (1, 2`, `INSERT INTO t VALUES (1, 2`},
		{"VALUES stops at an unbalanced later row", `INSERT INTO t VALUES (1), (2, 3`, `INSERT INTO t VALUES (1), (2, 3`},
		{"IN as the last token", `SELECT 1 FROM t WHERE x IN`, `SELECT 1 FROM t WHERE x IN`},
		{"VALUES as the last token", `SELECT 1 FROM t VALUES`, `SELECT 1 FROM t VALUES`},
		{"IN followed by a non-paren", `SELECT 1 FROM t WHERE x IN $1`, `SELECT 1 FROM t WHERE x IN $1`},
		{"row-value elements are shape, not constants", `SELECT 1 FROM t WHERE x IN ((1, 2), (3, 4))`, `SELECT 1 FROM t WHERE x IN ((1, 2), (3, 4))`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Normalize(tc.in).SQL; got != tc.want {
				t.Errorf("Normalize(%q).SQL\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalize_OperationAndTablesEdgeCases(t *testing.T) {
	for _, tc := range []struct {
		in     string
		op     string
		tables []string
	}{
		{``, "", nil},
		{`"quoted" FROM t`, "", []string{"t"}},
		{`WITH x AS (SELECT 1) INSERT INTO t SELECT * FROM x`, "INSERT", []string{"t"}},
		{`WITH x AS MATERIALIZED (SELECT 1 FROM a) SELECT * FROM x`, "SELECT", []string{"a"}},
		{`WITH x AS NOT MATERIALIZED (SELECT 1 FROM a) SELECT * FROM x`, "SELECT", []string{"a"}},
		{`WITH "Cte" AS (SELECT 1 FROM a) SELECT * FROM "Cte"`, "SELECT", []string{"a"}},
		{`SELECT * FROM LATERAL generate_series(1, 3) g`, "SELECT", nil},
		{`SELECT * FROM s.t, "My Schema"."My Table"`, "SELECT", []string{"s.t"}},
		{`SELECT SUBSTRING(a FROM 2), TRIM(BOTH FROM b) FROM t`, "SELECT", []string{"t"}},
		{`SELECT EXTRACT(EPOCH FROM (SELECT now() FROM clock)) FROM t`, "SELECT", []string{"t"}},
		{`INSERT INTO t (a) SELECT a FROM u ON CONFLICT DO UPDATE SET a = 1`, "INSERT", []string{"t", "u"}},
		{`SELECT * FROM t t1 JOIN t t2 ON true`, "SELECT", []string{"t"}},
		{`SELECT * FROM`, "SELECT", nil},
		{`VALUES (1)`, "VALUES", nil},
		{`TABLE t`, "TABLE", nil},
	} {
		st := Normalize(tc.in)
		if st.Operation != tc.op || !reflect.DeepEqual(st.Tables, tc.tables) {
			t.Errorf("Normalize(%q): operation %q tables %q; want %q %q", tc.in, st.Operation, st.Tables, tc.op, tc.tables)
		}
	}
}

func TestIdentName(t *testing.T) {
	for _, tc := range []struct {
		tok  token
		want string
	}{
		{token{kind: tokWord, text: "Users"}, "users"},
		{token{kind: tokQIdent, text: `"Users"`}, "Users"},
		{token{kind: tokQIdent, text: `"a""b"`}, `a"b`},
		{token{kind: tokQIdent, text: `"unterminated`}, "unterminated"},
	} {
		if got := identName(tc.tok); got != tc.want {
			t.Errorf("identName(%+v) = %q, want %q", tc.tok, got, tc.want)
		}
	}
}

func TestPackageOf(t *testing.T) {
	for fn, want := range map[string]string{
		"github.com/a/b.(*T).M.func1": "github.com/a/b",
		"main.main":                   "main",
		"runtime.Callers":             "runtime",
		"nodots":                      "nodots",
		"example.com/x/y_test.TestZ":  "example.com/x/y_test",
	} {
		if got := packageOf(fn); got != want {
			t.Errorf("packageOf(%q) = %q, want %q", fn, got, want)
		}
	}
}

func TestTestFunc(t *testing.T) {
	for _, tc := range []struct{ file, fn, want string }{
		{"/a/x_test.go", "example.com/a_test.TestThing", "TestThing"},
		{"/a/x_test.go", "example.com/a_test.TestThing.func2", "TestThing"},
		{"/a/x_test.go", "example.com/a.TestThing.func1.2", "TestThing"},
		{"/a/x_test.go", "example.com/a.helper", ""},
		{"/a/x.go", "example.com/a.TestThing", ""},
		{"/a/x_test.go", "TestBare", "TestBare"},
	} {
		if got := testFunc(tc.file, tc.fn); got != tc.want {
			t.Errorf("testFunc(%q, %q) = %q, want %q", tc.file, tc.fn, got, tc.want)
		}
	}
}

func TestModulePath(t *testing.T) {
	for _, tc := range []struct{ gomod, want string }{
		{"module example.com/m\n\ngo 1.22\n", "example.com/m"},
		{"// comment\n\tmodule\texample.com/tabbed\n", "example.com/tabbed"},
		{"module \"example.com/quoted\"\n", "example.com/quoted"},
		{"modulefoo example.com/not-a-directive\n", ""},
		{"go 1.22\n", ""},
		{"", ""},
	} {
		path := filepath.Join(t.TempDir(), "go.mod")
		if err := os.WriteFile(path, []byte(tc.gomod), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		got := modulePath(f)
		_ = f.Close()
		if got != tc.want {
			t.Errorf("modulePath(%q) = %q, want %q", tc.gomod, got, tc.want)
		}
	}
}

func TestFrame_ResolvesTheModuleOfTheFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "internal", "db")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	r := New(t.TempDir())

	got := r.frame(runtime.Frame{File: filepath.Join(sub, "x.go"), Line: 9, Function: "example.com/app/internal/db.F"})
	want := Frame{File: "internal/db/x.go", AbsFile: filepath.Join(sub, "x.go"), Module: "example.com/app", Line: 9, Function: "example.com/app/internal/db.F"}
	if got != want {
		t.Errorf("frame in a module:\n got %+v\nwant %+v", got, want)
	}
	// The second lookup of the same directory is served from the cache.
	if _, ok := r.modules.Load(sub); !ok {
		t.Error("want the directory's module cached")
	}
	if again := r.frame(runtime.Frame{File: filepath.Join(sub, "y.go"), Line: 1}); again.Module != "example.com/app" || again.File != "internal/db/y.go" {
		t.Errorf("cached lookup: got %+v", again)
	}

	// No go.mod above the file: the absolute path is all there is.
	// (The temp dir is not inside a Go module.)
	loose := filepath.Join(t.TempDir(), "loose.go")
	got = r.frame(runtime.Frame{File: loose, Line: 2})
	if got.File != loose || got.AbsFile != loose || got.Module != "" {
		t.Errorf("frame outside any module: got %+v", got)
	}

	// A -trimpath file name is split at the module of this binary.
	got = r.frame(runtime.Frame{File: "github.com/reliant-labs/forge/pkg/orm/client.go", Line: 3})
	if got.File != "pkg/orm/client.go" || got.Module != "github.com/reliant-labs/forge" || got.AbsFile != "" {
		t.Errorf("trimpath frame: got %+v", got)
	}
	// A relative name no module of the binary owns is kept as it is.
	got = r.frame(runtime.Frame{File: "unknown.example/x/y.go", Line: 3})
	if got.File != "unknown.example/x/y.go" || got.Module != "" {
		t.Errorf("unknown relative frame: got %+v", got)
	}
}

func TestTrimmedModuleFile_VersionedAndBareModuleNames(t *testing.T) {
	mods := buildModules()
	if len(mods) == 0 {
		t.Skip("no build info in this binary")
	}
	m := mods[0]
	for _, tc := range []struct {
		file, rel string
		ok        bool
	}{
		{m + "@v1.2.3/sub/x.go", "sub/x.go", true},
		{m + "@v1.2.3", "", false},   // a version with no file below it
		{m, "", false},               // the module path alone
		{m + "/", "", false},         // a trailing slash and nothing else
		{m + "x/file.go", "", false}, // shares a prefix with the module only
		{m + "/sub/x.go", "sub/x.go", true},
	} {
		mod, rel, ok := trimmedModuleFile(tc.file)
		if ok != tc.ok || (ok && (rel != tc.rel || mod != m)) {
			t.Errorf("trimmedModuleFile(%q) = %q %q %v; want %q %q %v", tc.file, mod, rel, ok, m, tc.rel, tc.ok)
		}
	}
}

func TestRecorder_WriteFailureWarnsOnceAndStops(t *testing.T) {
	var warn bytes.Buffer
	old := warnOut
	warnOut = &warn
	t.Cleanup(func() { warnOut = old })

	dir := t.TempDir()
	r := New(dir)
	r.Record("SELECT 1 FROM a")
	// Break the open file under the recorder: the next write fails.
	r.mu.Lock()
	_ = r.file.Close()
	r.mu.Unlock()
	r.Record("SELECT 2 FROM b")

	if !r.disabled.Load() {
		t.Fatal("a failed write must switch capture off")
	}
	if n := strings.Count(warn.String(), "query capture"); n != 1 || !strings.Contains(warn.String(), dir) {
		t.Fatalf("want one warning naming the directory, got %q", warn.String())
	}
	if r.file != nil {
		t.Error("the broken file must be released")
	}
	r.Record("SELECT 3 FROM c")
	if n := strings.Count(warn.String(), "query capture"); n != 1 {
		t.Errorf("capture is off: want no second warning, got %q", warn.String())
	}
	c, err := Read(dir)
	if err != nil || c.Records != 1 {
		t.Fatalf("only the record written before the failure survives: %+v, %v", c, err)
	}
}

func TestRecorder_ZeroValueRecords(t *testing.T) {
	// A Recorder declared rather than built by New must work, not deadlock
	// or panic on its unset dedup set.
	r := &Recorder{dir: t.TempDir()}
	r.Record("SELECT 1 FROM t")
	r.Record("SELECT 1 FROM t")
	_ = r.Close()
	c, err := Read(r.dir)
	if err != nil || c.Records != 1 {
		t.Fatalf("want one deduplicated record, got %+v, %v", c, err)
	}
}

func TestRecorder_CloseThenRecordOpensANewFile(t *testing.T) {
	dir := t.TempDir()
	r := New(dir)
	if err := r.Close(); err != nil {
		t.Fatalf("closing a recorder that never wrote: %v", err)
	}
	r.Record("SELECT 1 FROM a")
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r.Record("SELECT 2 FROM b")
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) != 2 {
		t.Fatalf("want a new file after Close, got %v", files)
	}
}

func TestRecorder_SkipsStatementsExplainCannotPlan(t *testing.T) {
	dir := t.TempDir()
	r := New(dir)
	for _, q := range []string{"BEGIN", "SET LOCAL x = 1", "CREATE TABLE t (a int)", ""} {
		r.Record(q)
	}
	_ = r.Close()
	if files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl")); len(files) != 0 {
		t.Fatalf("nothing explainable was run, want no file, got %v", files)
	}
}

func TestRecorder_NormalizeCachesUpToTheBound(t *testing.T) {
	r := New(t.TempDir())
	a := r.normalize("SELECT 1 FROM t")
	if b := r.normalize("SELECT 1 FROM t"); !reflect.DeepEqual(a, b) {
		t.Fatalf("cached statement differs: %+v vs %+v", a, b)
	}
	if got := r.stmtCount.Load(); got != 1 {
		t.Fatalf("want one cached statement, got %d", got)
	}
	r.stmtCount.Store(maxCachedStatements)
	r.normalize("SELECT 2 FROM t")
	if got := r.stmtCount.Load(); got != maxCachedStatements {
		t.Errorf("past the bound nothing more is cached, count = %d", got)
	}
	if _, ok := r.stmts.Load("SELECT 2 FROM t"); ok {
		t.Error("a statement past the bound must not be cached")
	}
}

func TestRecorder_OpenFailsWhenTheDirectoryCannotBeCreated(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(filepath.Join(blocker, "sub"))
	if f, err := r.open(); err == nil {
		_ = f.Close()
		t.Fatal("want an error creating a directory below a regular file")
	}
}

func TestSameFrame(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b Frame
		want bool
	}{
		{"different lines", Frame{File: "x.go", Line: 1}, Frame{File: "x.go", Line: 2}, false},
		{"same absolute path", Frame{AbsFile: "/a/x.go", File: "x.go", Line: 1}, Frame{AbsFile: "/a/x.go", File: "y.go", Line: 1}, true},
		{"same relative path in two modules", Frame{AbsFile: "/a/internal/db/x.go", File: "internal/db/x.go", Line: 1}, Frame{AbsFile: "/b/internal/db/x.go", File: "internal/db/x.go", Line: 1}, false},
		{"no absolute path: file and module decide", Frame{File: "x.go", Module: "m", Line: 1}, Frame{File: "x.go", Module: "m", Line: 1}, true},
		{"no absolute path: other module", Frame{File: "x.go", Module: "m", Line: 1}, Frame{File: "x.go", Module: "n", Line: 1}, false},
		{"one side lacks an absolute path", Frame{AbsFile: "/a/x.go", File: "x.go", Module: "m", Line: 1}, Frame{File: "x.go", Module: "m", Line: 1}, true},
	} {
		if got := sameFrame(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: sameFrame = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRead_ReportsUnreadableInput(t *testing.T) {
	file := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(file); err == nil {
		t.Error("a path that is a file, not a directory: want an error")
	}

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "weird.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err == nil || !strings.Contains(err.Error(), "weird.jsonl") {
		t.Errorf("a capture file that cannot be read: want an error naming it, got %v", err)
	}
}

func TestRead_DirectoryNameWithGlobMetacharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run[1]")
	r := New(dir)
	r.Record("SELECT 1 FROM t")
	_ = r.Close()

	c, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Files != 1 || c.Records != 1 {
		t.Fatalf("a directory named with glob characters must still be read: %+v", c)
	}
}

func TestRead_MergesCallersOfOneShape(t *testing.T) {
	dir := t.TempDir()
	lines := strings.Join([]string{
		`{"v":1,"fingerprint":"aa","sql":"SELECT 1","operation":"SELECT","caller":{"file":"a.go","abs_file":"/m/a.go","module":"m","line":1,"function":"m.A"},"test":"TestB"}`,
		`{"v":1,"fingerprint":"aa","sql":"SELECT 1","caller":{"file":"a.go","abs_file":"/m/a.go","module":"m","line":1,"function":"m.A"},"generated":{"file":"g_gen.go","line":5,"function":"m.G"},"test":"TestA"}`,
		`{"v":1,"fingerprint":"aa","sql":"SELECT 1","caller":{"file":"a.go","abs_file":"/m/a.go","module":"m","line":1,"function":"m.A"},"test":"TestA"}`,
		`{"v":1,"fingerprint":"aa","sql":"SELECT 1","caller":{"file":"a.go","abs_file":"/other/a.go","module":"o","line":1,"function":"o.A"}}`,
		`{"v":1,"fingerprint":"aa","sql":"SELECT 1","caller":{"file":"0.go","line":9,"function":"m.Z"}}`,
		`{"v":0,"fingerprint":"aa","sql":"SELECT 1","caller":{}}`,
		`{"v":1,"fingerprint":"","sql":"SELECT 1","caller":{}}`,
		`not json`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "1-x.jsonl"), []byte(lines+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Records != 5 || c.Skipped != 3 || len(c.Shapes) != 1 {
		t.Fatalf("records %d skipped %d shapes %d; want 5 3 1", c.Records, c.Skipped, len(c.Shapes))
	}
	s := c.Shapes[0]
	if s.Count != 5 || s.Operation != "SELECT" || len(s.Callers) != 3 {
		t.Fatalf("shape: %+v", s)
	}
	// Callers sort by file then line: 0.go, then the two a.go:1.
	if s.Callers[0].Caller.File != "0.go" || s.Callers[1].Caller.File != "a.go" || s.Callers[2].Caller.File != "a.go" {
		t.Errorf("callers not sorted by file: %+v", s.Callers)
	}
	var merged *CallSite
	for i := range s.Callers {
		if s.Callers[i].Caller.AbsFile == "/m/a.go" {
			merged = &s.Callers[i]
		}
	}
	if merged == nil || merged.Count != 3 {
		t.Fatalf("the three records of /m/a.go:1 must merge into one call site: %+v", s.Callers)
	}
	if !reflect.DeepEqual(merged.Tests, []string{"TestA", "TestB"}) {
		t.Errorf("tests = %v, want the distinct tests sorted", merged.Tests)
	}
	if merged.Generated == nil || merged.Generated.Function != "m.G" {
		t.Errorf("a later record's generated frame fills the call site: %+v", merged.Generated)
	}
}
