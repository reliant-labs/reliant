package codemod

import (
	"bufio"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The proof. testdata/connecterrors is a module holding every
// connect.NewError shape of reliant's P7a census, the CodeOf test
// assertions, an in-process reader in the package and in an importer, and
// an interceptor. Its wiretest prints what a real Connect client reads for
// each site, through svcerr.Interceptor for handler code and without it for
// the interceptor. This test runs that before the rewrite and after it, and
// requires the identical lines — then checks the rewrite against goldens,
// the worklist against the shapes that must be left alone, and a second run
// against a no-op.
func TestConnectErrors_WireIdenticalOverTheCorpus(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks a scratch module against forge and runs its tests twice; runs in task test")
	}
	fixture, dir := connectErrorsScratch(t)

	before := wireLines(t, dir)
	if len(before) < 30 {
		t.Fatalf("wiretest printed %d lines, want one per site:\n%s", len(before), strings.Join(before, "\n"))
	}

	dry, err := runConnectErrors(dir, Options{})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if got := readFile(t, dir, "internal/handlers/things/things.go"); got != readFile(t, fixture, "internal/handlers/things/things.go") {
		t.Fatal("--dry-run wrote a file")
	}
	// A dry run's positions are the files as they are: unrewritten.
	assertSitesOnDisk(t, dir, dry, false)

	rep, err := runConnectErrors(dir, Options{Write: true})
	if err != nil {
		t.Fatalf("codemod: %v", err)
	}
	t.Logf("rewrites:\n%s\n\nnext:\n%s", strings.Join(rep.Rewrites, "\n"), strings.Join(rep.Next, "\n"))
	// The same rewrites; only the lines moved, to the rewritten files'.
	if got, want := withoutLines(rep.Rewrites), withoutLines(dry.Rewrites); got != want {
		t.Errorf("dry run and run disagree:\n%s\n---\n%s", want, got)
	}
	assertSitesOnDisk(t, dir, rep, true)
	for _, rel := range []string{
		"internal/handlers/things/things.go",
		"internal/handlers/things/things_test.go",
		"internal/handlers/reader/reader.go",
		"internal/handlers/reader/reader_test.go",
		"internal/handlers/blocked/blocked.go",
		"internal/handlers/asserted/asserted.go",
		"internal/handlers/testifyas/testifyas.go",
		"internal/handlers/suiteas/suiteas.go",
		"internal/handlers/viaiface/viaiface.go",
		"internal/handlers/testonly/testonly.go",
		"internal/handlers/wired/wired.go",
		"internal/handlers/pingsvc/pingsvc.go",
		"internal/interceptors/auth.go",
	} {
		got := readFile(t, dir, rel)
		if os.Getenv("FORGE_UPDATE_GOLDEN") == "1" {
			if err := os.WriteFile(filepath.Join(fixture, filepath.FromSlash(rel)+".golden"), []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if want := readFile(t, fixture, rel+".golden"); got != want {
			t.Errorf("rewritten %s differs from %s.golden:\n%s", rel, rel, got)
		}
	}
	for _, rel := range []string{"internal/app/retry.go", "internal/wiretest/wire_test.go", "internal/handlers/asserted/asserted_test.go",
		"internal/handlers/testifyas/testifyas_test.go", "internal/handlers/suiteas/suiteas_test.go", "internal/lister/lister.go",
		"internal/wireclient/pingv1connect/ping.go", "internal/compose/compose.go", "internal/cliapp/cli.go"} {
		if readFile(t, dir, rel) != readFile(t, fixture, rel) {
			t.Errorf("%s has nothing to rewrite but changed", rel)
		}
	}

	// Every left-alone shape is on the worklist, with its reason, and every
	// reader that keeps a package wrapped is named with how it reaches it.
	next := strings.Join(rep.Next, "\n")
	for _, want := range []string{
		`things.go:\d+: connect.NewError\(connect.CodeInternal, fmt.Errorf\("failed to push: %w", err\)\) — wraps an error value`,
		`things.go:\d+: connect.NewError\(connect.CodeInvalidArgument, err\) — wraps an error value`,
		`things.go:\d+: connect.NewError\(connect.CodeInternal, fmt.Errorf\("list failed: %v", err\)\) — wraps an error value`,
		`things.go:\d+: connect.NewError\(connect.CodeUnavailable, errors.New\(err.Error\(\)\)\) — wraps an error value`,
		`things.go:\d+: connect.NewError\(connect.CodeUnauthenticated, nil\) — nil error`,
		`things.go:\d+: connect.NewError\(connect.CodeInvalidArgument, errors.New\(msg\)\) — computed message`,
		`things.go:\d+: connect.NewError\(connect.CodeInvalidArgument, fmt.Errorf\("%s", s\)\) — computed message`,
		`things.go:\d+: connect.NewError\(connect.CodeInternal, errors.New\(""\)\) — empty message`,
		`things.go:\d+: connect.NewError\(code, errors.New\("x"\)\) — computed code`,
		`things.go:\d+: connect.NewError\(connect.CodeFailedPrecondition, errLegacy\) — wraps an error value`,
	} {
		if !regexp.MustCompile(want).MatchString(next) {
			t.Errorf("worklist is missing %s:\n%s", want, next)
		}
	}
	assertKept(t, rep.Next)

	// A re-run rewrites nothing — and still names every reader keeping a
	// package wrapped: with no edit to make, the reason is what is left.
	again, err := runConnectErrors(dir, Options{Write: true})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if again.Changed() {
		t.Errorf("second run is not a no-op:\n%s", strings.Join(again.Rewrites, "\n"))
	}
	assertKept(t, again.Next)

	// The report's advice for a kept package, followed: switch the reader in
	// internal/app to svcerr.Code and re-run. blocked's svcerr.ToConnect
	// wrapper goes; what a client (and app.Probe) reads does not change.
	retry := filepath.Join(dir, "internal", "app", "retry.go")
	src := readFile(t, dir, "internal/app/retry.go")
	src = strings.Replace(src, "\tvar ce *connect.Error\n\treturn errors.As(err, &ce) && ce.Code() == connect.CodeUnavailable",
		"\treturn svcerr.Code(err) == connect.CodeUnavailable", 1)
	src = strings.Replace(src, "import (\n\t\"errors\"\n\n\t\"connectrpc.com/connect\"\n",
		"import (\n\t\"connectrpc.com/connect\"\n\n\t\"github.com/reliant-labs/forge/pkg/svcerr\"\n", 1)
	if err := os.WriteFile(retry, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := runConnectErrors(dir, Options{Write: true})
	if err != nil {
		t.Fatalf("run after switching the reader: %v", err)
	}
	if got := strings.Join(third.Rewrites, "\n"); !regexp.MustCompile(`^internal/handlers/blocked/blocked.go:\d+: svcerr.ToConnect\(svcerr.Unavailable\("no daemon connected"\)\) → svcerr.Unavailable\("no daemon connected"\)$`).MatchString(got) {
		t.Errorf("after the reader moved to svcerr.Code, want blocked's wrapper unwrapped and nothing else, got:\n%s", got)
	}
	assertSitesOnDisk(t, dir, third, true)
	if strings.Contains(strings.Join(third.Next, "\n"), "internal/handlers/blocked: its rewrites stay") {
		t.Errorf("blocked is still reported kept:\n%s", strings.Join(third.Next, "\n"))
	}

	goCmd(t, dir, "vet", "./...")
	if after := wireLines(t, dir); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("the rewrite changed what a client reads.\nafter:\n%s\n\nbefore:\n%s", strings.Join(after, "\n"), strings.Join(before, "\n"))
	}
}

// assertKept checks the kept-package entries of a report over the corpus:
// every reader is named, with where it is and how it reaches the package.
func assertKept(t *testing.T, next []string) {
	t.Helper()
	all := strings.Join(next, "\n")
	const pfx = "example.com/cescratch/internal/handlers/"
	for pkg, readers := range map[string][]string{
		"reader":  {`internal/handlers/reader/reader.go:\d+ reader.IsMissing \(connect.CodeOf\) — in the package`},
		"blocked": {`internal/app/retry.go:17 app.Retryable \(errors.As into a \*connect.Error\) — internal/app imports it`},
		// Every reader, not the first: two in asserted's tests.
		"asserted": {
			`internal/handlers/asserted/asserted_test.go:13 asserted.codeOf \(errors.As into a \*connect.Error\) — in its own tests, which call it directly`,
			`internal/handlers/asserted/asserted_test.go:28 asserted.TestRefuseIsConnect \(type assertion to \*connect.Error\) — in its own tests`,
		},
		// An As-style helper (testify's require.ErrorAs, a suite's ErrorAs
		// method) is a reader by its **connect.Error argument alone.
		"testifyas": {`internal/handlers/testifyas/testifyas_test.go:20 testifyas.TestApprove \(errorAs into a \*connect.Error\)`},
		"suiteas":   {`internal/handlers/suiteas/suiteas_test.go:23 suiteas.TestDeny \(assertions\{…\}.ErrorAs into a \*connect.Error\)`},
		// Reached through a consumer-declared interface; nothing imports it.
		"viaiface": {`internal/lister/lister.go:37 lister.unwrap \(errors.As into a \*connect.Error\) — internal/lister calls it through the interface lister.Getter.Get at internal/lister/lister.go:28, which \*viaiface.Service implements and internal/compose/compose.go:13 hands it`},
	} {
		var entry string
		for _, n := range next {
			if strings.HasPrefix(n, pfx+pkg+": its rewrites stay *connect.Error (svcerr.ToConnect)") {
				entry = n
			}
		}
		if entry == "" {
			t.Errorf("%s is not reported kept:\n%s", pkg, all)
			continue
		}
		if got := strings.Count(entry, "\n     - "); got != len(readers) {
			t.Errorf("%s: %d readers named, want %d:\n%s", pkg, got, len(readers), entry)
		}
		for _, r := range readers {
			if !regexp.MustCompile(`\n     - ` + r).MatchString(entry) {
				t.Errorf("%s: reader %s is not named:\n%s", pkg, r, entry)
			}
		}
	}
	// A test-only importer is no edge, a Connect client's error is no
	// in-process read, and an interface only ever handed a client is no
	// seam: none keeps a package wrapped.
	for _, pkg := range []string{"testonly", "wired", "pingsvc", "things"} {
		if strings.Contains(all, pfx+pkg+": its rewrites stay") {
			t.Errorf("%s is reported kept:\n%s", pkg, all)
		}
	}
}

var siteLine = regexp.MustCompile(`^(?:left as written: )?(\S+\.go):(\d+): (.*)$`)

// withoutLines is a report's rewrites with their line numbers dropped.
func withoutLines(lines []string) string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if m := siteLine.FindStringSubmatch(l); m != nil {
			l = m[1] + ": " + m[3]
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// assertSitesOnDisk checks every file:line a report prints against the file
// on disk: a rewrite's line holds what it was rewritten to (written) or what
// it will rewrite (a dry run), a worklist line holds the call left as
// written, and a reader's line holds the read.
func assertSitesOnDisk(t *testing.T, dir string, rep Report, written bool) {
	t.Helper()
	lineOf := func(rel string, n int) string {
		lines := strings.Split(readFile(t, dir, rel), "\n")
		if n < 1 || n > len(lines) {
			return ""
		}
		return lines[n-1]
	}
	check := func(rel, n, want, from string) {
		t.Helper()
		line, _ := strconv.Atoi(n)
		if got := lineOf(rel, line); !strings.Contains(got, want) {
			t.Errorf("%s:%d is %q, want it to hold %q (from %q)", rel, line, got, want, from)
		}
	}
	for _, r := range rep.Rewrites {
		m := siteLine.FindStringSubmatch(r)
		if m == nil {
			t.Errorf("rewrite %q has no file:line", r)
			continue
		}
		from, to, _ := strings.Cut(m[3], " → ")
		if !written {
			to = from
		}
		if i := strings.Index(to, "("); i > 0 {
			to = to[:i+1]
		}
		check(m[1], m[2], to, r)
	}
	reader := regexp.MustCompile(`\n     - (\S+\.go):(\d+) \S+ \((connect.CodeOf|errors.As) `)
	for _, n := range rep.Next {
		if m := siteLine.FindStringSubmatch(n); m != nil && strings.HasPrefix(n, "left as written: ") {
			check(m[1], m[2], "connect.NewError(", n)
		}
		for _, m := range reader.FindAllStringSubmatch(n, -1) {
			check(m[1], m[2], strings.TrimPrefix(m[3], "connect.")+"(", n)
		}
	}
}

// connectErrorsScratch copies the corpus into a scratch module that
// resolves forge from this checkout, and returns the fixture and the copy.
func connectErrorsScratch(t *testing.T) (fixture, dir string) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	fixture = filepath.Join("testdata", "connecterrors")
	dir = t.TempDir()
	copyTree(t, fixture, dir)
	copyFile(t, filepath.Join(root, "go.sum"), filepath.Join(dir, "go.sum"))
	writeScratchGoMod(t, root, dir, "example.com/cescratch")
	return fixture, dir
}

// --scope bounds the writes and the report, never the analysis.
func TestConnectErrors_ScopeRewritesOnlyItsSlice(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks a scratch module against forge twice; runs in task test")
	}
	fixture, dir := connectErrorsScratch(t)
	unchanged := func(except ...string) {
		t.Helper()
		err := filepath.WalkDir(fixture, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			rel, _ := filepath.Rel(fixture, path)
			rel = filepath.ToSlash(rel)
			for _, e := range except {
				if rel == e {
					return nil
				}
			}
			if readFile(t, dir, rel) != readFile(t, fixture, rel) {
				t.Errorf("%s is outside --scope but changed", rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// A one-file scope: things.go is rewritten, but its own tests are
	// outside the scope and read connect.CodeOf, which would read a bare kind
	// as unknown — so things stays *connect.Error, and the report says why.
	scope, err := ParseScope(dir, []string{"internal/handlers/things/things.go"})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := runConnectErrors(dir, Options{Write: true, Scope: scope})
	if err != nil {
		t.Fatalf("scoped run: %v", err)
	}
	t.Logf("rewrites:\n%s\n\nnext:\n%s", strings.Join(rep.Rewrites, "\n"), strings.Join(rep.Next, "\n"))
	unchanged("internal/handlers/things/things.go")
	if got := readFile(t, dir, "internal/handlers/things/things.go"); !strings.Contains(got, "svcerr.ToConnect(") {
		t.Errorf("things.go should keep *connect.Error while its tests are out of scope:\n%s", got)
	}
	next := strings.Join(rep.Next, "\n")
	if !regexp.MustCompile(`internal/handlers/things/things_test.go:\d+ things.TestCodes \(connect.CodeOf\) — in its own tests, which call it directly, outside --scope`).MatchString(next) {
		t.Errorf("the out-of-scope test keeping things wrapped is not named:\n%s", next)
	}
	for _, r := range append(append([]string(nil), rep.Rewrites...), rep.Next...) {
		if m := siteLine.FindStringSubmatch(r); m != nil && m[1] != "internal/handlers/things/things.go" {
			t.Errorf("reported a site outside --scope: %s", r)
		}
	}
	assertSitesOnDisk(t, dir, rep, true)

	// A package scope whose importer's test is outside it: testonly gets its
	// bare kind, and reader's test — which would switch to svcerr.Code — is
	// listed, not rewritten.
	scope, err = ParseScope(dir, []string{"internal/handlers/testonly"})
	if err != nil {
		t.Fatal(err)
	}
	rep, err = runConnectErrors(dir, Options{Write: true, Scope: scope})
	if err != nil {
		t.Fatalf("scoped run: %v", err)
	}
	unchanged("internal/handlers/things/things.go", "internal/handlers/testonly/testonly.go")
	if got, want := readFile(t, dir, "internal/handlers/testonly/testonly.go"), readFile(t, fixture, "internal/handlers/testonly/testonly.go.golden"); got != want {
		t.Errorf("testonly.go differs from its golden:\n%s", got)
	}
	next = strings.Join(rep.Next, "\n")
	if !strings.Contains(next, "outside --scope, so left as written") ||
		!regexp.MustCompile(`\n     - internal/handlers/reader/reader_test.go:\d+: connect.CodeOf → svcerr.Code`).MatchString(next) {
		t.Errorf("the out-of-scope test rewrite is not listed:\n%s", next)
	}
}

func TestParseScope(t *testing.T) {
	dir := writeTree(t, map[string]string{"internal/a/a.go": "package a\n", "internal/b/b.go": "package b\n"})
	s, err := ParseScope(dir, []string{"internal/b", " internal/a/a.go ", "internal/b"})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.String(); got != "internal/a/a.go, internal/b" {
		t.Errorf("paths = %q", got)
	}
	for rel, want := range map[string]bool{
		"internal/a/a.go": true, "internal/b/b.go": true, "internal/b/x/y.go": true,
		"internal/a/a_test.go": false, "internal/bb/b.go": false, "internal/c.go": false,
	} {
		if s.Contains(rel) != want {
			t.Errorf("Contains(%q) = %v, want %v", rel, !want, want)
		}
	}
	var whole *Scope
	if !whole.Contains("anything.go") || whole.Paths() != nil {
		t.Error("a nil scope is the whole project")
	}
	for _, bad := range [][]string{{"internal/nope"}, {"../elsewhere"}, {"."}, {" "}} {
		if _, err := ParseScope(dir, bad); err == nil {
			t.Errorf("ParseScope(%q) accepted it", bad)
		}
	}
	if s, err := ParseScope(dir, nil); s != nil || err != nil {
		t.Errorf("no --scope is the whole project, got %v, %v", s, err)
	}
}

func TestLineMapper(t *testing.T) {
	before := "package p\n\nimport (\n\t\"errors\"\n)\n\nfunc A() error {\n\treturn x(\n\t\t1,\n\t)\n}\n\nfunc B() {}\n"
	after := "package p\n\nimport (\n\t\"errors\"\n\n\t\"svcerr\"\n)\n\nfunc A() error {\n\treturn y(1)\n}\n\nfunc B() {}\n"
	m := lineMapper([]byte(before), []byte(after))
	for from, want := range map[int]int{1: 1, 4: 4, 7: 9, 8: 10, 10: 10, 11: 11, 13: 13} {
		if got := m(from); got != want {
			t.Errorf("line %d → %d, want %d", from, got, want)
		}
	}
}

// wireLines runs the corpus's tests (TestCodes included, which must pass
// before and after) and returns wiretest's WIRE lines.
func wireLines(t *testing.T, dir string) []string {
	t.Helper()
	out := goCmd(t, dir, "test", "-count=1", "-v", "./...")
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "WIRE ") {
			lines = append(lines, sc.Text())
		}
	}
	return lines
}

// copyTree copies the fixture's .go sources (not its goldens) under dst.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		copyFile(t, path, to)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFormatText(t *testing.T) {
	cases := []struct {
		format string
		lit    string
		verbs  int
		hasW   bool
	}{
		{"plain", "plain", 0, false},
		{"%s", "", 1, false},
		{"%s not found", " not found", 1, false},
		{"100%% used", "100% used", 0, false},
		{"%-8.3f|%[1]d|%*d", "||", 3, false},
		{"push: %w", "push: ", 1, true},
		{"%+v", "", 1, false},
		{"trailing %", "trailing ", 1, false},
	}
	for _, tc := range cases {
		lit, verbs, hasW := formatText(tc.format)
		if lit != tc.lit || verbs != tc.verbs || hasW != tc.hasW {
			t.Errorf("formatText(%q) = %q, %d, %v; want %q, %d, %v", tc.format, lit, verbs, hasW, tc.lit, tc.verbs, tc.hasW)
		}
	}
}

func TestIsHandlerFile(t *testing.T) {
	for rel, want := range map[string]bool{
		"internal/handlers/chat/chat.go":  true,
		"handlers/x.go":                   true,
		"internal/handlersx/x.go":         false,
		"internal/grpc/interceptors/a.go": false,
		"main.go":                         false,
	} {
		if got := isHandlerFile(rel); got != want {
			t.Errorf("isHandlerFile(%q) = %v, want %v", rel, got, want)
		}
	}
}

// A project that never imports connect is not loaded at all.
func TestConnectErrors_NothingToDoWithoutConnect(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"go.mod":  "module example.com/plain\n\ngo 1.27.0\n",
		"main.go": "package main\n\nimport \"errors\"\n\nvar ErrX = errors.New(\"NewError CodeOf\")\n",
	})
	rep, err := runConnectErrors(dir, Options{Write: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Changed() || len(rep.Next) != 0 {
		t.Errorf("rewrote a project that does not use connect: %+v", rep)
	}
}
