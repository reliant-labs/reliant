// Package scratchmod writes the go.mod and go.sum of a throwaway module that
// builds against a forge checkout with the module proxy off (GOPROXY=off).
//
// It exists because one failure keeps coming back. A scratch module whose
// go.mod requires only forge makes the go command expand the module graph to
// find the modules forge's packages come from, and the expansion reads the
// go.mod of every module an unpruned (pre-go1.17) dependency requires:
// github.com/go-logr/stdr@v1.2.2 (go 1.16) pulls in
// github.com/go-logr/logr@v1.2.2's go.mod, which nothing that builds or tests
// forge itself downloads. Offline, whether the build works then depends on
// what the module cache happens to hold:
//
//	go: github.com/go-logr/stdr@v1.2.2 requires
//		github.com/go-logr/logr@v1.2.2: module lookup disabled by GOPROXY=off
//
// An offline `go mod tidy` is worse: it needs every module in the "all"
// graph, tests of dependencies included.
//
// Copying forge's own requirement list into the scratch go.mod makes the
// scratch module's build list forge's. Lazy module loading then resolves
// every package forge's packages import straight from that list, never
// loading the wider graph, so the build needs exactly the module files a
// build of forge needs: the ones CI's module cache (keyed on forge's go.sum)
// is warmed with. internal/codemod fixed this locally first; internal/templates
// then hit the identical failure, so the fix lives in one importable place.
package scratchmod

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// forgePseudoVersion is the version the scratch module requires forge at; the
// replace directive points it at the checkout, so the version is never fetched.
const forgePseudoVersion = "v0.0.0-00010101000000-000000000000"

// Write writes dir/go.mod and dir/go.sum for module: it requires the forge
// checkout at forgeRoot (replaced to it), carries forge's go version, its
// whole requirement list and its replace directives (relative directory
// replacements made absolute), and forge's go.sum. Build it with
// GOFLAGS=-mod=mod or -mod=readonly; never `go mod tidy` it offline.
func Write(forgeRoot, dir, module string) error {
	data, err := os.ReadFile(filepath.Join(forgeRoot, "go.mod"))
	if err != nil {
		return err
	}
	forge, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		return err
	}
	if forge.Module == nil || forge.Go == nil {
		return fmt.Errorf("%s/go.mod declares no module or go version", forgeRoot)
	}
	f := &modfile.File{}
	for _, err := range []error{
		f.AddModuleStmt(module),
		f.AddGoStmt(forge.Go.Version),
		f.AddRequire(forge.Module.Mod.Path, forgePseudoVersion),
		f.AddReplace(forge.Module.Mod.Path, "", forgeRoot, ""),
	} {
		if err != nil {
			return err
		}
	}
	for _, r := range forge.Require {
		f.AddNewRequire(r.Mod.Path, r.Mod.Version, true) // the scratch module imports none of them itself
	}
	for _, r := range forge.Replace {
		newPath := r.New.Path
		if modfile.IsDirectoryPath(newPath) && !filepath.IsAbs(newPath) {
			newPath = filepath.Join(forgeRoot, newPath)
		}
		if err := f.AddReplace(r.Old.Path, r.Old.Version, newPath, r.New.Version); err != nil {
			return err
		}
	}
	f.Cleanup()
	out, err := f.Format()
	if err != nil {
		return err
	}
	sum, err := os.ReadFile(filepath.Join(forgeRoot, "go.sum"))
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(filepath.Join(dir, "go.mod"), out, 0o644); err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(dir, "go.sum"), sum, 0o644)
}
