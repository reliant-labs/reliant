package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

// goMemoFixture is a two-module project: the root requires ./gen through a
// local replace, and frontends/ holds files no Go build reads.
func goMemoFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":              "module example.com/app\n\ngo 1.22\n\nrequire example.com/app/gen v0.0.0\n\nreplace example.com/app/gen => ./gen\n",
		"go.sum":              "",
		"internal/a/a.go":     "package a\n\nimport \"fmt\"\n\nfunc A() { fmt.Println(\"a\") }\n",
		"internal/a/data.txt": "embedded\n",
		"db/embed.go":         "package db\n\nimport \"embed\"\n\n//go:embed migrations\nvar FS embed.FS\n",
		"db/migrations/1.sql": "create table t (id int);\n",
		"gen/go.mod":          "module example.com/app/gen\n\ngo 1.22\n",
		"gen/v1/x.pb.go":      "package v1\n\nimport \"strings\"\n\nvar X = strings.ToUpper(\"x\")\n",
		"frontends/web/a.ts":  "export const a = 1;\n",
		"other/go.mod":        "module example.com/other\n\ngo 1.22\n",
		"other/o.go":          "package other\n\nimport \"os\"\n\nvar _ = os.Args\n",
	}
	for rel, content := range files {
		writeTestFile(t, filepath.Join(root, rel), content)
	}
	return root
}

// fakeGoMemoSys resolves "go" to a file in the fixture's own temp space, so
// keys never depend on the machine's toolchain.
func fakeGoMemoSys(t *testing.T) generateCacheSys {
	t.Helper()
	goBin := filepath.Join(t.TempDir(), "go")
	writeTestFile(t, goBin, "fake go toolchain v1")
	return generateCacheSys{
		lookPath: func(name string) (string, error) {
			if name == "go" {
				return goBin, nil
			}
			return "", errors.New("not found")
		},
		now:          time.Now,
		runningForge: "sha256:test-forge",
	}
}

// TestGoMemoKeysTheRunningForge: a forge upgrade can change what a tool's
// result means (which test-file diagnostics fail validate), so its records
// are not the next forge's.
func TestGoMemoKeysTheRunningForge(t *testing.T) {
	root := goMemoFixture(t)
	keys := func(forgeID string) (string, goTidyKey) {
		t.Helper()
		sys := fakeGoMemoSys(t)
		sys.runningForge = forgeID
		end := beginGenerateMemo(root, pipelineFlags{Cache: true}, sys)
		defer end()
		m := goMemoFor(root)
		v, ok1 := m.goValidateMemoKey(nil)
		tk, ok2 := m.goTidyMemoKey(root, nil)
		if !ok1 || !ok2 {
			t.Fatal("the fixture could not be keyed")
		}
		return v, tk
	}
	v1, t1 := keys("sha256:forge-one")
	v2, t2 := keys("sha256:forge-two")
	if v1 == v2 {
		t.Error("another forge binary got the same validate key")
	}
	if t1.full == t2.full {
		t.Error("another forge binary got the same tidy key")
	}
}

func newTestGoMemo(t *testing.T, root string, flags pipelineFlags) *generateMemo {
	t.Helper()
	end := beginGenerateMemo(root, flags, fakeGoMemoSys(t))
	t.Cleanup(end)
	m := goMemoFor(root)
	if m == nil {
		t.Fatalf("no memo is live for %s", root)
	}
	return m
}

func editMemoFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, rel), content)
}

