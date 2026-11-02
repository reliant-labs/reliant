package contractcheck

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/forgeconv"
)

// TestLintDepsAreInterfaces_FiresOnUnmarkedPackage is the regression
// that pins the rule's scope. The fixture package carries NO role
// marker — it is an ordinary internal package with a `type Deps struct`
// — and it must still be flagged.
//
// The rule was previously gated on a per-package role marker, and the
// gate is why it covered zero packages in forge's own tree and zero in
// control-plane, which was carrying 28 concrete-typed Deps fields at
// the time. Restore any such gate and this test fails.
func TestLintDepsAreInterfaces_FiresOnUnmarkedPackage(t *testing.T) {
	if testing.Short() {
		t.Skip("loads fixture packages via go/packages; runs in task test")
	}
	t.Parallel()
	root := filepath.Join("testdata", "deps_concrete")

	// Guard the guard: if the fixture ever grows a role marker, the test
	// stops proving the un-gated property it exists to prove.
	src := readFixture(t, filepath.Join(root, "internal", "checkout", "contract.go"))
	if strings.Contains(src, "forge:") {
		t.Fatalf("fixture must carry NO forge directive — that is what makes this an un-gated-rule test:\n%s", src)
	}

	fs, err := Inspect(context.Background(), root,
		Options{Rules: []Rule{RuleDepsAreInterfaces}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleDepsAreInterfaces))
	if len(got) != 2 {
		t.Fatalf("expected 2 findings (Charger, Store), got %d:\n%s",
			len(got), AsResult(fs).FormatText())
	}

	joined := got[0].Message + "\n" + got[1].Message
	// Both concrete shapes must be caught: the same-package struct
	// pointer and the cross-package selector pointer. The second is the
	// `Deps: *db.PostgresRepository` shape this rule exists for.
	for _, want := range []string{"Charger", "*stripeAdapter", "Store", "*db.PostgresRepository"} {
		if !strings.Contains(joined, want) {
			t.Errorf("findings should name %q; got:\n%s", want, joined)
		}
	}

	// Severity is ERROR. It was a warning while the rule assumed every
	// off-module `pkg.T` was concrete — an assumption that produced 9 false
	// findings out of 28 on control-plane (jetstream.JetStream ×7,
	// client.Client, audit.Store — all three declared `type … interface` by
	// the package that ships them), and warning severity was how that was
	// absorbed. Off-module selector resolution removed the class, which is
	// what makes the promotion honest: a rule you can leave at warning is a
	// rule authors learn to scroll past. Demote this and the un-gated rule
	// buys nothing over the marker-gated one it replaced.
	for _, f := range got {
		if f.Severity != forgeconv.SeverityError {
			t.Errorf("rule must be an error — see the header of deps_are_interfaces.go; got %s", f.Severity)
		}
		if !strings.Contains(f.Remediation, "mock_gen.go") {
			t.Errorf("remediation should point at the mocks forge already generates; got: %s", f.Remediation)
		}
	}
	if !HasErrors(fs) {
		t.Errorf("a concrete Deps field must fail the lint gate; HasErrors() = false")
	}
}

// TestLintDepsAreInterfaces_CleanFixture verifies a well-formed package
// (Deps fields are interfaces, plus the always-allowed Logger and
// primitive config data) produces no findings.
func TestLintDepsAreInterfaces_CleanFixture(t *testing.T) {
	t.Parallel()
	fs, err := Inspect(context.Background(),
		filepath.Join("testdata", "deps_clean"),
		Options{Rules: []Rule{RuleDepsAreInterfaces}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleDepsAreInterfaces))
	if len(got) != 0 {
		t.Fatalf("expected 0 findings on clean fixture, got %d:\n%s",
			len(got), AsResult(fs).FormatText())
	}
}

