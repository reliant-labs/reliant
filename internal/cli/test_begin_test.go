package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/testcapture"
	"github.com/reliant-labs/forge/pkg/testreport"
)

func runTestBegin(t *testing.T) (string, string) {
	t.Helper()
	cmd := newTestBeginCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("forge test begin: %v", err)
	}
	return strings.TrimSpace(out.String()), errb.String()
}

func TestTestBeginPrintsTheRunFORGE_TEST_RUNNames(t *testing.T) {
	run := t.TempDir()
	if err := os.WriteFile(filepath.Join(run, testreport.RunManifestFile), []byte(`{"schema":"forge.test.run/v1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(testcapture.EnvTests, "")
	t.Setenv(testcapture.EnvRun, run)
	if got, _ := runTestBegin(t); got != run {
		t.Fatalf("forge test begin printed %q, want the run %s names: %s", got, testcapture.EnvRun, run)
	}
}

func TestTestBeginWithTestsOffPrintsAScratchDirectory(t *testing.T) {
	t.Setenv(testcapture.EnvRun, "")
	t.Setenv(testcapture.EnvTests, "off")
	got, _ := runTestBegin(t)
	t.Cleanup(func() { _ = os.RemoveAll(got) })
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Fatalf("forge test begin printed %q (%v), want a directory a lane can write into", got, err)
	}
	if _, err := os.Stat(filepath.Join(got, testreport.RunManifestFile)); !os.IsNotExist(err) {
		t.Fatalf("FORGE_TESTS=off wrote a run: %v", err)
	}
}
