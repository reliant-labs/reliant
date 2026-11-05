// File: internal/contractcheck/internal_pkg_contract_test.go
//
// Coverage for the internal-package-contract-names rule. These tests
// were ported from internal/linter/forgeconv/forgeconv_test.go when
// the rule moved into this package; assertions, fixtures, and table
// rows are byte-for-byte identical so a green run here proves the
// move preserved behavior.

package contractcheck

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/forgeconv"
)

// TestInternalContracts_GoodFixtureClean verifies a contract.go that uses
// the canonical Service/Deps/New(Deps) Service shape produces zero findings.
func TestInternalContracts_GoodFixtureClean(t *testing.T) {
	fs, err := Inspect(context.Background(),
		filepath.Join("testdata", "contracts_good"),
		Options{Rules: []Rule{RuleInternalPackageContractNames}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(fs) != 0 {
		t.Fatalf("expected 0 findings on canonical contract, got %d:\n%s",
			len(fs), AsResult(fs).FormatText())
	}
	if HasErrors(fs) {
		t.Errorf("good fixture should not have errors")
	}
}

// TestInternalContracts_BadFixtureFiresThreeFindings verifies a contract.go
// using the wrong names (Sender/Config/NewSender) produces one finding for
// each of the three canonical pieces (Service, Deps, New(Deps) Service) so
// the user sees the full delta in one run rather than discovering it
// piecemeal across re-runs.
func TestInternalContracts_BadFixtureFiresThreeFindings(t *testing.T) {
	fs, err := Inspect(context.Background(),
		filepath.Join("testdata", "contracts_bad"),
		Options{Rules: []Rule{RuleInternalPackageContractNames}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleInternalPackageContractNames))
	if len(got) != 3 {
		t.Fatalf("expected 3 findings (Service/Deps/New), got %d:\n%s",
			len(got), AsResult(fs).FormatText())
	}

	// The type findings carry the canonical sentinel so users can grep for
	// the convention doc. The constructor finding does NOT: its requirement
	// is the signature, not the identifier `New`, and a sentinel that spells
	// out `func New(Deps) Service` would put the forced name back into the
	// prose the rule was fixed to stop printing.
	const sentinel = "internal-package contracts must declare 'type Service interface', 'type Deps struct', and 'func New(Deps) Service'"
	for _, f := range got {
		if strings.Contains(f.Message, "NewSender") {
			if !strings.Contains(f.Message, "//forge:constructor") {
				t.Errorf("constructor finding must offer the marker escape hatch; got: %s", f.Message)
			}
		} else if !strings.Contains(f.Message, sentinel) {
			t.Errorf("finding missing canonical sentinel; got: %s", f.Message)
		}
		if f.Severity != forgeconv.SeverityError {
			t.Errorf("internal-package-contract-names should be an error, got %s", f.Severity)
		}
	}

	// The three findings should reference the three actual names found, so
	// the user sees what to rename.
	combined := AsResult(fs).FormatText()
	for _, want := range []string{"Sender", "Config", "NewSender"} {
		if !strings.Contains(combined, want) {
			t.Errorf("expected finding text to reference non-canonical name %q; got:\n%s", want, combined)
		}
	}

	if !HasErrors(fs) {
		t.Errorf("non-canonical contract must gate the build")
	}
}

// TestInternalContracts_HonorsExcludes verifies that directories listed
// in the excludes set are skipped — packages that legitimately don't
// follow the convention (analyzer sub-packages, embed-only packages,
// packages that aren't bootstrap-managed) opt out via
// contracts.exclude in forge.yaml and the analyzer must respect it.
func TestInternalContracts_HonorsExcludes(t *testing.T) {
	// First, prove the fixture would otherwise fire (no exclude → findings).
	resBefore, err := Inspect(context.Background(),
		filepath.Join("testdata", "contracts_excluded"),
		Options{Rules: []Rule{RuleInternalPackageContractNames}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(resBefore) == 0 {
		t.Fatalf("fixture sanity: contracts_excluded must produce findings without an exclude")
	}

	// Now apply the exclude. Findings drop to zero.
	resAfter, err := Inspect(context.Background(),
		filepath.Join("testdata", "contracts_excluded"),
		Options{
			Rules:    []Rule{RuleInternalPackageContractNames},
			Excludes: []string{"internal/legacyshape"},
		},
	)
	if err != nil {
		t.Fatalf("Inspect (excluded): %v", err)
	}
	if len(resAfter) != 0 {
		t.Fatalf("expected 0 findings with exclude, got %d:\n%s",
			len(resAfter), AsResult(resAfter).FormatText())
	}
}

// TestInternalContracts_NoInternalDir verifies the analyzer is a no-op
// in projects without an internal/ directory (CLI/library kinds typically
// don't have one).
func TestInternalContracts_NoInternalDir(t *testing.T) {
	tmp := t.TempDir()
	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleInternalPackageContractNames}},
	)
	if err != nil {
		t.Fatalf("Inspect on empty project: %v", err)
	}
	if len(fs) != 0 {
		t.Errorf("empty project should produce 0 findings, got %d", len(fs))
	}
}

// TestInternalContracts_NewSignatureRejectsPointerDeps verifies a
// `func New(*Deps) Service` shape is rejected — the bootstrap template
// emits `<pkg>.New(<pkg>.Deps{...})` (a value), so a pointer receiver
// signature would compile-fail at the call site.
func TestInternalContracts_NewSignatureRejectsPointerDeps(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "ptr")
	must(t, mkdirAll(pkgDir))
	must(t, writeFile(filepath.Join(pkgDir, "contract.go"), `package ptr

type Service interface { Do() error }
type Deps struct{}

// Pointer parameter — bootstrap template won't compile against this.
func New(d *Deps) Service { return nil }
`))
	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleInternalPackageContractNames}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleInternalPackageContractNames))
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for ptr-Deps mismatch, got %d:\n%s",
			len(got), AsResult(fs).FormatText())
	}
	if !strings.Contains(got[0].Message, "New") {
		t.Errorf("finding should reference New constructor; got: %s", got[0].Message)
	}
}

