package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeOptionalGuardPkg lays down a minimal handlers/<pkg>/ package with
// a Deps struct (Logger required, Svc + Hook optional, Repo NOT
// optional) plus the given extra source appended into handlers.go. The
// fixture mirrors the conventional forge handler shape: Service struct
// holding `deps Deps`, methods dereferencing through `s.deps.<Field>`.
func writeOptionalGuardPkg(t *testing.T, projectDir, pkg, methods string) {
	t.Helper()
	dir := filepath.Join(projectDir, "internal", "handlers", pkg)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	service := `package ` + pkg + `

type Svc interface {
	Do() error
	Get(id string) (string, error)
}

type Repository interface {
	Load() error
}

type Deps struct {
	Logger *Logger
	// forge:optional-dep
	Svc Svc
	// forge:optional-dep
	Hook func(string) error
	Repo Repository
}

type Logger struct{}

func (l *Logger) Info(msg string) {}

type Service struct {
	deps Deps
}

func New(deps Deps) *Service { return &Service{deps: deps} }
`
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(service), 0o644); err != nil {
		t.Fatal(err)
	}
	handlers := "package " + pkg + "\n\n" + methods
	if err := os.WriteFile(filepath.Join(dir, "handlers.go"), []byte(handlers), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestOptionalDepsGuard_Table drives the walker over fixture method
// bodies covering every guard / deref shape the linter contracts.
func TestOptionalDepsGuard_Table(t *testing.T) {
	t.Parallel()

	type want struct {
		field  string
		method string
	}
	cases := []struct {
		name    string
		methods string
		want    []want // nil → expect zero findings
	}{
		{
			name: "guarded-early-return",
			methods: `
func (s *Service) Call() error {
	if s.deps.Svc == nil {
		return nil
	}
	return s.deps.Svc.Do()
}
`,
		},
		{
			name: "guarded-early-return-or-chain",
			// `X == nil || cond` with an exiting body still guarantees
			// X != nil afterward (short-circuit: X nil forces the exit).
			methods: `
func (s *Service) Call(ready bool) error {
	if s.deps.Svc == nil || !ready {
		return nil
	}
	return s.deps.Svc.Do()
}
`,
		},
		{
			name: "guarded-enclosing-if",
			methods: `
func (s *Service) Call() error {
	if s.deps.Svc != nil {
		return s.deps.Svc.Do()
	}
	return nil
}
`,
		},
		{
			name: "guarded-enclosing-if-and-chain",
			methods: `
func (s *Service) Call(ready bool) error {
	if s.deps.Svc != nil && ready {
		return s.deps.Svc.Do()
	}
	return nil
}
`,
		},
		{
			name: "guarded-else-branch",
			methods: `
func (s *Service) Call() error {
	if s.deps.Svc == nil {
		_ = 1
	} else {
		return s.deps.Svc.Do()
	}
	return nil
}
`,
		},
		{
			name: "alias-then-check",
			methods: `
func (s *Service) Call() error {
	svc := s.deps.Svc
	if svc == nil {
		return nil
	}
	return svc.Do()
}
`,
		},
		{
			name: "alias-enclosing-check",
			methods: `
func (s *Service) Call() error {
	if svc := s.deps.Svc; svc != nil {
		return svc.Do()
	}
	return nil
}
`,
		},
		{
			name: "unguarded-direct",
			methods: `
func (s *Service) Call() error {
	return s.deps.Svc.Do()
}
`,
			want: []want{{field: "Svc", method: "Call"}},
		},
		{
			name: "unguarded-chained",
			methods: `
func (s *Service) Call() (string, error) {
	return s.deps.Svc.Get("id")
}
`,
			want: []want{{field: "Svc", method: "Call"}},
		},
		{
			name: "unguarded-func-field-call",
			methods: `
func (s *Service) Call() error {
	return s.deps.Hook("event")
}
`,
			want: []want{{field: "Hook", method: "Call"}},
		},
		{
			name: "unguarded-alias-deref",
			// Aliasing alone is not a guard — the deref on the alias
			// still needs a nil-check.
			methods: `
func (s *Service) Call() error {
	svc := s.deps.Svc
	return svc.Do()
}
`,
			want: []want{{field: "Svc", method: "Call"}},
		},
		{
			name: "guard-does-not-leak-across-methods",
			// A guard in one method must not silence a deref in another.
			methods: `
func (s *Service) Guarded() error {
	if s.deps.Svc == nil {
		return nil
	}
	return s.deps.Svc.Do()
}

func (s *Service) Unguarded() error {
	return s.deps.Svc.Do()
}
`,
			want: []want{{field: "Svc", method: "Unguarded"}},
		},
		{
			name: "passed-as-arg-not-flagged",
			methods: `
func helper(v Svc) error { return nil }

func (s *Service) Call() error {
	return helper(s.deps.Svc)
}
`,
		},
		{
			name: "assignment-and-accessor-not-flagged",
			// Assigning TO the field and returning it are not derefs.
			methods: `
func (s *Service) Set(v Svc) { s.deps.Svc = v }

func (s *Service) Get() Svc { return s.deps.Svc }

func (s *Service) Compare() bool { return s.deps.Svc == nil }
`,
		},
		{
			name: "suppression-comment-trailing",
			methods: `
func (s *Service) Call() error {
	return s.deps.Svc.Do() // forge:optional-checked
}
`,
		},
		{
			name: "suppression-comment-line-above",
			methods: `
func (s *Service) Call() error {
	// forge:optional-checked
	return s.deps.Svc.Do()
}
`,
		},
		{
			name: "non-optional-field-never-flagged",
			// Repo lacks the marker — unguarded derefs are validateDeps'
			// jurisdiction, not this lint's.
			methods: `
func (s *Service) Call() error {
	return s.deps.Repo.Load()
}
`,
		},
		{
			name: "guard-on-one-field-not-another",
			// Guarding Svc must not silence the Hook deref.
			methods: `
func (s *Service) Call() error {
	if s.deps.Svc == nil {
		return nil
	}
	if err := s.deps.Svc.Do(); err != nil {
		return err
	}
	return s.deps.Hook("event")
}
`,
			want: []want{{field: "Hook", method: "Call"}},
		},
		{
			name: "guard-with-continue-in-loop",
			// continue exits the remainder of the loop body — the deref
			// later in the same iteration is dominated by the guard.
			methods: `
func (s *Service) CallAll(ids []string) {
	for range ids {
		if s.deps.Svc == nil {
			continue
		}
		_ = s.deps.Svc.Do()
	}
}
`,
		},
		{
			name: "deps-receiver-deref",
			// Methods on Deps itself root the bare d.X form.
			methods: `
func (d Deps) ping() error {
	return d.Svc.Do()
}
`,
			want: []want{{field: "Svc", method: "ping"}},
		},
		{
			name: "deref-inside-guard-condition-flagged",
			// The condition runs BEFORE the guard it establishes.
			methods: `
func (s *Service) Call() error {
	if s.deps.Svc.Do() == nil {
		return nil
	}
	return nil
}
`,
			want: []want{{field: "Svc", method: "Call"}},
		},
		{
			name: "short-circuit-and-within-condition",
			// `x != nil && x.M()` — the right operand only evaluates
			// when x is non-nil (kalshi trader-worker idiom).
			methods: `
func (s *Service) Call(ready bool) bool {
	return ready && s.deps.Hook != nil && s.deps.Hook("event") == nil
}
`,
		},
		{
			name: "short-circuit-or-within-condition",
			methods: `
func (s *Service) Call() bool {
	return s.deps.Svc == nil || s.deps.Svc.Do() == nil
}
`,
		},
		{
			name: "tagless-switch-case-guard",
			methods: `
func (s *Service) Call() error {
	switch {
	case s.deps.Svc != nil:
		return s.deps.Svc.Do()
	}
	return nil
}
`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			projectDir := t.TempDir()
			writeOptionalGuardPkg(t, projectDir, "fixture", tc.methods)

			got, err := collectOptionalDepsGuardFindings(projectDir)
			if err != nil {
				t.Fatalf("collect: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("findings = %d, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				if got[i].Field != w.field || got[i].Method != w.method {
					t.Errorf("finding[%d] = %s in %s, want %s in %s", i, got[i].Field, got[i].Method, w.field, w.method)
				}
				if got[i].Line == 0 || got[i].Col == 0 {
					t.Errorf("finding[%d] missing position: %+v", i, got[i])
				}
				if got[i].Package != "fixture" || got[i].Role != "internal/handlers" {
					t.Errorf("finding[%d] wrong package attribution: %+v", i, got[i])
				}
			}
		})
	}
}

// TestOptionalDepsGuard_SkipsGeneratedAndTests asserts that _gen.go and
// _test.go files in the package are never scanned — generated files are
// forge-owned, tests construct zero-value Deps on purpose.
func TestOptionalDepsGuard_SkipsGeneratedAndTests(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	writeOptionalGuardPkg(t, projectDir, "fixture", `
func (s *Service) Clean() error {
	if s.deps.Svc == nil {
		return nil
	}
	return s.deps.Svc.Do()
}
`)
	dir := filepath.Join(projectDir, "internal", "handlers", "fixture")
	genSrc := `package fixture

func (s *Service) generated() error {
	return s.deps.Svc.Do()
}
`
	if err := os.WriteFile(filepath.Join(dir, "mock_gen.go"), []byte(genSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := collectOptionalDepsGuardFindings(projectDir)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 findings (generated file must be skipped), got %+v", got)
	}
}

// TestOptionalDepsGuard_NoOptionalFields asserts a package whose Deps
// has no optional-dep markers is never walked, even with unguarded
// derefs of regular fields.
func TestOptionalDepsGuard_NoOptionalFields(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	dir := filepath.Join(projectDir, "internal", "plain")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package plain

type Repository interface{ Load() error }

type Deps struct {
	Repo Repository
}

type impl struct{ deps Deps }

func (s *impl) Call() error { return s.deps.Repo.Load() }
`
	if err := os.WriteFile(filepath.Join(dir, "contract.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := collectOptionalDepsGuardFindings(projectDir)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 findings, got %+v", got)
	}
}

// TestOptionalDepsGuard_JSONMapping asserts the --json collector carries
// position, rule id, warning severity, and the suppression escape hatch
// in fix_hint.
func TestOptionalDepsGuard_JSONMapping(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	writeOptionalGuardPkg(t, projectDir, "fixture", `
func (s *Service) Call() error {
	return s.deps.Svc.Do()
}
`)

	fs, err := collectOptionalDepsGuardJSON(projectDir)
	if err != nil {
		t.Fatalf("collect JSON: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1: %+v", len(fs), fs)
	}
	f := fs[0]
	if f.Rule != "forge-optional-deps-guard" {
		t.Errorf("rule = %q", f.Rule)
	}
	if f.Severity != lintSevWarning {
		t.Errorf("severity = %q, want warning", f.Severity)
	}
	if f.File == "" || f.Line == 0 || f.Col == 0 {
		t.Errorf("missing position: %+v", f)
	}
	if !strings.Contains(f.Message, "Deps.Svc") || !strings.Contains(f.Message, "Call") {
		t.Errorf("message missing field/method: %q", f.Message)
	}
	if !strings.Contains(f.FixHint, "forge:optional-checked") {
		t.Errorf("fix_hint must document the suppression directive: %q", f.FixHint)
	}
}

// TestOptionalDepsGuard_AuditCategory moved to
// internal/cli/audit_deps_category_test.go with the audit roll-up it
// exercises (auditOptionalDepsGuard consumes this package's
// CollectOptionalDepsGuardFindings).
