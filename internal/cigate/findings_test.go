package cigate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const testModule = "example.com/shop"

// findingsRoot is a project tree holding the files the fixtures name, so a
// generic `path:line:` line is placed only when the file exists.
func findingsRoot(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func assertFindings(t *testing.T, got, want []Finding) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		var g, w strings.Builder
		for _, f := range got {
			fmt.Fprintf(&g, "  %+v\n", f)
		}
		for _, f := range want {
			fmt.Fprintf(&w, "  %+v\n", f)
		}
		t.Errorf("findings:\n%swant:\n%s", g.String(), w.String())
	}
}

// go test's plain output: a test log line names only a file's base name, and
// the package line that follows the block places it. Subtests, multi-line
// messages, a panic with no log line, a passing package, and a package that
// failed with no test (TestMain exit) are each placed.
func TestExtractFindings_GoTest(t *testing.T) {
	out := `task: [test] go test -race -count=1 ./...
--- FAIL: TestCheckout (0.00s)
    checkout_test.go:42: total = 10, want 12
--- FAIL: TestOrders (0.00s)
    --- FAIL: TestOrders/empty (0.00s)
        orders_test.go:17: got error: boom
            more detail
            and more
    --- FAIL: TestOrders/nil (0.00s)
        orders_test.go:30: unexpected nil
FAIL
FAIL	example.com/shop/internal/orders	0.031s
ok  	example.com/shop/internal/billing	0.010s
--- FAIL: TestCrash (0.00s)
panic: runtime error: index out of range [3] with length 1 [recovered]

goroutine 7 [running]:
	/home/ci/shop/internal/cart/cart_test.go:12 +0x1d
FAIL	example.com/shop/internal/cart	0.020s
FAIL	example.com/shop	0.004s
FAIL
`
	got, omitted := ExtractFindings(out, FindingsInput{Module: testModule})
	assertFindings(t, got, []Finding{
		{Path: ".", Level: LevelError, Message: "example.com/shop failed"},
		{Path: "internal/cart", Level: LevelError, Message: "TestCrash: panic: runtime error: index out of range [3] with length 1 [recovered]"},
		{Path: "internal/orders/checkout_test.go", Line: 42, Level: LevelError, Message: "TestCheckout: total = 10, want 12"},
		{Path: "internal/orders/orders_test.go", Line: 17, Level: LevelError, Message: "TestOrders/empty: got error: boom\nmore detail\nand more"},
		{Path: "internal/orders/orders_test.go", Line: 30, Level: LevelError, Message: "TestOrders/nil: unexpected nil"},
	})
	if omitted != 0 {
		t.Errorf("omitted %d", omitted)
	}
}

// go test -json carries the same text in its output events, in -v order: a
// test's log lines come BEFORE its --- FAIL line, and a passing test's log is
// printed too, which is not a finding. Each event's Test attributes its line.
func TestExtractFindings_GoTestJSON(t *testing.T) {
	root := findingsRoot(t, "internal/orders/orders_test.go")
	out := `{"Action":"start","Package":"example.com/shop/internal/orders"}
{"Action":"run","Package":"example.com/shop/internal/orders","Test":"TestOK"}
{"Action":"output","Package":"example.com/shop/internal/orders","Test":"TestOK","Output":"=== RUN   TestOK\n"}
{"Action":"output","Package":"example.com/shop/internal/orders","Test":"TestOK","Output":"    orders_test.go:3: just logging\n"}
{"Action":"output","Package":"example.com/shop/internal/orders","Test":"TestOK","Output":"--- PASS: TestOK (0.00s)\n"}
{"Action":"pass","Package":"example.com/shop/internal/orders","Test":"TestOK"}
{"Action":"run","Package":"example.com/shop/internal/orders","Test":"TestOrders"}
{"Action":"output","Package":"example.com/shop/internal/orders","Test":"TestOrders","Output":"=== RUN   TestOrders\n"}
{"Action":"output","Package":"example.com/shop/internal/orders","Test":"TestOrders","Output":"    orders_test.go:9: want 2 rows\n"}
{"Action":"output","Package":"example.com/shop/internal/orders","Test":"TestOrders","Output":"--- FAIL: TestOrders (0.00s)\n"}
{"Action":"fail","Package":"example.com/shop/internal/orders","Test":"TestOrders"}
{"Action":"output","Package":"example.com/shop/internal/orders","Output":"FAIL\n"}
{"Action":"output","Package":"example.com/shop/internal/orders","Output":"FAIL\texample.com/shop/internal/orders\t0.01s\n"}
{"Action":"fail","Package":"example.com/shop/internal/orders"}
`
	got, _ := ExtractFindings(out, FindingsInput{Root: root, Module: testModule})
	assertFindings(t, got, []Finding{
		{Path: "internal/orders/orders_test.go", Line: 9, Level: LevelError, Message: "TestOrders: want 2 rows"},
	})
}