// TestInternalContracts_ServiceMarkerFreesInterfaceName asserts the
// marker escape hatch: an interface named `Gateway` (role-oriented, which
// the adapter skill encourages) carrying `//forge:service` satisfies the
// contract-shape check under its own name — Service/New keyed off the
// marker, no rename required. `Deps` + `New(Deps) Gateway` stay canonical.
func TestInternalContracts_ServiceMarkerFreesInterfaceName(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "payments")
	must(t, mkdirAll(pkgDir))
	must(t, writeFile(filepath.Join(pkgDir, "contract.go"), `package payments

import "context"

// Gateway is the payments boundary.
//forge:service
type Gateway interface {
	Charge(ctx context.Context, amount int) error
}

type Deps struct{}

func New(d Deps) (Gateway, error) { return nil, nil }
`))
	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleInternalPackageContractNames}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleInternalPackageContractNames))
	if len(got) != 0 {
		t.Fatalf("a `//forge:service`-marked Gateway should produce 0 findings, got %d:\n%s",
			len(got), AsResult(fs).FormatText())
	}
}

// TestInternalContracts_ContractMarkerAlsoAccepted asserts the
// `//forge:contract` synonym is honored identically to `//forge:service`,
// including the single-result `func New(Deps) <Iface>` form.
func TestInternalContracts_ContractMarkerAlsoAccepted(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "dispatch")
	must(t, mkdirAll(pkgDir))
	must(t, writeFile(filepath.Join(pkgDir, "contract.go"), `package dispatch

// Dispatcher routes jobs.
// forge:contract
type Dispatcher interface {
	Dispatch(job string) error
}

type Deps struct{}

func New(d Deps) Dispatcher { return nil }
`))
	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleInternalPackageContractNames}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleInternalPackageContractNames))
	if len(got) != 0 {
		t.Fatalf("a `//forge:contract`-marked Dispatcher should produce 0 findings, got %d:\n%s",
			len(got), AsResult(fs).FormatText())
	}
}