func TestGoTidyMemoKeyFollowsImportsNotBodies(t *testing.T) {
	root := goMemoFixture(t)
	m := newTestGoMemo(t, root, pipelineFlags{Cache: true})
	key := func() goTidyKey {
		t.Helper()
		k, ok := m.goTidyMemoKey(root, nil)
		if !ok {
			t.Fatal("the fixture's tidy inputs could not be keyed")
		}
		return k
	}
	base := key()
	if again := key(); again != base {
		t.Fatalf("the key is not stable: %+v then %+v", base, again)
	}

	editMemoFixture(t, root, "internal/a/a.go", "package a\n\nimport \"fmt\"\n\nfunc A() { fmt.Println(\"a body edit\") }\n")
	if got := key(); got != base {
		t.Error("a function-body edit changed the tidy key; tidy reads only imports")
	}
	editMemoFixture(t, root, "internal/a/a.go", "package a\n\nimport (\n\tf \"fmt\"\n)\n\nfunc A() { f.Println(\"a\") }\n")
	if got := key(); got != base {
		t.Error("regrouping and aliasing an import (what goimports does to codegen output) changed the tidy key")
	}
	editMemoFixture(t, root, "frontends/web/a.ts", "export const a = 2;\n")
	editMemoFixture(t, root, "other/o.go", "package other\n\nimport \"net/http\"\n\nvar _ = http.Get\n")
	if got := key(); got != base {
		t.Error("a file outside the module (a frontend, a module nothing replaces to) changed the tidy key")
	}

	editMemoFixture(t, root, "internal/a/a.go", "package a\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc A() { fmt.Println(os.Args) }\n")
	withImport := key()
	if withImport.full == base.full || withImport.sources == base.sources {
		t.Error("a new import did not change the tidy key")
	}

	editMemoFixture(t, root, "gen/v1/x.pb.go", "package v1\n\nimport \"bytes\"\n\nvar X = bytes.ToUpper([]byte(\"x\"))\n")
	withGenImport := key()
	if withGenImport.full == withImport.full {
		t.Error("an import in the local replace target ./gen did not change the root's tidy key")
	}

	editMemoFixture(t, root, "go.sum", "example.com/x v1.0.0 h1:abc=\n")
	modEdit := key()
	if modEdit.full == withGenImport.full {
		t.Error("a go.sum edit did not change the full tidy key")
	}
	if modEdit.sources != withGenImport.sources {
		t.Error("a go.sum edit changed the sources digest; it must cover only what tidy does not rewrite")
	}

	editMemoFixture(t, root, "internal/a/a_linux.go", "//go:build linux\n\npackage a\n")
	if got := key(); got.full == modEdit.full {
		t.Error("a new build-constrained file did not change the tidy key")
	}
}

func TestGoValidateMemoKeyCoversWhatABuildReads(t *testing.T) {
	root := goMemoFixture(t)
	m := newTestGoMemo(t, root, pipelineFlags{Cache: true})
	key := func() string {
		t.Helper()
		k, ok := m.goValidateMemoKey(nil)
		if !ok {
			t.Fatal("the fixture's build inputs could not be keyed")
		}
		return k
	}
	prev := key()
	if again := key(); again != prev {
		t.Fatal("the key is not stable")
	}

	editMemoFixture(t, root, "frontends/web/a.ts", "export const a = 3;\n")
	if got := key(); got != prev {
		t.Error("a file under no Go package changed the validate key")
	}

	for rel, content := range map[string]string{
		"internal/a/a.go":        "package a\n\nfunc A() {}\n",              // a body edit compiles differently
		"internal/a/data.txt":    "edited\n",                                // a package can embed any file in its directory
		"db/migrations/1.sql":    "-- edited\n",                             // ... or in a subdirectory
		"gen/go.mod":             "module example.com/app/gen\n\ngo 1.23\n", // module files count wherever they sit
		"internal/a/new_test.go": "package a\n",                             // a new test file is compiled by -test
	} {
		editMemoFixture(t, root, rel, content)
		got := key()
		if got == prev {
			t.Errorf("editing %s did not change the validate key", rel)
		}
		prev = got
	}

	if err := os.Remove(filepath.Join(root, "internal/a/data.txt")); err != nil {
		t.Fatal(err)
	}
	if got := key(); got == prev {
		t.Error("deleting a file under a package did not change the validate key")
	}
	prev = key()

	// `//go:embed all:assets` reaches every descendant of the package's
	// directory, however deep, including directories that hold no .go file.
	// It reaches nothing else: Go refuses a pattern with `..` ("invalid
	// pattern syntax") and a file in another module. So a directory with no Go
	// file is an embed input exactly when it sits below a package.
	const deep = "internal/a/assets/img/icons/logo.svg"
	editMemoFixture(t, root, deep, "<svg/>\n")
	if got := key(); got == prev {
		t.Errorf("creating %s, deep under a package, did not change the validate key", deep)
	}
	prev = key()
	editMemoFixture(t, root, deep, "<svg viewBox=\"0 0 1 1\"/>\n")
	if got := key(); got == prev {
		t.Errorf("editing %s, deep under a package, did not change the validate key", deep)
	}
}