// go test -json with packages interleaved (`go test -json -p N ./...`):
// every line is attributed by its event's Package and Test, and a package's
// findings are placed by its own terminal fail event — b has no `FAIL <pkg>`
// summary line at all. a passes (its log is no finding); c's stream is cut
// short before its terminal event (its failed test is still placed, with no
// package-level finding); d fails to build (Go 1.24 build-output events), and
// is not reported again as a package; e fails with no test, after d, and is
// reported — d's build error does not carry over to it.
func TestExtractFindings_GoTestJSONInterleaved(t *testing.T) {
	root := findingsRoot(t, "internal/d/d.go")
	const m = testModule
	out := strings.Join([]string{
		`{"Action":"start","Package":"` + m + `/internal/a"}`,
		`{"Action":"start","Package":"` + m + `/internal/b"}`,
		`{"Action":"output","Package":"` + m + `/internal/a","Test":"TestA","Output":"=== RUN   TestA\n"}`,
		`{"Action":"output","Package":"` + m + `/internal/b","Test":"TestB","Output":"=== RUN   TestB\n"}`,
		`{"Action":"output","Package":"` + m + `/internal/a","Test":"TestA","Output":"    a_test.go:5: only logging\n"}`,
		`{"Action":"output","Package":"` + m + `/internal/b","Test":"TestB","Output":"    b_test.go:7: b broke\n"}`,
		`{"Action":"output","Package":"` + m + `/internal/c","Test":"TestC","Output":"--- FAIL: TestC (0.00s)\n"}`,
		`{"Action":"output","Package":"` + m + `/internal/b","Test":"TestB","Output":"--- FAIL: TestB (0.00s)\n"}`,
		`{"Action":"output","Package":"` + m + `/internal/a","Test":"TestA","Output":"--- PASS: TestA (0.00s)\n"}`,
		`{"Action":"output","Package":"` + m + `/internal/c","Test":"TestC","Output":"    c_test.go:2: c broke\n"}`,
		`{"Action":"pass","Package":"` + m + `/internal/a","Test":"TestA"}`,
		`{"Action":"fail","Package":"` + m + `/internal/b","Test":"TestB"}`,
		`{"Action":"output","Package":"` + m + `/internal/a","Output":"ok  \t` + m + `/internal/a\t0.01s\n"}`,
		`{"Action":"pass","Package":"` + m + `/internal/a"}`,
		`{"Action":"fail","Package":"` + m + `/internal/b"}`,
		`{"ImportPath":"` + m + `/internal/d [` + m + `/internal/d.test]","Action":"build-output","Output":"# ` + m + `/internal/d [` + m + `/internal/d.test]\n"}`,
		`{"ImportPath":"` + m + `/internal/d [` + m + `/internal/d.test]","Action":"build-output","Output":"internal/d/d.go:3:1: undefined: x\n"}`,
		`{"ImportPath":"` + m + `/internal/d [` + m + `/internal/d.test]","Action":"build-fail"}`,
		`{"Action":"output","Package":"` + m + `/internal/d","Output":"FAIL\t` + m + `/internal/d [build failed]\n"}`,
		`{"Action":"fail","Package":"` + m + `/internal/d","FailedBuild":"` + m + `/internal/d [` + m + `/internal/d.test]"}`,
		`{"Action":"output","Package":"` + m + `/internal/e","Output":"panic: boom\n"}`,
		`{"Action":"fail","Package":"` + m + `/internal/e"}`,
	}, "\n")
	got, _ := ExtractFindings(out, FindingsInput{Root: root, Module: m})
	assertFindings(t, got, []Finding{
		{Path: "internal/b/b_test.go", Line: 7, Level: LevelError, Message: "TestB: b broke"},
		{Path: "internal/c/c_test.go", Line: 2, Level: LevelError, Message: "TestC: c broke"},
		{Path: "internal/d/d.go", Line: 3, Column: 1, Level: LevelError, Message: "undefined: x"},
		{Path: "internal/e", Level: LevelError, Message: m + "/internal/e failed: panic: boom"},
	})
}

