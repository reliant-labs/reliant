package telemetry

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// heavyImports are the packages pkg/telemetry must never reach. It is the
// contract every collector reads (forge's agents, pkg/release, the hosted
// control plane, a project's own tooling through the KCL plugin), so whatever
// it imports, all of them link. #2458 once validated dashboards by importing
// pkg/dashspec, which reaches pkg/alerting, pkg/observe, pkg/auth and about
// 130 OpenTelemetry packages; the validator is now passed in
// (Options.ValidateDashboard).
var heavyImports = []string{
	"go.opentelemetry.io/",
	"github.com/reliant-labs/forge/pkg/observe",
	"github.com/reliant-labs/forge/pkg/auth",
	"github.com/reliant-labs/forge/pkg/dashspec",
	"github.com/reliant-labs/forge/pkg/alerting",
}

func isHeavyImport(path string) bool {
	for _, h := range heavyImports {
		if strings.HasSuffix(h, "/") {
			if strings.HasPrefix(path, h) {
				return true
			}
		} else if path == h || strings.HasPrefix(path, h+"/") {
			return true
		}
	}
	return false
}

// TestImportGraphIsALeaf pins that importing pkg/telemetry, or its collector
// translator, links none of heavyImports.
//
// Cache soundness: `go list` reads the module's sources in a child process,
// which Go's test cache cannot see. So the test reads go.mod and stats every
// .go file of every in-module dependency itself; an edit that adds an import
// to any of them (the only way the graph can grow) invalidates the cached
// result.
func TestImportGraphIsALeaf(t *testing.T) {
	testkit.Slow(t, "execs go list")
	t.Parallel()
	out, err := exec.Command("go", "list", "-deps",
		"-f", "{{.ImportPath}}\t{{.Dir}}\t{{if .Module}}{{.Module.Main}}{{end}}", ".", "./collector").Output()
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
		if isHeavyImport(path) {
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
		t.Fatalf("pkg/telemetry reaches packages every collector would then link — pass what needs them in "+
			"(as Options.ValidateDashboard does) instead of importing them:\n  %s",
			strings.Join(forbidden, "\n  "))
	}
}

func TestIsHeavyImport(t *testing.T) {
	for path, want := range map[string]bool{
		"go.opentelemetry.io/otel":                              true,
		"github.com/reliant-labs/forge/pkg/observe":             true,
		"github.com/reliant-labs/forge/pkg/observe/metrics":     true,
		"github.com/reliant-labs/forge/pkg/auth":                true,
		"github.com/reliant-labs/forge/pkg/authn":               false,
		"github.com/reliant-labs/forge/pkg/telemetry":           false,
		"github.com/reliant-labs/forge/pkg/telemetry/collector": false,
		"encoding/json": false,
	} {
		if got := isHeavyImport(path); got != want {
			t.Errorf("isHeavyImport(%q) = %v, want %v", path, got, want)
		}
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
