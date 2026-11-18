// Regression guard for the kalshi-trader migration round friction
// reports that surfaced the same root cause from four separate task
// lanes:
//
//   - add-cycle-run-entity: `forge generate` final validate step fails
//     with `undefined: orm.TypeDoublePrecision` across every gen/db/v1
//     /*.pb.orm.go file. The pinned forge/pkg in go.mod predates the
//     symbol the in-PATH protoc-gen-forge emits.
//   - add-list-algo-predictions-rpc: same wedge, blocked the project-
//     wide go build sanity check.
//   - forge-doctor-observe-check: same wedge, cascaded into 5 downstream
//     packages.
//   - add-config-parity-test: same wedge, forced the user to scope
//     builds to a narrow subdirectory.
//
// The friction was not just the failure itself — the fix-hint
// `runGoBuildValidate` emitted ("ensure all referenced types are
// imported and re-run 'forge generate'") was wrong. Re-running generate
// produces the same broken output; the actual fix is to bump the
// forge/pkg pin in both root go.mod and gen/go.mod. This test pins the
// hint-selection so the next round's agent sees the actionable
// remediation immediately.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoBuildValidateFixHint(t *testing.T) {
	cases := []struct {
		name      string
		errOutput string
		// wantContains is a list of substrings that MUST appear in the
		// returned hint. Multiple substrings let us assert both the
		// classification ("forge/pkg pin is older") AND the actionable
		// command (`go get github.com/reliant-labs/forge@latest` — never the
		// retired forge/pkg module).
		wantContains []string
	}{
		{
			name: "orm-TypeDoublePrecision-skew",
			errOutput: `gen/db/v1/model_performance.pb.orm.go:42:18: undefined: orm.TypeDoublePrecision
gen/db/v1/cycle_run.pb.orm.go:55:18: undefined: orm.TypeDoublePrecision`,
			wantContains: []string{
				"forge/pkg pin is older",
				"go get github.com/reliant-labs/forge@latest",
				"go mod tidy",
				"gen/",
			},
		},
		{
			name:      "orm-TypeReal-skew",
			errOutput: `gen/db/v1/model_performance.pb.orm.go:48:18: undefined: orm.TypeReal`,
			wantContains: []string{
				"forge/pkg pin is older",
				"go get github.com/reliant-labs/forge@latest",
			},
		},
		{
			name:      "pkg-config-undefined",
			errOutput: `bootstrap.go:42:18: undefined: pkg/config.Foo`,
			wantContains: []string{
				"proto/<project>/config/v1/",
				"annotated config fields",
			},
		},
		{
			// Code written when the aggregate store was an interface.
			name:         "aggregate-store-used-as-an-interface",
			errOutput:    `internal/report/fake.go:12:22: cannot use &fakeStore{} (value of type *fakeStore) as db.Store value in variable declaration`,
			wantContains: []string{"db.Store", "is a struct since forge v1.0.12", "db.<Entity>Store", "db.NewStore(handle)"},
		},
		{
			// Another package's Store is not the generated aggregate.
			name:         "other-store-not-claimed",
			errOutput:    `internal/report/fake.go:12:22: cannot use x (value of type int) as activity.Store value in assignment`,
			wantContains: []string{"is hand-written"},
		},
		{
			name:         "aggregate-mock-gone",
			errOutput:    `internal/report/service_test.go:30:9: undefined: report.MockStore`,
			wantContains: []string{"no generated MockStore", "per-entity stores"},
		},
		{
			name:      "empty-stderr",
			errOutput: "",
			wantContains: []string{
				"ensure all referenced types are imported",
			},
		},
		{
			// A hand-written file is fixed where it stands — and the
			// hint names it, rather than guessing at imports.
			name:      "hand-written-file-named",
			errOutput: "# example.com/app/internal/foo\ninternal/foo/bar.go:42:18: undefined: SomeUnrelatedSymbol",
			wantContains: []string{
				"internal/foo/bar.go is hand-written",
				"re-run 'forge generate'",
			},
		},
		{
			// A generated file is fixed at its inputs, never by editing it.
			name:      "generated-file-named",
			errOutput: "internal/handlers/x/handlers_crud_ops_gen.go:9:2: undefined: db.UpdateThing",
			wantContains: []string{
				"internal/handlers/x/handlers_crud_ops_gen.go is generated — do not edit it",
				"forge bug",
			},
		},
		{
			// compose.go is the author's, but forge injects lines into it.
			// Calling it "hand-written" blamed the author for a call forge
			// had just emitted (a component it could not construct).
			name:      "reconciled-file-named",
			errOutput: "internal/app/compose.go:58:14: undefined: foo.New",
			wantContains: []string{
				"internal/app/compose.go is yours, but forge reconciles it",
				"forge injected",
				"forge bug worth reporting",
			},
		},
		{
			name:      "contract-derived-mock-names-its-source",
			errOutput: "internal/releasehealth/mock_gen.go:25:17: cannot use (*MockService)(nil) as Service value: *MockService does not implement Service (missing method Warm)",
			wantContains: []string{
				"generated from contract.go",
				"forge rewrites it on every 'forge generate'",
				"did not rewrite it",
			},
		},
		{
			name:      "no-coordinate-fallthrough",
			errOutput: "go: updates to go.mod needed; to update it:\n\tgo mod tidy",
			wantContains: []string{
				"ensure all referenced types are imported",
			},
		},
	}

	originOf := func(rel string) goSourceOrigin {
		switch {
		case strings.HasSuffix(rel, "_gen.go"):
			return originGenerated
		case rel == "internal/app/compose.go":
			return originReconciled
		}
		return originHandWritten
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := goBuildValidateFixHint(tc.errOutput, originOf)
			if strings.Contains(got, "hand-written") && originOf(errorLineFile(tc.errOutput)) == originReconciled {
				t.Errorf("a reconciled file must not be called hand-written: %q", got)
			}
			if got == "" {
				t.Fatalf("goBuildValidateFixHint returned empty hint for %q", tc.errOutput)
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("goBuildValidateFixHint(%q) = %q, want substring %q",
						tc.errOutput, got, want)
				}
			}
		})
	}
}