// jsonEvents builds a go test -json stream of events in package m/internal/<pkg>.
func jsonEvents(t *testing.T, events ...[4]string) string {
	t.Helper()
	lines := make([]string, 0, len(events))
	for _, e := range events {
		b, err := json.Marshal(map[string]string{"Package": testModule + "/internal/" + e[0], "Action": e[1], "Test": e[2], "Output": e[3]})
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(b))
	}
	return strings.Join(lines, "\n")
}

// Output events that name no Test are the package's own (TestMain, an init
// panic, a leak checker, `FAIL`, `exit status`). They are never assigned to a
// test, whichever test is running or printed last; a package's own located
// diagnostics are placed when the package fails, and a failure nothing
// located is reported on the package with its panic and the tail of its log.
//
//   - j: every test passed, then TestMain's leak check prints a located line
//     and the package fails: the diagnostic is the finding; no test is blamed
//     and no package-level fallback is added.
//   - f: TestF2 is running when a Test-less panic is printed and the binary
//     dies: the panic is the package's, not TestF2's.
//   - g: TestG passed and logged, then TestMain panics in teardown: the
//     package carries the panic and its log tail; TestG's log is no finding.
//   - h: an init panic before any test.
//   - i: two tests failed, and the package's own output panicked: both tests,
//     and the package carrying its panic.
func TestExtractFindings_GoTestJSONPackageOutput(t *testing.T) {
	root := findingsRoot(t, "internal/j/main_test.go")
	const m = testModule
	out := jsonEvents(t,
		[4]string{"j", "run", "TestJ", ""},
		[4]string{"j", "output", "TestJ", "=== RUN   TestJ\n"},
		[4]string{"j", "output", "TestJ", "--- PASS: TestJ (0.00s)\n"},
		[4]string{"j", "pass", "TestJ", ""},
		[4]string{"j", "output", "", "PASS\n"},
		[4]string{"j", "output", "", "internal/j/main_test.go:14: goroutine leak: 1 goroutine still running\n"},
		[4]string{"j", "output", "", "FAIL\t" + m + "/internal/j\t0.010s\n"},
		[4]string{"j", "fail", "", ""},

		[4]string{"f", "run", "TestF1", ""},
		[4]string{"f", "output", "TestF1", "=== RUN   TestF1\n"},
		[4]string{"f", "output", "TestF1", "--- PASS: TestF1 (0.00s)\n"},
		[4]string{"f", "pass", "TestF1", ""},
		[4]string{"f", "run", "TestF2", ""},
		[4]string{"f", "output", "TestF2", "=== RUN   TestF2\n"},
		[4]string{"f", "output", "", "panic: assignment to entry in nil map\n"},
		[4]string{"f", "output", "", "\n"},
		[4]string{"f", "output", "", "goroutine 7 [running]:\n"},
		[4]string{"f", "output", "", "exit status 2\n"},
		[4]string{"f", "output", "", "FAIL\t" + m + "/internal/f\t0.020s\n"},
		[4]string{"f", "fail", "", ""},

		[4]string{"g", "run", "TestG", ""},
		[4]string{"g", "output", "TestG", "=== RUN   TestG\n"},
		[4]string{"g", "output", "TestG", "    g_test.go:4: setting up\n"},
		[4]string{"g", "output", "TestG", "--- PASS: TestG (0.00s)\n"},
		[4]string{"g", "pass", "TestG", ""},
		[4]string{"g", "output", "", "PASS\n"},
		[4]string{"g", "output", "", "teardown: closing db\n"},
		[4]string{"g", "output", "", "panic: teardown: connection refused\n"},
		[4]string{"g", "output", "", "FAIL\t" + m + "/internal/g\t0.011s\n"},
		[4]string{"g", "fail", "", ""},

		[4]string{"h", "output", "", "panic: init: missing config\n"},
		[4]string{"h", "output", "", "FAIL\t" + m + "/internal/h\t0.003s\n"},
		[4]string{"h", "fail", "", ""},

		[4]string{"i", "run", "TestI1", ""},
		[4]string{"i", "run", "TestI2", ""},
		[4]string{"i", "output", "", "panic: send on closed channel\n"},
		[4]string{"i", "fail", "TestI1", ""},
		[4]string{"i", "fail", "TestI2", ""},
		[4]string{"i", "fail", "", ""},
	)
	got, _ := ExtractFindings(out, FindingsInput{Root: root, Module: m})
	assertFindings(t, got, []Finding{
		{Path: "internal/f", Level: LevelError, Message: m + "/internal/f failed: panic: assignment to entry in nil map\ngoroutine 7 [running]:"},
		{Path: "internal/g", Level: LevelError, Message: m + "/internal/g failed: panic: teardown: connection refused\nteardown: closing db"},
		{Path: "internal/h", Level: LevelError, Message: m + "/internal/h failed: panic: init: missing config"},
		{Path: "internal/i", Level: LevelError, Message: "TestI1 failed"},
		{Path: "internal/i", Level: LevelError, Message: "TestI2 failed"},
		{Path: "internal/i", Level: LevelError, Message: m + "/internal/i: panic: send on closed channel"},
		{Path: "internal/j/main_test.go", Line: 14, Level: LevelError, Message: "goroutine leak: 1 goroutine still running"},
	})
}

