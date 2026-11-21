package testretry

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunnerRetriesOnlyTheUnchangedFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns Git and Go against an isolated module; runs in task test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("requires the same POSIX shell fixture as the merge runners")
	}
	for _, program := range []string{"git", "go"} {
		if _, err := exec.LookPath(program); err != nil {
			t.Skipf("%s unavailable: %v", program, err)
		}
	}
	root := t.TempDir()
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-trimpath")
	t.Setenv("GOEXPERIMENT", "")
	t.Setenv("GOCOVERDIR", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.test")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.test")
	callGit := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fixture := `package f
import("os";"testing")
func TestOriginal(t *testing.T) {
    f, err := os.OpenFile(os.Getenv("ORIGINAL_FILE"),os.O_CREATE|os.O_EXCL,0600)
    if err == nil { _=f.Close(); t.Fatalf("the initial observed failure") }
}
`
	outside := t.TempDir()
	t.Setenv("ORIGINAL_FILE", filepath.Join(outside, "original"))
	write("go.mod", "module example.com/fixture\n\ngo 1.27.0\n")
	const testDir = "internal/pkg"
	if err := os.MkdirAll(filepath.Join(root, testDir, "neighbor"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(testDir+"/f_test.go", fixture)
	// A recursive object read must not confuse a subpackage's same-named
	// test with the exact parent's package identity.
	write(testDir+"/neighbor/n_test.go", "package n\nimport \"testing\"\nfunc TestOriginal(t *testing.T) {}\n")
	write("r_test.go", "package f\nimport \"testing\"\nfunc TestRoot(t *testing.T) {}\n")
	for dir, source := range map[string]string{
		"interaction": `package context
import "testing"
var initialized bool
func TestSetup(t *testing.T) { initialized = true }
func TestOriginal(t *testing.T) {
    if initialized { t.Fatal("fails only after its sibling runs") }
}
`,
		"testmain": `package context_test
import ("flag"; "os"; "testing")
var ordinary bool
func TestMain(m *testing.M) {
    flag.Parse()
    ordinary = flag.Lookup("test.run").Value.String() == ""
    os.Exit(m.Run())
}
func TestOriginal(t *testing.T) {
    if ordinary { t.Fatal("fails with the original TestMain setup") }
}
`,
	} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
		write(dir+"/context_test.go", source)
	}
	callGit("init", "-qb", "main")
	callGit("add", ".")
	callGit("commit", "-qm", "base with unchanged test")
	base := callGit("rev-parse", "HEAD")
	callGit("commit", "-q", "--allow-empty", "-m", "candidate")
	var diagnostic bytes.Buffer
	cfg := Config{Root: root, Repository: "acme/lib", Base: base, Head: callGit("rev-parse", "HEAD"),
		RunID: "gate-123", PR: 12, Timeout: time.Minute, Packages: []string{"./" + testDir}, Stderr: &diagnostic,
		JSONPath: filepath.Join(outside, "initial.ndjson"), ReportPath: filepath.Join(outside, "result.json")}
	gitExe, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	rootSource, err := locate(t.Context(), cfg, gitExe, "example.com/fixture", base, identity{"example.com/fixture", "TestRoot"})
	if err != nil || rootSource.file != "r_test.go" {
		t.Fatalf("root package source lookup: %+v, %v", rootSource, err)
	}
	if err := Run(t.Context(), cfg); err != nil {
		t.Fatalf("unexpected red: %v\n%s", err, &diagnostic)
	}
	content, err := os.ReadFile(cfg.ReportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report Report
	if err := json.Unmarshal(content, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Tests) != 1 || report.Tests[0].Test != "TestOriginal" || report.Tests[0].SourceBlob != callGit("rev-parse", "HEAD:"+testDir+"/f_test.go") ||
		report.Head != cfg.Head || report.Base != cfg.Base || report.Tree != callGit("rev-parse", "HEAD^{tree}") {
		t.Fatalf("unbound or unexpected report: %s", content)
	}
	if !strings.Contains(diagnostic.String(), "retried:TestOriginal") || strings.Contains(diagnostic.String(), "retried:TestStable") {
		t.Fatalf("wrong retry set: %s", &diagnostic)
	}
	if _, err := Verify(t.Context(), cfg, report.Tree, "example.com/fixture"); err != nil {
		t.Fatalf("authentic proof refused by offline verification: %v", err)
	}
	wrongRun := cfg
	wrongRun.RunID = "other-job"
	if _, err := Verify(t.Context(), wrongRun, report.Tree, "example.com/fixture"); err == nil {
		t.Fatal("report replayed under another run")
	}
	report.Tests[0].File = "../../another_test.go"
	tampered, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.ReportPath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(t.Context(), cfg, report.Tree, "example.com/fixture"); err == nil {
		t.Fatal("tampered deletion path was accepted")
	}

	// The same initially green-able behavior must remain red when the exact
	// source changed in the candidate, even though a second run would pass.
	write(testDir+"/f_test.go", fixture+"\n// candidate edited the test file\n")
	callGit("add", testDir+"/f_test.go")
	callGit("commit", "-qm", "edited test source")
	if err := os.Remove(filepath.Join(outside, "original")); err != nil {
		t.Fatal(err)
	}
	cfg.Head = callGit("rev-parse", "HEAD")
	cfg.JSONPath += ".changed"
	cfg.ReportPath += ".changed"
	diagnostic.Reset()
	if err := Run(t.Context(), cfg); err == nil || !strings.Contains(err.Error(), "test source is not unchanged") {
		t.Fatalf("edited test did not stay red: %v\n%s", err, &diagnostic)
	}
	if strings.Contains(diagnostic.String(), "checking") {
		t.Fatalf("edited test was retried: %s", &diagnostic)
	}
	if _, err := os.Stat(cfg.ReportPath); !os.IsNotExist(err) {
		t.Fatalf("unexpected changed-test report: %v", err)
	}
	for _, tc := range []struct{ dir, reason string }{
		{"interaction", "original test selection"}, {"testmain", "custom TestMain"},
	} {
		t.Run(tc.dir, func(t *testing.T) {
			cfg.Packages = []string{"./" + tc.dir}
			cfg.JSONPath = filepath.Join(outside, tc.dir+".ndjson")
			cfg.ReportPath = filepath.Join(outside, tc.dir+".json")
			diagnostic.Reset()
			if err := Run(t.Context(), cfg); err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("unsafe context did not stay red (%s): %v\n%s", tc.reason, err, &diagnostic)
			}
			if strings.Contains(diagnostic.String(), "checking") {
				t.Fatalf("ineligible test was retried: %s", &diagnostic)
			}
			if _, err := os.Stat(cfg.ReportPath); !os.IsNotExist(err) {
				t.Fatalf("ineligible retry left a report: %v", err)
			}
		})
	}
}
