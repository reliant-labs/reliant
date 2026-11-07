package main

import (
	"fmt"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/exp/apidiff"
	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/goexec"
)

// apiRoot is the directory, relative to the module root, whose packages are
// forge's public API: the ones generated projects import.
const apiRoot = "pkg"

// Break is one incompatible change to an exported API.
type Break struct {
	// Pkg is the package's directory relative to the module root ("pkg/orm").
	Pkg string
	// Message is apidiff's description ("RunTx: changed from func(...) to ...").
	Message string
}

func (b Break) String() string { return b.Pkg + ": " + b.Message }

// loadAPI type-checks every package under root/pkg and returns them keyed by
// their directory relative to root. Types come from compiler export data, so
// the cost is the module's build cache, not a second type-check from source.
func loadAPI(root string) (map[string]*types.Package, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps | packages.NeedModule,
		Dir:  root,
		Env:  goexec.CheckEnv("GOWORK=off"),
	}
	pkgs, err := packages.Load(cfg, "./"+apiRoot+"/...")
	if err != nil {
		return nil, fmt.Errorf("load %s/...: %w", apiRoot, err)
	}
	out := map[string]*types.Package{}
	var failures []string
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			for _, e := range p.Errors {
				failures = append(failures, fmt.Sprintf("%s: %v", p.PkgPath, e))
			}
			continue
		}
		if p.Module == nil {
			failures = append(failures, p.PkgPath+": not in a module")
			continue
		}
		rel := strings.TrimPrefix(p.PkgPath, p.Module.Path+"/")
		out[rel] = p.Types
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return nil, fmt.Errorf("cannot judge the API of %s: %d package(s) failed to load:\n  %s", root, len(failures), strings.Join(failures, "\n  "))
	}
	return out, nil
}

// diffAPI compares the exported API under pkg/ of the module at oldRoot with
// the one at newRoot and returns every incompatible change, in a stable order.
// A removed package is a break; a new one is not.
func diffAPI(oldRoot, newRoot string) (breaks []Break, compatible int, err error) {
	oldPkgs, err := loadAPI(oldRoot)
	if err != nil {
		return nil, 0, fmt.Errorf("base: %w", err)
	}
	newPkgs, err := loadAPI(newRoot)
	if err != nil {
		return nil, 0, fmt.Errorf("head: %w", err)
	}
	names := make([]string, 0, len(oldPkgs))
	for n := range oldPkgs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		np, ok := newPkgs[n]
		if !ok {
			breaks = append(breaks, Break{Pkg: n, Message: "package removed"})
			continue
		}
		for _, c := range apidiff.Changes(oldPkgs[n], np).Changes {
			if c.Compatible {
				compatible++
				continue
			}
			breaks = append(breaks, Break{Pkg: n, Message: c.Message})
		}
	}
	return breaks, compatible, nil
}