// TestInternalContracts_UnmarkedRoleInterfaceStillFires asserts the
// zero-annotation path is unchanged: a role-named interface WITHOUT the
// marker still fires, and the remediation now surfaces the marker escape.
func TestInternalContracts_UnmarkedRoleInterfaceStillFires(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "payments")
	must(t, mkdirAll(pkgDir))
	must(t, writeFile(filepath.Join(pkgDir, "contract.go"), `package payments

type Gateway interface {
	Charge(amount int) error
}

type Deps struct{}

func New(d Deps) (Gateway, error) { return nil, nil }
`))
	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleInternalPackageContractNames}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleInternalPackageContractNames))
	// Unmarked, serviceIfaceName defaults to "Service": the interface isn't
	// named Service (missing-Service finding) AND `New` returns Gateway, not
	// Service (missing-New finding) — the exact 9-file-rename friction, still
	// loud without the marker. The Service finding must now advertise the
	// marker escape so the author reaches for it instead of the rename.
	if len(got) == 0 {
		t.Fatalf("unmarked Gateway should still fire; got 0 findings")
	}
	foundMarkerHint := false
	for _, f := range got {
		if strings.Contains(f.Message, "forge:service") {
			foundMarkerHint = true
		}
	}
	if !foundMarkerHint {
		t.Errorf("a finding should surface the //forge:service marker escape; got:\n%s",
			AsResult(fs).FormatText())
	}
}

// TestInternalContracts_InterfaceCatalogueSkipped asserts that a
// package whose contract.go declares only narrow interfaces (>= 2,
// no Deps struct, no New func) is treated as an "interface catalogue"
// — a collection of contracts consumed elsewhere, not a Service-shape
// package the bootstrap binds to. No findings should fire, so the user
// doesn't have to add the package to contracts.exclude.
//
// FRICTION 2026-06-02: cp-forge layer-3 natsio shipped a contract.go
// with 7 narrow interfaces (Publisher, CommandHandler, three Runners,
// two Repositories) and the lint fired three findings; the user had to
// add the package to forge.yaml's contracts.exclude to silence them.
func TestInternalContracts_InterfaceCatalogueSkipped(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "natsio")
	must(t, mkdirAll(pkgDir))
	must(t, writeFile(filepath.Join(pkgDir, "contract.go"), `package natsio

// A catalogue of narrow interfaces consumed by other packages. There
// is no canonical Service surface and no Deps/New trio — this package
// just describes contracts.

type Publisher interface {
	Publish(subject string, data []byte) error
}

type CommandHandler interface {
	HandleCommand(msg []byte) error
}

type EventConsumer interface {
	Consume(subject string) error
}
`))
	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleInternalPackageContractNames}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleInternalPackageContractNames))
	if len(got) != 0 {
		t.Fatalf("interface-catalogue package should produce 0 findings, got %d:\n%s",
			len(got), AsResult(fs).FormatText())
	}
}

// TestInternalContracts_SingleInterfaceStillFires guards the
// catalogue heuristic's lower bound: a single interface declaration
// (no Deps/New) is more likely an incomplete contract than a
// deliberate catalogue, so it should still fire all three findings.
func TestInternalContracts_SingleInterfaceStillFires(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "incomplete")
	must(t, mkdirAll(pkgDir))
	must(t, writeFile(filepath.Join(pkgDir, "contract.go"), `package incomplete

// One interface, no Deps, no New — looks like an unfinished Service
// scaffold, not an interface catalogue. Lint must surface the gap.

type Sender interface {
	Send(b []byte) error
}
`))
	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleInternalPackageContractNames}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleInternalPackageContractNames))
	if len(got) != 3 {
		t.Fatalf("single-interface package should fire 3 findings (Service/Deps/New missing), got %d:\n%s",
			len(got), AsResult(fs).FormatText())
	}
}