func TestGoValidateMemoKeyRefusesWhatItCannotEnumerate(t *testing.T) {
	t.Run("cgo", func(t *testing.T) {
		root := goMemoFixture(t)
		editMemoFixture(t, root, "internal/c/c.go", "package c\n\n// #include \"../../../outside.h\"\nimport \"C\"\n")
		m := newTestGoMemo(t, root, pipelineFlags{Cache: true})
		if _, ok := m.goValidateMemoKey(nil); ok {
			t.Error("a cgo package was keyed; its #includes can read outside the tree")
		}
	})
	t.Run("symlinked directory", func(t *testing.T) {
		root := goMemoFixture(t)
		outside := t.TempDir()
		writeTestFile(t, filepath.Join(outside, "l.go"), "package l\n")
		if err := os.Symlink(outside, filepath.Join(root, "internal", "linked")); err != nil {
			if runtime.GOOS == "windows" {
				// Unprivileged Windows accounts cannot create symlinks.
				t.Skipf("symlink privilege unavailable on this host: %v", err)
			}
			t.Fatal(err)
		}
		m := newTestGoMemo(t, root, pipelineFlags{Cache: true})
		if _, ok := m.goValidateMemoKey(nil); ok {
			t.Error("a tree with a symlinked directory was keyed")
		}
	})
}

// TestMemoizedGoTidyRecordsWhatTidyLeft drives the wrapper the tidy steps
// use: the record is the post-tidy state, it is consulted on the next call,
// and nothing is recorded for a failed tidy or one that raced an edit.
func TestMemoizedGoTidyRecordsWhatTidyLeft(t *testing.T) {
	root := goMemoFixture(t)
	newTestGoMemo(t, root, pipelineFlags{Cache: true})
	runs := 0
	tidy := func() error {
		runs++
		// What a real tidy does: rewrite go.sum.
		editMemoFixture(t, root, "go.sum", "tidied by run "+string(rune('0'+runs))+"\n")
		return nil
	}
	call := func(fn func() error) error { return memoizedGoTidy(root, "tidy:.", "skipped", fn) }

	if err := call(tidy); err != nil || runs != 1 {
		t.Fatalf("first call: err=%v runs=%d, want one run", err, runs)
	}
	if err := call(tidy); err != nil || runs != 1 {
		t.Fatalf("second call over what the first left: runs=%d, want the skip", runs)
	}
	editMemoFixture(t, root, "internal/a/a.go", "package a\n\nimport \"strings\"\n\nvar A = strings.ToUpper\n")
	if err := call(tidy); err != nil || runs != 2 {
		t.Fatalf("after an import edit: runs=%d, want a run", runs)
	}

	editMemoFixture(t, root, "internal/a/a.go", "package a\n\nimport \"bytes\"\n\nvar A = bytes.ToUpper\n")
	failing := func() error { runs++; return errors.New("tidy failed") }
	if err := call(failing); err == nil {
		t.Fatal("a failed tidy's error was swallowed")
	}
	if err := call(tidy); err != nil || runs != 4 {
		t.Fatalf("after a failed tidy: runs=%d, want a run (a failure records nothing)", runs)
	}

	editMemoFixture(t, root, "internal/a/a.go", "package a\n\nimport \"errors\"\n\nvar A = errors.New\n")
	racing := func() error {
		runs++
		// An import lands while tidy runs: tidy never saw it.
		editMemoFixture(t, root, "internal/a/b.go", "package a\n\nimport \"io\"\n\nvar B = io.EOF\n")
		return nil
	}
	if err := call(racing); err != nil {
		t.Fatal(err)
	}
	if err := call(tidy); err != nil || runs != 6 {
		t.Fatalf("after a tidy that raced an import edit: runs=%d, want a run", runs)
	}
}

