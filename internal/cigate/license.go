package cigate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/config"
)

// The pinned go-licenses v1.6.0 exits 1 both for findings and for scan
// errors. Accept only its finding lines; any other diagnostic (including an
// error after an earlier finding) must keep the row failing. Inspect the full
// output so a long list of findings cannot push an error out of the tail.
// ci.license.allowed swaps the blocklist line for go-licenses' allowlist one.
var licenseFindingLine = regexp.MustCompile(`^((Forbidden|Restricted) license type|Not allowed license) \S+ found for library .+$`)

func licenseFindingsOnly(err error, output string) bool {
	if exitCode(err) != 1 {
		return false
	}
	found := false
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !licenseFindingLine.MatchString(line) {
			return false
		}
		found = true
	}
	return found
}

// licensePlan is the license-check row as the project's policy
// (forge.yaml ci.license) shapes it.
type licensePlan struct {
	// argv is the go-licenses invocation.
	argv []string
	// expired names the exceptions past their review date. Any makes the
	// row FAIL without running: an expired entry fails the gate, as an
	// expired ci.vuln_scan.exemptions entry does.
	expired []string
	// overbroad names the required modules an exception's --ignore would
	// also exempt past its own module path (go-licenses matches a raw
	// prefix). Any makes the row FAIL without running.
	overbroad string
}

// planLicenseCheck is the go-licenses invocation for the project at root.
//
// go-licenses looks up a license for every package it loads, the project's
// own included, and the project's own code is not what a license check is
// for. A fresh project has no LICENSE file, so every first-party package
// logged "Failed to find license" and the check then passed anyway, which
// teaches people to ignore its output. --ignore exempts the project's own
// modules; the dependencies they import are still checked.
//
// With no ci.license policy the argv is the blocklist forge has always run
// (`--disallowed_types=forbidden,restricted`). ci.license.allowed replaces it
// with `--allowed_licenses` (go-licenses refuses the two together), and each
// live ci.license.exceptions entry adds one more --ignore. An expired entry
// adds none: the row fails naming it. now decides expiry, so a test need not
// wait for a date.
func planLicenseCheck(root string, policy config.CILicenseConfig, now time.Time) licensePlan {
	argv := []string{"go-licenses", "check", "./..."}
	if len(policy.Allowed) > 0 {
		names := make([]string, 0, len(policy.Allowed))
		for _, n := range policy.Allowed {
			names = append(names, strings.TrimSpace(n))
		}
		argv = append(argv, "--allowed_licenses="+strings.Join(names, ","))
	} else {
		argv = append(argv, "--disallowed_types=forbidden,restricted")
	}
	own, deps := projectModules(root)
	isOwn := map[string]bool{}
	for _, m := range own {
		isOwn[m] = true
	}
	ignored := map[string]bool{}
	for _, prefix := range licenseIgnoresOf(own, deps) {
		ignored[prefix] = true
		argv = append(argv, "--ignore", prefix)
	}

	var plan licensePlan
	var reach, live []string
	for _, ex := range policy.Exceptions {
		mod := strings.TrimSpace(ex.Module)
		if mod == "" {
			continue
		}
		if ex.Expired(now) {
			plan.expired = append(plan.expired, fmt.Sprintf("%s (expired %s)", mod, strings.TrimSpace(ex.Expires)))
			continue
		}
		if !ignored[mod] {
			ignored[mod] = true
			argv = append(argv, "--ignore", mod)
		}
		live = append(live, mod)
	}
	// go-licenses v1.6.0 drops every package whose import path merely starts
	// with an --ignore string (licenses/library.go: strings.HasPrefix), so
	// `github.com/foo/bar` also silences github.com/foo/barbaz and
	// github.com/foo/bar/v2: other modules, with licenses of their own. An
	// exception that reaches a required module past its own path is refused
	// (the row FAILs naming it) unless that module has an exception too.
	for _, mod := range live {
		for _, d := range sortedKeys(deps) {
			if d == mod || isOwn[d] || !strings.HasPrefix(d, mod) || ignored[d] || exceptionCovers(mod, d) {
				continue
			}
			reach = append(reach, fmt.Sprintf("%s would also exempt %s", mod, d))
		}
	}
	plan.argv = argv
	if len(reach) > 0 {
		plan.overbroad = "go-licenses matches --ignore as a raw prefix of package paths, so " + strings.Join(reach, "; ") +
			" — add an exception for each of those modules too (with its own reason), or remove the entry"
	}
	return plan
}