// TestLintDepsAreInterfaces_NoDepsStructIsSilent confirms the rule's
// scope: a package that never declared `type Deps struct` never opted
// into the composition shape, so the rule has nothing to say about it.
// contracts_good/ has a contract.go with a Service and no Deps.
func TestLintDepsAreInterfaces_NoDepsStructIsSilent(t *testing.T) {
	t.Parallel()
	fs, err := Inspect(context.Background(),
		filepath.Join("testdata", "contracts_good"),
		Options{Rules: []Rule{RuleDepsAreInterfaces}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got := findingsForRule(fs, string(RuleDepsAreInterfaces)); len(got) != 0 {
		t.Fatalf("a package with no Deps struct must produce 0 findings, got %d:\n%s",
			len(got), AsResult(fs).FormatText())
	}
}

// TestLintDepsAreInterfaces_ResolvesSameModuleSelectors pins the check
// that makes the rule usable now that it runs on every package: a
// cross-package dep typed `pkg.T` is RESOLVED, not guessed.
//
// A correct Deps is mostly cross-package interfaces — `Users user.Service`
// is the shape the composition is built on. Firing on all of those would
// make every hit a false positive, and forge's own tree proved it: before
// resolution the rule reported exactly two findings across the whole
// repo, `codegen.Parser` and `contract.Service`, and both are interfaces.
//
// A pointer to a selector stays a finding whatever it points at: `*pkg.T`
// is a concrete pointer, and a pointer to an interface is a mistake, not
// a seam.
func TestLintDepsAreInterfaces_ResolvesSameModuleSelectors(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	must(t, writeFile(filepath.Join(tmp, "go.mod"), "module example.com/app\n\ngo 1.24\n"))

	storeDir := filepath.Join(tmp, "internal", "store")
	must(t, mkdirAll(storeDir))
	must(t, writeFile(filepath.Join(storeDir, "contract.go"), `package store

import "context"

// Service is an interface — a dep typed with it must NOT fire.
type Service interface {
	Get(ctx context.Context, id string) error
}

// Repo is a struct WITH BEHAVIOUR — a dep typed with it MUST fire.
// The method matters: a zero-method struct is data, and the rule
// deliberately does not demand an interface over nothing.
type Repo struct{ dsn string }

func (r *Repo) Save(ctx context.Context, id string) error { return nil }
`))

	orderDir := filepath.Join(tmp, "internal", "orders")
	must(t, mkdirAll(orderDir))
	must(t, writeFile(filepath.Join(orderDir, "contract.go"), `package orders

type Service interface{ Do() error }
`))
	must(t, writeFile(filepath.Join(orderDir, "service.go"), `package orders

import "example.com/app/internal/store"

type Deps struct {
	Store   store.Service // interface — silent
	Repo    store.Repo    // struct — fires
	RepoPtr *store.Repo   // pointer — fires
}

type service struct{ deps Deps }

func New(d Deps) Service { return &service{deps: d} }
`))

	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleDepsAreInterfaces}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleDepsAreInterfaces))
	if len(got) != 2 {
		t.Fatalf("expected 2 findings (Repo, RepoPtr) and silence on the interface dep, got %d:\n%s",
			len(got), AsResult(fs).FormatText())
	}
	joined := got[0].Message + "\n" + got[1].Message
	if strings.Contains(joined, `"Store"`) {
		t.Errorf("a dep typed with a same-module INTERFACE must not fire; got:\n%s", joined)
	}
	for _, want := range []string{`"Repo"`, `"RepoPtr"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected a finding for %s; got:\n%s", want, joined)
		}
	}
}

// TestLintDepsAreInterfaces_GenericInstantiations: a Deps field typed with
// an instantiated generic is judged by the generic type's declaration.
// reliant's `streaming.UpdateHub[db.UserUpdate]` is an interface and was
// refused as "concrete type *ast.IndexExpr" — wrong on both counts. A
// generic STRUCT still fires, a pointer to an instantiation still fires,
// and the message renders the type as source, never as an AST node name.
func TestLintDepsAreInterfaces_GenericInstantiations(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	must(t, writeFile(filepath.Join(tmp, "go.mod"), "module example.com/app\n\ngo 1.24\n"))

	hubDir := filepath.Join(tmp, "internal", "hub")
	must(t, mkdirAll(hubDir))
	must(t, writeFile(filepath.Join(hubDir, "hub.go"), `package hub

import "context"

// Subscriber is a generic INTERFACE: an instantiation must NOT fire.
type Subscriber[T any] interface {
	Subscribe(ctx context.Context, key string) (<-chan T, func())
}

// Pair is a generic interface with two type parameters.
type Pair[K comparable, V any] interface{ Get(K) (V, bool) }

// Broker is a generic STRUCT with behaviour: an instantiation MUST fire.
type Broker[T any] struct{ subs map[string][]chan T }

func (b *Broker[T]) Publish(ctx context.Context, key string, v T) error { return nil }
`))

	svcDir := filepath.Join(tmp, "internal", "events")
	must(t, mkdirAll(svcDir))
	must(t, writeFile(filepath.Join(svcDir, "service.go"), `package events

import "example.com/app/internal/hub"

type Event struct{ ID string }

// Local is a same-package generic interface.
type Local[T any] interface{ Next() T }

type Deps struct {
	Hub    hub.Subscriber[Event]     // generic interface — silent
	Lookup hub.Pair[string, Event]   // two type args — silent
	Next   Local[Event]              // same-package generic — silent
	Broker hub.Broker[Event]         // generic struct — fires
	BPtr   *hub.Broker[Event]        // pointer to instantiation — fires
	HubPtr *hub.Subscriber[Event]    // pointer to an interface — fires
}
`))

	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleDepsAreInterfaces}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleDepsAreInterfaces))
	var joined strings.Builder
	for _, f := range got {
		joined.WriteString(f.Message + "\n")
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 findings (Broker, BPtr, HubPtr), got %d:\n%s", len(got), joined.String())
	}
	for _, silent := range []string{`"Hub"`, `"Lookup"`, `"Next"`} {
		if strings.Contains(joined.String(), silent) {
			t.Errorf("a generic interface dep %s must not fire; got:\n%s", silent, joined.String())
		}
	}
	for _, want := range []string{
		`"Broker" has concrete type hub.Broker[Event]`,
		`"BPtr" has concrete type *hub.Broker[Event]`,
		`"HubPtr" has concrete type *hub.Subscriber[Event]`,
	} {
		if !strings.Contains(joined.String(), want) {
			t.Errorf("expected %q in:\n%s", want, joined.String())
		}
	}
	if strings.Contains(joined.String(), "*ast.") {
		t.Errorf("a finding named a Go AST node instead of the type:\n%s", joined.String())
	}
}

// TestLintDepsAreInterfaces_NoInternalDir confirms projects without an
// internal/ tree (CLI / library kinds) get an empty result rather than
// an error.
func TestLintDepsAreInterfaces_NoInternalDir(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleDepsAreInterfaces}},
	)
	if err != nil {
		t.Fatalf("Inspect on empty project: %v", err)
	}
	if len(fs) != 0 {
		t.Errorf("empty project should produce 0 findings, got %d", len(fs))
	}
}

// TestLintDepsAreInterfaces_ResolvesOffModuleSelectors is the regression
// behind this rule's promotion to error severity.
//
// The rule used to resolve `pkg.T` only when pkg lived in the project's OWN
// module, and assumed everything else was concrete. Measured on
// control-plane that assumption produced 9 false findings out of 28 —
// `jetstream.JetStream` (7 fields), `client.Client` and forge's own
// `audit.Store` — every one of them declared `type … interface` by the
// package that ships it. Warning severity existed to absorb exactly that,
// so the false positives and the un-promotable severity were one defect.
//
// The fixture reaches another module through a filesystem `replace`, so the
// lookup is hermetic: no module cache, no network, nothing to download. The
// dep module exports BOTH shapes under one package qualifier, which is what
// makes the assertion sharp — resolving must silence the interface WITHOUT
// silencing the concrete pointer beside it. A resolver that gave up and
// returned "not an interface" would flag both; one that gave up the other
// way would flag neither.
func TestLintDepsAreInterfaces_ResolvesOffModuleSelectors(t *testing.T) {
	// The fixture is a standalone module that is deliberately NOT a member
	// of forge's own go.work; without this, go list refuses to resolve it
	// from inside the workspace. A real project is linted at its own root,
	// where its own go.work (if any) is the correct one to honour.
	t.Setenv("GOWORK", "off")

	root := filepath.Join("testdata", "deps_offmodule")
	fs, err := Inspect(context.Background(), root,
		Options{Rules: []Rule{RuleDepsAreInterfaces}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleDepsAreInterfaces))
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 finding (*stream.Conn), got %d:\n%s\n"+
			"More than one means the off-module INTERFACE was flagged too — the\n"+
			"false-positive class that made error severity dishonest.",
			len(got), AsResult(fs).FormatText())
	}
	if !strings.Contains(got[0].Message, "*stream.Conn") {
		t.Errorf("the one finding must be the concrete pointer; got: %s", got[0].Message)
	}
	if strings.Contains(got[0].Message, "Events") {
		t.Errorf("stream.Publisher is an interface in another module and must not be flagged; got: %s", got[0].Message)
	}
}

// TestLintDepsAreInterfaces_FailedLookupIsAnErrorNotAFinding pins what the
// rule does when it cannot ASK where an off-module package lives.
//
// An unresolvable selector is judged concrete, which is right for a package
// that is not on disk and wrong for a `go list` that never answered. The two
// used to be one case: on a loaded box (load ~1500) the lookup of
// controller-runtime's client package outlived its 20s bound, and `forge
// lint` failed control-plane on five `client.Client` / `client.Reader` Deps
// fields — interfaces — at error severity, while a rerun of the same tree
// passed. A failed lookup must fail the rule with its cause, and report no
// findings built on the unanswered question.
//
// The same fixture as the off-module test, with a `go` on PATH that fails
// the way a broken toolchain does. Not parallel: it sets PATH.
func TestLintDepsAreInterfaces_FailedLookupIsAnErrorNotAFinding(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the failing `go` stub is a POSIX sh script; the lookup code it exercises is not OS-specific")
	}
	t.Setenv("GOWORK", "off")
	bin := t.TempDir()
	stub := "#!/bin/sh\necho 'go: simulated toolchain failure' >&2\nexit 2\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	root := filepath.Join("testdata", "deps_offmodule")
	fs, err := Inspect(context.Background(), root,
		Options{Rules: []Rule{RuleDepsAreInterfaces}},
	)
	if err == nil {
		t.Fatalf("a failed go list must fail the rule; got no error and %d finding(s):\n%s",
			len(fs), AsResult(fs).FormatText())
	}
	for _, want := range []string{"go list ", "simulated toolchain failure", string(RuleDepsAreInterfaces)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q so the cause is readable; got: %v", want, err)
		}
	}
	if len(fs) != 0 {
		t.Errorf("no findings may be reported from an unanswered lookup; got:\n%s", AsResult(fs).FormatText())
	}
}

