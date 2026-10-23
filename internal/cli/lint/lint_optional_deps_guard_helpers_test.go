package lint

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestOptionalDepsGuard_HelperGuards drives the walker over guards that
// live in a same-package helper (lint_optional_deps_guard_helpers.go):
// the shapes it must read as guarded, and the near-misses it must still
// flag because the helper does not prove the field non-nil.
func TestOptionalDepsGuard_HelperGuards(t *testing.T) {
	t.Parallel()

	const imports = `import (
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

var _ = fmt.Errorf
var _ = errors.New

`
	type want struct {
		field  string
		method string
	}
	cases := []struct {
		name    string
		methods string
		want    []want // nil → expect zero findings
	}{
		// --- guarded: the helper proves the field non-nil -------------
		{
			name: "error-helper-if-init",
			methods: `
func (s *Service) gate() error {
	if s.deps.Svc == nil {
		return svcerr.Unavailable("svc is not configured")
	}
	return nil
}

func (s *Service) Call() error {
	if err := s.gate(); err != nil {
		return err
	}
	return s.deps.Svc.Do()
}
`,
		},
		{
			// control-plane internal/handlers/alerting: the helper checks
			// one dep itself and takes the rest as a `configured` bool the
			// caller computes from nil checks.
			name: "error-helper-bool-param",
			methods: `
func (s *Service) gate(orgID string, mutate, configured bool) error {
	if s.deps.Svc == nil {
		return svcerr.Wrap(svcerr.Unavailable("not configured"))
	}
	if orgID == "" {
		return svcerr.Wrap(svcerr.InvalidArgument("org_id is required"))
	}
	var err error
	if mutate {
		_, err = s.deps.Svc.Get(orgID)
	}
	if err != nil {
		return svcerr.Wrap(err)
	}
	if !configured {
		return svcerr.Wrap(svcerr.Unavailable("not configured"))
	}
	return nil
}

func (s *Service) Call(orgID string) error {
	if err := s.gate(orgID, true, s.deps.Hook != nil && s.deps.Svc != nil); err != nil {
		return err
	}
	if err := s.deps.Hook(orgID); err != nil {
		return err
	}
	return s.deps.Svc.Do()
}
`,
		},
		{
			name: "error-helper-two-statements",
			methods: `
func (s *Service) gate() error {
	if s.deps.Svc == nil {
		return errors.New("svc is not configured")
	}
	return nil
}

func (s *Service) Call() error {
	err := s.gate()
	if err != nil {
		return err
	}
	return s.deps.Svc.Do()
}
`,
		},
		{
			name: "error-helper-multi-value",
			methods: `
func (s *Service) svc() (Svc, error) {
	if s.deps.Svc == nil {
		return nil, fmt.Errorf("svc is not configured")
	}
	return s.deps.Svc, nil
}

func (s *Service) Call() error {
	_, err := s.svc()
	if err != nil {
		return err
	}
	return s.deps.Svc.Do()
}
`,
		},
		{
			name: "error-helper-enclosing-nil-check",
			methods: `
func (s *Service) gate() error {
	if s.deps.Svc == nil {
		return errors.New("svc is not configured")
	}
	return nil
}

func (s *Service) Call() error {
	if err := s.gate(); err == nil {
		return s.deps.Svc.Do()
	}
	return nil
}
`,
		},
		{
			// `if err != nil { return err }` inside the helper is a
			// failure exit: the branch proved err non-nil.
			name: "error-helper-returns-checked-local",
			methods: `
func (s *Service) gate(id string) error {
	if s.deps.Svc == nil {
		return &configError{}
	}
	if _, err := s.deps.Svc.Get(id); err != nil {
		return err
	}
	return nil
}

type configError struct{}

func (*configError) Error() string { return "not configured" }

func (s *Service) Call(id string) error {
	if err := s.gate(id); err != nil {
		return err
	}
	return s.deps.Svc.Do()
}
`,
		},
		{
			name: "error-helper-sentinel",
			methods: `
var errNotConfigured = errors.New("not configured")

func (s *Service) gate() error {
	if s.deps.Svc == nil {
		return errNotConfigured
	}
	return nil
}

func (s *Service) Call() error {
	if err := s.gate(); err != nil {
		return err
	}
	return s.deps.Svc.Do()
}
`,
		},
		{
			name: "error-helper-composed",
			// gate succeeds only through inner, which checks Svc.
			methods: `
func (s *Service) inner() error {
	if s.deps.Svc == nil {
		return errors.New("svc is not configured")
	}
	return nil
}

func (s *Service) gate() error {
	return s.inner()
}

func (s *Service) Call() error {
	if err := s.gate(); err != nil {
		return err
	}
	return s.deps.Svc.Do()
}
`,
		},
		{
			name: "bool-helper",
			methods: `
func (s *Service) hasSvc() bool { return s.deps.Svc != nil }

func (s *Service) Call() error {
	if !s.hasSvc() {
		return nil
	}
	return s.deps.Svc.Do()
}

func (s *Service) Enclosed() error {
	if s.hasSvc() {
		return s.deps.Svc.Do()
	}
	return nil
}
`,
		},
		{
			name: "void-helper-panics",
			methods: `
func (s *Service) mustHaveSvc() {
	if s.deps.Svc == nil {
		panic("svc is not configured")
	}
}

func (s *Service) Call() error {
	s.mustHaveSvc()
	return s.deps.Svc.Do()
}
`,
		},
		{
			// control-plane internal/handlers/identity: the helper copies
			// the deps and checks several fields in one || chain.
			name: "error-helper-deps-copy-or-chain",
			methods: `
func (s *Service) authorize(id string) error {
	d := s.deps
	if d.Svc == nil || d.Hook == nil || id == "" {
		return svcerr.Wrap(svcerr.Unavailable("not configured"))
	}
	return nil
}

func (s *Service) Call(id string) error {
	if err := s.authorize(id); err != nil {
		return err
	}
	if err := s.deps.Hook(id); err != nil {
		return err
	}
	return s.deps.Svc.Do()
}
`,
		},
		{
			name: "deps-method-helper",
			methods: `
func (d Deps) ready() error {
	if d.Svc == nil {
		return errors.New("svc is not configured")
	}
	return nil
}

func (s *Service) Call() error {
	if err := s.deps.ready(); err != nil {
		return err
	}
	return s.deps.Svc.Do()
}
`,
		},

		// --- flagged: the helper does not prove the field non-nil ------
		{
			name: "no-guard-helper",
			methods: `
func (s *Service) gate() error { return nil }

func (s *Service) Call() error {
	if err := s.gate(); err != nil {
		return err
	}
	return s.deps.Svc.Do()
}
`,
			want: []want{{field: "Svc", method: "Call"}},
		},
		{
			name: "helper-succeeds-when-nil",
			methods: `
func (s *Service) gate() error {
	if s.deps.Svc == nil {
		return nil
	}
	return errors.New("busy")
}

func (s *Service) Call() error {
	if err := s.gate(); err != nil {
		return err
	}
	return s.deps.Svc.Do()
}
`,
			want: []want{{field: "Svc", method: "Call"}},
		},
		{
			// The nil path returns another call's error, which may be
			// nil: the helper is not credited with the guard.
			name: "helper-nil-path-returns-unknown-call",
			methods: `
func (s *Service) audit() error { return nil }

func (s *Service) gate() error {
	if s.deps.Svc == nil {
		return s.audit()
	}
	return nil
}

func (s *Service) Call() error {
	if err := s.gate(); err != nil {
		return err
	}
	return s.deps.Svc.Do()
}
`,
			want: []want{{field: "Svc", method: "Call"}},
		},
		{
			// svcerr.Wrap(nil) is nil: wrapping an unchecked error is
			// not a failure exit.
			name: "helper-wraps-unchecked-error",
			methods: `
func (s *Service) gate(cause error) error {
	if s.deps.Svc == nil {
		return svcerr.Wrap(cause)
	}
	return nil
}

func (s *Service) Call(cause error) error {
	if err := s.gate(cause); err != nil {
		return err
	}
	return s.deps.Svc.Do()
}
`,
			want: []want{{field: "Svc", method: "Call"}},
		},
		{
			name: "helper-result-ignored",
			methods: `
func (s *Service) gate() error {
	if s.deps.Svc == nil {
		return errors.New("svc is not configured")
	}
	return nil
}

func (s *Service) Call() error {
	_ = s.gate()
	s.gate()
	return s.deps.Svc.Do()
}
`,
			want: []want{{field: "Svc", method: "Call"}},
		},
		{
			name: "helper-check-does-not-exit",
			methods: `
func (s *Service) gate() error {
	if s.deps.Svc == nil {
		return errors.New("svc is not configured")
	}
	return nil
}

func (s *Service) Call() error {
	if err := s.gate(); err != nil {
		s.deps.Logger.Info(err.Error())
	}
	return s.deps.Svc.Do()
}
`,
			want: []want{{field: "Svc", method: "Call"}},
		},
		{
			// err is bound only on one branch: a nil err afterwards does
			// not mean gate ran.
			name: "helper-bound-in-branch",
			methods: `
func (s *Service) gate() error {
	if s.deps.Svc == nil {
		return errors.New("svc is not configured")
	}
	return nil
}

func (s *Service) Call(ready bool) error {
	var err error
	if ready {
		err = s.gate()
	}
	if err != nil {
		return err
	}
	return s.deps.Svc.Do()
}
`,
			want: []want{{field: "Svc", method: "Call"}},
		},
		{
			name: "helper-binding-overwritten",
			methods: `
func (s *Service) gate() error {
	if s.deps.Svc == nil {
		return errors.New("svc is not configured")
	}
	return nil
}

func (s *Service) Call() error {
	err := s.gate()
	err = s.deps.Repo.Load()
	if err != nil {
		return err
	}
	return s.deps.Svc.Do()
}
`,
			want: []want{{field: "Svc", method: "Call"}},
		},
		{
			// The bool param is required true, but the argument proves
			// nothing about Svc.
			name: "bool-param-argument-unrelated",
			methods: `
func (s *Service) gate(configured bool) error {
	if !configured {
		return errors.New("not configured")
	}
	return nil
}

func (s *Service) Call(ready bool) error {
	if err := s.gate(ready && s.deps.Hook != nil); err != nil {
		return err
	}
	_ = s.deps.Hook("ok")
	return s.deps.Svc.Do()
}
`,
			want: []want{{field: "Svc", method: "Call"}},
		},
		{
			name: "bool-param-reassigned",
			methods: `
func (s *Service) gate(configured bool) error {
	configured = true
	if !configured {
		return errors.New("not configured")
	}
	return nil
}

func (s *Service) Call() error {
	if err := s.gate(s.deps.Svc != nil); err != nil {
		return err
	}
	return s.deps.Svc.Do()
}
`,
			want: []want{{field: "Svc", method: "Call"}},
		},
		{
			// The helper checks a Deps it was handed, not its receiver's.
			name: "helper-checks-other-deps",
			methods: `
func (s *Service) check(d Deps) error {
	if d.Svc == nil {
		return errors.New("svc is not configured")
	}
	return nil
}

func (s *Service) Call(other Deps) error {
	if err := s.check(other); err != nil {
		return err
	}
	return s.deps.Svc.Do()
}
`,
			want: []want{{field: "Svc", method: "Call"}},
		},
		{
			// A local copy of the deps roots derefs as well as guards.
			name: "deps-copy-unguarded",
			methods: `
func (s *Service) Call() error {
	d := s.deps
	return d.Svc.Do()
}
`,
			want: []want{{field: "Svc", method: "Call"}},
		},
		{
			name: "helper-on-another-receiver",
			methods: `
func (s *Service) gate() error {
	if s.deps.Svc == nil {
		return errors.New("svc is not configured")
	}
	return nil
}

func (s *Service) Call(other *Service) error {
	if err := other.gate(); err != nil {
		return err
	}
	return s.deps.Svc.Do()
}
`,
			want: []want{{field: "Svc", method: "Call"}},
		},
		{
			name: "recursive-helper",
			methods: `
func (s *Service) gate(n int) error {
	if n > 0 {
		return s.gate(n - 1)
	}
	return nil
}

func (s *Service) Call() error {
	if err := s.gate(3); err != nil {
		return err
	}
	return s.deps.Svc.Do()
}
`,
			want: []want{{field: "Svc", method: "Call"}},
		},
		{
			// A closure's return leaves the closure, not the helper.
			name: "closure-return-is-not-helper-exit",
			methods: `
func (s *Service) gate() error {
	f := func() error { return nil }
	_ = f
	if s.deps.Svc == nil {
		go func() {
			_ = f()
		}()
	}
	return errors.New("never succeeds")
}

func (s *Service) Call() error {
	if err := s.gate(); err != nil {
		return err
	}
	return s.deps.Svc.Do()
}
`,
			want: []want{{field: "Svc", method: "Call"}},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			projectDir := t.TempDir()
			writeOptionalGuardPkg(t, projectDir, "fixture", imports+tc.methods)

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
			}
		})
	}
}

