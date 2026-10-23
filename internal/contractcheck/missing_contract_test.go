package contractcheck

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/forgeconv"
)

// The missing-contract rule closes a hole that was measured, not imagined: a
// dogfood run created an internal package with a raw file write instead of
// `forge scaffold package`, so it had no contract.go, no mock_gen.go and no
// wiring — and `forge lint` passed. contract.go's ABSENCE was invisible
// because every surface that looks at internal packages starts by finding
// one.
//
// The tests below pin both halves. It has to FIRE on a package that speaks
// forge's contract vocabulary without a contract.go, and it has to stay
// SILENT on the packages internal/ legitimately holds — because a rule that
// tells a constants package to run a scaffold verb gets muted, and a muted
// rule reports green forever.

// writePkg writes a package's files under root/internal/<name>/ and returns
// the project root.
func writePkg(t *testing.T, root, name string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, "internal", filepath.FromSlash(name))
	must(t, mkdirAll(dir))
	for f, body := range files {
		must(t, writeFile(filepath.Join(dir, f), body))
	}
	return root
}

func missingContractFindings(t *testing.T, root string) []forgeconv.Finding {
	t.Helper()
	fs, err := Inspect(context.Background(), root, Options{Rules: []Rule{RuleInternalPackageMissingContract}})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	return fs
}

// TestMissingContract_FiresOnEachVocabularyClaim asserts the rule fires on
// each declaration that is an unambiguous claim to be the thing
// `forge scaffold package` produces. Table-driven so a claim removed from
// the predicate cannot quietly stop being checked.
func TestMissingContract_FiresOnEachVocabularyClaim(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		pkg        string
		files      map[string]string
		wantDecl   string // substring the message must name
		wantAnchor string // file the finding must point at
	}{
		{
			name: "interface named Service",
			pkg:  "worktree",
			files: map[string]string{"worktree.go": `package worktree

import "context"

// Service is the worktree boundary.
type Service interface {
	Create(ctx context.Context, name string) error
}

type service struct{}

func NewService() (Service, error) { return &service{}, nil }

func (s *service) Create(ctx context.Context, name string) error { return nil }
`},
			wantDecl:   "a contract interface `Service`",
			wantAnchor: "internal/worktree/worktree.go",
		},
		{
			name: "interface carrying //forge:service",
			pkg:  "gateway",
			files: map[string]string{"gateway.go": `package gateway

import "context"

//forge:service
type Gateway interface {
	Send(ctx context.Context) error
}
`},
			wantDecl:   "a contract interface `Gateway`",
			wantAnchor: "internal/gateway/gateway.go",
		},
		{
			name: "type Deps struct with no interface at all",
			pkg:  "mailer",
			files: map[string]string{"mailer.go": `package mailer

import "log/slog"

// Deps holds dependencies for the mailer package.
type Deps struct {
	Logger *slog.Logger
}

type mailer struct{ deps Deps }
`},
			wantDecl:   "a `type Deps struct`",
			wantAnchor: "internal/mailer/mailer.go",
		},
		{
			// Deps in one file, the boundary interface in another under a
			// role-oriented name with no marker — the split the --kind=client
			// scaffold uses. Only the Deps claim fires, and the finding must
			// point at the file that declares it, not at the first file
			// alphabetically.
			name: "type Deps struct declared in a sibling file",
			pkg:  "pricer",
			files: map[string]string{
				"pricer.go": `package pricer

type Quoter interface{ Quote() int }
`,
				"types.go": `package pricer

type Deps struct{}
`,
			},
			wantDecl:   "a `type Deps struct`",
			wantAnchor: "internal/pricer/types.go",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := writePkg(t, t.TempDir(), tc.pkg, tc.files)
			fs := missingContractFindings(t, root)
			if len(fs) != 1 {
				t.Fatalf("expected exactly 1 finding, got %d:\n%s", len(fs), AsResult(fs).FormatText())
			}
			f := fs[0]
			if f.File != tc.wantAnchor {
				t.Errorf("finding anchored at %q, want %q — it must point at the declaration that earned the "+
					"verdict, never at the contract.go that does not exist", f.File, tc.wantAnchor)
			}
			if f.Line == 0 {
				t.Errorf("finding has no line; an LSP diagnostic on line 0 lands nowhere")
			}
			if !strings.Contains(f.Message, tc.wantDecl) {
				t.Errorf("message does not name the declaration that fired (%s):\n%s", tc.wantDecl, f.Message)
			}
			if !strings.Contains(f.Remediation, "forge scaffold package") {
				t.Errorf("remediation must name the verb that would have produced the shape:\n%s", f.Remediation)
			}
		})
	}
}

