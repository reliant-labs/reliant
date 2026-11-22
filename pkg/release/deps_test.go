package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestImportGraphHasNoObserve pins that importing pkg/release (the bundle and
// shape contract a control plane decodes) links neither pkg/observe, pkg/auth
// nor OpenTelemetry. It once did, through pkg/telemetry, whose dashboard
// validation imported pkg/dashspec; that validator is now passed in
// (telemetry.Options.ValidateDashboard), and pkg/telemetry pins its own
// graph (TestImportGraphIsALeaf).
//
// Cache soundness: `go list` reads the module's sources in a child process,
// which Go's test cache cannot see. So the test reads go.mod and stats every
// .go file of every in-module dependency itself; an edit that adds an import
// to any of them invalidates the cached result.
func TestImportGraphHasNoObserve(t *testing.T) {
	testkit.Slow(t, "execs go list")
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
		if strings.HasPrefix(path, "go.opentelemetry.io/") || underPackage(path, "github.com/reliant-labs/forge/pkg/observe") ||
			underPackage(path, "github.com/reliant-labs/forge/pkg/auth") {
			forbidden = append(forbidden, path)
		}
		if mainModule {
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
	}
	if gomod, err := exec.Command("go", "env", "GOMOD").Output(); err == nil {
		if p := strings.TrimSpace(string(gomod)); p != "" && p != os.DevNull {
			if _, err := os.ReadFile(p); err != nil {
				t.Fatalf("read go.mod: %v", err)
			}
		}
	}

	if len(forbidden) > 0 {
		t.Fatalf("pkg/release reaches packages every control plane that decodes a bundle would then link:\n  %s",
			strings.Join(forbidden, "\n  "))
	}
}

// underPackage reports whether path is pkg or one of its subpackages.
func underPackage(path, pkg string) bool {
	return path == pkg || strings.HasPrefix(path, pkg+"/")
}