// TestInternalContracts_NotAComponentSkipped: a package the server
// composition never constructs (`//forge:not-a-component`) has no
// constructor for the shape to make bindable, so its one-interface
// contract.go is not an unfinished Service. The same package without the
// marker still fires (TestInternalContracts_SingleInterfaceStillFires).
func TestInternalContracts_NotAComponentSkipped(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "grants")
	must(t, mkdirAll(pkgDir))
	must(t, writeFile(filepath.Join(pkgDir, "contract.go"), `// Package grants stores grants.
//
//forge:not-a-component: built by a hand-wired server, not forge's composition root
package grants

type Store interface {
	Get(id string) (string, error)
}
`))
	must(t, writeFile(filepath.Join(pkgDir, "store_sql.go"), `package grants

type SQLStore struct{ db any }

func NewSQLStore(db any) *SQLStore { return &SQLStore{db: db} }

func (s *SQLStore) Get(id string) (string, error) { return id, nil }
`))
	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleInternalPackageContractNames}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got := findingsForRule(fs, string(RuleInternalPackageContractNames)); len(got) != 0 {
		t.Fatalf("a not-a-component package needs no Service/Deps/New shape, got %d finding(s):\n%s",
			len(got), AsResult(fs).FormatText())
	}
}

// TestInternalContracts_ZeroInterfacePackagesSkipped is the table-driven
// guard for the zero-interface early-out. Each row models one of the
// recurring shapes that previously forced cp-forge porters to add
// `contracts.exclude` entries during migration: constants-only,
// structs-only, top-level-funcs-only, and a sibling file split. The two
// negative-control rows (genuine service package, Deps+New without
// Service) prove the early-out is conservative — it doesn't swallow real
// incomplete-Service bugs.
//
// The early-out is a RULE PREDICATE, not a package category: "declares
// no interface, therefore cannot declare `Service`". It needs no name,
// no marker and no forge.yaml entry, which is why there is nothing here
// for an author to learn.
//
// FRICTION 2026-06-02 / 2026-06-03: cp-forge migration shipped at
// least eight such packages with no service-shape surface
// (internal/config, internal/metrics, internal/billing/provideradapters,
// internal/db, internal/planlimits, internal/ratelimit, internal/natsio,
// internal/daemonstate). natsio/daemonstate are already covered by the
// interface-catalogue early-out; the rest are now covered here.
func TestInternalContracts_ZeroInterfacePackagesSkipped(t *testing.T) {
	cases := []struct {
		name    string
		pkgName string
		// files maps file basename → contents. Multiple files supported so
		// we can test the sibling-file split (constants in contract.go,
		// helpers in service.go) the way real utility packages ship.
		files       map[string]string
		wantSkipped bool
	}{
		{
			// internal/planlimits — pure constants/data.
			name:    "constants-only utility",
			pkgName: "planlimits",
			files: map[string]string{
				"contract.go": `package planlimits

const (
	MaxRequestsPerMinute = 60
	MaxUploadBytes       = 10 * 1024 * 1024
)

var DefaultTier = "free"
`,
			},
			wantSkipped: true,
		},
		{
			// internal/config — struct + Load func, no interface.
			name:    "structs and funcs utility",
			pkgName: "config",
			files: map[string]string{
				"contract.go": `package config

type Config struct {
	DatabaseURL string
	Port        int
}

func Load(path string) (*Config, error) { return nil, nil }
`,
			},
			wantSkipped: true,
		},
		{
			// internal/metrics — top-level funcs operating on package globals.
			name:    "top-level funcs only",
			pkgName: "metrics",
			files: map[string]string{
				"contract.go": `package metrics

func IncRequest(label string) {}
func ObserveLatency(label string, ms float64) {}
`,
			},
			wantSkipped: true,
		},
		{
			// internal/db — sibling-file split: contract.go declares the
			// types, helpers.go declares the funcs. The auto-skip must
			// look across all non-test, non-gen files in the package.
			name:    "sibling-file split utility",
			pkgName: "db",
			files: map[string]string{
				"contract.go": `package db

type Pool struct{}

type Tx struct{}
`,
				"helpers.go": `package db

func Open(dsn string) (*Pool, error) { return nil, nil }
func (p *Pool) Begin() (*Tx, error) { return nil, nil }
`,
			},
			wantSkipped: true,
		},
		{
			// Negative control: a genuine, canonical Service package
			// must NOT be auto-skipped. (If it were, the rule would be
			// useless.)
			name:    "genuine service still fires",
			pkgName: "email",
			files: map[string]string{
				"contract.go": `package email

type Sender interface { Send(b []byte) error }
type Config struct{}
func NewSender(c Config) Sender { return nil }
`,
			},
			wantSkipped: false,
		},
		{
			// Negative control: a package with the canonical `Service`
			// interface but missing `Deps` / `New` MUST still fire. The
			// auto-skip only triggers on zero-interface packages; once an
			// interface is declared the rule examines names normally.
			name:    "service interface without deps still fires",
			pkgName: "almost",
			files: map[string]string{
				"contract.go": `package almost

type Service interface { Do() error }
`,
			},
			wantSkipped: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			pkgDir := filepath.Join(tmp, "internal", tc.pkgName)
			must(t, mkdirAll(pkgDir))
			for name, content := range tc.files {
				must(t, writeFile(filepath.Join(pkgDir, name), content))
			}
			fs, err := Inspect(context.Background(), tmp,
				Options{Rules: []Rule{RuleInternalPackageContractNames}},
			)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			got := findingsForRule(fs, string(RuleInternalPackageContractNames))
			if tc.wantSkipped {
				if len(got) != 0 {
					t.Fatalf("zero-interface package %q should be auto-skipped, got %d findings:\n%s",
						tc.pkgName, len(got), AsResult(fs).FormatText())
				}
			} else {
				if len(got) == 0 {
					t.Fatalf("service-shape package %q must fire (not be auto-skipped); got 0 findings",
						tc.pkgName)
				}
			}
		})
	}
}

