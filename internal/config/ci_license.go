package config

import (
	"fmt"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
	"golang.org/x/mod/module"
)

// CILicenseConfig is `ci.license:`, the policy of the gate's license-check
// row (`forge ci gate`, internal/cigate/license.go). Absent, the row is a
// blocklist: go-licenses fails a dependency whose license TYPE is forbidden
// or restricted, and passes every other, an unrecognised license included.
//
// Allowed turns it into an allowlist. A blocklist catches only the licenses
// someone thought to name; the one that hurts is the license nobody
// anticipated, in a transitive dependency a routine `go get` added. Under an
// allowlist an unrecognised license fails, and passing it takes an explicit,
// reviewed decision: an entry in Exceptions.
//
//	ci:
//	  license:
//	    allowed: [Apache-2.0, MIT, BSD-2-Clause, BSD-3-Clause, ISC]
//	    exceptions:
//	      - module: github.com/hashicorp/go-version
//	        reason: "MPL-2.0 is file-level copyleft; we import it unmodified"
//	        expires: "2027-06-01"
type CILicenseConfig struct {
	// Allowed lists the license names that pass, as go-licenses' classifier
	// names them: SPDX identifiers (`Apache-2.0`, `MIT`, `BSD-3-Clause`).
	// Set, it replaces the forbidden/restricted blocklist with
	// `--allowed_licenses`, and any other license fails the row.
	Allowed []string `yaml:"allowed,omitempty"`
	// Exceptions exempt single modules from the check, whichever mode it
	// runs in. See CILicenseException.
	Exceptions []CILicenseException `yaml:"exceptions,omitempty"`
}

// CILicenseException exempts ONE module from license-check: a dependency
// whose license is fine but that the classifier misreads (a dual-licensed
// module, an unusual BSD variant), or one the project accepted on terms (a
// file-level copyleft library imported unmodified).
//
// It names a module, never a license: a license that is broadly acceptable
// belongs in Allowed, where the decision is visible in one place. Reason is
// required, because an exemption nobody had to justify is one nobody revisits.
// Expires is optional; past it the entry stops exempting and the row FAILS
// naming it, the same contract as ci.vuln_scan.exemptions.
//
// go-licenses matches an exemption as a package-path PREFIX, so the entry for
// example.com/a also covers example.com/a-b and example.com/a/v2. The gate
// notes on the row any module the project requires that an entry reaches that
// way.
type CILicenseException struct {
	// Module is the module path (github.com/foo/bar). Every package under it
	// is skipped; the dependencies they import are still checked.
	Module string `yaml:"module"`
	// Reason says why this module is acceptable: the license and the terms
	// the project uses it under, not a ticket number.
	Reason string `yaml:"reason"`
	// Expires is an optional YYYY-MM-DD review date. The entry is honored
	// through that day.
	Expires string `yaml:"expires,omitempty"`
}

// ExpiresAt is the entry's parsed expiry; ok is false when it has none, or
// none that parses (a load error, so a loaded config never has one).
func (e CILicenseException) ExpiresAt() (t time.Time, ok bool) {
	s := strings.TrimSpace(e.Expires)
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.DateOnly, s)
	return t, err == nil
}

// Expired reports whether the entry no longer exempts anything at now.
// Expiry is end of day, as for ci.vuln_scan.exemptions: an entry dated today
// is live today and lapses tomorrow.
func (e CILicenseException) Expired(now time.Time) bool {
	t, ok := e.ExpiresAt()
	return ok && now.After(t.AddDate(0, 0, 1))
}

// validateCILicense holds ci.license to the shape the gate can honor: license
// names go-licenses can take in one comma list, and exceptions that each name
// one module, once, with a reason and a parseable expiry. Each is a load
// error at its own line, so a malformed policy never reaches the gate as a
// check that silently checks less.
func validateCILicense(cfg *ProjectConfig, root *yaml.Node) []validationIssue {
	lic := cfg.CI.License
	var out []validationIssue
	issue := func(path []string, fallback []string, msg, fix string) {
		line, col := findNodePos(root, path)
		if line == 0 {
			line, col = findNodePos(root, fallback)
		}
		out = append(out, validationIssue{line: line, column: col, msg: msg, fix: fix})
	}

	seenLicense := map[string]bool{}
	for i, name := range lic.Allowed {
		at := []string{"ci", "license", "allowed", fmt.Sprintf("[%d]", i)}
		parent := []string{"ci", "license", "allowed"}
		n := strings.TrimSpace(name)
		switch {
		case n == "":
			issue(at, parent, fmt.Sprintf("ci.license.allowed[%d] is empty", i),
				"name a license by its SPDX identifier (MIT, Apache-2.0, BSD-3-Clause), or remove the entry.")
		case strings.ContainsAny(n, ", \t"):
			issue(at, parent, fmt.Sprintf("ci.license.allowed[%d] %q is not one license name", i, name),
				"list each license as its own entry, by its SPDX identifier (MIT, Apache-2.0).")
		case seenLicense[n]:
			issue(at, parent, fmt.Sprintf("ci.license.allowed lists %q twice", n), "remove the duplicate.")
		}
		seenLicense[n] = true
	}

	seenModule := map[string]int{}
	for i, ex := range lic.Exceptions {
		entry := []string{"ci", "license", "exceptions", fmt.Sprintf("[%d]", i)}
		field := func(k string) []string { return append(append([]string(nil), entry...), k) }
		where := fmt.Sprintf("ci.license.exceptions[%d]", i)
		mod := strings.TrimSpace(ex.Module)
		switch {
		case mod == "":
			issue(field("module"), entry, where+" has no `module`",
				"name the one module this exception exempts (github.com/foo/bar). An exception never names a license: add an acceptable license to ci.license.allowed instead.")
		case module.CheckImportPath(mod) != nil:
			issue(field("module"), entry, fmt.Sprintf("%s: %q is not a module path: %v", where, ex.Module, module.CheckImportPath(mod)),
				"use the module path exactly as go.mod requires it (github.com/foo/bar).")
		default:
			if first, dup := seenModule[mod]; dup {
				issue(field("module"), entry, fmt.Sprintf("%s exempts %s, which ci.license.exceptions[%d] already exempts", where, mod, first),
					"keep one entry per module, carrying the reason that still holds.")
			} else {
				seenModule[mod] = i
			}
		}
		if strings.TrimSpace(ex.Reason) == "" {
			issue(field("reason"), entry, fmt.Sprintf("%s (%s) has no `reason`", where, mod),
				"say why this module is acceptable — its license and the terms you use it under. An unexplained exception cannot be reviewed.")
		}
		if s := strings.TrimSpace(ex.Expires); s != "" {
			if _, err := time.Parse(time.DateOnly, s); err != nil {
				issue(field("expires"), entry, fmt.Sprintf("%s (%s) has an unparseable `expires: %s`", where, mod, ex.Expires),
					"write the review date as YYYY-MM-DD, quoted (\"2027-06-01\"), or remove `expires`.")
			}
		}
	}
	return out
}
