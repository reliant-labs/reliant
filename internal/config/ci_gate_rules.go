package config

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// validateCIGatePaths rejects a ci.gate.affected_rules entry that could not
// mean anything (no paths, a malformed glob, a rule that selects nothing or
// contradicts itself) and an extra check's malformed `paths`, so a typo is a
// load error at its line rather than a rule that silently never matches.
// Unknown keys are the strict loader's (walkUnknownKeys).
func validateCIGatePaths(cfg *ProjectConfig, root *yaml.Node) []validationIssue {
	var out []validationIssue
	issue := func(segs []string, msg, fix string) {
		line, col := 0, 0
		for n := len(segs); n > 0 && line == 0; n-- {
			line, col = findNodePos(root, segs[:n])
		}
		out = append(out, validationIssue{line: line, column: col, msg: msg, fix: fix})
	}
	globs := func(segs []string, label string, list []string) {
		for j, g := range list {
			if why := badGateGlob(g); why != "" {
				issue(append(append([]string(nil), segs...), fmt.Sprintf("[%d]", j)),
					fmt.Sprintf("%s[%d] %q %s", label, j, g, why),
					"use a project-relative glob: `*.md` (a base name anywhere), `frontends/web/**` (a whole tree), `deploy/kcl/*/main.k` (the whole path).")
			}
		}
	}
	for i, r := range cfg.CI.Gate.AffectedRules {
		at := []string{"ci", "gate", "affected_rules", fmt.Sprintf("[%d]", i)}
		label := fmt.Sprintf("ci.gate.affected_rules[%d]", i)
		key := func(k string) []string { return append(append([]string(nil), at...), k) }
		if len(r.Paths) == 0 {
			issue(key("paths"), label+" has no paths",
				"set `paths:` to the globs of the changed files the rule claims, e.g. [\"frontends/web/**\"].")
		}
		globs(key("paths"), label+".paths", r.Paths)
		globs(key("except"), label+".except", r.Except)
		for j, p := range r.Packages {
			if strings.TrimSpace(p) == "" || strings.ContainsAny(p, " \t") {
				issue(append(key("packages"), fmt.Sprintf("[%d]", j)),
					fmt.Sprintf("%s.packages[%d] %q is not a Go package pattern", label, j, p),
					"use `./internal/store/...` (a subtree), `./internal/db` (one package) or an import path.")
			}
		}
		for j, f := range r.Frontends {
			if strings.TrimSpace(f) == "" {
				issue(append(key("frontends"), fmt.Sprintf("[%d]", j)),
					fmt.Sprintf("%s.frontends[%d] is empty", label, j),
					"name a frontend by its discovered name (the directory under frontends/, e.g. web).")
			}
		}
		switch {
		case r.Full && (len(r.Packages) > 0 || len(r.Frontends) > 0):
			issue(key("full"), label+" sets full: true and also packages or frontends",
				"a full rule already runs everything: drop full, or drop packages and frontends.")
		case !r.Full && len(r.Packages) == 0 && len(r.Frontends) == 0:
			issue(at, label+" selects nothing",
				"give it packages, frontends, or full: true with a reason. A file no test reads belongs in ci.gate.affected_ignore.")
		}
	}
	for i, c := range cfg.CI.Gate.ExtraChecks {
		globs([]string{"ci", "gate", "extra_checks", fmt.Sprintf("[%d]", i), "paths"}, fmt.Sprintf("ci.gate.extra_checks[%d].paths", i), c.Paths)
	}
	return out
}

// badGateGlob says why g cannot be a project-relative gate glob, or "".
func badGateGlob(g string) string {
	s := filepath.ToSlash(strings.TrimSpace(g))
	switch {
	case s == "":
		return "is empty"
	case strings.HasPrefix(s, "/") || filepath.IsAbs(g):
		return "is absolute; changed files are project-relative"
	case s == ".." || strings.HasPrefix(s, "../") || strings.Contains(s, "/../"):
		return "leaves the project"
	}
	if _, err := path.Match(strings.TrimSuffix(strings.TrimPrefix(s, "./"), "/**"), ""); err != nil {
		return "is not a valid glob (" + err.Error() + ")"
	}
	return ""
}