func TestMemoizedGoValidateRecordsOnlyAPass(t *testing.T) {
	root := goMemoFixture(t)
	m := newTestGoMemo(t, root, pipelineFlags{Cache: true})
	runs := 0
	pass := func() error { runs++; return nil }
	fail := func() error { runs++; return errors.New("does not compile") }

	if err := memoizedGoValidate(root, fail); err == nil || runs != 1 {
		t.Fatalf("a failing validate: err=%v runs=%d", err, runs)
	}
	if err := memoizedGoValidate(root, pass); err != nil || runs != 2 {
		t.Fatalf("after a failure: runs=%d, want a run", runs)
	}
	if err := memoizedGoValidate(root, pass); err != nil || runs != 2 {
		t.Fatalf("over the inputs that passed: runs=%d, want the skip", runs)
	}
	if got := m.skipped; len(got) != 1 || got[0] != "validate" {
		t.Errorf("skipped = %v, want [validate]", got)
	}
	editMemoFixture(t, root, "internal/a/a.go", "package a\n")
	if err := memoizedGoValidate(root, pass); err != nil || runs != 3 {
		t.Fatalf("after an edit: runs=%d, want a run", runs)
	}
}

// TestMemoizedGoValidateRerunsWhenPackageSelectionMoves: build tags, the
// target platform and cgo change which files `go list -test ./...` compiles
// without changing a byte of the tree, so each must miss the record a pass
// left, through the wrapper the pipeline calls (which reads the process env).
func TestMemoizedGoValidateRerunsWhenPackageSelectionMoves(t *testing.T) {
	root := goMemoFixture(t)
	editMemoFixture(t, root, "internal/a/a_tagged.go", "//go:build extra\n\npackage a\n\nvar Broken = undefinedName\n")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOOS", runtime.GOOS)
	t.Setenv("CGO_ENABLED", "0")
	t.Setenv("CC", "")
	m := newTestGoMemo(t, root, pipelineFlags{Cache: true})
	cc := ""
	goLookPath := m.sys.lookPath
	m.sys.lookPath = func(name string) (string, error) {
		if name == "go" {
			return goLookPath("go")
		}
		if name == cc {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	runs := 0
	pass := func() error { runs++; return nil }
	if err := memoizedGoValidate(root, pass); err != nil || runs != 1 {
		t.Fatalf("first validate: err=%v runs=%d", err, runs)
	}
	if err := memoizedGoValidate(root, pass); err != nil || runs != 1 {
		t.Fatalf("same selection: runs=%d, want the skip", runs)
	}
	for _, step := range []struct {
		what string
		move func()
	}{
		{"GOFLAGS set to -tags=extra", func() { t.Setenv("GOFLAGS", "-tags=extra") }},
		{"another GOOS", func() { t.Setenv("GOOS", "plan9") }},
		{"CGO_ENABLED unset", func() { _ = os.Unsetenv("CGO_ENABLED") }},
		{"a C compiler appearing on PATH", func() { cc = "gcc" }},
		{"CC naming another compiler", func() { t.Setenv("CC", "clang") }},
	} {
		before := runs
		step.move()
		if err := memoizedGoValidate(root, pass); err != nil || runs != before+1 {
			t.Errorf("%s: runs=%d, want a run (it selects other files from the same tree)", step.what, runs)
		}
		if err := memoizedGoValidate(root, pass); err != nil || runs != before+1 {
			t.Errorf("%s, again: runs=%d, want the skip", step.what, runs)
		}
	}
}

// TestGoValidateMemoKeyFollowsWorkspaceSelection: GOWORK chooses which
// go.work the build uses, and two with the same bytes in different places
// resolve their relative use paths to different modules, so moving the
// selection must change the key even when the contents match. A relative
// GOWORK, whose meaning depends on the working directory, is not keyed.
func TestGoValidateMemoKeyFollowsWorkspaceSelection(t *testing.T) {
	root := goMemoFixture(t)
	m := newTestGoMemo(t, root, pipelineFlags{Cache: true})
	work := "go 1.22\n\nuse ./mod\n"
	var works []string
	for _, dir := range []string{t.TempDir(), t.TempDir()} {
		writeTestFile(t, filepath.Join(dir, "go.work"), work)
		writeTestFile(t, filepath.Join(dir, "mod", "go.mod"), "module example.com/mod\n\ngo 1.22\n")
		works = append(works, filepath.Join(dir, "go.work"))
	}
	key := func(env ...string) string {
		t.Helper()
		k, ok := m.goValidateMemoKey(env)
		if !ok {
			t.Fatalf("env %v could not be keyed", env)
		}
		return k
	}
	none := key()
	first := key("GOWORK=" + works[0])
	second := key("GOWORK=" + works[1])
	if first == none || first == second {
		t.Error("selecting another go.work with the same contents kept the key")
	}
	if key("GOWORK=off") == first {
		t.Error("GOWORK=off kept the key of a selected workspace")
	}
	if _, ok := m.goValidateMemoKey([]string{"GOWORK=go.work"}); ok {
		t.Error("a relative GOWORK was keyed; it resolves against the working directory")
	}
}

// TestGoMemoKeysConcurrently: the tidy and validate wrappers may key and
// record from several goroutines; the shared identity cache and the record
// are touched only under the memo's lock (run under -race to see it).
func TestGoMemoKeysConcurrently(t *testing.T) {
	root := goMemoFixture(t)
	m := newTestGoMemo(t, root, pipelineFlags{Cache: true})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if k, ok := m.goValidateMemoKey(nil); ok {
				m.store("validate", k)
			}
			if k, ok := m.goTidyMemoKey(root, nil); ok {
				m.store("tidy:"+strconv.Itoa(i), k.full)
			}
			_ = m.hit("validate", "x")
		}(i)
	}
	wg.Wait()
	if len(m.file.Entries["validate"]) != 1 {
		t.Errorf("validate record = %v, want the one key every goroutine computed", m.file.Entries["validate"])
	}
}