// TestSvcerrClassificationCoversPackage pins svcerrConstructors and
// svcerrWrappers against pkg/svcerr: every exported func returning a lone
// error must be classified, or listed here as deliberately neither, so a
// new svcerr func cannot silently be read as "may return nil" — nor a
// removed one linger.
func TestSvcerrClassificationCoversPackage(t *testing.T) {
	t.Parallel()
	// Neither: Cause may return nil for a non-nil argument.
	neither := map[string]bool{"Cause": true}

	dir := filepath.Join("..", "..", "..", "pkg", "svcerr")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var exported []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !fn.Name.IsExported() || fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
				continue
			}
			if id, ok := fn.Type.Results.List[0].Type.(*ast.Ident); ok && id.Name == "error" {
				exported = append(exported, fn.Name.Name)
			}
		}
	}
	sort.Strings(exported)
	seen := map[string]bool{}
	for _, name := range exported {
		seen[name] = true
		if !svcerrConstructors[name] && !svcerrWrappers[name] && !neither[name] {
			t.Errorf("svcerr.%s returns error but is unclassified: add it to svcerrConstructors (never nil), svcerrWrappers (nil iff its first arg is) or this test's neither set", name)
		}
	}
	for _, set := range []map[string]bool{svcerrConstructors, svcerrWrappers, neither} {
		for name := range set {
			if !seen[name] {
				t.Errorf("svcerr.%s is classified but pkg/svcerr exports no such func returning error", name)
			}
		}
	}
}
