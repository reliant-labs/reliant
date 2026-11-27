package lint

// A service is one slice under `forge lint --scope`: its proto and its
// handler directory.
//
// A service's API lives in proto/services/<svc>/ and its implementation in
// internal/handlers/<svc>/, and several rules judge the two together while
// reporting on only one of them. forgeconv-activity-declared, for one, is
// about the rpc an agent is implementing, but it lands on the rpc's line in
// the .proto. So an agent that scoped its lint to the handler it owned
// (`--scope internal/handlers`) passed, and the same finding failed the
// unscoped run at the end (Ranger build, friction #9).
//
// A scope that reaches one half of a service therefore REPORTS findings in
// the other half too. It only reports them: the companion paths never widen
// what a lane runs on (golangci-lint still gets the asked packages, so a
// scoped run stays fast) nor what --fix writes (formatKeep reads contains,
// not reports).

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// serviceTrees are the two halves of a service, by project-relative
// directory: the protos and the handlers. A path under one names the
// service directory beneath it; the companion is the same name under the
// other.
var serviceTrees = [2]string{"proto/services", "internal/handlers"}

// serviceCompanions returns the paths a scope over paths reports beside
// them: for a path inside one half of a service (proto/services/jobs/v1,
// internal/handlers/jobs/rpc_create.go), the other half's directory of that
// service; for a path containing a whole tree (internal/handlers, internal),
// the other tree. Only directories that exist are returned, already-covered
// ones are dropped, and the result is sorted.
func serviceCompanions(root string, paths []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(dir string) {
		if seen[dir] || coveredBy(dir, paths) {
			return
		}
		if fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir))); err != nil || !fi.IsDir() {
			return
		}
		seen[dir] = true
		out = append(out, dir)
	}
	for _, p := range paths {
		p = scopeRel(p)
		for i, tree := range serviceTrees {
			other := serviceTrees[1-i]
			switch {
			case p == tree || strings.HasPrefix(tree, p+"/"):
				// The scope holds the whole tree: every service's other half.
				add(other)
			case strings.HasPrefix(p, tree+"/"):
				svc, _, _ := strings.Cut(strings.TrimPrefix(p, tree+"/"), "/")
				add(path.Join(other, svc))
			}
		}
	}
	sort.Strings(out)
	return out
}

// coveredBy reports whether dir is one of paths or lies under one.
func coveredBy(dir string, paths []string) bool {
	for _, p := range paths {
		if p = scopeRel(p); dir == p || strings.HasPrefix(dir, p+"/") {
			return true
		}
	}
	return false
}

// reports reports whether a finding in file is shown by a scoped run: the
// file lies under the scope, or under a companion half of a service the
// scope reaches (serviceCompanions).
func (s *lintScope) reports(file string) bool {
	if s == nil || s.contains(file) {
		return true
	}
	if len(s.companions) == 0 {
		return false
	}
	companion := lintScope{root: s.root, paths: s.companions}
	return companion.contains(file)
}
