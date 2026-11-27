package slowtest_test

import (
	"reflect"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/reliant-labs/forge/internal/linter/slowtest"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// One analysistest run covers every fixture package: the fixtures share their
// dependencies (helper, the testkit and testcontainers stand-ins), whose facts
// are checked too.
func TestAnalyzer(t *testing.T) {
	testkit.Slow(t, "loads the fixture packages through `go list` (analysistest)",
		testkit.Inputs("internal/linter/slowtest/**"))
	results := analysistest.Run(t, analysistest.TestData(), slowtest.Analyzer, "a", "mainguard", "helper")

	// The inventory a selection tool reads: every marked test of package a
	// with the declarations it reaches, its own or a helper's.
	got := map[string][]slowtest.Declaration{}
	for _, r := range results {
		if r.Pass.Pkg.Path() != "a" || r.Result == nil {
			continue
		}
		for _, test := range r.Result.(*slowtest.Result).Tests {
			if !strings.HasSuffix(test.Position.Filename, "a_test.go") || test.Position.Line == 0 {
				t.Errorf("%s has no usable position: %+v", test.Name, test.Position)
			}
			for i := range test.Declarations {
				test.Declarations[i].At = "" // positions are exercised above
			}
			got[test.Name] = test.Declarations
		}
	}
	want := map[string][]slowtest.Declaration{
		"TestMarkedWithTestkit":          {{Why: "builds the module", Inputs: []string{"go.mod", "internal/**"}}},
		"TestMarkedWithShortGuard":       nil,
		"TestBypassesUnderShort":         nil,
		"TestMarkedInHelper":             {{Why: "inits a git repo"}},
		"TestMarkedByOtherPackageHelper": {{Why: "renders prod through forge", Inputs: []string{"deploy/**", "forge.yaml"}}},
		"TestMarkedByRawGuardHelper":     nil,
		"TestInputsNotConstant":          {{Why: "renders", Inputs: []string{"forge.yaml"}}},
		"TestInputsThroughVariable":      {{Why: "renders"}},
		"TestInputsSpread":               {{Why: "renders"}},
	}
	if !reflect.DeepEqual(normalize(got), normalize(want)) {
		t.Errorf("slow-test inventory of package a:\n got: %+v\nwant: %+v", got, want)
	}
}

// normalize maps an empty declaration list to nil so the comparison is about
// content.
func normalize(m map[string][]slowtest.Declaration) map[string][]slowtest.Declaration {
	out := map[string][]slowtest.Declaration{}
	for k, v := range m {
		if len(v) == 0 {
			v = nil
		}
		out[k] = v
	}
	return out
}