// One test running, and the package prints a located line and a panic with
// no Test: neither is attributed to the running test, which never failed.
func TestExtractFindings_GoTestJSONRunningTestNotBlamed(t *testing.T) {
	root := findingsRoot(t, "internal/k/k.go")
	out := jsonEvents(t,
		[4]string{"k", "run", "TestK", ""},
		[4]string{"k", "output", "TestK", "=== RUN   TestK\n"},
		[4]string{"k", "output", "", "internal/k/k.go:9: background worker: unexpected EOF\n"},
		[4]string{"k", "output", "", "panic: worker died\n"},
		[4]string{"k", "fail", "", ""},
	)
	got, _ := ExtractFindings(out, FindingsInput{Root: root, Module: testModule})
	assertFindings(t, got, []Finding{
		{Path: "internal/k/k.go", Line: 9, Level: LevelError, Message: "background worker: unexpected EOF"},
	})
	for _, f := range got {
		if strings.Contains(f.Message, "TestK") {
			t.Errorf("package output blamed on the running test: %+v", f)
		}
	}
}

// A located line a test printed is that test's: no finding while the test
// passes, and it does not count as a build error, so a package that then
// fails with nothing else located still gets its package-level finding. The
// same line from a failed test is a finding at its own path.
func TestExtractFindings_GoTestJSONTestLocatedLine(t *testing.T) {
	root := findingsRoot(t, "internal/l/l.go")
	out := jsonEvents(t,
		[4]string{"l", "run", "TestL", ""},
		[4]string{"l", "output", "TestL", "=== RUN   TestL\n"},
		[4]string{"l", "output", "TestL", "internal/l/l.go:5: fixture loaded\n"},
		[4]string{"l", "output", "TestL", "--- PASS: TestL (0.00s)\n"},
		[4]string{"l", "pass", "TestL", ""},
		[4]string{"l", "output", "", "os.Exit(3) after tests\n"},
		[4]string{"l", "fail", "", ""},

		[4]string{"n", "run", "TestN", ""},
		[4]string{"n", "output", "TestN", "--- FAIL: TestN (0.00s)\n"},
		[4]string{"n", "output", "TestN", "internal/l/l.go:7: wrong value\n"},
		[4]string{"n", "fail", "TestN", ""},
		[4]string{"n", "fail", "", ""},
	)
	got, _ := ExtractFindings(out, FindingsInput{Root: root, Module: testModule})
	assertFindings(t, got, []Finding{
		{Path: "internal/l", Level: LevelError, Message: testModule + "/internal/l failed\nos.Exit(3) after tests"},
		{Path: "internal/l/l.go", Line: 7, Level: LevelError, Message: "TestN: wrong value"},
	})
}