// TestInternalContracts_StrategyRegistryOptsOutWithExcludeContract pins
// the ONE opt-out an author has to reach for. A strategy-registry
// package — one interface, several impl structs each with its own
// constructor and dep shape — has no single `New(Deps) Service` for the
// injector to bind, so it needs out of the canonical-shape rule.
//
// It says so with `//forge:exclude-contract`, the same header directive
// that takes a package out of bootstrap wiring and mock generation. That
// is one directive covering every "forge does not manage this package"
// case, and it is the directive authors already reach for: 54 files
// across forge and control-plane carry it.
//
// The negative half is the point of the test: the directive is OPT-IN.
// A strategy-shaped package that does NOT carry it still fires all three
// findings — we do not auto-detect the shape, because an incomplete
// Service scaffold looks identical from the AST.
func TestInternalContracts_StrategyRegistryOptsOutWithExcludeContract(t *testing.T) {
	const registryBody = `

type Strategy interface {
	Name() string
	Run(ctx context.Context, input []float64) (float64, error)
}

type momentum struct{ window int }

func NewMomentum(window int) Strategy { return &momentum{window: window} }

func (m *momentum) Name() string { return "momentum" }

func (m *momentum) Run(_ context.Context, _ []float64) (float64, error) { return 0, nil }
`

	cases := []struct {
		name      string
		contract  string
		wantFires int
	}{
		{
			name: "with //forge:exclude-contract the registry is silent",
			contract: `// Strategy-registry package: each algorithm has its own constructor,
// so there is no single New(Deps) Service for forge to bind.
//
//forge:exclude-contract
package algos

import "context"` + registryBody,
			wantFires: 0,
		},
		{
			name: "without the directive the same shape still fires",
			contract: `// Strategy-registry package that never opted out.
package algos

import "context"` + registryBody,
			wantFires: 3,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			pkgDir := filepath.Join(tmp, "internal", "algos")
			must(t, mkdirAll(pkgDir))
			must(t, writeFile(filepath.Join(pkgDir, "contract.go"), tc.contract))

			fs, err := Inspect(context.Background(), tmp,
				Options{Rules: []Rule{RuleInternalPackageContractNames}},
			)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			got := findingsForRule(fs, string(RuleInternalPackageContractNames))
			if len(got) != tc.wantFires {
				t.Fatalf("want %d findings, got %d:\n%s",
					tc.wantFires, len(got), AsResult(fs).FormatText())
			}
		})
	}
}

