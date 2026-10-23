package storage

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeKubectl installs a `kubectl` that prints a deprecation warning to STDERR
// and JSON to STDOUT, which is exactly what kubectl 1.36 does for
// `get componentstatuses,…`. The JSON depends on the verb so a full
// protected-set scan can run against it.
func fakeKubectl(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake kubectl is a POSIX shell script")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
echo "Warning: v1 ComponentStatus is deprecated in v1.19+" >&2
for a in "$@"; do
  if [ "$a" = "api-resources" ]; then printf 'componentstatuses\npods\n'; exit 0; fi
done
printf '{"items":[{"spec":{"containers":[{"image":"localhost:5051/app:live"}]}}]}\n'
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestProtectedSetScanIgnoresKubectlWarnings is the B3 parse defect. Exec used
// CombinedOutput, so kubectl's stderr warning was prepended to the JSON on
// stdout, the scan failed to parse it, and registry GC refused on every pass
// — fail-closed, but permanently off. Measured on k3d (k3s v1.36): batch 1 of
// 4 failed with "PARSE FAIL … head: Warning: v1 ComponentStatus is
// deprecated".
func TestProtectedSetScanIgnoresKubectlWarnings(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	fakeKubectl(t)
	refs, err := (Runner{}).clusterReferences(context.Background(), "k3d-test")
	if err != nil {
		t.Fatalf("a kubectl warning on stderr broke the protected-set scan: %v", err)
	}
	found := false
	for _, ref := range refs {
		if ref == "localhost:5051/app:live" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the live image reference was not found: %v", refs)
	}
}

// TestExecKeepsStderrForErrors: stdout is the only result, but a failing
// command must still say why.
func TestExecKeepsStderrForErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	out, err := Exec(context.Background(), "sh", "-c", "echo result; echo noise >&2")
	if err != nil || strings.TrimSpace(string(out)) != "result" {
		t.Fatalf("Exec = %q, %v; want stdout only", out, err)
	}
	_, err = Exec(context.Background(), "sh", "-c", "echo partial; echo 'the real reason' >&2; exit 3")
	if err == nil || !strings.Contains(err.Error(), "the real reason") {
		t.Fatalf("a failing command's stderr is missing from its error: %v", err)
	}
}