// A build error is credited to the package its `# <pkg>` header names, never
// to the next package: a without a FAIL summary line (go vet's output has
// none) must not hide b's failure.
func TestExtractFindings_BuildErrorDoesNotCarryOver(t *testing.T) {
	root := findingsRoot(t, "internal/a/a.go")
	out := "# example.com/shop/internal/a\n" +
		"internal/a/a.go:3:1: undefined: x\n" +
		"FAIL\texample.com/shop/internal/b\t0.010s\n" +
		"# example.com/shop/internal/a\n" +
		"internal/a/a.go:3:1: undefined: x\n" +
		"FAIL\texample.com/shop/internal/a [build failed]\n"
	got, _ := ExtractFindings(out, FindingsInput{Root: root, Module: testModule})
	assertFindings(t, got, []Finding{
		{Path: "internal/a/a.go", Line: 3, Column: 1, Level: LevelError, Message: "undefined: x"},
		{Path: "internal/b", Level: LevelError, Message: "example.com/shop/internal/b failed"},
	})
}

// A package that does not compile: the compiler's located lines are the
// findings, and the package is not reported a second time.
func TestExtractFindings_GoTestBuildFailure(t *testing.T) {
	root := findingsRoot(t, "internal/orders/orders.go")
	out := `# example.com/shop/internal/orders
internal/orders/orders.go:12:2: undefined: total
FAIL	example.com/shop/internal/orders [build failed]
`
	got, _ := ExtractFindings(out, FindingsInput{Root: root, Module: testModule})
	assertFindings(t, got, []Finding{
		{Path: "internal/orders/orders.go", Line: 12, Column: 2, Level: LevelError, Message: "undefined: total"},
	})
}

// golangci-lint's text output: the located line, then the source line and a
// caret, which are not findings. A path that names no file in the project
// (a URL, a generated path elsewhere) is dropped.
func TestExtractFindings_Golangci(t *testing.T) {
	root := findingsRoot(t, "internal/orders/orders.go", "cmd/server/main.go")
	out := `internal/orders/orders.go:23:9: Error return value of ` + "`rows.Close`" + ` is not checked (errcheck)
	defer rows.Close()
	      ^
cmd/server/main.go:88:1: Function 'main' is too long (171 > 150) (funlen)
internal/orders/orders.go:40:2: warning: S1009: should omit nil check (gosimple)
vendor/x/y.go:1:1: not ours (revive)
see https://golangci-lint.run:443: for help
3 issues:
* errcheck: 1
`
	got, _ := ExtractFindings(out, FindingsInput{Root: root, Module: testModule})
	assertFindings(t, got, []Finding{
		{Path: "cmd/server/main.go", Line: 88, Column: 1, Level: LevelError, Message: "Function 'main' is too long (171 > 150) (funlen)"},
		{Path: "internal/orders/orders.go", Line: 23, Column: 9, Level: LevelError, Message: "Error return value of `rows.Close` is not checked (errcheck)"},
		{Path: "internal/orders/orders.go", Line: 40, Column: 2, Level: LevelWarning, Message: "S1009: should omit nil check (gosimple)"},
	})
}

// buf lint (no space after the last colon), tsc relative to the check's
// directory, and an absolute path under the root.
func TestExtractFindings_BufTSCAndAbsolute(t *testing.T) {
	root := findingsRoot(t, "proto/shop/v1/shop.proto", "frontends/web/src/app.tsx", "internal/a/a.go")
	out := "proto/shop/v1/shop.proto:12:3:Field name \"Total\" should be lower_snake_case.\n" +
		"src/app.tsx(7,14): error TS2322: Type 'string' is not assignable to type 'number'.\n" +
		filepath.Join(root, "internal", "a", "a.go") + ":5:1: vet: unreachable code\n"
	// A relative path is relative to the check's directory: from the
	// frontend, tsc's path is found and buf's is not. The absolute path is
	// placed either way.
	got, _ := ExtractFindings(out, FindingsInput{Root: root, Dir: "frontends/web", Module: testModule})
	assertFindings(t, got, []Finding{
		{Path: "frontends/web/src/app.tsx", Line: 7, Column: 14, Level: LevelError, Message: "TS2322: Type 'string' is not assignable to type 'number'."},
		{Path: "internal/a/a.go", Line: 5, Column: 1, Level: LevelError, Message: "vet: unreachable code"},
	})
	got, _ = ExtractFindings(out, FindingsInput{Root: root, Module: testModule})
	assertFindings(t, got, []Finding{
		{Path: "internal/a/a.go", Line: 5, Column: 1, Level: LevelError, Message: "vet: unreachable code"},
		{Path: "proto/shop/v1/shop.proto", Line: 12, Column: 3, Level: LevelError, Message: `Field name "Total" should be lower_snake_case.`},
	})
}