// TestGoBuildValidateFixHint_OrmSkewBeatsOtherClassifiers pins the
// pattern-match ORDER: when multiple classifiers could match the same
// stderr blob, the orm.Type* skew classifier must win because the
// other hints would send the user down dead-end remediation paths
// ("re-run forge generate" produces the same broken output, since the
// generator is what emitted the bad reference).
func TestGoBuildValidateFixHint_OrmSkewBeatsOtherClassifiers(t *testing.T) {
	// Stderr that contains BOTH the orm.Type marker AND a `pkg/config`
	// reference. The orm marker wins.
	mixed := `gen/db/v1/foo.pb.orm.go:42:18: undefined: orm.TypeDoublePrecision
internal/bootstrap.go:90:5: imports pkg/config: foo.go:1:1: package config`
	got := goBuildValidateFixHint(mixed, nil)
	if !strings.Contains(got, "forge/pkg pin is older") {
		t.Errorf("orm.Type* classifier did not win against pkg/config classifier:\n  got: %q", got)
	}
	if strings.Contains(got, "config/v1/ has annotated") {
		t.Errorf("orm.Type* classifier should suppress the pkg/config hint; got mixed message: %q", got)
	}
}

// goSourceOriginOf must recognise the header forge actually writes on its
// reconciled files, so the hint stops calling compose.go hand-written. Read
// from the templates themselves: a reworded header must fail here, not
// silently fall back to "hand-written".
func TestGoSourceOriginOf(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tmpl := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", "templates", "project", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cases := []struct {
		name string
		path string
		want goSourceOrigin
	}{
		{"compose.go template", write("compose.go", tmpl("compose.go.tmpl")), originReconciled},
		{"lifecycle.go template", write("lifecycle.go", tmpl("lifecycle.go.tmpl")), originReconciled},
		{"go generated header", write("x.pb.go", "// Code generated by protoc-gen-go. DO NOT EDIT.\n\npackage x\n"), originGenerated},
		{"plain", write("y.go", "package y\n"), originHandWritten},
		{"unreadable", filepath.Join(dir, "missing.go"), originHandWritten},
	}
	for _, tc := range cases {
		if got := goSourceOriginOf(tc.path); got != tc.want {
			t.Errorf("%s: goSourceOriginOf = %d, want %d", tc.name, got, tc.want)
		}
	}
}
