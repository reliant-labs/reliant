package scaffolds

import (
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot is the forge checkout, three levels up from this package.
func repoRoot() string { return filepath.Join("..", "..", "..") }

// TestCrossTierSymbols is the GATE. It is a test rather than a `forge lint`
// step on purpose:
//
//   - the invariant is about forge's OWN template tree, so it is only ever
//     checkable inside this repo — `forge lint` in a generated project would
//     have nothing to walk;
//   - `forge lint`'s scaffold rules are advisory output a user may reasonably
//     ignore, and this one must not be ignorable: a violation means forge
//     emits a tree that does not compile;
//   - `go test ./...` already gates every merge, so the check runs on the one
//     path every template edit passes through.
//
// CrossTierLintRoot is exported with BannerLintRoot's signature so the same
// walk can additionally be surfaced as a `forge lint` step without touching
// anything here.
func TestCrossTierSymbols(t *testing.T) {
	t.Parallel()
	res, err := CrossTierLintRoot(repoRoot())
	if err != nil {
		t.Fatalf("CrossTierLintRoot returned error: %v", err)
	}
	for _, f := range res.Findings {
		if f.Severity == SeverityError {
			t.Errorf("cross-tier violation\n  [%s] %s\n  %s", f.Rule, f.Path, f.Message)
			continue
		}
		t.Logf("warning [%s] %s: %s", f.Rule, f.Path, f.Message)
	}
}

// TestCrossTierLintRoot_Violation proves the lint is not vacuous: a Tier-1
// template referencing a symbol family that only a scaffold-once template
// projects is an error, and the message names both files and both tiers.
func TestCrossTierLintRoot_Violation(t *testing.T) {
	t.Parallel()
	res, err := CrossTierLintRoot(filepath.Join("testdata", "cross_tier", "violation"))
	if err != nil {
		t.Fatalf("CrossTierLintRoot returned error: %v", err)
	}
	if !findingMatches(res.Findings, "cross-tier-derived-symbol") {
		t.Fatalf("expected a cross-tier-derived-symbol finding, got: %+v", res.Findings)
	}
	var msg string
	for _, f := range res.Findings {
		if f.Rule == "cross-tier-derived-symbol" {
			if f.Severity != SeverityError {
				t.Errorf("cross-tier-derived-symbol must be an error, got %q", f.Severity)
			}
			// Slash-separated on every OS: relPath normalizes it (scaffolds.go).
			if f.Path != "internal/templates/project/cmd-widget-group.go.tmpl" {
				t.Errorf("finding should be pinned to the referencing template, got %q", f.Path)
			}
			msg = f.Message
		}
	}
	for _, want := range []string{
		"cmd-widget-group.go.tmpl",           // the referencing file
		"lifecycle.go.tmpl",                  // the defining file
		"Tier-1 (regenerated every run",      // the referencing file's tier
		"scaffold-once (written once at bir", // the defining file's tier
		`"Widget{{.FieldName}}"`,             // the symbol family
		"move the definition into a Tier-1 (regenerated-every-run) template",
		"move the reference out of",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q\ngot: %s", want, msg)
		}
	}
}

// TestCrossTierLintRoot_DefinitionIsTier1 is the same fixture pair with fix
// (1) applied — the projection moved into a Tier-1 template. The lint goes
// quiet.
func TestCrossTierLintRoot_DefinitionIsTier1(t *testing.T) {
	t.Parallel()
	res, err := CrossTierLintRoot(filepath.Join("testdata", "cross_tier", "definition_is_tier1"))
	if err != nil {
		t.Fatalf("CrossTierLintRoot returned error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("expected zero findings once the projection lives in a Tier-1 template, got %d: %+v",
			len(res.Findings), res.Findings)
	}
}