// Windows paths, on any OS: a drive-qualified path is the one colon a located
// line's path can hold, `\` separates, and the drive letter matches in either
// case. A relative `internal\a\a.go` is placed like `internal/a/a.go`.
func TestExtractFindings_WindowsPaths(t *testing.T) {
	for _, c := range []struct {
		line, file string
		ln, col    int
		msg        string
	}{
		{`C:\Users\ci\shop\internal\a\a.go:5:1: vet: unreachable code`, `C:\Users\ci\shop\internal\a\a.go`, 5, 1, "vet: unreachable code"},
		{`d:/work/shop/cmd/main.go:12: undefined: x`, `d:/work/shop/cmd/main.go`, 12, 0, "undefined: x"},
		{`internal\a\a.go:3:2: unused`, `internal\a\a.go`, 3, 2, "unused"},
	} {
		m := reLocated.FindStringSubmatch(c.line)
		if m == nil {
			t.Errorf("%q: not parsed as a located line", c.line)
			continue
		}
		if m[1] != c.file || m[2] != strconv.Itoa(c.ln) || (c.col != 0 && m[3] != strconv.Itoa(c.col)) || m[4] != c.msg {
			t.Errorf("%q parsed as file %q line %s col %s msg %q", c.line, m[1], m[2], m[3], m[4])
		}
	}
	if reLocated.MatchString(`see C:foo.go:1: x`) || reLocated.MatchString(`https://example.com:443: refused`) {
		t.Error("a colon that is not a drive must not start a path")
	}

	for _, c := range []struct {
		root, file, want string
		ok               bool
	}{
		{`C:\Users\ci\shop`, `C:\Users\ci\shop\internal\a\a.go`, "internal/a/a.go", true},
		{`C:\Users\ci\shop`, `c:/Users/ci/shop/internal/a/a.go`, "internal/a/a.go", true},
		{`c:\Users\ci\shop\`, `C:\Users\ci\shop\go.mod`, "go.mod", true},
		{`C:\`, `C:\a.go`, "a.go", true},
		{`C:\Users\ci\shop`, `D:\Users\ci\shop\a.go`, "", false},
		{`C:\Users\ci\shop`, `C:\Users\ci\shopping\a.go`, "", false},
		{`C:\Users\ci\shop`, `C:\Users\ci\shop`, "", false},
		{`C:\Users\ci\shop`, `/Users/ci/shop/a.go`, "", false},
		{`/home/ci/shop`, `/home/ci/shop/internal/a/a.go`, "internal/a/a.go", true},
		{`/`, `/a.go`, "a.go", true},
	} {
		got, ok := relUnder(c.root, c.file)
		if got != c.want || ok != c.ok {
			t.Errorf("relUnder(%q, %q) = %q, %v; want %q, %v", c.root, c.file, got, ok, c.want, c.ok)
		}
	}

	root := findingsRoot(t, "internal/a/a.go")
	got, _ := ExtractFindings(`internal\a\a.go:3:2: unused`+"\n", FindingsInput{Root: root})
	assertFindings(t, got, []Finding{{Path: "internal/a/a.go", Line: 3, Column: 2, Level: LevelError, Message: "unused"}})
}

// The same output always gives the same list: sorted, de-duplicated, capped.
func TestExtractFindings_DeterministicAndCapped(t *testing.T) {
	root := findingsRoot(t, "a.go")
	var b strings.Builder
	for i := MaxFindings + 25; i >= 1; i-- {
		fmt.Fprintf(&b, "a.go:%d:1: issue %d\n", i, i)
		fmt.Fprintf(&b, "a.go:%d:1: issue %d\n", i, i) // repeated: one finding
	}
	got, omitted := ExtractFindings(b.String(), FindingsInput{Root: root})
	if len(got) != MaxFindings || omitted != 25 {
		t.Fatalf("got %d findings, %d omitted; want %d and 25", len(got), omitted, MaxFindings)
	}
	for i, f := range got {
		if f.Line != i+1 {
			t.Fatalf("finding %d is line %d: not sorted", i, f.Line)
		}
	}
	again, _ := ExtractFindings(b.String(), FindingsInput{Root: root})
	if !reflect.DeepEqual(got, again) {
		t.Error("the same output gave a different list")
	}
}
