package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/config"
)

// writeGoModuleTree lays out a multi-module project: the root module, a
// nested tools module (the control-plane case: tools/registry-reliant carried
// called advisories while the root scan passed), a go.mod-only boundary
// module, and modules in directories go never builds from a parent.
func writeGoModuleTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range map[string]string{
		"go.mod":                              "module example.com/acme\n",
		"main.go":                             "package main\n",
		"internal/svc/svc.go":                 "package svc\n",
		"tools/registry/go.mod":               "module example.com/acme/tools/registry\n",
		"tools/registry/main.go":              "package main\n",
		"tools/registry/inner/go.mod":         "module example.com/acme/tools/registry/inner\n",
		"tools/registry/inner/x.go":           "package inner\n",
		"boundary/go.mod":                     "module example.com/acme/boundary\n",
		"boundary/README.md":                  "no Go here\n",
		"vendor/example.com/v/go.mod":         "module example.com/v\n",
		"vendor/example.com/v/v.go":           "package v\n",
		"testdata/fixture/go.mod":             "module fixture\n",
		"testdata/fixture/f.go":               "package fixture\n",
		"frontends/web/node_modules/m/go.mod": "module m\n",
		"frontends/web/node_modules/m/m.go":   "package m\n",
		".tools/gobin/go.mod":                 "module tools\n",
		".tools/gobin/t.go":                   "package tools\n",
		"_scratch/go.mod":                     "module scratch\n",
		"_scratch/s.go":                       "package scratch\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDiscoverGoModules(t *testing.T) {
	root := writeGoModuleTree(t)

	modules, empty, err := discoverGoModules(root, false)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(modules)
	if want := []string{".", "tools/registry", "tools/registry/inner"}; !slices.Equal(modules, want) {
		t.Errorf("modules = %v, want %v (vendor/, testdata/, node_modules/, .x and _x skipped)", modules, want)
	}
	if want := []string{"boundary"}; !slices.Equal(empty, want) {
		t.Errorf("modules with no Go packages = %v, want %v", empty, want)
	}

	modules, empty, err = discoverGoModules(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(modules, []string{"."}) || len(empty) != 0 {
		t.Errorf("--root-module: modules = %v, empty = %v; want [.] and none", modules, empty)
	}
}

// A project whose only Go package is the root directory itself is one module
// with packages: the root is recorded as its own module before any of its
// files are seen, so a root-level .go file counts.
func TestDiscoverGoModules_SinglePackageAtTheRoot(t *testing.T) {
	root := t.TempDir()
	for rel, body := range map[string]string{
		"go.mod":  "module example.com/tiny\n",
		"main.go": "package main\n",
	} {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, rootOnly := range []bool{false, true} {
		modules, empty, err := discoverGoModules(root, rootOnly)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(modules, []string{"."}) || len(empty) != 0 {
			t.Errorf("rootOnly=%v: modules = %v, empty = %v; want [.] and none", rootOnly, modules, empty)
		}
	}
}

// A subtree the walk cannot read fails the discovery: it may hold a module,
// or the root module's packages, and a scan that skipped it would pass
// without having looked (forge#2192 review).
func TestDiscoverGoModules_FailsClosedOnAnUnreadableSubtree(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("mode 000 does not stop this user reading a directory (root, or Windows, where chmod sets only the read-only bit), so there is nothing unreadable to walk")
	}
	root := writeGoModuleTree(t)
	locked := filepath.Join(root, "internal", "svc") // the root module's own package
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	for _, rootOnly := range []bool{false, true} {
		modules, empty, err := discoverGoModules(root, rootOnly)
		if err == nil {
			t.Errorf("rootOnly=%v: discovery over an unreadable subtree passed: modules = %v, empty = %v", rootOnly, modules, empty)
		}
	}
}

// The gate scans every module and fails on a called advisory in a NESTED
// one. `forge ci vuln-scan --go` used to run `govulncheck ./...` in the
// working directory only, which stops at a nested go.mod, and printed
// "govulncheck (Go modules + stdlib)" over a project whose tools module
// carried called advisories (forge bug report 7d19e656).
func TestScanGoModules_FailsOnANestedModulesFinding(t *testing.T) {
	root := writeGoModuleTree(t)
	const vulnJSON = `{"osv":{"id":"GO-2026-0001","summary":"bad parse"}}
{"finding":{"osv":"GO-2026-0001","trace":[{"module":"example.com/dep","version":"v1.0.0","package":"example.com/dep","function":"Parse"}]}}
`
	var scanned []string
	run := func(_ context.Context, dir string) ([]byte, error) {
		rel, _ := filepath.Rel(root, dir)
		rel = filepath.ToSlash(rel)
		scanned = append(scanned, rel)
		if rel == "tools/registry" {
			return []byte(vulnJSON), nil
		}
		return nil, nil
	}
	var out bytes.Buffer
	_, err := scanGoModules(context.Background(), root, false, nil, run, &out, time.Now())
	if err == nil || !strings.Contains(err.Error(), "1 vulnerability") {
		t.Fatalf("a called advisory in tools/registry must fail the gate, got err=%v\n%s", err, out.String())
	}
	slices.Sort(scanned)
	if want := []string{".", "tools/registry", "tools/registry/inner"}; !slices.Equal(scanned, want) {
		t.Errorf("scanned %v, want %v", scanned, want)
	}
	for _, want := range []string{"GO-2026-0001", "in tools/registry", "boundary is a Go module with no Go packages", "✓ .: no called vulnerabilities"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not name %q:\n%s", want, out.String())
		}
	}

	// The same advisory, accepted once, is accepted in every module that
	// calls it — and is not reported stale by the modules that do not.
	ex := []config.CIVulnExemption{{ID: "GO-2026-0001", Reason: "unreachable", Expires: "2099-01-01"}}
	out.Reset()
	what, err := scanGoModules(context.Background(), root, false, ex, run, &out, time.Now())
	if err != nil {
		t.Fatalf("an accepted advisory must pass: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "matched nothing") {
		t.Errorf("an exemption that matched in one module was reported stale:\n%s", out.String())
	}
	if !strings.Contains(what, "3 modules") {
		t.Errorf("the success line should say how many modules it scanned: %q", what)
	}

	// --root-module keeps the old scope.
	scanned = nil
	if _, err := scanGoModules(context.Background(), root, true, nil, run, &out, time.Now()); err != nil {
		t.Errorf("--root-module scans the root alone, which is clean: %v", err)
	}
	if !slices.Equal(scanned, []string{"."}) {
		t.Errorf("--root-module scanned %v, want [.]", scanned)
	}
}

// A module the scanner cannot complete fails the gate, but only after the
// other modules ran and reported.
func TestScanGoModules_AFailingModuleDoesNotHideTheOthers(t *testing.T) {
	root := writeGoModuleTree(t)
	var scanned int
	run := func(_ context.Context, dir string) ([]byte, error) {
		scanned++
		if filepath.Base(dir) == "registry" {
			return nil, errors.New("exit status 1")
		}
		return nil, nil
	}
	var out bytes.Buffer
	_, err := scanGoModules(context.Background(), root, false, nil, run, &out, time.Now())
	if err == nil || !strings.Contains(err.Error(), "tools/registry") {
		t.Fatalf("want the failed module named, got %v", err)
	}
	if scanned != 3 {
		t.Errorf("scanned %d modules, want all 3", scanned)
	}
}

func TestScanGoModules_NoModuleIsAnError(t *testing.T) {
	_, err := scanGoModules(context.Background(), t.TempDir(), false, nil,
		func(context.Context, string) ([]byte, error) { return nil, nil }, &bytes.Buffer{}, time.Now())
	if err == nil {
		t.Fatal("a project with no Go module must not report a pass")
	}
}