// TestGoMemoKeepsRecentStates: a run tidies at three points whose imports
// differ (codegen's, then goimports'), and the next run must match each one,
// not only the last; the record stays bounded.
func TestGoMemoKeepsRecentStates(t *testing.T) {
	root := goMemoFixture(t)
	m := newTestGoMemo(t, root, pipelineFlags{Cache: true})
	runs := 0
	pass := func() error { runs++; return nil }
	stateA := "package a\n\nimport \"fmt\"\n\nvar A = fmt.Sprint\n"
	stateB := "package a\n\nimport \"os\"\n\nvar A = os.Args\n"
	for i, content := range []string{stateA, stateB, stateA, stateB} {
		editMemoFixture(t, root, "internal/a/a.go", content)
		if err := memoizedGoValidate(root, pass); err != nil {
			t.Fatal(err)
		}
		if want := min(i+1, 2); runs != want {
			t.Fatalf("call %d: runs=%d, want %d (both states recorded after their first run)", i, runs, want)
		}
	}
	for i := 0; i < 2*generateMemoKeep; i++ {
		editMemoFixture(t, root, "internal/a/n.go", "package a\n\nconst N = "+string(rune('a'+i))+"\n")
		_ = memoizedGoValidate(root, pass)
	}
	if got := len(m.file.Entries["validate"]); got != generateMemoKeep {
		t.Errorf("the record holds %d states, want the %d most recent", got, generateMemoKeep)
	}
}

