package codegen

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Supervised-component discovery: the worker and operator inventory, read from
// the project's REAL source — the internal/workers/<pkg>/ and
// internal/operators/<pkg>/ package directories.
//
// This is the same rule webhook discovery already follows (see
// WebhookNamesForService: "the file IS the declaration"). It is NOT a
// convention guess layered on top of user code: `forge scaffold worker` and
// `forge scaffold operator` are the things that create those directories,
// and every other generator already hard-codes the same two role roots (see
// assembleBuildComponents' addRole("internal/workers") / addRole(
// "internal/operators") and GenerateWorkerFiles / GenerateOperatorBinaryOnly).
// Enumerating a directory forge itself writes adds no coupling that the
// per-component codegen did not already have.
//
// Why it has to exist: internal/app/lifecycle.go projects one accessor per
// supervised component, and internal/app/compose.go carries the field +
// construction those accessors read. Without an inventory both files are
// frozen at whatever the project looked like the day it was scaffolded, and
// every `forge scaffold worker|operator` afterwards leaves a tree that
// references accessors nobody wrote.

// DiscoverWorkerSpecs returns one WorkerSpec per worker package under
// internal/workers/, sorted by name. Best-effort: a project with no
// internal/workers/ directory yields nil rather than an error.
func DiscoverWorkerSpecs(projectDir string) []WorkerSpec {
	names := discoverComponentPackages(projectDir, filepath.Join("internal", "workers"))
	specs := make([]WorkerSpec, 0, len(names))
	for _, n := range names {
		specs = append(specs, WorkerSpec{Name: n})
	}
	return specs
}

// DiscoverOperatorSpecs is the operator-side analog of DiscoverWorkerSpecs,
// over internal/operators/.
func DiscoverOperatorSpecs(projectDir string) []OperatorSpec {
	names := discoverComponentPackages(projectDir, filepath.Join("internal", "operators"))
	specs := make([]OperatorSpec, 0, len(names))
	for _, n := range names {
		specs = append(specs, OperatorSpec{Name: n})
	}
	return specs
}

// discoverComponentPackages lists the immediate subdirectories of
// <projectDir>/<roleRoot> that hold at least one non-test Go file — i.e. the
// real Go packages, skipping bare/leftover directories and any testdata tree.
// Results are sorted so codegen output is byte-stable across runs.
func discoverComponentPackages(projectDir, roleRoot string) []string {
	if projectDir == "" {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(projectDir, roleRoot))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" {
			continue
		}
		if !dirHasGoSource(filepath.Join(projectDir, roleRoot, name)) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// dirHasGoSource reports whether dir carries at least one non-test .go file —
// the signal that the directory is a real Go package rather than an empty
// leftover the build would 404 on.
func dirHasGoSource(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			return true
		}
	}
	return false
}