// TestLintDepsAreInterfaces_FuncSeamsAreNotConcreteDeps pins the
// exemption for FUNCTION-typed Deps fields.
//
// forge documents and WIRES this shape: the Clock/IDGen seam
// (service-layer skill, "Deterministic time & IDs") is a Deps field typed
// exactly `func() time.Time` / `func() string`, filled by TYPE in the
// generated compose.go (`Now: time.Now, // framework clock`) and defaulted
// again in the generated test harness. This rule used to fire on it, so
// two forge mechanisms contradicted each other: codegen wrote the field,
// the lint failed the build on it. A real run resolved the contradiction
// by DELETING the seam from two packages — the app permanently lost its
// testable clock and the gate went green over the loss.
//
// The exemption is the class, not the two wired signatures: a func value
// is already substitutable in one line with no mock and no generated file,
// so the rule's own remedy ("declare a narrow interface naming the methods
// you call") has nothing to name.
//
// The other half of the assertion is that the exemption is NARROW: the
// concrete repository pointer sitting in the same Deps struct still fires,
// because that is the type the rule exists for.
func TestLintDepsAreInterfaces_FuncSeamsAreNotConcreteDeps(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	must(t, writeFile(filepath.Join(tmp, "go.mod"), "module example.com/app\n\ngo 1.24\n"))

	storeDir := filepath.Join(tmp, "internal", "store")
	must(t, mkdirAll(storeDir))
	must(t, writeFile(filepath.Join(storeDir, "contract.go"), `package store

import "context"

// Repo is the concrete repository this rule exists to catch.
type Repo struct{ dsn string }

func (r *Repo) Save(ctx context.Context, id string) error { return nil }
`))

	svcDir := filepath.Join(tmp, "internal", "orders")
	must(t, mkdirAll(svcDir))
	must(t, writeFile(filepath.Join(svcDir, "contract.go"), `package orders

type Service interface{ Do() error }
`))
	must(t, writeFile(filepath.Join(svcDir, "service.go"), `package orders

import (
	"context"
	"time"

	"example.com/app/internal/store"
)

type Deps struct {
	Now     func() time.Time                    // framework Clock seam — silent
	NewID   func() string                       // framework IDGen seam — silent
	Notify  func(context.Context, string) error // an app's own func dep — silent
	Repo    *store.Repo                         // concrete repository — fires
}

type service struct{ deps Deps }

func New(d Deps) Service { return &service{deps: d} }
`))

	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleDepsAreInterfaces}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleDepsAreInterfaces))
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 finding (Repo) and silence on every func-typed field, got %d:\n%s",
			len(got), AsResult(fs).FormatText())
	}
	if !strings.Contains(got[0].Message, `"Repo"`) {
		t.Errorf("the one finding must be the concrete repository pointer; got: %s", got[0].Message)
	}
	for _, seam := range []string{`"Now"`, `"NewID"`, `"Notify"`} {
		if strings.Contains(got[0].Message, seam) {
			t.Errorf("a func-typed Deps field is already a substitution seam and must not fire (%s); got: %s",
				seam, got[0].Message)
		}
	}
}