func TestGoMemoHonorsTheCacheMode(t *testing.T) {
	root := goMemoFixture(t)
	runs := 0
	pass := func() error { runs++; return nil }
	end := beginGenerateMemo(root, pipelineFlags{Cache: true}, fakeGoMemoSys(t))
	_ = memoizedGoValidate(root, pass)
	end()

	t.Run("--no-cache runs and records", func(t *testing.T) {
		newTestGoMemo(t, root, pipelineFlags{Cache: true, NoCache: true})
		before := runs
		if err := memoizedGoValidate(root, pass); err != nil || runs != before+1 {
			t.Fatalf("--no-cache skipped the validate build")
		}
	})
	t.Run("a narrowed run has no memo", func(t *testing.T) {
		end := beginGenerateMemo(root, pipelineFlags{Cache: true, Steps: "harness"}, fakeGoMemoSys(t))
		defer end()
		if goMemoFor(root) != nil {
			t.Fatal("--steps opened a memo")
		}
	})
	t.Run("no memo outside a pipeline run", func(t *testing.T) {
		if goMemoFor(root) != nil {
			t.Fatal("a memo is live with no pipeline running")
		}
		before := runs
		if err := memoizedGoValidate(root, pass); err != nil || runs != before+1 {
			t.Fatal("the validate build was skipped outside a pipeline run")
		}
	})
	t.Run("another project's memo is not this one's", func(t *testing.T) {
		newTestGoMemo(t, t.TempDir(), pipelineFlags{Cache: true})
		if goMemoFor(root) != nil {
			t.Fatal("a memo opened for another root answered for this one")
		}
	})
}

func TestGoMemoIsNotAGenerateInput(t *testing.T) {
	if !generateCacheIgnoredState[filepath.Base(generateMemoRel)] {
		t.Fatalf("%s is in the whole-run fingerprint: rewriting it mid-run would read as the tree moving", generateMemoRel)
	}
}

// TestGitListingsPrefetchedAreTakenOnce pins the prefetch's freshness rule:
// the first fingerprint inside the bracket takes the prefetched listing, any
// later call lists afresh, and nothing outlives the bracket.
func TestGitListingsPrefetchedAreTakenOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("execs git; runs in task test")
	}
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	writeTestFile(t, filepath.Join(root, ".gitignore"), "*.log\n")
	writeTestFile(t, filepath.Join(root, "a.log"), "a")
	var first, second []string
	withGitListingsPrefetched(root, func() {
		first = gitIgnoredFiles(root)
		writeTestFile(t, filepath.Join(root, "b.log"), "b")
		second = gitIgnoredFiles(root)
		if _, ok := prefetchedGitListing(root, "submodules"); !ok {
			t.Error("the gitlink listing was not prefetched")
		}
	})
	if len(first) != 1 || first[0] != "a.log" {
		t.Errorf("prefetched listing = %v, want [a.log]", first)
	}
	if len(second) != 2 {
		t.Errorf("second listing = %v: a prefetched listing was served twice", second)
	}
	if _, ok := prefetchedGitListing(root, "ignored"); ok {
		t.Error("a prefetched listing outlived its bracket")
	}
}

func TestGoImportHeader(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "x.go")
	src := "//go:build linux\n\n// Package x.\npackage x\n\nimport (\n\t\"fmt\"\n)\n\nfunc F() { fmt.Println() }\n"
	writeTestFile(t, file, src)
	got, err := goImportHeader(file)
	if err != nil {
		t.Fatal(err)
	}
	want := "//go:build linux\npackage x\nimport \"fmt\"\n"
	if string(got) != want {
		t.Errorf("header = %q, want %q", got, want)
	}
	// goimports regroups and aliases; tidy reads neither.
	writeTestFile(t, file, "//go:build linux\n\npackage x\n\nimport f \"fmt\"\nimport \"fmt\"\n\nfunc F() { f.Println() }\n")
	if got, _ := goImportHeader(file); string(got) != want {
		t.Errorf("regrouped imports: header = %q, want %q", got, want)
	}
	writeTestFile(t, file, "package x\n\nimport (\n\t\"os\"\n\t\"fmt\"\n)\n\nfunc F( {\n")
	if got, err := goImportHeader(file); err != nil || string(got) != "package x\nimport \"fmt\"\nimport \"os\"\n" {
		t.Errorf("a body syntax error past the imports: header = %q, %v", got, err)
	}
	writeTestFile(t, file, "packag x\n")
	if got, _ := goImportHeader(file); string(got) != "packag x\n" {
		t.Errorf("an unparsable header must be keyed whole, got %q", got)
	}
}