// TestInternalContracts_ConstructorMarkerFreesConstructorName is the
// constructor half of the marker escape hatch, and it must agree with
// codegen: `codegen.IsComponentConstructor` treats a
// `// forge:constructor`-marked func as THE constructor whatever it is
// named, and `codegen.DetectConstructorName` is what the injector emits.
// A lint that still insisted on the identifier `New` would reject a shape
// forge itself wires — forge contradicting forge, with the user told to
// rename to a worse name.
func TestInternalContracts_ConstructorMarkerFreesConstructorName(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "mailer")
	must(t, mkdirAll(pkgDir))
	must(t, writeFile(filepath.Join(pkgDir, "contract.go"), `package mailer

import "context"

//forge:service
type Mailer interface {
	Send(ctx context.Context, to string) error
}

type Deps struct{}

// Open dials the upstream and returns the mailer.
// forge:constructor
func Open(d Deps) (Mailer, error) { return nil, nil }
`))
	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleInternalPackageContractNames}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleInternalPackageContractNames))
	if len(got) != 0 {
		t.Fatalf("a `// forge:constructor`-marked Open(Deps) (Mailer, error) is the constructor "+
			"codegen wires; lint must accept it. Got %d findings:\n%s",
			len(got), AsResult(fs).FormatText())
	}
}

// TestInternalContracts_MarkedConstructorWithBadSignatureIsTheNearMiss
// asserts the diagnostic points at the func the author already wrote. A
// marked constructor whose signature is wrong is the single most useful
// thing to name; reporting "no constructor" instead sends the author
// hunting for something that is right there.
func TestInternalContracts_MarkedConstructorWithBadSignatureIsTheNearMiss(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "store")
	must(t, mkdirAll(pkgDir))
	must(t, writeFile(filepath.Join(pkgDir, "contract.go"), `package store

//forge:service
type Store interface { Get(k string) string }

type Deps struct{}

// Connect takes a DSN instead of Deps — the injector needs func(Deps) Store,
// so this is a near miss, not an absence.
//forge:constructor
func Connect(dsn string) (Store, error) { return nil, nil }
`))
	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleInternalPackageContractNames}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleInternalPackageContractNames))
	if len(got) != 1 {
		t.Fatalf("expected 1 constructor finding, got %d:\n%s", len(got), AsResult(fs).FormatText())
	}
	if !strings.Contains(got[0].Message, "Connect") {
		t.Errorf("the marked func is the near miss and must be named in the message; got: %s", got[0].Message)
	}
	if got[0].Line != 11 {
		t.Errorf("finding should point at the marked func (line 11), got line %d", got[0].Line)
	}
}

// TestInternalContracts_ConstructorFindingDoesNotDemandTheNameNew pins the
// user-facing prose. The requirement is the SIGNATURE; the way to keep your
// own name is the marker. Text that orders the author to "rename to 'New'"
// makes forge enforce an antipattern — `New` returning `Service` says
// nothing about a mailer or a store — which is the opposite of forge's job.
func TestInternalContracts_ConstructorFindingDoesNotDemandTheNameNew(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "reports")
	must(t, mkdirAll(pkgDir))
	must(t, writeFile(filepath.Join(pkgDir, "contract.go"), `package reports

type Service interface { Run() error }
type Deps struct{}
`))
	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleInternalPackageContractNames}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleInternalPackageContractNames))
	if len(got) != 1 {
		t.Fatalf("expected 1 constructor finding, got %d:\n%s", len(got), AsResult(fs).FormatText())
	}
	f := got[0]
	for _, banned := range []string{"rename to 'New'", "rename the constructor to"} {
		if strings.Contains(f.Message, banned) || strings.Contains(f.Remediation, banned) {
			t.Errorf("finding still orders a rename (%q):\nmessage: %s\nremediation: %s",
				banned, f.Message, f.Remediation)
		}
	}
	// It must instead state the signature and offer the marker.
	for _, want := range []string{"func(Deps) Service", "//forge:constructor"} {
		if !strings.Contains(f.Message+f.Remediation, want) {
			t.Errorf("finding must offer %q:\nmessage: %s\nremediation: %s", want, f.Message, f.Remediation)
		}
	}
}