// TestLintDepsAreInterfaces_DataStructsAreNotCollaborators pins the
// discriminator between a dep and a data bag, in its simplest form: a
// struct with no methods at all.
//
// The rule's remedy is "declare a narrow interface naming the methods you
// call". A type with no methods has none to name — an interface over it is
// the empty interface, which asserts nothing and mocks nothing — so firing
// there demands a change that cannot be made. Config-on-Deps is a real
// design question and forge-config-deps owns it; this rule is about
// behaviour you cannot fake.
//
// A zero-method struct is the VACUOUS case of the general test (every
// method is a pure accessor); the method-carrying config that motivated
// the general form is pinned by
// TestLintDepsAreInterfaces_ConfigWithPureAccessorsIsData.
//
// The escape is closed on the side that matters: an embedded field promotes
// the embedded type's entire method set, so a wrapper that declares nothing
// and carries hundreds of methods still fires.
func TestLintDepsAreInterfaces_DataStructsAreNotCollaborators(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	must(t, writeFile(filepath.Join(tmp, "go.mod"), "module example.com/app\n\ngo 1.24\n"))

	settingsDir := filepath.Join(tmp, "internal", "settings")
	must(t, mkdirAll(settingsDir))
	must(t, writeFile(filepath.Join(settingsDir, "settings.go"), `package settings

import "database/sql"

// Limits is DATA: parsed from YAML, zero methods. Nothing to mock.
type Limits struct {
	MaxUploadBytes int64
	AllowedHosts   []string
}

// Wrapper declares no methods of its own but EMBEDS one that has many,
// so its method set is large and it is a collaborator, not data.
type Wrapper struct{ *sql.DB }
`))

	appDir := filepath.Join(tmp, "internal", "uploads")
	must(t, mkdirAll(appDir))
	must(t, writeFile(filepath.Join(appDir, "contract.go"), `package uploads

type Service interface{ Do() error }
`))
	must(t, writeFile(filepath.Join(appDir, "service.go"), `package uploads

import "example.com/app/internal/settings"

type Deps struct {
	Limits    *settings.Limits           // zero-method data — silent
	Rosters   []settings.Limits          // a collection of data is data — silent
	ByTier    map[string]*settings.Limits // likewise — silent
	Wrapped   *settings.Wrapper          // promotes *sql.DB's methods — fires
	Wrappers  []*settings.Wrapper        // a collection of collaborators — fires
}

type service struct{ deps Deps }

func New(d Deps) Service { return &service{deps: d} }
`))

	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleDepsAreInterfaces}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleDepsAreInterfaces))
	if len(got) != 2 {
		t.Fatalf("expected exactly 2 findings (Wrapped, Wrappers), got %d:\n%s",
			len(got), AsResult(fs).FormatText())
	}
	text := got[0].Message + "\n" + got[1].Message
	for _, want := range []string{`"Wrapped"`, `"Wrappers"`} {
		if !strings.Contains(text, want) {
			t.Errorf("the embedding wrapper %s must be a finding; got:\n%s", want, text)
		}
	}
	for _, silent := range []string{`"Limits"`, `"Rosters"`, `"ByTier"`} {
		if strings.Contains(text, silent) {
			t.Errorf("data (or a collection of it) must not fire — there is no interface to extract; %s fired:\n%s", silent, text)
		}
	}
}

