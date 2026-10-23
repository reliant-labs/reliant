package suppress

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

// declaredSrc carries one of every directive shape.
var declaredSrc = strings.Join([]string{
	"package api", // 1
	"// forge:lint-disable-file whole-file-rule: generated-ish shim",                                               // 2
	"//forge:lint-disable-next-line forge-exclude-contract-outbound-io: 40-method DB layer; split tracked in #123", // 3
	"//forge:exclude-contract: hand-written data layer, split pending",                                             // 4
	"func a() {}", // 5
	"x := 1 //nolint:errcheck,gosec // checked by the caller", // 6
	"//nolint:revive", // 7
	"func b() {}",     // 8
	"// forge:lint-disable block-rule, other-rule: legacy section", // 9
	"c()",                             // 10
	"d()",                             // 11
	"// forge:lint-enable block-rule", // 12
	"// forge:lint-disable open-rule", // 13
	"e()",                             // 14
}, "\n")

func TestDeclarationsListsEveryDirectiveWithWhatItCovers(t *testing.T) {
	got := Declarations(declaredSrc)
	want := []Declared{
		{Rule: "whole-file-rule", Line: 2, Directive: TokenDisableFile, Covers: "the file", Reason: "generated-ish shim"},
		// gofmt-style placement: the directive covers the marker on line 4
		// and the code on line 5, as ParseFile does.
		{Rule: "forge-exclude-contract-outbound-io", Line: 3, Directive: TokenDisableNextLine, Covers: "lines 4-5", Reason: "40-method DB layer; split tracked in #123"},
		{Rule: "errcheck", Line: 6, Directive: TokenNolint, Covers: "line 6", Reason: "checked by the caller"},
		{Rule: "gosec", Line: 6, Directive: TokenNolint, Covers: "line 6", Reason: "checked by the caller"},
		{Rule: "revive", Line: 7, Directive: TokenNolint, Covers: "line 8"},
		{Rule: "block-rule", Line: 9, Directive: TokenDisable, Covers: "lines 9-11", Reason: "legacy section"},
		{Rule: "other-rule", Line: 9, Directive: TokenDisable, Covers: "line 9 to the end of the file", Reason: "legacy section"},
		{Rule: "open-rule", Line: 13, Directive: TokenDisable, Covers: "line 13 to the end of the file"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Declarations:\n got %+v\nwant %+v", got, want)
	}
}

// The inventory must describe what Apply actually does: a finding on every
// line an entry claims to cover is suppressed, and one on the line just past
// a bounded span is not.
func TestDeclarationsAgreeWithApply(t *testing.T) {
	lines := len(strings.Split(declaredSrc, "\n"))
	for _, d := range Declarations(declaredSrc) {
		from, to := coveredLines(t, d, lines)
		for l := from; l <= to; l++ {
			res := Apply(declaredSrc, []finding.Finding{warnFinding(d.Rule, l)})
			if len(res.Suppressed) != 1 {
				t.Errorf("%s (line %d) claims to cover %s, but a finding on line %d was not suppressed", d.Rule, d.Line, d.Covers, l)
			}
		}
		if d.Covers != "the file" && !strings.HasSuffix(d.Covers, "end of the file") {
			res := Apply(declaredSrc, []finding.Finding{warnFinding(d.Rule, to+1)})
			if len(res.Suppressed) != 0 {
				t.Errorf("%s (line %d) claims to cover %s, but line %d was suppressed too", d.Rule, d.Line, d.Covers, to+1)
			}
		}
	}
}

func coveredLines(t *testing.T, d Declared, lines int) (int, int) {
	t.Helper()
	var from, to int
	switch {
	case d.Covers == "the file":
		return 1, lines
	case strings.HasSuffix(d.Covers, " to the end of the file"):
		if _, err := fmt.Sscanf(d.Covers, "line %d to the end of the file", &from); err != nil {
			t.Fatal(err)
		}
		return from, lines
	case strings.HasPrefix(d.Covers, "lines "):
		parts := strings.SplitN(strings.TrimPrefix(d.Covers, "lines "), "-", 2)
		from, _ = strconv.Atoi(parts[0])
		to, _ = strconv.Atoi(parts[1])
		return from, to
	default:
		if _, err := fmt.Sscanf(d.Covers, "line %d", &from); err != nil {
			t.Fatal(err)
		}
		return from, from
	}
}
