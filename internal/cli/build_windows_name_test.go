package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A windows-targeted host build must land at bin/<name>.exe — the path
// hostlaunch's binary/delve runners execute.
func TestBuildGoTargetWindowsOutputIsExe(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real cross-compile")
	}
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.21\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))
	t.Chdir(dir)

	res := buildGoTarget(context.Background(), goBuildTarget{cmd: ".", outputName: "x", goos: "windows"}, "bin", false, "", versionInfo{}, buildMemoryCaps{})
	if res.err != nil {
		t.Fatalf("build: %v", res.err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "x.exe")); err != nil {
		t.Errorf("windows target must produce bin/x.exe: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "x")); err == nil {
		t.Error("bare bin/x must not exist for a windows target")
	}
}