// TestLintDepsAreInterfaces_ConfigWithPureAccessorsIsData is the
// regression for the bug this discriminator was rewritten to fix.
//
// The exemption used to be "zero declared methods", and its own comment
// named the case it existed for: control-plane's `*config.WorkspaceConfig`,
// a YAML-deserialized bag of storage defaults and probe shapes. That config
// later grew two tier-lookup accessors — `StorageSizeForTier(tier string)
// string` and `DockerStorageSizeForTier`, each a lookup in the struct's own
// map with a fallback to its own default field — and the exemption stopped
// covering the case it was written for. `forge lint` failed control-plane's
// gate on a struct the rule's author had already decided was data.
//
// A method COUNT was never the distinction. A config that grows two
// convenience accessors over its OWN fields is still data: there is nothing
// behind it for a test to fake, and the remedy the finding prescribes
// produces an interface wrapping a map index. What actually separates the
// two is whether the methods reach anything — so the test is purity, and a
// zero-method struct passes it vacuously.
//
// The fixture is a faithful reduction of the real type: the same two-ladder
// shape, the same fallback-to-own-field, the same package-level const and
// map defaults, and a `time.Duration(...)` conversion — which is
// syntactically a call and must not be read as one, or the exemption breaks
// again the first time a config renders a duration.
func TestLintDepsAreInterfaces_ConfigWithPureAccessorsIsData(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	must(t, writeFile(filepath.Join(tmp, "go.mod"), "module example.com/app\n\ngo 1.24\n"))

	cfgDir := filepath.Join(tmp, "internal", "config")
	must(t, mkdirAll(cfgDir))
	must(t, writeFile(filepath.Join(cfgDir, "workspace_config.go"), `package config

import "time"

const defaultStorageSize = "20Gi"

var defaultDaemonStorageSizes = map[string]string{"small": "20Gi", "large": "80Gi"}

// WorkspaceConfig is DATA: deserialized from YAML, with two accessors that
// index its own maps and fall back to its own default fields.
type WorkspaceConfig struct {
	DefaultStorageSize string            `+"`"+`yaml:"defaultStorageSize"`+"`"+`
	DaemonStorageSizes map[string]string `+"`"+`yaml:"daemonStorageSizes"`+"`"+`
	IdleSeconds        int               `+"`"+`yaml:"idleSeconds"`+"`"+`
}

// StorageSizeForTier is the real shape: own map, own default field.
func (c *WorkspaceConfig) StorageSizeForTier(tier string) string {
	if size, ok := c.DaemonStorageSizes[tier]; ok && size != "" {
		return size
	}
	return c.DefaultStorageSize
}

// TierCount uses a builtin over its own field.
func (c *WorkspaceConfig) TierCount() int { return len(c.DaemonStorageSizes) }

// IdleTimeout performs a CONVERSION, which is syntactically a call. Reading
// it as a call would make this config a collaborator and re-break the rule.
func (c *WorkspaceConfig) IdleTimeout() time.Duration {
	return time.Duration(c.IdleSeconds) * time.Second
}

// DefaultWorkspaceConfig is a package-level FUNC, not a method. Its
// presence must not make the type a collaborator.
func DefaultWorkspaceConfig() *WorkspaceConfig {
	return &WorkspaceConfig{
		DefaultStorageSize: defaultStorageSize,
		DaemonStorageSizes: defaultDaemonStorageSizes,
	}
}
`))

	opDir := filepath.Join(tmp, "internal", "workspaceop")
	must(t, mkdirAll(opDir))
	must(t, writeFile(filepath.Join(opDir, "contract.go"), `package workspaceop

type Service interface{ Do() error }
`))
	must(t, writeFile(filepath.Join(opDir, "operator.go"), `package workspaceop

import "example.com/app/internal/config"

type Deps struct {
	WorkspaceConfig *config.WorkspaceConfig // pure-accessor config data — silent
}

type service struct{ deps Deps }

func New(d Deps) Service { return &service{deps: d} }
`))

	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleDepsAreInterfaces}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got := findingsForRule(fs, string(RuleDepsAreInterfaces)); len(got) != 0 {
		t.Fatalf("a config whose every method is a pure accessor over its own fields is DATA "+
			"and must not fire — an interface over StorageSizeForTier asserts nothing. got %d:\n%s",
			len(got), AsResult(fs).FormatText())
	}
}