// TestMissingContract_SilentOnPackagesThatAreNotComponents is the other half
// of the contract, and the more important one. internal/ legitimately holds
// packages that want no Service, no Deps and no mock. Every fixture here is
// reduced from a package that exists in a real tree — the first two are the
// packages from the dogfood run that motivated the rule, the rest are the
// shapes a sweep of forge, control-plane and reliant turned up.
func TestMissingContract_SilentOnPackagesThatAreNotComponents(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		pkg   string
		files map[string]string
	}{
		{
			name: "domain reason constants and a value type",
			pkg:  "domain",
			files: map[string]string{"reasons.go": `package domain

const ReasonPrescriptionExpired = "prescription_expired"

// ReasonError preserves a stable domain reason.
type ReasonError struct {
	Reason   string
	Sentinel error
}

func (e ReasonError) Error() string { return e.Reason }

func PrescriptionExpired() ReasonError {
	return ReasonError{Reason: ReasonPrescriptionExpired}
}
`},
		},
		{
			name: "constants plus pure functions over them",
			pkg:  "moneyfmt",
			files: map[string]string{"moneyfmt.go": `package moneyfmt

import "fmt"

const CentsPerUnit = 100

func Format(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/CentsPerUnit, cents%CentsPerUnit)
}
`},
		},
		{
			name: "an abstraction seam: one interface plus its implementation",
			pkg:  "localfs",
			files: map[string]string{"localfs.go": `package localfs

// FS abstracts filesystem operations.
type FS interface {
	ReadFile(name string) ([]byte, error)
}

// Local implements FS using the standard library.
type Local struct{}

func (Local) ReadFile(name string) ([]byte, error) { return nil, nil }
`},
		},
		{
			name: "a narrow dep interface declared at the consumer",
			pkg:  "devautograntplan",
			files: map[string]string{"devautograntplan.go": `package devautograntplan

import "context"

// PlanAssigner is the narrow seam this helper needs.
type PlanAssigner interface {
	AssignPlanForUser(ctx context.Context, userID string) error
}

func Ensure(ctx context.Context, assigner PlanAssigner, userID string) {}
`},
		},
		{
			name: "a catalogue of narrow interfaces consumed elsewhere",
			pkg:  "natsio",
			files: map[string]string{"natsio.go": `package natsio

type Publisher interface{ Publish(subject string, data []byte) error }
type Subscriber interface{ Subscribe(subject string) error }
type Flusher interface{ Flush() error }
`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := writePkg(t, t.TempDir(), tc.pkg, tc.files)
			if fs := missingContractFindings(t, root); len(fs) != 0 {
				t.Fatalf("internal/%s/ is not a component and must produce no finding; got %d:\n%s\n"+
					"A rule that tells a helper package to run a scaffold verb is a rule authors switch off.",
					tc.pkg, len(fs), AsResult(fs).FormatText())
			}
		})
	}
}