// TestCrossTierLintRoot_BothScaffoldOnce pins a semantic that is easy to get
// wrong: the rule is about WHERE THE DEFINITION LIVES, not about the two
// files being in different tiers. Making the caller scaffold-once as well
// does NOT clear the finding — the projection is still frozen, and the
// per-component caller is still written at a moment the definer is not.
func TestCrossTierLintRoot_BothScaffoldOnce(t *testing.T) {
	t.Parallel()
	res, err := CrossTierLintRoot(filepath.Join("testdata", "cross_tier", "both_scaffold_once"))
	if err != nil {
		t.Fatalf("CrossTierLintRoot returned error: %v", err)
	}
	if !findingMatches(res.Findings, "cross-tier-derived-symbol") {
		t.Fatalf("same-tier does not clear a frozen projection; expected a finding, got: %+v", res.Findings)
	}
}

// TestCrossTierLintRoot_Reconciled exercises the sanctioned escape valve: a
// generate-time reconciler that injects the accessor into the existing
// scaffold-once file. The exemption is earned by the reconciler's presence in
// internal/codegen, not asserted in an allowlist — delete it and the fixture
// is the `violation` fixture again.
func TestCrossTierLintRoot_Reconciled(t *testing.T) {
	t.Parallel()
	res, err := CrossTierLintRoot(filepath.Join("testdata", "cross_tier", "reconciled"))
	if err != nil {
		t.Fatalf("CrossTierLintRoot returned error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a reconciled projection must not be reported, got %d: %+v", len(res.Findings), res.Findings)
	}
}

