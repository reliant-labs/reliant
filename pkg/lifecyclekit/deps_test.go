package lifecyclekit_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestImportGraphHasNoKubernetes pins that importing lifecyclekit — to get
// Supervise, WrapWorker or Singleton — does not link controller-runtime or
// any k8s.io module. The controller-manager bridge lives in
// forge/pkg/appkit/operatorkit, which a project imports only when it runs
// operators. lifecyclekit once re-exported operatorkit's Run/Controller/
// Options, and every binary that adopted Supervise gained
// sigs.k8s.io/controller-runtime in its go.mod and ~1.8 MB of code it never
// ran.
//
// Cache soundness: `go list` reads the module's sources in a child process,
// which Go's test cache cannot see. So the test reads go.mod and stats every
// .go file of every in-module dependency itself; an edit that adds an import
// to any of them (the only way the graph can grow) invalidates the cached
// result.
func TestImportGraphHasNoKubernetes(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("go", "list", "-deps",
		"-f", "{{.ImportPath}}\t{{.Dir}}\t{{if .Module}}{{.Module.Main}}{{end}}", ".").Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("go list -deps: %v\n%s", err, stderr)
	}

	var forbidden []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			continue
		}
		path, dir, mainModule := fields[0], fields[1], fields[2] == "true"
		if strings.HasPrefix(path, "sigs.k8s.io/controller-runtime") || strings.HasPrefix(path, "k8s.io/") {
			forbidden = append(forbidden, path)
		}
		if mainModule {
			statGoFiles(t, dir)
		}
	}
	if gomod, err := exec.Command("go", "env", "GOMOD").Output(); err == nil {
		if p := strings.TrimSpace(string(gomod)); p != "" && p != os.DevNull {
			if _, err := os.ReadFile(p); err != nil {
				t.Fatalf("read go.mod: %v", err)
			}
		}
	}

	if len(forbidden) > 0 {
		t.Fatalf("pkg/lifecyclekit reaches Kubernetes packages — the operator bridge belongs in "+
			"pkg/appkit/operatorkit, imported only by projects that run operators:\n  %s",
			strings.Join(forbidden, "\n  "))
	}
}

// statGoFiles stats each .go file in dir so the test cache keys on it.
func statGoFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".go") {
			if _, err := os.Stat(filepath.Join(dir, e.Name())); err != nil {
				t.Fatalf("stat %s: %v", e.Name(), err)
			}
		}
	}
}
