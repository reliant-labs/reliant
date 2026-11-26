package lint

// A Go lane that cannot judge a scope because of code outside it.
//
// golangci-lint and the contract linter type-check the scope's packages, and
// a package cannot be type-checked against an import that does not compile.
// In a shared checkout that import is usually another agent's half-written
// package: a scoped lint of a leaf package failed with "could not import
// …/internal/store" or "packages failed to load", naming no problem in the
// files it was asked about (Ranger build, friction #10). The lane did not
// find a defect in the scope, and it did not pass it either: it could not
// decide. So under --scope it says that — a lane NOT DECIDED, with the
// packages that stopped it — and does not fail the run. Unscoped, nothing
// changes: every module package is a root there, so a broken one is judged as
// the failure it is.
//
// The rule is narrow on purpose. Only an error that is wholly about an
// out-of-scope import qualifies; any other error in the scope (an in-scope
// package that does not compile, an `undefined: store.Foo` after another
// agent renamed Foo) still fails, because the scoped code itself does not
// build against the tree as it is.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/mod/modfile"
)

// laneUndecidedError is a collector's report that it could not judge the
// scope: the packages it was given import broken packages outside it. The
// engine turns it into an undecided outcome under --scope, and into the
// lane's failure otherwise (Error is the full cause).
type laneUndecidedError struct {
	// broken are the import paths that stopped the lane, sorted.
	broken []string
	// cause is the lane's own error text, shown when the run is unscoped.
	cause string
}

func (e *laneUndecidedError) Error() string { return e.cause }

// reason is the one line an undecided lane prints.
func (e *laneUndecidedError) reason() string {
	const maxShown = 3
	shown := e.broken
	if len(shown) > maxShown {
		shown = append(append([]string{}, shown[:maxShown]...), fmt.Sprintf("and %d more", len(e.broken)-maxShown))
	}
	if len(e.broken) == 1 {
		return fmt.Sprintf("the scope imports %s, which lies outside it and does not compile — re-run once it builds", shown[0])
	}
	return fmt.Sprintf("the scope imports %s, which lie outside it and do not compile — re-run once they build",
		strings.Join(shown, ", "))
}

// asUndecided reports whether err is a laneUndecidedError.
func asUndecided(err error) (*laneUndecidedError, bool) {
	var ud *laneUndecidedError
	if errors.As(err, &ud) {
		return ud, true
	}
	return nil, false
}

// couldNotImportRE matches go/types' message for an import that failed to
// type-check, which golangci-lint relays as a typecheck finding and
// go/packages records on the importing package.
var couldNotImportRE = regexp.MustCompile(`could not import (\S+)`)

// couldNotImport returns the import path a "could not import X (…)" message
// opens with, and the last one in its chain (the package whose own errors
// started it: "could not import A (a.go: could not import B (…))" is B), or
// "" when msg is not one.
func couldNotImport(msg string) (first, root string) {
	if !strings.HasPrefix(strings.TrimSpace(msg), "could not import ") {
		return "", ""
	}
	all := couldNotImportRE.FindAllStringSubmatch(msg, -1)
	if len(all) == 0 {
		return "", ""
	}
	return all[0][1], all[len(all)-1][1]
}

// golangciUndecided reports whether a scoped golangci-lint run could not
// judge its scope: every error it reported in a file under the scope is a
// "could not import" of a package outside the scope. Nil when unscoped, when
// nothing in the scope failed, or when anything in the scope failed for
// another reason.
func golangciUndecided(rc *lintRunCtx, fs []lintJSONFinding) *laneUndecidedError {
	if rc.scope == nil {
		return nil
	}
	modDir := rc.moduleDir()
	modPath := modulePathAt(modDir)
	broken := map[string]bool{}
	for _, f := range fs {
		if f.Severity != lintSevError || f.File == "" || !rc.scope.contains(f.File) {
			continue
		}
		first, root := couldNotImport(f.Message)
		if first == "" || importInScope(rc.scope, modDir, modPath, first) {
			return nil
		}
		broken[root] = true
	}
	if len(broken) == 0 {
		return nil
	}
	return &laneUndecidedError{broken: sortedSet(broken), cause: "golangci-lint could not type-check the scope"}
}

// importInScope reports whether importPath is a package of the module at
// modDir (module path modPath) whose directory lies under the scope.
func importInScope(scope *lintScope, modDir, modPath, importPath string) bool {
	if modPath == "" || (importPath != modPath && !strings.HasPrefix(importPath, modPath+"/")) {
		return false
	}
	dir := filepath.Join(modDir, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(importPath, modPath), "/")))
	return scope.contains(dir)
}

// modulePathAt is the module path go.mod in dir declares, or "".
func modulePathAt(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	return modfile.ModulePath(data)
}