// TestCrossTierLintRoot_OutsideForge: the walk is a no-op where there is no
// template tree, so the exported entry point is safe to call from anywhere.
func TestCrossTierLintRoot_OutsideForge(t *testing.T) {
	t.Parallel()
	res, err := CrossTierLintRoot(t.TempDir())
	if err != nil {
		t.Fatalf("CrossTierLintRoot returned error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("expected zero findings outside the forge repo, got %+v", res.Findings)
	}
}

func TestFamilyKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		want string
		ok   bool
	}{
		// Derived families normalize across differing action bodies, so a
		// definition and a reference match even when they read different
		// fields of different data shapes.
		{"Worker{{.FieldName}}", "Worker" + wildcard, true},
		{"Mount{{.MountFieldName}}", "Mount" + wildcard, true},
		{"handleWebhook{{.Name | pascalCase}}", "handleWebhook" + wildcard, true},
		{"New{{.FieldName}}Cmd", "New" + wildcard + "Cmd", true},
		{"{{.EntityLower}}ToProto", wildcard + "ToProto", true},
		// Adjacent actions collapse to one wildcard.
		{"Get{{.A}}{{.B}}Value", "Get" + wildcard + "Value", true},
		// Fixed names are out of scope by design (see cross_tier.go).
		{"NewComponents", "", false},
		{"AllWorkers", "", false},
		// A wholly-interpolated name carries no signal.
		{"{{.FieldName}}", "", false},
		{"{{.Name}}", "", false},
		// One static character is below the floor.
		{"{{.A}}x", "", false},
	}
	for _, c := range cases {
		got, ok := familyKey(c.name)
		if ok != c.ok || got != c.want {
			t.Errorf("familyKey(%q) = (%q, %v), want (%q, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestScanTemplateSymbols_RangeScoping(t *testing.T) {
	t.Parallel()
	src := `package app
{{if .HasWorkers}}
func (c *Components) Solo{{.FieldName}}() Worker { return nil }
{{end}}
{{range .Workers}}
func (c *Components) Worker{{.FieldName}}() Worker {
	return wrap(c.{{.FieldName}})
}
{{end}}
func (c *Components) AllWorkers() []Worker {
{{- range .Workers}}
	c.Worker{{.FieldName}}(),
{{- end}}
}
`
	defs, refs := scanTemplateSymbols(src)

	byKey := map[string]symbolSite{}
	for _, d := range defs {
		byKey[d.key] = d
	}
	// A definition guarded by {{if}} is NOT a projection: the set does not
	// grow with discovered state, so {{if}} must not increment range depth.
	solo, ok := byKey["Solo"+wildcard]
	if !ok {
		t.Fatalf("expected a Solo definition, got %+v", defs)
	}
	if solo.inRange {
		t.Errorf("a definition inside {{if}} must not count as a projection")
	}
	// A definition inside {{range}} is a projection.
	worker, ok := byKey["Worker"+wildcard]
	if !ok {
		t.Fatalf("expected a Worker definition, got %+v", defs)
	}
	if !worker.inRange {
		t.Errorf("a definition inside {{range}} must count as a projection")
	}
	// Fixed-name declarations are not recorded at all.
	if _, bad := byKey["AllWorkers"]; bad {
		t.Errorf("fixed-name declarations must not be recorded as derived families")
	}

	var sawWorkerRef bool
	for _, r := range refs {
		if r.key == "Worker"+wildcard {
			sawWorkerRef = true
		}
	}
	if !sawWorkerRef {
		t.Errorf("expected a Worker%s selector reference, got %+v", wildcard, refs)
	}
}

// TestScanTemplateSymbols_MethodExpression pins the shape that used to be
// invisible. forge's own cmd-svc-group.go.tmpl hands Serve a METHOD
// EXPRESSION rather than calling it:
//
//	cmd.ServeSpec{Mount: (*app.Components).Mount{{.MountFieldName}}}
//
// A matcher keyed on `.Name(` sees only `Serve` and `Context` on that line,
// so the Mount<Svc> reference — a genuine cross-tier pin into
// mounts_services_gen.go.tmpl — did not exist as far as the lint was concerned.
// That is not a cosmetic miss: it let a proposed Tier-2 reclassification of
// mounts_services.go read as SAFE with a fully green suite, while in fact
// shipping the `has no field or method Mount<Svc>` build break for any
// service added after project birth.
func TestScanTemplateSymbols_MethodExpression(t *testing.T) {
	t.Parallel()
	src := `package services

func New{{.FieldName}}Cmd(deps cmd.Deps) *cobra.Command {
	return newCmd(func(c *cobra.Command, args []string) error {
		deps.Logger = c.Logger{{.FieldName}}()
		return cmd.Serve(c.Context(), deps, cmd.ServeSpec{Mount: (*app.Components).Mount{{.MountFieldName}}})
	})
}
`
	_, refs := scanTemplateSymbols(src)

	var got *symbolSite
	for i, r := range refs {
		if r.key == "Mount"+wildcard {
			got = &refs[i]
		}
	}
	if got == nil {
		t.Fatalf("method expression (*app.Components).Mount{{...}} must be recorded as a reference, got %+v", refs)
	}
	if got.display != "Mount{{.MountFieldName}}" {
		t.Errorf("display = %q, want %q", got.display, "Mount{{.MountFieldName}}")
	}
	if got.line != 6 {
		t.Errorf("line = %d, want 6 (the ServeSpec line)", got.line)
	}
	// Ordinary derived selector CALLS must still be seen — the method
	// expression pass is additive, not a replacement.
	found := false
	for _, r := range refs {
		if r.key == "Logger"+wildcard {
			found = true
		}
	}
	if !found {
		t.Errorf("selector-call references must survive the method-expression pass; missing %q in %+v",
			"Logger"+wildcard, refs)
	}
	// Refs come back in source order regardless of which pattern matched
	// them, so the earliest real reference is still reported first.
	for i := 1; i < len(refs); i++ {
		if refs[i-1].line > refs[i].line {
			t.Errorf("refs must stay ordered by line, got %+v", refs)
			break
		}
	}
}

// TestMethodExprRE_Precision guards the deliberate narrowness of the rule.
// The parenthesized receiver IS the signal: `(*T).M` has no reading other
// than a method expression, because a field cannot be selected off a type.
// A bare `x.Mount{{.Name}}` is textually identical to reading a struct
// field — the far more common spelling — so matching it would flood the
// lint. Measured against the live template tree the broad rule added 41
// spurious reference records and zero findings.
func TestMethodExprRE_Precision(t *testing.T) {
	t.Parallel()
	cases := []struct {
		src   string
		match bool
		why   string
	}{
		{`Mount: (*app.Components).Mount{{.FieldName}}`, true, "pointer method expression, qualified type"},
		{`Mount: (*Components).Mount{{.FieldName}}`, true, "pointer method expression, local type"},
		{`Mount: (* app.Components).Mount{{.FieldName}}`, true, "gofmt-hostile spacing after the star still binds"},
		{`f := (app.Components).Mount{{.FieldName}}`, true, "value method expression on a qualified type"},
		{`Workers: (*app.Components).All{{.FieldName}}Workers,`, true, "derived name with a static suffix"},
		// Precision floor: these must NOT match.
		{`row := got.Msg.Get{{.EntityName}}()`, false, "chained field read into a protoc getter is not a method expression"},
		{`w.deps.Logger.With{{.FieldName}}`, false, "bare chained selector read carries no type signal"},
		{`c.Mount{{.FieldName}}(mux)`, false, "an ordinary selector call is refRE's job, not this pattern's"},
		{`v := (x).Mount{{.FieldName}}`, false, "a parenthesized VALUE is not a type; excluded on purpose"},
	}
	for _, c := range cases {
		got := methodExprRE.MatchString(c.src)
		if got != c.match {
			t.Errorf("methodExprRE.MatchString(%q) = %v, want %v — %s", c.src, got, c.match, c.why)
		}
	}
}

// TestScanTemplateSymbols_MethodExpressionInComment: a method expression
// spelled in prose does not compile, so it is not a reference. forge's
// templates describe their own wiring in the header — cmd-svc-group.go.tmpl
// names `(*app.Components).Mount{{.MountFieldName}}` in its doc comment 29
// lines above the real one — so without this the reported line would be the
// comment rather than the line that actually breaks.
func TestScanTemplateSymbols_MethodExpressionInComment(t *testing.T) {
	t.Parallel()
	src := `package services

// cmd.Serve() with the TYPED mount method expression (*app.Components).Mount{{.MountFieldName}}
func New{{.FieldName}}Cmd() {}
`
	_, refs := scanTemplateSymbols(src)
	for _, r := range refs {
		if r.key == "Mount"+wildcard {
			t.Errorf("a method expression inside a // comment must not count as a reference, got %+v", r)
		}
	}
}

func TestResolveTemplateTier(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		rel  string
		head string
		want templateTier
	}{
		{
			name: "canonical generated banner wins over the name list",
			rel:  "internal/templates/project/providers.go.tmpl",
			head: "// Code generated by forge. DO NOT EDIT.\n",
			want: tier1Generated,
		},
		{
			name: "canonical scaffold-once banner",
			rel:  "internal/templates/whatever.go.tmpl",
			head: "// yours: scaffolded once, never touched again — forge will not overwrite this file\n",
			want: tier2Scaffold,
		},
		{
			name: "write-once banner spelling used by the cmd-tree + app composition scaffolds",
			rel:  "internal/templates/project/lifecycle.go.tmpl",
			head: "// forge-scaffold (USER-OWNED): forge writes this file ONCE (write-if-absent),\n",
			want: tier2Scaffold,
		},
		{
			name: "forge:allow marks a user-owned skeleton",
			rel:  "internal/templates/whatever.go.tmpl",
			head: "//forge:allow\n",
			want: tier3UserOwned,
		},
		{
			name: "no banner falls back to the name classifier",
			rel:  "internal/templates/service/handlers_gen.go.tmpl",
			head: "package svc\n",
			want: tier1Generated,
		},
	}
	for _, c := range cases {
		if got := resolveTemplateTier(c.rel, c.head); got != c.want {
			t.Errorf("%s: resolveTemplateTier(%q) = %d, want %d", c.name, c.rel, got, c.want)
		}
	}
}