// exceptionCovers reports whether d is within the module path mod: mod
// itself or a package below it. A major-version suffix (mod/v2) is a
// different module, and so is a sibling sharing a prefix (modbaz).
func exceptionCovers(mod, d string) bool {
	if d == mod {
		return true
	}
	rest, ok := strings.CutPrefix(d, mod+"/")
	if !ok {
		return false
	}
	first, _, _ := strings.Cut(rest, "/")
	return !isMajorSuffix(first)
}

// isMajorSuffix is a module major-version path element: v2, v3, …
func isMajorSuffix(s string) bool {
	if len(s) < 2 || s[0] != 'v' || s[1] == '0' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != "v1"
}

// expiredLicenseNote is the FAIL note of a row with expired exceptions.
func expiredLicenseNote(expired []string) string {
	return "ci.license.exceptions past their review date: " + strings.Join(expired, ", ") +
		" — re-review each (is its license still what the reason says?) and either extend `expires` with a fresh reason or remove the entry"
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// licenseIgnores returns the --ignore prefixes that exempt the project's own
// modules: the module at root, and every module a go.work at root uses from
// inside the project tree. A use outside the tree (a --link-forge checkout)
// is a dependency, so it stays checked.
//
// go-licenses matches --ignore as a plain string prefix, not at a path
// boundary: "example.com/app" also exempts a dependency named
// "example.com/app-client". So the bare module path, which also covers a
// package at the module root, is passed only when no required module's path
// extends it that way. Otherwise the prefix is the module's subtree
// ("example.com/app/"), and a package at the module root, if there is one, is
// checked like a dependency.
func licenseIgnores(root string) []string {
	return licenseIgnoresOf(projectModules(root))
}

// projectModules reads the project's own modules — the module at root, and
// every module a go.work at root uses from inside the project tree — and
// the module paths they require.
func projectModules(root string) (own []string, deps map[string]bool) {
	deps = map[string]bool{}
	addModule := func(dir string) {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err != nil {
			return
		}
		f, err := modfile.ParseLax("go.mod", data, nil)
		if err != nil || f.Module == nil {
			return
		}
		own = append(own, f.Module.Mod.Path)
		for _, r := range f.Require {
			deps[r.Mod.Path] = true
		}
	}
	addModule(root)
	if data, err := os.ReadFile(filepath.Join(root, "go.work")); err == nil {
		if wf, err := modfile.ParseWork("go.work", data, nil); err == nil {
			for _, u := range wf.Use {
				dir := u.Path
				if !filepath.IsAbs(dir) {
					dir = filepath.Join(root, dir)
				}
				if rel, err := filepath.Rel(root, dir); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
					addModule(dir)
				}
			}
		}
	}
	return own, deps
}

// licenseIgnoresOf is licenseIgnores over modules already read.
func licenseIgnoresOf(own []string, deps map[string]bool) []string {
	isOwn := map[string]bool{}
	for _, m := range own {
		isOwn[m] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range own {
		if seen[m] || coveredBy(m, own) {
			continue
		}
		seen[m] = true
		prefix := m
		for d := range deps {
			if !isOwn[d] && strings.HasPrefix(d, m) && !strings.HasPrefix(d, m+"/") {
				prefix = m + "/"
				break
			}
		}
		out = append(out, prefix)
	}
	return out
}

// coveredBy reports whether module m is nested under another of the
// project's modules (example.com/app/api under example.com/app), whose
// prefix already exempts it.
func coveredBy(m string, own []string) bool {
	for _, o := range own {
		if strings.HasPrefix(m, o+"/") {
			return true
		}
	}
	return false
}
