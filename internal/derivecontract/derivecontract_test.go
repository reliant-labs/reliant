package derivecontract

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/contractsrc"
	"github.com/reliant-labs/forge/internal/depsurface"
	"github.com/reliant-labs/forge/internal/generator/contract"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// typeChecks is why every test that calls Plan past its preflight is slow:
// Plan type-checks the package through go/packages, which execs `go list`.
const typeChecks = "type-checks the package with go/packages (execs go list)"

// writePkg writes files into root/internal/billing of a fresh module
// (example.com/proj) and returns root and the package directory.
func writePkg(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"go.mod": "module example.com/proj\n\ngo 1.22\n"})
	dir := filepath.Join(root, "internal", "billing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, files)
	return root, dir
}

// TestMain makes every fixture its own module: no go.work in the test's
// environment may claim it. Set once here, not per test, so the refusal
// cases can run in parallel.
func TestMain(m *testing.M) {
	if err := os.Setenv("GOWORK", "off"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// writeFiles writes each slash-separated path of files under base.
func writeFiles(t *testing.T, base string, files map[string]string) {
	t.Helper()
	for name, src := range files {
		path := filepath.Join(base, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func migrate(t *testing.T, root, dir string) *Migration {
	t.Helper()
	m, err := Plan(root, dir)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := Apply(m); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return m
}

const happyContract = `// Package billing charges customers.
//
//forge:outbound-io: calls the payment provider
package billing

import (
	"context"
	"time"
)

// Service charges customers.
type Service interface {
	// Charge bills a customer.
	Charge(ctx context.Context, id string, cents int64) error
	// Refund reverses a charge.
	//
	// forge:no-observe
	Refund(ctx context.Context, id string) error
	Status(ctx context.Context, id string) (string, error) // Status reports a charge's state.
}

// Clock tells the time.
type Clock interface {
	Now() time.Time
}
`

const happyService = `package billing

import "context"

// Deps are the package's collaborators.
type Deps struct {
	Clock Clock
}

type service struct {
	deps Deps
}

// New builds the service.
func New(d Deps) (Service, error) { return &service{deps: d}, nil }

func (s *service) Charge(ctx context.Context, customer string, amount int64) error { return nil }

// Refund undoes a charge.
func (s *service) Refund(ctx context.Context, id string) error { return nil }

func (s service) Status(_ context.Context, id string) (string, error) { return "", nil }

func (s *service) helper() {}
`

// The single-implementation package with only dependency interfaces left in
// contract.go: the type is marked and takes the interface's docs, the
// interfaces move to a forge:deps deps.go, contract.go goes, and its package
// doc and directive survive. The contract derived from the result is the
// Service it replaced.
func TestPlan_MovesDepsAndRemovesContract(t *testing.T) {
	testkit.Slow(t, typeChecks)
	root, dir := writePkg(t, map[string]string{"contract.go": happyContract, "service.go": happyService})
	m := migrate(t, root, dir)

	if m.Type != "service" || m.TypeFile != "service.go" || m.KeptContract || m.DepsFile != "deps.go" {
		t.Errorf("migration = %+v", m)
	}
	if _, err := os.Stat(filepath.Join(dir, "contract.go")); !os.IsNotExist(err) {
		t.Errorf("contract.go should be removed, stat err = %v", err)
	}
	deps := read(t, filepath.Join(dir, "deps.go"))
	if !strings.HasPrefix(deps, depsurface.DepsFileMarker+"\n") || !depsurface.HasDepsMarker(dir) {
		t.Errorf("deps.go must start with the forge:deps marker:\n%s", deps)
	}
	for _, want := range []string{"// Clock tells the time.\ntype Clock interface", `import "time"`} {
		if !strings.Contains(deps, want) {
			t.Errorf("deps.go lacks %q:\n%s", want, deps)
		}
	}
	if strings.Contains(deps, `"context"`) {
		t.Errorf("deps.go kept the import only Service used:\n%s", deps)
	}

	svc := read(t, filepath.Join(dir, "service.go"))
	for _, want := range []string{
		"// Package billing charges customers.\n//\n//forge:outbound-io: calls the payment provider\npackage billing",
		"// service charges customers.\n//\n//forge:contract\ntype service struct",
		"// Charge bills a customer.\nfunc (s *service) Charge(",
		"// Refund undoes a charge.\n// forge:no-observe\nfunc (s *service) Refund(",
		"// Status reports a charge's state.\nfunc (s service) Status(",
	} {
		if !strings.Contains(svc, want) {
			t.Errorf("service.go lacks %q:\n%s", want, svc)
		}
	}
	if !contractsrc.HasMarkedType(dir) || len(contractsrc.HandWrittenContracts(dir)) != 0 {
		t.Errorf("the package must now be marked, with no hand-written contract")
	}

	gen, err := contract.RenderDerivedContract(dir, nil)
	if err != nil {
		t.Fatalf("RenderDerivedContract: %v", err)
	}
	for _, want := range []string{
		"\t// Charge bills a customer.\n\tCharge(ctx context.Context, customer string, amount int64) error",
		"\t// forge:no-observe\n\tRefund(ctx context.Context, id string) error",
		"\tStatus(_ context.Context, id string) (string, error)",
	} {
		if !strings.Contains(string(gen), want) {
			t.Errorf("derived contract lacks %q:\n%s", want, gen)
		}
	}

	// A second run refuses: the package already derives its contract.
	var refused *RefusedError
	if _, err := Plan(root, dir); !errors.As(err, &refused) || !strings.Contains(err.Error(), "already derives") {
		t.Errorf("second run: %v", err)
	}
}

// contract.go that still declares the Deps struct (and New) stays, minus
// Service and the imports only Service used; its interfaces stay with it.
// The type in a `type (...)` group takes the marker inside the group.
func TestPlan_KeepsContractWithDeps(t *testing.T) {
	testkit.Slow(t, typeChecks)
	root, dir := writePkg(t, map[string]string{
		"contract.go": `package billing

import (
	"context"
	"time"
)

// Service schedules.
type Service interface {
	Next(after time.Time) time.Time
}

// Store persists.
type Store interface {
	Save(ctx context.Context, key string) error
}

// Deps are the collaborators.
type Deps struct {
	Store Store
}

// New builds it.
func New(d Deps) (Service, error) { return &scheduler{deps: d}, nil }
`,
		"scheduler.go": `package billing

import "time"

type (
	tick int

	// scheduler is the one implementation.
	scheduler struct {
		deps Deps
	}
)

func (s *scheduler) Next(after time.Time) time.Time { return after }
`,
	})
	m := migrate(t, root, dir)
	if !m.KeptContract || m.DepsFile != "" {
		t.Errorf("migration = %+v", m)
	}
	c := read(t, filepath.Join(dir, "contract.go"))
	if strings.Contains(c, "Service interface") || strings.Contains(c, `"time"`) {
		t.Errorf("contract.go kept Service or its only import:\n%s", c)
	}
	for _, want := range []string{`import "context"`, "type Store interface", "type Deps struct", "func New("} {
		if !strings.Contains(c, want) {
			t.Errorf("contract.go lacks %q:\n%s", want, c)
		}
	}
	s := read(t, filepath.Join(dir, "scheduler.go"))
	if !strings.Contains(s, "\t// scheduler is the one implementation.\n\t//\n\t//forge:contract\n\tscheduler struct") {
		t.Errorf("scheduler.go:\n%s", s)
	}
	if _, err := contract.RenderDerivedContract(dir, nil); err != nil {
		t.Errorf("RenderDerivedContract: %v", err)
	}
}

// A package whose deps.go is forge's gains contract.go's interfaces there,
// with their imports.
func TestPlan_AppendsToMarkedDeps(t *testing.T) {
	testkit.Slow(t, typeChecks)
	root, dir := writePkg(t, map[string]string{
		"contract.go": `package billing

import (
	"context"
	"io"
)

type Service interface {
	Run(ctx context.Context) error
}

// Sink receives output.
type Sink interface {
	io.Writer
}
`,
		"deps.go": depsurface.DepsFileMarker + `

package billing

import "context"

// Store persists.
type Store interface {
	Save(ctx context.Context) error
}
`,
		"service.go": `package billing

import "context"

type Deps struct {
	Store Store
	Sink  Sink
}

type service struct{ deps Deps }

func (s *service) Run(ctx context.Context) error { return nil }
`,
	})
	migrate(t, root, dir)
	deps := read(t, filepath.Join(dir, "deps.go"))
	if !strings.HasPrefix(deps, depsurface.DepsFileMarker) {
		t.Errorf("deps.go lost its marker:\n%s", deps)
	}
	for _, want := range []string{"type Store interface", "// Sink receives output.\ntype Sink interface", `"io"`, `"context"`} {
		if !strings.Contains(deps, want) {
			t.Errorf("deps.go lacks %q:\n%s", want, deps)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "contract.go")); !os.IsNotExist(err) {
		t.Errorf("contract.go should be removed")
	}
	s := read(t, filepath.Join(dir, "service.go"))
	if !strings.Contains(s, "//forge:contract\ntype service struct") {
		t.Errorf("service.go:\n%s", s)
	}
}

// The shape forge's old scaffold wrote: contract.go with Service and the
// dependency-interface guide but no interface, beside a service.go with the
// same ownership banner. contract.go goes; its banner is not duplicated, its
// scaffold description and guide go with it, and a package directive left
// in its body survives in service.go.
func TestPlan_OldScaffoldShape(t *testing.T) {
	testkit.Slow(t, typeChecks)
	root, dir := writePkg(t, map[string]string{
		"contract.go": `// yours: scaffolded once, never touched again — forge will not overwrite this file
// Package contract; canonical pattern lives in ` + "`forge skill load contracts`" + `.
package billing

import "context"

// Service defines the billing package boundary.
// All exported methods on the implementation type must be declared here.
type Service interface {
	Run(ctx context.Context) error
}

// ─── Dependency interfaces ──────────────────────────────────────────
//
// Declare here the *behaviour* this package needs from each thing it
// calls.

//forge:outbound-io: calls the payment provider
`,
		"service.go": `// yours: scaffolded once, never touched again — forge will not overwrite this file
// Service implementation; canonical pattern in ` + "`forge skill load service-layer`" + `.
package billing

import "context"

type Deps struct{}

type service struct {
	deps Deps
}

func (s *service) Run(ctx context.Context) error { return nil }
`,
	})
	m := migrate(t, root, dir)
	if _, err := os.Stat(filepath.Join(dir, "contract.go")); !os.IsNotExist(err) {
		t.Errorf("contract.go should be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "deps.go")); !os.IsNotExist(err) {
		t.Errorf("no dependency interface moved, so no deps.go")
	}
	s := read(t, filepath.Join(dir, "service.go"))
	want := "// yours: scaffolded once, never touched again — forge will not overwrite this file\n// Service implementation; canonical pattern in `forge skill load service-layer`.\npackage billing\n"
	if !strings.HasPrefix(s, want) {
		t.Errorf("service.go header changed:\n%s", s)
	}
	for _, want := range []string{
		"// service defines the billing package boundary.\n// All exported methods on the implementation type must be declared here.\n//\n//forge:contract\ntype service struct",
		"\n//forge:outbound-io: calls the payment provider\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("service.go lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "Dependency interfaces") || strings.Contains(s, "Package contract") {
		t.Errorf("contract.go's own prose moved into service.go:\n%s", s)
	}
	if !strings.Contains(strings.Join(m.Rewrites, "\n"), "3 free-standing comment line(s)") {
		t.Errorf("the dropped guide must be reported: %q", m.Rewrites)
	}
}

// refusalCase is a package Plan must refuse with want in the reason, or
// migrate when want is "". extra files are written relative to the module
// root.
type refusalCase struct {
	name  string
	files map[string]string
	extra map[string]string
	want  string
}

func runRefusals(t *testing.T, cases []refusalCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root, dir := writePkg(t, tc.files)
			writeFiles(t, root, tc.extra)
			m, err := Plan(root, dir)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Plan: %v", err)
				}
				return
			}
			var refused *RefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("Plan = %+v, %v; want a refusal", m, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal = %v\nwant it to contain %q", err, tc.want)
			}
			assertUnchanged(t, dir, tc.files)
		})
	}
}

// assertUnchanged fails for every file of files that is not byte-identical
// in dir, and for any file in dir that files does not name.
func assertUnchanged(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, src := range files {
		if got := read(t, filepath.Join(dir, name)); got != src {
			t.Errorf("%s was rewritten by a refused plan", name)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if _, ok := files[e.Name()]; !ok {
			t.Errorf("a refused plan created %s", e.Name())
		}
	}
}

const runImpl = `package billing

import "context"

type service struct{}

func (s *service) Run(ctx context.Context) error { return nil }
`

const runContract = "package billing\n\nimport \"context\"\n\ntype Service interface {\n\tRun(ctx context.Context) error\n}\n"

// The packages the migration does not apply to at all are refused before
// anything is type-checked.
func TestPlan_PreflightRefusals(t *testing.T) {
	runRefusals(t, []refusalCase{
		{
			name: "already marked",
			files: map[string]string{
				"contract.go": "package billing\n\ntype Deps struct{}\n",
				"service.go":  strings.Replace(runImpl, "type service struct{}", "//forge:contract\ntype service struct{}", 1),
			},
			want: "already derives its contract from the //forge:contract type service (service.go:6)",
		},
		{
			name: "no Service",
			files: map[string]string{
				"contract.go": "package billing\n\ntype Deps struct{}\n",
				"service.go":  runImpl,
			},
			want: "contract.go declares no `type Service interface`",
		},
		{
			name:  "no contract.go",
			files: map[string]string{"service.go": runImpl},
			want:  "it has no contract.go",
		},
		{
			name: "differently named contract interface",
			files: map[string]string{
				"contract.go": "package billing\n\nimport \"context\"\n\n//forge:service\ntype Runner interface {\n\tRun(ctx context.Context) error\n}\n",
				"service.go":  runImpl,
			},
			want: "its contract interface is Runner (contract.go:6), and a derived contract is always named Service",
		},
		{
			name: "excluded package",
			files: map[string]string{
				"contract.go": "//forge:exclude-contract: hand-rolled\npackage billing\n\nimport \"context\"\n\ntype Service interface {\n\tRun(ctx context.Context) error\n}\n",
				"service.go":  runImpl,
			},
			want: "contract.go:1 opts it out of contract generation",
		},
	})
}

// Every shape the migration would change the contract of is refused, with
// the reason, and nothing is written. The implementation is the type
// checker's: method sets, not method names.
func TestPlan_Refusals(t *testing.T) {
	testkit.Slow(t, typeChecks)
	runRefusals(t, []refusalCase{
		{
			name: "two implementations",
			files: map[string]string{
				"contract.go": runContract,
				"service.go":  runImpl,
				"other.go":    "package billing\n\nimport \"context\"\n\ntype fake struct{}\n\nfunc (fake) Run(context.Context) error { return nil }\n",
			},
			want: "2 types implement Service — fake (other.go:5), service (service.go:5)",
		},
		{
			// service's value method set implements Service; so does *other's.
			name: "a value implementation and a pointer one",
			files: map[string]string{
				"contract.go": runContract,
				"service.go":  strings.Replace(runImpl, "(s *service)", "(s service)", 1),
				"other.go":    "package billing\n\nimport \"context\"\n\ntype other struct{}\n\nfunc (o *other) Run(context.Context) error { return nil }\n",
			},
			want: "2 types implement Service — other (other.go:5), service (service.go:5)",
		},
		{
			name: "embedded interface",
			files: map[string]string{
				"contract.go": "package billing\n\nimport (\n\t\"context\"\n\t\"io\"\n)\n\ntype Service interface {\n\tio.Closer\n\tRun(ctx context.Context) error\n}\n",
				"service.go":  runImpl,
			},
			want: "Service embeds io.Closer",
		},
		{
			name: "extra exported method",
			files: map[string]string{
				"contract.go": runContract,
				"service.go":  runImpl + "\nfunc (s *service) Close() error { return nil }\n",
			},
			want: "service exports methods Service does not have — Close (service.go:9)",
		},
		{
			name: "generic implementation",
			files: map[string]string{
				"contract.go": runContract,
				"service.go":  "package billing\n\nimport \"context\"\n\ntype service[T any] struct{}\n\nfunc (s *service[T]) Run(ctx context.Context) error { return nil }\n",
			},
			want: "is generic",
		},
		{
			name: "signature differs",
			files: map[string]string{
				"contract.go": runContract,
				"service.go":  "package billing\n\nimport \"context\"\n\ntype service struct{}\n\nfunc (s *service) Run(ctx context.Context) (int, error) { return 0, nil }\n",
			},
			want: "service.Run is Run(ctx context.Context) (int, error), Service has Run(ctx context.Context) error",
		},
		{
			// The method reads `jobs.Job` in both files, but the two
			// `jobs` are different packages: the same text, another type.
			name: "same text, different named type",
			files: map[string]string{
				"contract.go": "package billing\n\nimport \"example.com/proj/internal/jobs\"\n\ntype Service interface {\n\tGet() jobs.Job\n}\n",
				"service.go":  "package billing\n\nimport \"example.com/proj/internal/legacy/jobs\"\n\ntype service struct{}\n\nfunc (s *service) Get() jobs.Job { return jobs.Job{} }\n",
			},
			extra: map[string]string{
				"internal/jobs/jobs.go":        "package jobs\n\ntype Job struct{}\n",
				"internal/legacy/jobs/jobs.go": "package jobs\n\ntype Job struct{ Legacy bool }\n",
			},
			want: "service.Get is Get() example.com/proj/internal/legacy/jobs.Job, Service has Get() example.com/proj/internal/jobs.Job",
		},
		{
			name: "only through an embedded field",
			files: map[string]string{
				"contract.go": "package billing\n\nimport \"context\"\n\ntype Service interface {\n\tRun(ctx context.Context) error\n\tStop()\n}\n",
				"service.go":  "package billing\n\nimport \"context\"\n\ntype runner struct{}\n\nfunc (runner) Run(ctx context.Context) error { return nil }\n\ntype service struct{ runner }\n\nfunc (s *service) Stop() {}\n",
			},
			want: "service gets Service's Run only through its embedded field runner",
		},
		{
			// The derived contract lists exported methods: stop would go.
			name: "unexported Service method",
			files: map[string]string{
				"contract.go": "package billing\n\nimport \"context\"\n\ntype Service interface {\n\tRun(ctx context.Context) error\n\tstop()\n}\n",
				"service.go":  runImpl + "\nfunc (s *service) stop() {}\n",
			},
			want: "the derived contract would lose Service.stop",
		},
		{
			name: "does not type-check",
			files: map[string]string{
				"contract.go": runContract,
				"service.go":  runImpl + "\nvar _ = undefinedThing\n",
			},
			want: "the package does not type-check, so the migration cannot prove it keeps the contract — fix the build first: internal/billing/service.go:9:9: undefined: undefinedThing",
		},
		{
			name: "a file outside the build",
			files: map[string]string{
				"contract.go": runContract,
				"service.go":  runImpl,
				"plan9.go":    "//go:build plan9\n\npackage billing\n\nfunc (s *service) Plan9() {}\n",
			},
			want: "build constraints leave plan9.go out of this build",
		},
		{
			name: "only a promoted implementation",
			files: map[string]string{
				"contract.go": runContract,
				"service.go":  runImpl + "\ntype wrapper struct{ *service }\n",
			},
			// service declares Run: wrapper only promotes it, so it is not a
			// second implementation; this case migrates.
			want: "",
		},
	})
}

// Parameter names and import aliases do not make a signature differ: the
// implementation's file may name a package differently from contract.go.
func TestPlan_AliasesAndNamesAreTheSameSignature(t *testing.T) {
	testkit.Slow(t, typeChecks)
	root, dir := writePkg(t, map[string]string{
		"contract.go": "package billing\n\nimport (\n\tstdctx \"context\"\n\t\"time\"\n)\n\ntype Service interface {\n\tAt(c stdctx.Context, d time.Duration, more ...string) (t time.Time, err error)\n}\n",
		"service.go":  "package billing\n\nimport (\n\t\"context\"\n\ttm \"time\"\n)\n\ntype service struct{}\n\nfunc (service) At(context.Context, tm.Duration, ...string) (tm.Time, error) { return tm.Time{}, nil }\n",
	})
	migrate(t, root, dir)
	if _, err := os.Stat(filepath.Join(dir, "contract.go")); !os.IsNotExist(err) {
		t.Errorf("contract.go should be removed")
	}
}

// The rewritten package, with the contract_gen.go forge derives from it,
// builds and vets: the migration is a compile-preserving change.
func TestPlan_ResultBuilds(t *testing.T) {
	testkit.Slow(t, "execs go vet")
	root, dir := writePkg(t, map[string]string{"contract.go": happyContract, "service.go": happyService})
	if m := migrate(t, root, dir); !m.PointerOnly {
		t.Errorf("service's Charge and Refund have pointer receivers, so only *service implements Service: %+v", m)
	}
	if _, err := contract.SyncDerivedContract(dir, contract.Options{}); err != nil {
		t.Fatalf("SyncDerivedContract: %v", err)
	}
	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go vet: %v\n%s", err, out)
	}
}

// An implementation whose methods all have pointer receivers implements
// Service only through *T: it migrates, and the plan says so. One with value
// receivers only does not need the pointer.
func TestPlan_PointerAndValueMethodSets(t *testing.T) {
	testkit.Slow(t, typeChecks)
	root, dir := writePkg(t, map[string]string{"contract.go": runContract, "service.go": runImpl})
	if m := migrate(t, root, dir); !m.PointerOnly || m.Type != "service" {
		t.Errorf("pointer receivers: migration = %+v, want PointerOnly", m)
	}
	root, dir = writePkg(t, map[string]string{"contract.go": runContract, "service.go": strings.Replace(runImpl, "(s *service)", "(s service)", 1)})
	if m := migrate(t, root, dir); m.PointerOnly {
		t.Errorf("value receivers: migration = %+v, want !PointerOnly", m)
	}
}

// A package-local interface whose method set satisfies Service — here a
// role embedding it, and one equal to it — is a contract, not a second
// implementation: the concrete type still migrates.
func TestPlan_LocalInterfaceIsNotAnImplementation(t *testing.T) {
	testkit.Slow(t, typeChecks)
	root, dir := writePkg(t, map[string]string{
		"contract.go": runContract,
		"service.go":  runImpl,
		"roles.go":    "package billing\n\nimport \"context\"\n\n// Runner is a role callers declare.\ntype Runner interface {\n\tRun(ctx context.Context) error\n}\n\n// Closer embeds the contract.\ntype Closer interface {\n\tService\n\tClose() error\n}\n",
	})
	if m := migrate(t, root, dir); m.Type != "service" {
		t.Fatalf("migration = %+v, want the concrete service", m)
	}
}

// Signatures are equal when their types are, whatever the text: a type
// alias spells context.Context another way.
func TestPlan_TypeAliasIsTheSameSignature(t *testing.T) {
	testkit.Slow(t, typeChecks)
	root, dir := writePkg(t, map[string]string{
		"contract.go": runContract,
		"service.go":  "package billing\n\nimport \"context\"\n\ntype ctx = context.Context\n\ntype service struct{}\n\nfunc (s *service) Run(c ctx) error { return nil }\n",
	})
	migrate(t, root, dir)
	if !contractsrc.HasMarkedType(dir) {
		t.Errorf("service should carry //forge:contract")
	}
}

// The migration writes only inside the package, and the package only inside
// the project: a package directory, or a contract.go, that is a symlink out
// of the project is refused, and nothing outside is touched.
func TestPlan_RefusesSymlinksOutOfTheProject(t *testing.T) {
	testkit.Slow(t, typeChecks)
	files := map[string]string{"contract.go": happyContract, "service.go": happyService}

	t.Run("package directory", func(t *testing.T) {
		root, dir := writePkg(t, nil)
		outside := t.TempDir()
		writeFiles(t, outside, files)
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, dir); err != nil {
			t.Skipf("host resource missing: this filesystem cannot create symlinks: %v", err)
		}
		_, err := Plan(root, "internal/billing")
		var refused *RefusedError
		if !errors.As(err, &refused) || !strings.Contains(err.Error(), "outside the project") {
			t.Fatalf("Plan = %v; want a refusal naming the escape", err)
		}
		assertUnchanged(t, outside, files)
	})

	t.Run("contract.go", func(t *testing.T) {
		root, dir := writePkg(t, map[string]string{"service.go": happyService})
		outside := t.TempDir()
		writeFiles(t, outside, map[string]string{"contract.go": happyContract})
		if err := os.Symlink(filepath.Join(outside, "contract.go"), filepath.Join(dir, "contract.go")); err != nil {
			t.Skipf("host resource missing: this filesystem cannot create symlinks: %v", err)
		}
		_, err := Plan(root, dir)
		var refused *RefusedError
		if !errors.As(err, &refused) || !strings.Contains(err.Error(), "contract.go is a symlink to") {
			t.Fatalf("Plan = %v; want a refusal naming contract.go's escape", err)
		}
		assertUnchanged(t, outside, map[string]string{"contract.go": happyContract})
		if got := read(t, filepath.Join(dir, "service.go")); got != happyService {
			t.Errorf("service.go was rewritten by a refused plan")
		}
	})
}

