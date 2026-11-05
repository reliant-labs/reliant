package contract

// One package, one declaration.
//
// `//forge:exclude-contract` and `//forge:not-a-component` are properties of a
// PACKAGE, the way its package clause is: they apply from whichever file
// declares them. A package may repeat one (a file that grew a package doc
// often copies it), but every repetition must say the same thing — the same
// reason, and the same `forge:lint-disable` allowances in its comment block.
//
// The gate used to read the FIRST reasoned marker by file name and honour only
// the allowances above that one. Which file sorts first is an accident of
// naming, so moving one file out of a package (reliant's approval.go leaving
// internal/grpc/services) silently moved the package's declaration to another
// file that lacked the allowance, and `forge lint` started refusing a package
// nothing about had changed. Two declarations that disagree are now refused,
// naming both, so the package's answer never depends on file order.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/linter/finding"
)

// Rule IDs of the one-declaration check.
const (
	// RuleExcludeConflict refuses two `//forge:exclude-contract` markers in
	// one package that disagree on their reason or their allowances.
	RuleExcludeConflict = "forge-exclude-contract-conflict"
	// RuleNotAComponentConflict is the same for `//forge:not-a-component`.
	RuleNotAComponentConflict = "forge-not-a-component-conflict"
)

// declaration is what one marker says: its reason and the gate rules a
// suppression in its comment block allows.
type declaration struct {
	marker  codegen.ReasonedMarker
	allowed []string // in the order of the gate's rules
}

// packageDeclaration reduces a package's markers for one directive to the
// declaration the package makes. With one marker, or several that agree, it
// returns the first. With two that disagree it returns a finding naming both,
// anchored on the second; the gate's other checks then wait for the package
// to say one thing.
//
// rules are the gate rules an allowance in a marker's block can suppress;
// allowing one of them on one marker and not another is a disagreement, since
// the refusal would then come and go with file order.
func packageDeclaration(pkgPath, directive string, markers []codegen.ReasonedMarker, rules []string, conflictRule string) (codegen.ReasonedMarker, *ExcludeFinding) {
	decls := make([]declaration, 0, len(markers))
	files := map[string]string{}
	for _, m := range markers {
		content, ok := files[m.File]
		if !ok {
			data, err := os.ReadFile(m.File)
			if err == nil {
				content = string(data)
			}
			files[m.File] = content
		}
		decls = append(decls, declaration{marker: m, allowed: allowedAt(content, m, rules)})
	}
	first := decls[0]
	for _, d := range decls[1:] {
		if d.marker.Reason == first.marker.Reason && strings.Join(d.allowed, ",") == strings.Join(first.allowed, ",") {
			continue
		}
		return first.marker, &ExcludeFinding{
			Rule:     conflictRule,
			Severity: finding.SeverityError,
			File:     d.marker.File,
			Line:     d.marker.Line,
			Message: fmt.Sprintf("package %s declares `//%s` twice, and the two disagree: %s says %s, %s says %s. "+
				"A package directive applies to the whole package from whichever file declares it, so the "+
				"package must give one answer",
				pkgPath, directive, markerPos(first.marker), first.describe(), markerPos(d.marker), d.describe()),
			FixHint: fmt.Sprintf("declare it once, in the package's doc.go — the reason on the `//%s: <why>` line and any "+
				"`forge:lint-disable-next-line` allowance in the same comment block — and delete the other markers. "+
				"A repetition is accepted only when it is identical", directive),
		}
	}
	return first.marker, nil
}

// allowedAt returns the rules (of rules) a suppression in m's comment block
// allows, by the same reading the gate uses to honour them.
func allowedAt(content string, m codegen.ReasonedMarker, rules []string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	var out []string
	for _, rule := range rules {
		kept, _ := suppressAtMarker(content, lines, ExcludeFinding{
			Rule: rule, Severity: finding.SeverityError, File: m.File, Line: m.Line,
		})
		if !kept {
			out = append(out, rule)
		}
	}
	return out
}

func (d declaration) describe() string {
	reason := "no reason"
	if d.marker.Reason != "" {
		reason = fmt.Sprintf("%q", d.marker.Reason)
	}
	allows := "no allowance"
	if len(d.allowed) > 0 {
		allows = "an allowance for " + strings.Join(d.allowed, ", ")
	}
	return reason + " with " + allows
}

func markerPos(m codegen.ReasonedMarker) string {
	return fmt.Sprintf("%s:%d", filepath.Base(m.File), m.Line)
}
