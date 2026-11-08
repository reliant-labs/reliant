// File: internal/codegen/infra_assignability_test.go
//
// Tests for the by-TYPE Infra-field matcher used by the generated injector
// (inject_gen.go). The focus here is the GENERATE-ORDERING robustness
// (Fix B): a differently-named Infra field must prove assignable even when
// internal/app is mid-write this run, and a no-match must only go LOUD
// (MatchNoName → MissingProvider) when the absence is PROVABLE.

package codegen

import (
	"path/filepath"
	"testing"
)

// writeInfraMatcherProject scaffolds the minimum go.mod + internal/app +
// component package the Infra matcher loads jointly. Returns the project root.
func writeInfraMatcherProject(t *testing.T, appSrc, compSrc, role, pkg string) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module example.com/proj\n\ngo 1.23\n")
	mustWrite(t, filepath.Join(dir, "internal", "app", "providers.go"), appSrc)
	mustWrite(t, filepath.Join(dir, role, pkg, "contract.go"), compSrc)
	return dir
}

// TestInfraAssignability_DifferentlyNamedAssignable: the Infra field is
// named DIFFERENTLY from the Deps field but is assignable — the matcher must
// resolve it by TYPE (MatchAssignable), proving the exact-name workaround is
// no longer required.
func TestInfraAssignability_DifferentlyNamedAssignable(t *testing.T) {
	dir := writeInfraMatcherProject(t,
		`package app

import "example.com/proj/internal/handlers/notify"

type Infra struct {
	Email notify.Sender
}
`,
		`package notify

type Sender interface{ Send() }

type Deps struct {
	Mailer Sender
}
`,
		"internal/handlers", "notify",
	)
	m := NewInfraAssignabilityMatcher(dir)
	infra := map[string]InfraField{"Email": {Name: "Email", Type: "notify.Sender"}}
	field, kind := m.ResolveInfraField("internal/handlers", "notify", "Mailer", "Sender", infra)
	if kind != MatchAssignable || field != "Email" {
		t.Fatalf("ResolveInfraField = (%q, %v), want (Email, MatchAssignable)", field, kind)
	}
}

// TestInfraAssignability_MidWriteDifferentlyNamedStillProves: the SAME
// shape as above, but internal/app is mid-write — app_services_gen.go
// references the not-yet-regenerated Build symbol, so the package has a
// type error. The Email field's type still resolves (it doesn't depend on
// Build), so the matcher must STILL prove the assignable match rather than
// emit a spurious MissingProvider. This is the generate-ORDERING fix.
func TestInfraAssignability_MidWriteDifferentlyNamedStillProves(t *testing.T) {
	dir := writeInfraMatcherProject(t,
		`package app

import "example.com/proj/internal/handlers/notify"

type Infra struct {
	Email notify.Sender
}
`,
		`package notify

type Sender interface{ Send() }

type Deps struct {
	Mailer Sender
}
`,
		"internal/handlers", "notify",
	)
	// Mid-write: a generated file references Build, which inject_gen.go has
	// not yet (re)defined this run — internal/app does not type-check.
	mustWrite(t, filepath.Join(dir, "internal", "app", "app_services_gen.go"),
		"package app\n\nfunc touch() { _ = Build }\n")

	m := NewInfraAssignabilityMatcher(dir)
	infra := map[string]InfraField{"Email": {Name: "Email", Type: "notify.Sender"}}
	field, kind := m.ResolveInfraField("internal/handlers", "notify", "Mailer", "Sender", infra)
	if kind != MatchAssignable || field != "Email" {
		t.Fatalf("mid-write ResolveInfraField = (%q, %v), want (Email, MatchAssignable) — generate-ordering regression", field, kind)
	}
}

// TestInfraAssignability_MidWriteUnprovenBackstop: Infra declares fields but
// the Deps field's own type is mid-write (the fresh component stub doesn't
// type-check), so neither a positive nor a negative can be proven. The
// matcher must degrade to MatchUnprovenBackstop (compile-time backstop on
// the Deps field name) — NOT a spurious loud MatchNoName.
func TestInfraAssignability_MidWriteUnprovenBackstop(t *testing.T) {
	dir := writeInfraMatcherProject(t,
		`package app

type Infra struct {
	// A real provider surface exists, but it does not name-match Mailer
	// and the component side won't type-check this run.
	Unrelated int
}
`,
		`package notify

// Sender is referenced by Deps but never declared — the fresh stub is
// mid-write, so Deps.Mailer's type is Invalid this run.
type Deps struct {
	Mailer Sender
}
`,
		"internal/handlers", "notify",
	)
	m := NewInfraAssignabilityMatcher(dir)
	infra := map[string]InfraField{"Unrelated": {Name: "Unrelated", Type: "int"}}
	field, kind := m.ResolveInfraField("internal/handlers", "notify", "Mailer", "Sender", infra)
	if kind != MatchUnprovenBackstop {
		t.Fatalf("ResolveInfraField kind = %v, want MatchUnprovenBackstop (unproven, must not go loud)", kind)
	}
	if field != "Mailer" {
		t.Fatalf("backstop field = %q, want the Deps field name Mailer (compile-time backstop target)", field)
	}
}

// TestInfraAssignability_ProvenMissingIsLoud: the universe type-checks
// cleanly, Infra is fully visible, and no field is assignable to the Deps
// field — the negative is PROVABLE, so the matcher returns MatchNoName and
// the caller raises the loud MissingProvider. Guards against the
// generate-ordering relaxation swallowing genuine missing providers.
func TestInfraAssignability_ProvenMissingIsLoud(t *testing.T) {
	dir := writeInfraMatcherProject(t,
		`package app

type Infra struct {
	Unrelated int
}
`,
		`package notify

type Sender interface{ Send() }

type Deps struct {
	Mailer Sender
}
`,
		"internal/handlers", "notify",
	)
	m := NewInfraAssignabilityMatcher(dir)
	infra := map[string]InfraField{"Unrelated": {Name: "Unrelated", Type: "int"}}
	field, kind := m.ResolveInfraField("internal/handlers", "notify", "Mailer", "Sender", infra)
	if kind != MatchNoName || field != "" {
		t.Fatalf("ResolveInfraField = (%q, %v), want (\"\", MatchNoName) — proven-missing must stay loud", field, kind)
	}
}

// TestInfraAssignability_NoSurfaceIsLoud: no providers.go / no Infra struct
// on disk (first generate, never declared). A required collaborator with no
// provider is GENUINELY missing and must stay loud — the empty AST-parsed
// infra map is the ground truth, independent of type-checking.
func TestInfraAssignability_NoSurfaceIsLoud(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module example.com/proj\n\ngo 1.23\n")
	mustWrite(t, filepath.Join(dir, "internal", "handlers", "notify", "contract.go"),
		"package notify\n\ntype Sender interface{ Send() }\n\ntype Deps struct {\n\tMailer Sender\n}\n")

	m := NewInfraAssignabilityMatcher(dir)
	field, kind := m.ResolveInfraField("internal/handlers", "notify", "Mailer", "Sender", map[string]InfraField{})
	if kind != MatchNoName || field != "" {
		t.Fatalf("ResolveInfraField = (%q, %v), want (\"\", MatchNoName) — no provider surface must stay loud", field, kind)
	}
}