// TestLintDepsAreInterfaces_CollaboratorsDoNotEscapeAsData is the other
// half, and the one that decides whether the exemption is a discriminator
// or a loophole.
//
// Widening "zero methods" to "pure methods" is only safe if a genuine
// collaborator cannot reach the widened shape. Each fixture type below is
// a way one might TRY to, and every one must still fire:
//
//   - Repo — named like config, in a package named config, few methods.
//     Neither the name nor the path is consulted, which is the property
//     the rule's own header insists on: a repository must not escape by
//     being called Settings.
//   - Cache — one method, no context, no error, returns a value. It passes
//     every SIGNATURE test and fails on the body, because it calls through
//     a field. A call is where I/O hides, and no attempt is made to guess
//     whether the field behind it holds data or a database.
//   - Client — has a genuinely pure accessor (BaseURL). Data is a property
//     of the WHOLE type, so one honest getter cannot vouch for the type
//     that also has Do(ctx) (…, error).
//   - Closer — a STUB: `func (c *Closer) Close() {}`, a body that is
//     syntactically pure. Purity of body alone would wave it through; it
//     fires on the signature, because a method that returns nothing did
//     not compute anything.
//   - Registry — every method is pure, but it reaches a package-level VAR.
//     That is mutable shared state, which is how a method reaches a
//     singleton. (Package-level CONSTs are allowed, and the data fixture
//     above uses one.)
func TestLintDepsAreInterfaces_CollaboratorsDoNotEscapeAsData(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	must(t, writeFile(filepath.Join(tmp, "go.mod"), "module example.com/app\n\ngo 1.24\n"))

	// Deliberately the most config-looking location available: package
	// `config`, under internal/config, with a type named like settings.
	cfgDir := filepath.Join(tmp, "internal", "config")
	must(t, mkdirAll(cfgDir))
	must(t, writeFile(filepath.Join(cfgDir, "config.go"), `package config

import "context"

var sharedPool = map[string]string{}

// Repo is a repository wearing config's clothes: config-shaped name, in
// the config package. It must still fire.
type Repo struct{ dsn string }

func (r *Repo) Save(ctx context.Context, id string) error { return nil }

// Cache passes every signature test — a value in, a value out, no ctx, no
// error — and fails on the body: it calls through one of its own fields.
type Cache struct{ backing *Repo }

func (c *Cache) Lookup(key string) string {
	_ = c.backing.Save(context.Background(), key)
	return key
}

// Client mixes a genuinely pure accessor with a real operation. Data is a
// property of the whole type, so BaseURL cannot vouch for Do.
type Client struct{ base string }

func (c *Client) BaseURL() string                        { return c.base }
func (c *Client) Do(ctx context.Context, p string) error { return nil }

// Closer is a STUB. Its body is syntactically pure; it is plainly a
// collaborator's method, and the signature is what says so.
type Closer struct{ name string }

func (c *Closer) Close() {}

// Registry's methods are pure in body shape but read package-level VAR
// state — how a method reaches a shared singleton.
type Registry struct{ prefix string }

func (r *Registry) Resolve(k string) string { return sharedPool[r.prefix+k] }
`))

	appDir := filepath.Join(tmp, "internal", "orders")
	must(t, mkdirAll(appDir))
	must(t, writeFile(filepath.Join(appDir, "contract.go"), `package orders

type Service interface{ Do() error }
`))
	must(t, writeFile(filepath.Join(appDir, "service.go"), `package orders

import "example.com/app/internal/config"

type Deps struct {
	Repo     *config.Repo     // config-shaped name and path — still fires
	Cache    *config.Cache    // accessor-shaped signature, calls through a field
	Client   *config.Client   // one pure getter cannot vouch for the type
	Closer   *config.Closer   // pure BODY, but returns nothing
	Registry *config.Registry // reads package-level var state
}

type service struct{ deps Deps }

func New(d Deps) Service { return &service{deps: d} }
`))

	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleDepsAreInterfaces}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleDepsAreInterfaces))
	if len(got) != 5 {
		t.Fatalf("every collaborator shape must still fire — the data exemption is a "+
			"discriminator, not an escape hatch. expected 5, got %d:\n%s",
			len(got), AsResult(fs).FormatText())
	}
	var joined string
	for _, f := range got {
		joined += f.Message + "\n"
	}
	for _, want := range []string{`"Repo"`, `"Cache"`, `"Client"`, `"Closer"`, `"Registry"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected a finding for %s; got:\n%s", want, joined)
		}
	}
}

// TestLintDepsAreInterfaces_CollectionsOfInterfacesAreSeams: a slice or map
// whose elements are interfaces (or funcs) is a table of collaborators, each
// already a seam, and its zero value is inert — a lookup in a nil map
// answers ok=false, a range runs nothing. reliant's trigger handler keys
// `map[string]IntegrationAccess` and `map[string]SenderDirectory` by
// integration name; firing on them made the lane write a one-method lookup
// interface plus an adapter type per map. A table of CONCRETE values still
// fires.
func TestLintDepsAreInterfaces_CollectionsOfInterfacesAreSeams(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	must(t, writeFile(filepath.Join(tmp, "go.mod"), "module example.com/app\n\ngo 1.24\n"))
	for dir, src := range map[string]string{
		"sender": `package sender

import "context"

// Directory looks people up in one integration.
type Directory interface {
	Resolve(ctx context.Context, id string) (string, error)
}
`,
		"store": `package store

import "context"

type Repo struct{ dsn string }

func (r *Repo) Save(ctx context.Context, id string) error { return nil }
`,
		"trigger": `package trigger

import (
	"context"

	"example.com/app/internal/sender"
	"example.com/app/internal/store"
)

// IntegrationAccess gates one integration's events.
type IntegrationAccess interface {
	Refresh(ctx context.Context, owner string) error
}

type local struct{ repo *store.Repo }

type Deps struct {
	Access  map[string]IntegrationAccess   // interfaces by name — silent
	Senders map[string]sender.Directory    // another package's interface — silent
	Nested  map[string][]IntegrationAccess // nested tables — silent
	Hooks   []func(context.Context) error  // funcs — silent
	Any     []any                          // silent
	Repos   map[string]*store.Repo         // concrete pointers — fires
	Locals  []local                        // concrete values — fires
}
`,
	} {
		must(t, mkdirAll(filepath.Join(tmp, "internal", dir)))
		must(t, writeFile(filepath.Join(tmp, "internal", dir, dir+".go"), src))
	}

	fs, err := Inspect(context.Background(), tmp, Options{Rules: []Rule{RuleDepsAreInterfaces}})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleDepsAreInterfaces))
	var fired []string
	for _, f := range got {
		fired = append(fired, f.Message)
	}
	if len(got) != 2 || !strings.Contains(fired[0]+fired[1], `"Repos"`) || !strings.Contains(fired[0]+fired[1], `"Locals"`) {
		t.Fatalf("want exactly Repos and Locals to fire, got %d:\n%s", len(got), AsResult(fs).FormatText())
	}
}

// TestLintDepsAreInterfaces_AdviceNamesTheSeam: when the package never calls
// a method on a concrete dep, "type the field with an interface naming the
// methods you call" has nothing to name. reliant's catalog handler passed
// *tools.ToolsFactory to a registry entry's func field (`def.Factory(f)`),
// and the lane added a ToolFor method to the factory just to have something
// to put in an interface. The finding now names the call that is the seam —
// or the func-typed field that is — with file:line, and a field the package
// calls a method on keeps the plain remedy.
func TestLintDepsAreInterfaces_AdviceNamesTheSeam(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	must(t, writeFile(filepath.Join(tmp, "go.mod"), "module example.com/app\n\ngo 1.24\n"))
	for dir, src := range map[string]string{
		"tools": `package tools

import "os"

type Tool interface{ Name() string }

// Definition is one registry entry; Factory builds its tool.
type Definition struct {
	Name    string
	Factory func(f *Factory) Tool
}

// Factory builds tools against the filesystem.
type Factory struct{ root string }

func (f *Factory) Open(name string) (*os.File, error) { return os.Open(f.root + name) }
`,
		"hooks": `package hooks

// Set holds the callbacks a save runs.
type Set struct {
	OnSave func(id string) error
	OnDrop func(id string) error
}
`,
		"store": `package store

type Repo struct{ dsn string }

func (r *Repo) Save(id string) error { return nil }
`,
		"catalog": `package catalog

import (
	"example.com/app/internal/hooks"
	"example.com/app/internal/store"
	"example.com/app/internal/tools"
)

type Deps struct {
	Tools *tools.Factory
	Hooks *hooks.Set
	Repo  *store.Repo
}

type service struct{ deps Deps }

func New(deps Deps) *service {
	if deps.Tools == nil {
		panic("catalog: Tools is required")
	}
	return &service{deps: deps}
}

func (s *service) Describe(d tools.Definition) string {
	return d.Factory(s.deps.Tools).Name()
}

func (s *service) Names(defs []tools.Definition) []string {
	factory := s.deps.Tools
	if factory == nil {
		factory = &tools.Factory{}
	}
	var out []string
	for _, def := range defs {
		out = append(out, def.Factory(factory).Name())
	}
	return out
}

func (s *service) Save(id string) error {
	if err := s.deps.Repo.Save(id); err != nil {
		return err
	}
	return s.deps.Hooks.OnSave(id)
}
`,
	} {
		must(t, mkdirAll(filepath.Join(tmp, "internal", dir)))
		must(t, writeFile(filepath.Join(tmp, "internal", dir, dir+".go"), src))
	}

	fs, err := Inspect(context.Background(), tmp, Options{Rules: []Rule{RuleDepsAreInterfaces}})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	byField := map[string]forgeconv.Finding{}
	for _, f := range findingsForRule(fs, string(RuleDepsAreInterfaces)) {
		for _, name := range []string{"Tools", "Hooks", "Repo"} {
			if strings.Contains(f.Message, `"`+name+`"`) {
				byField[name] = f
			}
		}
	}
	if len(byField) != 3 {
		t.Fatalf("want Tools, Hooks and Repo to fire, got:\n%s", AsResult(fs).FormatText())
	}
	// Both hand-offs, the direct one and the one through a local alias.
	if r := byField["Tools"].Remediation; !strings.Contains(r,
		"only hands it to d.Factory (internal/catalog/catalog.go:25), def.Factory (internal/catalog/catalog.go:35)") {
		t.Errorf("Tools remediation must name each hand-off call with file:line; got: %s", r)
	}
	if r := byField["Hooks"].Remediation; !strings.Contains(r, "func-typed field(s) of Deps.Hooks — OnSave (internal/catalog/catalog.go:44)") {
		t.Errorf("Hooks remediation must name the func field it calls; got: %s", r)
	}
	if r := byField["Repo"].Remediation; strings.Contains(r, "never calls a method") || strings.Contains(r, "func-typed field(s)") {
		t.Errorf("Repo's Save is a method call, so it keeps the plain remedy; got: %s", r)
	}
}
