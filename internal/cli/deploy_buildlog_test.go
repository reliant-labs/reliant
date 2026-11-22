package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildlog"
)

// A writing deploy runs its build steps quietly; --verbose and the previews
// stream as they always did.
func TestBeginDeployBuildLog(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, f := range []deployCmdFlags{{verbose: true}, {dryRun: true}, {explain: true}, {promote: promoteCmdFlags{plan: true}}, {promote: promoteCmdFlags{planOnly: true}}} {
		if s := beginDeployBuildLog("prod", f); s != nil {
			_ = s.End()
			t.Errorf("%+v started a quiet session; it must stream", f)
		}
	}
	s := beginDeployBuildLog("prod", deployCmdFlags{})
	if s == nil {
		t.Fatal("a writing deploy started no quiet session")
	}
	defer func() { _ = s.End() }()
	if buildlog.Active() != s {
		t.Error("the session is not the active one: build steps would still stream")
	}
	if !strings.HasSuffix(s.Path(), filepath.Join(".forge", "logs", "prod", "deploy.log")) {
		t.Errorf("log path = %s, want .forge/logs/prod/deploy.log", s.Path())
	}
}

// Under --json stdout carries one JSON document: a writing deploy's step
// lines, success or failure, go to stderr and never to stdout, even with
// os.Stdout not redirected.
func TestBeginDeployBuildLogJSONKeepsStdoutForTheDocument(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	stdout, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	realStdout, realStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdout, stderr
	// Close both files before t.TempDir's cleanup removes them (cleanups
	// run last-registered first): Windows cannot delete an open file.
	t.Cleanup(func() {
		os.Stdout, os.Stderr = realStdout, realStderr
		_ = stdout.Close()
		_ = stderr.Close()
	})

	s := beginDeployBuildLog("prod", deployCmdFlags{jsonOut: true})
	if s == nil {
		t.Fatal("a writing --json deploy started no quiet session")
	}
	// End is idempotent; this closes deploy.log even when the test fails
	// before its own End.
	t.Cleanup(func() { _ = s.End() })
	for i, stepErr := range []error{nil, errors.New("exit status 1")} {
		w, _, end := buildlog.Step(fmt.Sprintf("step %d", i))
		_, _ = fmt.Fprintln(w, "some build output")
		end(stepErr)
	}
	if err := s.End(); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONDocument(stdout, map[string]string{"mode": "apply"}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, got)
	}
	progress, err := os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Build steps", "✓ step 0", "✗ step 1 failed"} {
		if !strings.Contains(string(progress), want) {
			t.Errorf("stderr lacks %q:\n%s", want, progress)
		}
	}
}
