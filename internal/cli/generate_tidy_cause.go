package cli

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// A failed `go mod tidy` used to end generate with "fix go.mod or go.sum",
// which is almost never the fix. The run that rolls back is one whose code
// imports a package nobody provides, and when that package sits inside the
// project's own module, no go.mod edit can supply it. The P3b pilot read
// that advice, spent its time on go.mod, and found the cause two screens up:
// the scaffolded handler imported <module>/pkg/config, which reliant does
// not have. tidyFailureCause reads the cause out of go's own output, so the
// error names it.

// unresolvedImportRE matches go's two-line report of an import nothing
// provides:
//
//	go: example.com/app/internal/handlers/greeter imports
//		example.com/app/pkg/config: cannot find module providing package example.com/app/pkg/config: ...
var unresolvedImportRE = regexp.MustCompile(`(?m)^go: (\S+) imports\n\s+(\S+): (.+)$`)

// unresolvedImport is one import go could not resolve.
type unresolvedImport struct {
	importer, pkg, reason string
}

// tidyFailureCause returns the unresolved imports in go's output, worded as
// a cause, or "" when there are none (the failure is about go.mod itself,
// and the old advice stands). modulePath is the project's module, so an
// import inside it is named as a package that does not exist rather than
// as a dependency to fetch.
func tidyFailureCause(output, modulePath string) string {
	seen := map[string]bool{}
	var missingLocal, missingExternal []unresolvedImport
	for _, m := range unresolvedImportRE.FindAllStringSubmatch(output, -1) {
		u := unresolvedImport{importer: m[1], pkg: m[2], reason: m[3]}
		if seen[u.importer+" "+u.pkg] {
			continue
		}
		seen[u.importer+" "+u.pkg] = true
		if modulePath != "" && (u.pkg == modulePath || strings.HasPrefix(u.pkg, modulePath+"/")) {
			missingLocal = append(missingLocal, u)
		} else {
			missingExternal = append(missingExternal, u)
		}
	}
	if len(missingLocal) == 0 && len(missingExternal) == 0 {
		return ""
	}
	var b strings.Builder
	if len(missingLocal) > 0 {
		fmt.Fprintf(&b, "code imports %s that do not exist in this module (%s), so no go.mod or go.sum change can fix it:",
			plural(len(missingLocal), "a package", "packages"), modulePath)
		for _, u := range missingLocal {
			fmt.Fprintf(&b, "\n     %s imports %s%s", u.importer, u.pkg, localImportHint(u.pkg, modulePath))
		}
	}
	if len(missingExternal) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n   and ")
		}
		fmt.Fprintf(&b, "%s cannot be resolved:", plural(len(missingExternal), "a dependency", "dependencies"))
		sort.SliceStable(missingExternal, func(i, j int) bool { return missingExternal[i].pkg < missingExternal[j].pkg })
		for _, u := range missingExternal {
			fmt.Fprintf(&b, "\n     %s imports %s: %s", u.importer, u.pkg, u.reason)
		}
	}
	return b.String()
}

// localImportHint names the usual source of a missing in-module package
// forge's own output imports.
func localImportHint(pkg, modulePath string) string {
	rel := strings.TrimPrefix(strings.TrimPrefix(pkg, modulePath), "/")
	switch {
	case rel == "pkg/config":
		return " — typed config: add config protos under proto/<project>/config/v1 (forge generates pkg/config from them), or declare `type Config` in pkg/config yourself"
	case strings.HasPrefix(rel, "gen/"):
		return " — generated stubs: check that buf.gen.yaml writes this proto's Go output to " + rel + " inside this module"
	}
	return ""
}

// tidyImportError is a tidy failure whose cause is an unresolved import.
type tidyImportError struct {
	cause string
	err   error
}

func (e *tidyImportError) Error() string { return e.cause }
func (e *tidyImportError) Unwrap() error { return e.err }