// Apply is one transaction: a write or a removal failing partway leaves
// every file byte-identical to before — rewritten files restored, created
// ones gone, removed ones still there.
func TestApply_FailurePartwayRestoresEverything(t *testing.T) {
	setup := func(t *testing.T) (string, *Migration) {
		t.Helper()
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"a.go": "package a // a\n", "b.go": "package a // b\n", "c.go": "package a // c\n"})
		return dir, &Migration{
			Writes: []FileWrite{
				{Path: filepath.Join(dir, "a.go"), Content: []byte("package a // A\n")},
				{Path: filepath.Join(dir, "new.go"), Content: []byte("package a // new\n")},
				{Path: filepath.Join(dir, "b.go"), Content: []byte("package a // B\n")},
			},
			Removes: []string{filepath.Join(dir, "c.go")},
		}
	}
	before := map[string]string{"a.go": "package a // a\n", "b.go": "package a // b\n", "c.go": "package a // c\n"}
	boom := errors.New("disk full")

	for _, failAt := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("write %d fails", failAt), func(t *testing.T) {
			dir, m := setup(t)
			n := 0
			real := writeFile
			writeFile = func(path string, data []byte, perm os.FileMode) error {
				if n++; n == failAt {
					return boom
				}
				return real(path, data, perm)
			}
			t.Cleanup(func() { writeFile = real })
			if err := Apply(m); !errors.Is(err, boom) || !strings.Contains(err.Error(), "every file was restored") {
				t.Fatalf("Apply = %v; want the write's error, reported as restored", err)
			}
			assertUnchanged(t, dir, before)
		})
	}

	t.Run("removal fails", func(t *testing.T) {
		dir, m := setup(t)
		m.Removes = append([]string{filepath.Join(dir, "b.go")}, m.Removes...)
		m.Writes = m.Writes[:2]
		n := 0
		real := removeFile
		removeFile = func(path string) error {
			if n++; n == 2 {
				return boom
			}
			return real(path)
		}
		t.Cleanup(func() { removeFile = real })
		if err := Apply(m); !errors.Is(err, boom) {
			t.Fatalf("Apply = %v; want the removal's error", err)
		}
		assertUnchanged(t, dir, before)
	})

	t.Run("success", func(t *testing.T) {
		dir, m := setup(t)
		if err := Apply(m); err != nil {
			t.Fatal(err)
		}
		assertUnchanged(t, dir, map[string]string{"a.go": "package a // A\n", "b.go": "package a // B\n", "new.go": "package a // new\n"})
	})
}
