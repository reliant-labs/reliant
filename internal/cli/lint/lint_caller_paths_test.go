package lint

import (
	"slices"
	"strconv"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/linter/callerpath"
)

const callerPathSrc = `package x

import (
	"path/filepath"
	"runtime"
)

func dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(file)
}
`

// The lane reads every .go file (not only tests), skips the directories the
// go-markers lane skips, and honours a reasoned suppression.
func TestCallerPathsLint_WalksAndSkips(t *testing.T) {
	root := t.TempDir()
	writeGoMarkerFixture(t, root, map[string]string{
		"internal/x/x_test.go":           callerPathSrc,
		"cmd/tool/main.go":               callerPathSrc,
		"internal/x/testdata/fixture.go": callerPathSrc,
		"vendor/v/v.go":                  callerPathSrc,
		"gen/g/g.go":                     callerPathSrc,
		"node_modules/n/n.go":            callerPathSrc,
		".hidden/h.go":                   callerPathSrc,
		"internal/sup/sup.go": `package sup

import (
	"path/filepath"
	"runtime"
)

func dir() string {
	// forge:lint-disable-next-line forge-runtime-caller-path: a dev-only helper, never built trimmed
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(file)
}
`,
	})

	findings, err := collectCallerPathFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range findings {
		got = append(got, f.File+":"+strconv.Itoa(f.Line))
		if f.Rule != callerpath.RuleID || f.Severity != "error" {
			t.Errorf("%s:%d = %s/%s, want %s/error", f.File, f.Line, f.Rule, f.Severity, callerpath.RuleID)
		}
	}
	if want := []string{"cmd/tool/main.go:9", "internal/x/x_test.go:9"}; !slices.Equal(got, want) {
		t.Errorf("findings = %v, want %v", got, want)
	}
}

// The rule warns at the release that ships it and gates from the next.
func TestCallerPathsLint_WarnsFirst(t *testing.T) {
	root := t.TempDir()
	writeGoMarkerFixture(t, root, map[string]string{"internal/x/x_test.go": callerPathSrc})
	fs, gated, err := collectCallerPathsJSON(root)
	if err != nil || !gated || len(fs) != 1 {
		t.Fatalf("collectCallerPathsJSON = %v, %v, %v; want one gating finding", fs, gated, err)
	}
	ro := lintRollouts[callerpath.RuleID]
	if ro.IntroducedIn != "v1.11.0" || ro.GatesFrom != "v1.12.0" {
		t.Fatalf("rollout = %+v, want introduced v1.11.0, gating from v1.12.0", ro)
	}
	for _, tc := range []struct {
		pin       string
		wantGated bool
	}{{"v1.11.0", false}, {"v1.12.0", true}} {
		cfg := &config.ProjectConfig{ModulePath: "example.com/app", ForgeVersion: tc.pin}
		out, g := gradeProjectLane(cfg, slices.Clone(fs), gated)
		if g != tc.wantGated {
			t.Errorf("pin %s: gated = %v, want %v (findings %+v)", tc.pin, g, tc.wantGated, out)
		}
	}
}
