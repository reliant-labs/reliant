package forgereach

import "testing"

func TestIsGenerated(t *testing.T) {
	cases := map[string]bool{
		"internal/templates/project/Taskfile.yml.tmpl":        true,
		"internal/generator/orm.go":                           true,
		"internal/codegen/writers.go":                         true,
		"internal/cli/scaffold/scaffold.go":                   true,
		"internal/cli/generate_pipeline.go":                   true,
		"internal/templates/project/skills/forge/db/SKILL.md": false,
		"internal/generator/orm_test.go":                      false,
		"internal/generator/testdata/golden/x.go":             false,
		"internal/cli/lint/lint.go":                           false,
		"internal/cli/ci.go":                                  false,
		"docs/concepts.md":                                    false,
		"pkg/orm/orm.go":                                      false,
	}
	for f, want := range cases {
		if got := isGenerated(f); got != want {
			t.Errorf("isGenerated(%q) = %v, want %v", f, got, want)
		}
	}
}

func TestReachSummary(t *testing.T) {
	if got := (Reach{}).Summary(); got != "nothing a consumer sees" {
		t.Errorf("empty summary = %q", got)
	}
	r := Reach{All: true, Packages: []string{"a"}, Generated: []string{"b"}}
	if !r.API() || !r.Any() {
		t.Error("go.mod change and package change must reach the API")
	}
	if (Reach{Generated: []string{"b"}}).API() {
		t.Error("generator-only change does not change the Go API")
	}
	if !(Reach{Generated: []string{"b"}}).Any() {
		t.Error("generator-only change still reaches consumers")
	}
}

func TestRequirementsIgnoresIndirectPromotionAndComments(t *testing.T) {
	before := "module m\n\ngo 1.27\n\nrequire (\n\texample.com/a v1.0.0\n\texample.com/x v0.1.0 // indirect\n)\n"
	promoted := "module m\n\ngo 1.27\n\nrequire (\n\texample.com/a v1.0.0\n\texample.com/x v0.1.0\n)\n// a comment\n"
	bumped := "module m\n\ngo 1.27\n\nrequire (\n\texample.com/a v1.1.0\n\texample.com/x v0.1.0 // indirect\n)\n"
	replaced := before + "replace example.com/a => ../a\n"
	eq := func(a, b string) bool {
		x, y := requirements([]byte(a)), requirements([]byte(b))
		if len(x) != len(y) {
			return false
		}
		for k, v := range x {
			if y[k] != v {
				return false
			}
		}
		return true
	}
	if !eq(before, promoted) {
		t.Error("promoting a module from indirect to direct changes no build")
	}
	if eq(before, bumped) {
		t.Error("a version bump must differ")
	}
	if eq(before, replaced) {
		t.Error("a new replace must differ")
	}
	if requirements([]byte("not a go.mod {{{")) != nil {
		t.Error("an unparseable go.mod must yield nil so the caller treats it as changed")
	}
}

func TestReachSummaryNamesEveryReason(t *testing.T) {
	cases := []struct {
		name string
		r    Reach
		want string
	}{
		{"dependencies only", Reach{All: true}, "the module's dependencies changed"},
		{"packages only", Reach{Packages: []string{"a", "b"}}, "2 package(s) a consumer imports"},
		{"generated only", Reach{Generated: []string{"x"}}, "1 file(s) that change generated code"},
		{"all three in a fixed order", Reach{All: true, Packages: []string{"a"}, Generated: []string{"x", "y"}},
			"the module's dependencies changed, 1 package(s) a consumer imports, 2 file(s) that change generated code"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.Summary(); got != tc.want {
				t.Errorf("Summary() = %q, want %q", got, tc.want)
			}
		})
	}
}