// TestMissingContract_SilentOnOtherComponentRoles pins the scoping that
// keeps forge from warning about files forge wrote. internal/handlers/<svc>/,
// internal/workers/<pkg>/ and internal/operators/<pkg>/ are components of
// their own kind with their own scaffold verbs, and they deliberately carry
// no contract.go.
//
// This is not hypothetical on either side. forge's OWN handler scaffold
// emits `type Deps struct` + `func New(Deps) (*Service, error)` into
// internal/handlers/<svc>/service.go, so a recursive walk would fire on
// every handler in every forge project — measured on control-plane, five
// findings, all against files `forge scaffold service` wrote. The fixture
// below is that scaffold, reduced.
func TestMissingContract_SilentOnOtherComponentRoles(t *testing.T) {
	t.Parallel()

	const handlerService = `package billing

import (
	"fmt"
	"log/slog"
)

// Deps holds dependencies for the billing service.
type Deps struct {
	Logger *slog.Logger
}

func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("billing: Deps.Logger is required")
	}
	return nil
}

// Service implements the billing Connect RPC service.
type Service struct {
	deps Deps
}

func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}
`

	for _, role := range []string{"handlers", "workers", "operators"} {
		t.Run(role, func(t *testing.T) {
			t.Parallel()
			root := writePkg(t, t.TempDir(), role+"/billing", map[string]string{"service.go": handlerService})
			if fs := missingContractFindings(t, root); len(fs) != 0 {
				t.Fatalf("internal/%s/billing/ belongs to another scaffold verb and must produce no finding; got %d:\n%s",
					role, len(fs), AsResult(fs).FormatText())
			}
		})
	}

	// The identical package directly under internal/ IS in scope — otherwise
	// the scoping above would be indistinguishable from the rule not working.
	root := writePkg(t, t.TempDir(), "billing", map[string]string{"service.go": handlerService})
	if fs := missingContractFindings(t, root); len(fs) != 1 {
		t.Fatalf("the same package at internal/billing/ must fire (else the role scoping is hiding a broken rule); got %d:\n%s",
			len(fs), AsResult(fs).FormatText())
	}
}

// TestMissingContract_HonorsTheOptOuts asserts the escape hatches the
// remediation advertises actually work — a rule whose documented opt-out
// does not silence it is worse than no rule, because the author's only
// remaining move is to stop reading the output.
func TestMissingContract_HonorsTheOptOuts(t *testing.T) {
	t.Parallel()

	const component = `package widgets

import "context"

// Service is the widgets boundary.
type Service interface {
	Count(ctx context.Context) (int, error)
}
`

	// Sanity: without an opt-out the fixture fires. A test whose "silenced"
	// assertion passes because nothing ever fired proves nothing.
	if fs := missingContractFindings(t, writePkg(t, t.TempDir(), "widgets",
		map[string]string{"widgets.go": component})); len(fs) != 1 {
		t.Fatalf("fixture sanity: expected 1 finding without an opt-out, got %d", len(fs))
	}

	t.Run("forge:exclude-contract directive", func(t *testing.T) {
		t.Parallel()
		root := writePkg(t, t.TempDir(), "widgets",
			map[string]string{"widgets.go": "//forge:exclude-contract\n\n" + component})
		if fs := missingContractFindings(t, root); len(fs) != 0 {
			t.Fatalf("//forge:exclude-contract must silence the rule (the remediation says so); got %d:\n%s",
				len(fs), AsResult(fs).FormatText())
		}
	})

	t.Run("forge.yaml contracts.exclude", func(t *testing.T) {
		t.Parallel()
		root := writePkg(t, t.TempDir(), "widgets", map[string]string{"widgets.go": component})
		fs, err := Inspect(context.Background(), root, Options{
			Rules:    []Rule{RuleInternalPackageMissingContract},
			Excludes: []string{"internal/widgets"},
		})
		if err != nil {
			t.Fatalf("Inspect: %v", err)
		}
		if len(fs) != 0 {
			t.Fatalf("a contracts.exclude entry must silence the rule — telling an excluded package to run the "+
				"very verb the exclusion says it is out of is incoherent; got %d:\n%s", len(fs), AsResult(fs).FormatText())
		}
	})
}

// TestMissingContract_SilentOnceContractExists closes the loop: the rule
// must stop firing the moment the package acquires the file it was asked
// for, and hand off to the shape rule that judges its contents.
func TestMissingContract_SilentOnceContractExists(t *testing.T) {
	t.Parallel()

	root := writePkg(t, t.TempDir(), "widgets", map[string]string{
		"contract.go": `package widgets

import "context"

type Service interface {
	Count(ctx context.Context) (int, error)
}
`,
		"service.go": `package widgets

type Deps struct{}

type service struct{ deps Deps }

func New(deps Deps) (Service, error) { return &service{deps: deps}, nil }
`,
	})
	if fs := missingContractFindings(t, root); len(fs) != 0 {
		t.Fatalf("a package with a contract.go is the OTHER rule's business; got %d:\n%s",
			len(fs), AsResult(fs).FormatText())
	}
}
