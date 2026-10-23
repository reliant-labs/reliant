//go:build e2e

// ── Testing tiers (read this before running tests) ─────────────────────────
//
// 1. Inner loop (agents, every edit):   task test:short -- ./internal/<pkg>/...
//    Any test that takes >2s (subprocesses, network, real scaffolds)
//    must be -short-gated or have its slow side-effect bypassed under
//    -short, with the slow path still exercised in full mode.
// 2. Package-targeted (pre-commit):     task test -- ./internal/<pkg>/...
//    Full mode, no tag.
// 3. Full gate (once per round / CI):   task test:e2e
//    (go test -tags e2e -count=1 -timeout 60m -run '^TestE2E' ./internal/cli/)
//    Every test here is named TestE2E* — internal/e2eshard's guard
//    enforces it, and CI's shards select by that prefix
//    (.github/workflows/e2e-suite.yml); TestE2ENightly* run nightly only.
//    The e2e corpus: this file's five real-project fixtures, the
//    scaffold suite (scaffold_*_e2e_test.go), and the registration
//    lifecycle (serve_types_only_e2e_test.go). Each test scaffolds an
//    independent project in its own t.TempDir() and runs real
//    generate/tidy/build/boot, so they are t.Parallel(): wall-clock is
//    roughly the slowest fixture, not the sum. The forge binary is
//    built exactly once per process (sync.Once in scaffold_e2e_test.go).
//    Servers booted by tests must use freePortE2E(t) — never hard-code
//    a port.
//
// See reliant.md "Testing tiers".
// ────────────────────────────────────────────────────────────────────────────

// File: internal/cli/fixture_corpus_e2e_test.go
//
// Real-project fixture corpus: scaffold → generate → build → boot, with
// project shapes complex enough to catch the deps-matcher bug class
// upstream (kalshi FORGE_BACKLOG #13: nondeterministic
// DepsAssignabilityMatcher silently emitting nil for cross-package
// interface deps — caught only when a real downstream project was
// regenerated).
//
// Four fixtures:
//
//   - "cp-forge-shaped" (TestE2EFixtureCorpusCPForgeShaped): 3 services
//     (one with a webhook), 2 internal packages where one package's
//     Deps references a repository interface satisfied by a concrete
//     adapter living in ANOTHER package, a `forge:optional-dep` field,
//     and a component-to-component dep — all resolved off the owned
//     internal/app provider func (providers.go) by internal/app/compose.go.
//
//   - "kalshi-shaped" (TestE2EFixtureCorpusKalshiShaped): 3 workers
//     with snake_case multi-word names, one implementing
//     RunContext(ctx), one cron worker, and a worker Deps field typed
//     as a worker-local interface satisfied by a concrete adapter in a
//     third package — the literal kalshi regression shape.
//
//   - "zero-service" (TestE2EFixtureCorpusZeroService): bare `forge project new`
//     with NO --service. Pins the binary-is-not-an-entity contract:
//     zero services scaffolded, generate idempotent at zero,
//     zero-component appkit table compiles and boots, and
//     the documented first step (`forge scaffold service item`) works.
//
//   - "frontend-basepath-shaped" (TestE2EFixtureCorpusFrontendBasePath):
//     1 service + 1 Next.js frontend mounted under base_path /admin.
//     The first fixture that renders a FRONTEND: it pins next.config.ts
//     basePath/assetPrefix emission, the generated Tier-1
//     src/lib/basepath_gen.ts helper, prefix-clean generated TSX, and
//     (npm-gated) a real static-export `next build` with
//     /admin-prefixed assets plus the fail-loud empty-override guard.
//
// The Go-shaped fixtures assert, in order:
//
//  1. THE REFUSAL (half 1 of the cross-package deps guarantee). With a
//     Deps field typed as a consumer-local interface that only a
//     concrete type in ANOTHER package satisfies, and no provider of it in
//     the composition graph, `forge generate` must FAIL, revert every file
//     it wrote that run, and print the no-provider runbook naming the
//     component, the field and the interface — including the explicit
//     "will NOT emit a nil" guarantee. This is what makes the original
//     kalshi FORGE_BACKLOG #13 silent-nil regression STRUCTURALLY
//     impossible rather than merely fixed, so it is the single most
//     valuable thing this corpus pins.
//  2. THE WIRE (half 2). After adding a provider func to providers.go
//     (`func New<X>(<X>Deps) (T, error)`), generate succeeds and the
//     consumer's constructor method in internal/app/compose.go carries the
//     INLINE `Field: g.<X>()` assignment. One provider's concrete value
//     satisfies several different consumer-local interfaces across
//     packages; a component-to-component dep resolves to the other
//     component's accessor the same way, by type.
//  3. `forge generate` is idempotent: a second run produces zero file
//     changes (full tree hash) and zero ownership-machinery warnings.
//  4. The generated project compiles (`go build ./...` with a local
//     forge/pkg replace).
//  5. The built binary boots, /healthz returns 200, and SIGTERM shuts
//     it down cleanly within a bounded wait.
//  6. Disown lifecycle round-trip on internal/app/routes_gen.go
//     (the Tier-1 generated route sets — compose.go and providers.go
//     are user-owned and so are NOT disown targets):
//     hand-edit → generate (drift error with the new option text) →
//     `forge project disown --reason` (one-way transfer) → generate (file left
//     alone, zero warnings) → delete + generate (re-adopted to the
//     pristine render, entry back to Tier-1).
//
// Run with:
//
//	go test -tags e2e -run TestE2EFixtureCorpus -v ./internal/cli/
package cli

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/pkg/pgtest"

	// postgres driver (database/sql "pgx"): the boot tests apply the
	// project's own migrations to a real ephemeral postgres (pkg/pgtest)
	// before boot — the generated server does not auto-migrate.
	_ "github.com/jackc/pgx/v5/stdlib"
)

// ───────────────────────── fixture 1: cp-forge-shaped ─────────────────────────

func TestE2EFixtureCorpusCPForgeShaped(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()
	start := time.Now()

	// ── Scaffold: 3 services ──────────────────────────────────────────
	runCmd(t, dir, forgeBin, "project", "new", "cpforge",
		"--mod", "example.com/cpforge",
		"--service", "api,billing,reporting",
	)
	projectDir := filepath.Join(dir, "cpforge")
	addCorpusForgePkgReplace(t, projectDir)

	// Webhook on billing (cheap: scaffold-only, registered in forge.yaml).
	runCmd(t, projectDir, forgeBin, "scaffold", "webhook", "stripe", "--service", "billing")
	assertPathExistsE2E(t, filepath.Join(projectDir, "internal", "handlers", "billing", "webhook_stripe.go"))

	// ── Internal packages ─────────────────────────────────────────────
	// ledger: classic Service/Deps/New package whose Deps references a
	// repository interface satisfied by a concrete adapter in ANOTHER
	// package (internal/pgstore) — the deps-matcher killer.
	runCmd(t, projectDir, forgeBin, "scaffold", "package", "ledger")
	// pgstore: adapter package hosting the concrete *Store.
	runCmd(t, projectDir, forgeBin, "scaffold", "package", "pgstore", "--type", "adapter")

	// ledger contract: Service + the package-local Repository interface.
	// Tier-2 (one-shot) — user edits are the steady state.
	writeCorpusFile(t, filepath.Join(projectDir, "internal", "ledger", "contract.go"), `// yours: scaffolded once, never touched again — forge will not overwrite this file
package ledger

import "context"

// Service defines the ledger package boundary.
type Service interface {
	// Record persists one ledger entry.
	Record(ctx context.Context, entry string) error
}

// Repository is the ledger's persistence boundary. It is satisfied by
// the CONCRETE *pgstore.Store adapter living in internal/pgstore — the
// cross-package assignability case the composition must either PROVE or
// LOUDLY REFUSE, never silently wire nil (kalshi FORGE_BACKLOG #13 bug
// class). The context.Context in the method signatures is the
// cross-package named type that defeated the old two-universe matcher.
type Repository interface {
	SaveEntry(ctx context.Context, entry string) error
	ListEntries(ctx context.Context) ([]string, error)
}

// Notifier receives ledger events. Deliberately optional — see the
// forge:optional-dep marker on Deps.Notifier in service.go.
type Notifier interface {
	Notify(ctx context.Context, event string) error
}
`)

	// ledger Deps: the cross-package repo interface (REQUIRED — no
	// provider yet, which is what makes generate refuse below) plus an
	// optional-dep field that stays unprovided for the whole fixture.
	mustReplaceInFile(t, filepath.Join(projectDir, "internal", "ledger", "service.go"),
		"\tConfig *config.Config\n",
		`	Config *config.Config
	// LedgerRepo is satisfied by *pgstore.Store (concrete adapter in
	// another package), supplied by a provider func in providers.go.
	LedgerRepo Repository
	// Notifier is an optional collaborator that this fixture NEVER
	// provides — see the optional-dep assertion after the wire.
	// forge:optional-dep
	Notifier Notifier
`)

	// Service-interface implementation for ledger.
	writeCorpusFile(t, filepath.Join(projectDir, "internal", "ledger", "record.go"), `package ledger

import "context"

// Record persists the entry and (optionally) notifies.
func (s *service) Record(ctx context.Context, entry string) error {
	if err := s.deps.LedgerRepo.SaveEntry(ctx, entry); err != nil {
		return err
	}
	if s.deps.Notifier != nil {
		_ = s.deps.Notifier.Notify(ctx, "ledger.recorded")
	}
	return nil
}
`)

	// pgstore: the concrete adapter type. It never names the consumer
	// interfaces — assignability must be PROVEN across packages.
	writeCorpusFile(t, filepath.Join(projectDir, "internal", "pgstore", "store.go"), `package pgstore

import (
	"context"
	"sync"
)

// Store is a concrete in-memory adapter. ONE value satisfies two
// different consumer-local interfaces in two different packages
// (ledger.Repository and reporting.ReportSource) without ever importing
// either — the composition must prove each wire separately.
type Store struct {
	mu      sync.Mutex
	entries []string
}

// NewStore builds an empty Store.
func NewStore() *Store { return &Store{} }

// SaveEntry appends one entry.
func (s *Store) SaveEntry(_ context.Context, entry string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
	return nil
}

// ListEntries returns a copy of all entries.
func (s *Store) ListEntries(_ context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.entries))
	copy(out, s.entries)
	return out, nil
}
`)

	// reporting handler: a HANDLER-LOCAL interface satisfied by the same
	// cross-package concrete adapter — a second, independent proof that
	// one provider's value resolves several unrelated consumer interfaces.
	writeCorpusFile(t, filepath.Join(projectDir, "internal", "handlers", "reporting", "source.go"), `package reporting

import "context"

// ReportSource is reporting's read path. Handler-local interface,
// satisfied by the concrete *pgstore.Store a provider returns.
type ReportSource interface {
	ListEntries(ctx context.Context) ([]string, error)
}
`)
	mustReplaceInFile(t, filepath.Join(projectDir, "internal", "handlers", "reporting", "service.go"),
		"\tConfig *config.Config\n",
		`	Config *config.Config
	// Store is satisfied by the concrete *pgstore.Store a provider returns —
	// cross-package, handler-local interface.
	Store ReportSource
`)

	// api handler: a COMPONENT-TO-COMPONENT dep. ledger.Service is not a
	// provider at all; it resolves to the ledger component's accessor,
	// which is the other half of the resolution rule.
	mustReplaceInFile(t, filepath.Join(projectDir, "internal", "handlers", "api", "service.go"),
		"\tConfig *config.Config\n",
		`	Config *config.Config
	// Ledger is another COMPONENT, referenced by its Service interface
	// type — resolved to the ledger node, not to a provider.
	Ledger ledger.Service
`)
	mustReplaceInFile(t, filepath.Join(projectDir, "internal", "handlers", "api", "service.go"),
		"\t\"example.com/cpforge/pkg/config\"",
		"\t\"example.com/cpforge/internal/ledger\"\n\t\"example.com/cpforge/pkg/config\"")

	// ── 1. THE REFUSAL ────────────────────────────────────────────────
	// No node satisfies ledger.Repository or reporting.ReportSource yet. forge must REFUSE rather than emit a nil that passes
	// validateDeps and nil-derefs in production. Asserting on the MESSAGE
	// (component, field, interface, and the explicit no-nil guarantee) is
	// the point: a bare non-zero exit would pass on any unrelated failure.
	assertNoProviderRefusal(t, forgeBin, projectDir, []depGap{
		{Component: "Ledger", Field: "LedgerRepo", Iface: "Repository"},
		{Component: "Reporting", Field: "Store", Iface: "ReportSource"},
	})

	// ── 2. THE WIRE ───────────────────────────────────────────────────
	// One provider's CONCRETE value resolves both consumer-local
	// interfaces. Its type only has to be assignable to each dep type.
	mustReplaceInFile(t, filepath.Join(projectDir, "internal", "app", "providers.go"),
		"\t\"example.com/cpforge/pkg/config\"",
		"\t\"example.com/cpforge/internal/pgstore\"\n\t\"example.com/cpforge/pkg/config\"")
	appendCorpusFile(t, filepath.Join(projectDir, "internal", "app", "providers.go"), `
// LedgerRepoDeps is empty: the store needs nothing from the graph.
type LedgerRepoDeps struct{}

// NewLedgerRepo is the CONCRETE adapter. Its value satisfies BOTH
// ledger.Repository and reporting.ReportSource; generate proves each
// assignment independently, by type.
func NewLedgerRepo(LedgerRepoDeps) (*pgstore.Store, error) { return pgstore.NewStore(), nil }
`)

	// ── 3. generate ×2 — succeeds now, and is idempotent ─────────────
	generateTwiceIdempotent(t, forgeBin, projectDir)

	// The inline wire, at the one explicit construction site.
	compose := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "compose.go"))
	assertComposeWired(t, compose, "LedgerRepo", "g.LedgerRepo()")
	assertComposeWired(t, compose, "Store", "g.LedgerRepo()")
	// Component-to-component: resolved to the other component's accessor.
	assertComposeWired(t, compose, "Ledger", "g.Ledger()")

	// Optional-dep semantics under the new refusal rule, asserted from
	// OBSERVED behavior rather than assumed: an unprovided REQUIRED dep
	// is a hard refusal (step 1), but an unprovided field carrying
	// `// forge:optional-dep` is wired to an explicitly commented nil and
	// generate proceeds. That asymmetry IS the contract — optional means
	// "nil is a legitimate value here", and the comment keeps it visible
	// at the construction site instead of silent.
	if !strings.Contains(compose, "Notifier:") {
		t.Errorf("compose.go dropped the optional Notifier key entirely; want an explicit nil assignment:\n%s", compose)
	}
	if !regexp.MustCompile(`Notifier: +nil, // optional — no provider`).MatchString(compose) {
		t.Errorf("compose.go must wire an unprovided optional dep to an explicitly COMMENTED nil "+
			"(`Notifier: nil, // optional — no provider`) — an uncommented nil is indistinguishable "+
			"from the silent-nil bug this corpus exists to keep dead; got:\n%s", compose)
	}

	// ── 4. compiles ───────────────────────────────────────────────────
	runCmd(t, projectDir, "go", "build", "./...")

	// ── 5. boots: /healthz 200, clean SIGTERM shutdown ───────────────
	bootHealthzAndShutdown(t, projectDir)

	// ── 6. disown lifecycle round-trip ────────────────────────────────
	// Retargeted from the retired pkg/app/wire_gen.go and then
	// mounts_services_gen.go. The Tier-1 file in the current model is the
	// generated route sets; compose.go and providers.go are USER-OWNED
	// (compose.go is reconciled, not regenerated) and so carry no
	// forge:hash and no stomp guard — they are not disown targets at all.
	disownRoundTrip(t, forgeBin, projectDir, "internal/app/routes_gen.go")

	t.Logf("cp-forge-shaped fixture total: %s", time.Since(start))
}

// depGap is one expected entry in the no-provider refusal report.
type depGap struct {
	Component string // e.g. "Ledger"
	Field     string // e.g. "LedgerRepo"
	Iface     string // e.g. "Repository"
}

// assertNoProviderRefusal runs `forge generate` expecting it to FAIL with
// the composition's no-provider report, and pins the parts of that message
// a user actually acts on.
//
// This is the strongest guarantee in the corpus and it replaced a weaker
// one. The old fixtures grepped generated wiring for `Field: app.Field`
// and for the absence of `Field: nil` — an after-the-fact check that a
// silent nil had not been emitted. The current model does not emit one to
// catch: a required custom-interface dep with no provider stops the
// generate outright. So the pin moved from "no nil in the output" to
// "there IS no output, and the error says exactly what to wire", which is
// what makes the kalshi FORGE_BACKLOG #13 regression structurally
// impossible rather than merely absent on this run.
//
// Asserting on the message text (not just a non-zero exit) is load-bearing
// twice over: a bare exit check would pass on any unrelated generate
// failure, and the runbook lines ARE the feature — a refusal that does not
// name the field and the fix is just a broken build.
func assertNoProviderRefusal(t *testing.T, forgeBin, projectDir string, gaps []depGap) {
	t.Helper()

	out, err := runCorpusCmd(projectDir, forgeBin, "generate")
	if err == nil {
		t.Fatalf("generate must REFUSE while a required custom-interface dep has no provider — "+
			"emitting a nil there passes validateDeps and nil-derefs at runtime; output:\n%s", out)
	}

	// The guarantee itself, in forge's own words.
	for _, want := range []string{
		"cannot fill every Deps field",
		"will NOT emit a nil for a required dependency",
		"internal/app/providers.go",
		"internal/app/compose.go",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no-provider refusal missing %q; got:\n%s", want, out)
		}
	}

	// Every gap must be named specifically, with its runbook: the provider
	// func to write.
	for _, g := range gaps {
		if !strings.Contains(out, fmt.Sprintf("%s.Deps.%s (%s) has no provider", g.Component, g.Field, g.Iface)) {
			t.Errorf("refusal does not name the %s.Deps.%s (%s) gap; got:\n%s", g.Component, g.Field, g.Iface, out)
		}
		if !strings.Contains(out, fmt.Sprintf("func New%s(d %sDeps) (%s, error)", g.Field, g.Field, g.Iface)) {
			t.Errorf("refusal does not show the provider func for %s; got:\n%s", g.Field, out)
		}
	}

	// A failed generate must leave the tree in its pre-run state — the
	// revert is what makes the refusal safe to hit repeatedly.
	if !strings.Contains(out, "your tree is back to its pre-run state") {
		t.Errorf("a refused generate must report reverting what it wrote this run; got:\n%s", out)
	}
}

// assertComposeWired pins ONE inline Deps assignment at the explicit
// construction site (a constructor method in internal/app/compose.go): the
// field must be filled from the named expression, and must not be a bare
// nil. gofmt column-aligns struct-literal values, so match any run of spaces.
func assertComposeWired(t *testing.T, compose, field, want string) {
	t.Helper()
	wired := regexp.MustCompile(`\b` + regexp.QuoteMeta(field) + `: +` + regexp.QuoteMeta(want) + `(?:[,\s]|$)`)
	if !wired.MatchString(compose) {
		t.Errorf("compose.go: missing inline wire `%s: %s`; got:\n%s", field, want, compose)
	}
	nilWire := regexp.MustCompile(regexp.QuoteMeta(field) + `: +nil\b`)
	if nilWire.MatchString(compose) {
		t.Errorf("compose.go: `%s` wired to a bare nil — the silent-nil downgrade bug class", field)
	}
}

// ───────────────────────── fixture 2: kalshi-shaped ─────────────────────────

func TestE2EFixtureCorpusKalshiShaped(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()
	start := time.Now()

	newOut := runCmdKeep(t, dir, forgeBin, "project", "new", "kalshishape",
		"--mod", "example.com/kalshishape",
		"--service", "engine",
	)
	projectDir := filepath.Join(dir, "kalshishape")
	requireComposedE2E(t, projectDir, newOut)
	addCorpusForgePkgReplace(t, projectDir)

	// ── Workers: snake_case multi-word names, one cron ────────────────
	// --no-generate: scaffold-only; one explicit generate at the end
	// (the parallel-agent staging pattern the flag exists for). Every
	// scaffold below stages this way, `scaffold package` included: since
	// #1099 it ends with a generate run, and here that run would hit the
	// refusal this fixture provokes on purpose (step 1) one step early.
	runCmd(t, projectDir, forgeBin, "scaffold", "worker", "engine_shadow", "--no-generate")
	runCmd(t, projectDir, forgeBin, "scaffold", "worker", "settlement_processor", "--no-generate")
	runCmd(t, projectDir, forgeBin, "scaffold", "worker", "book_snapshotter",
		"--kind", "cron", "--schedule", "0 3 * * *", "--no-generate")
	for _, w := range []string{"engine_shadow", "settlement_processor", "book_snapshotter"} {
		assertPathExistsE2E(t, filepath.Join(projectDir, "internal", "workers", w, "worker.go"))
	}

	// settlement_processor: worker-local interface dep (ctx in the method
	// signature — the cross-package named type that defeated the
	// two-universe matcher), satisfied only by a concrete adapter in a
	// THIRD package. The literal kalshi shape. Left REQUIRED on purpose:
	// that is what makes the refusal below fire.
	mustReplaceInFile(t, filepath.Join(projectDir, "internal", "workers", "settlement_processor", "worker.go"),
		"// Deps contains the dependencies for the settlement_processor worker.\ntype Deps struct {\n\tLogger *slog.Logger\n\tConfig *config.Config\n}",
		`// UnsettledSource feeds the settlement loop. Worker-local
// interface, satisfied by the concrete *marketfeed.Adapter in another
// package, supplied by a provider func in providers.go.
type UnsettledSource interface {
	Pending(ctx context.Context) ([]string, error)
}

// Deps contains the dependencies for the settlement_processor worker.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config
	// Unsettled drives the settlement loop. REQUIRED: with no provider,
	// generate must refuse rather than hand the worker a nil
	// (kalshi FORGE_BACKLOG #13).
	Unsettled UnsettledSource
}`)
	mustReplaceInFile(t, filepath.Join(projectDir, "internal", "workers", "settlement_processor", "worker.go"),
		"\t// TODO: implement your per-cycle work here.",
		`	if w.deps.Unsettled != nil {
		pending, err := w.deps.Unsettled.Pending(ctx)
		if err != nil {
			return err
		}
		w.deps.Logger.InfoContext(ctx, "settlement cycle", "pending", len(pending))
	}`)

	// engine_shadow: ctx-aware run loop. Adding RunContext needs no
	// regenerate — appkit's wrapper detects it at boot.
	appendCorpusFile(t, filepath.Join(projectDir, "internal", "workers", "engine_shadow", "worker.go"), `
// RunContext is the ctx-aware run loop (serverkit.ContextWorker). The
// appkit wrapper prefers it over Start when present.
func (w *Worker) RunContext(ctx context.Context) error {
	return w.Start(ctx)
}
`)

	// marketfeed: the concrete adapter satisfying the worker-local
	// interface, living in a third package.
	runCmd(t, projectDir, forgeBin, "scaffold", "package", "marketfeed", "--type", "adapter", "--no-generate")
	writeCorpusFile(t, filepath.Join(projectDir, "internal", "marketfeed", "feed.go"), `package marketfeed

import "context"

// Adapter satisfies settlement_processor.UnsettledSource without ever
// naming it — assignability must be proven cross-package, in a single
// type universe.
type Adapter struct{}

// NewAdapter builds the adapter.
func NewAdapter() *Adapter { return &Adapter{} }

// Pending returns the open settlement candidates.
func (*Adapter) Pending(_ context.Context) ([]string, error) {
	return []string{"mkt-1"}, nil
}
`)

	// ── 1. THE REFUSAL ────────────────────────────────────────────────
	// The kalshi pin, in its strongest form: a worker-local interface dep
	// with no provider stops the generate and names the wiring, instead
	// of silently handing the worker a nil it will deref on its first
	// cycle.
	assertNoProviderRefusal(t, forgeBin, projectDir, []depGap{
		{Component: "SettlementProcessor", Field: "Unsettled", Iface: "UnsettledSource"},
	})

	// ── 2. THE WIRE ───────────────────────────────────────────────────
	// A provider of the third package's concrete adapter fills the
	// WORKER-LOCAL interface — the cross-package hop proven by type.
	mustReplaceInFile(t, filepath.Join(projectDir, "internal", "app", "providers.go"),
		"\t\"example.com/kalshishape/pkg/config\"",
		"\t\"example.com/kalshishape/internal/marketfeed\"\n\t\"example.com/kalshishape/pkg/config\"")
	appendCorpusFile(t, filepath.Join(projectDir, "internal", "app", "providers.go"), `
// UnsettledDeps is empty: the adapter needs nothing from the graph.
type UnsettledDeps struct{}

// NewUnsettled satisfies the settlement_processor worker's local
// UnsettledSource, via the concrete *marketfeed.Adapter.
func NewUnsettled(UnsettledDeps) (*marketfeed.Adapter, error) { return marketfeed.NewAdapter(), nil }
`)

	// ── 3. generate ×2 — succeeds now, and is idempotent ─────────────
	generateTwiceIdempotent(t, forgeBin, projectDir)

	compose := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "compose.go"))
	assertComposeWired(t, compose, "Unsettled", "g.Unsettled()")
	// All three snake_case workers must be constructed at the one
	// explicit site. The old `wireWorker<Name>Deps` functions are gone
	// with wire_gen.go; the equivalent pin is the inline construction.
	for _, ctor := range []string{
		"engine_shadow.New(engine_shadow.Deps{",
		"settlement_processor.New(settlement_processor.Deps{",
		"book_snapshotter.New(book_snapshotter.Deps{",
	} {
		if !strings.Contains(compose, ctor) {
			t.Errorf("compose.go missing %s — snake_case worker not constructed; got:\n%s", ctor, compose)
		}
	}
	// Cron worker scaffold must carry its schedule.
	cronWorker := readFileE2E(t, filepath.Join(projectDir, "internal", "workers", "book_snapshotter", "worker.go"))
	if !strings.Contains(cronWorker, "0 3 * * *") {
		t.Errorf("book_snapshotter worker.go does not carry the cron schedule")
	}
	// engine_shadow's ctx-aware loop must have survived the round-trip.
	if !strings.Contains(readFileE2E(t, filepath.Join(projectDir, "internal", "workers", "engine_shadow", "worker.go")), "func (w *Worker) RunContext(") {
		t.Errorf("engine_shadow lost its RunContext(ctx) loop across generate")
	}

	// ── 4. compiles ───────────────────────────────────────────────────
	runCmd(t, projectDir, "go", "build", "./...")

	// ── 5. boots with all workers registered; clean shutdown ─────────
	bootHealthzAndShutdown(t, projectDir)

	// ── 6. disown lifecycle round-trip (see the cp-forge fixture for
	// why this retargeted off pkg/app/wire_gen.go) ────────────────────
	disownRoundTrip(t, forgeBin, projectDir, "internal/app/routes_gen.go")

	t.Logf("kalshi-shaped fixture total: %s", time.Since(start))
}

// ─────────────────── fixture 3: frontend-basepath-shaped ───────────────────

// TestE2EFixtureCorpusFrontendBasePath is the first corpus fixture that
// renders a FRONTEND — template features without an end-to-end fixture
// are exactly how frontend regressions ship. It pins the
// frontends[].base_path feature end to end:
//
//	render level (always, no node needed):
//	  - base_path is detected from next.config.ts (no forge.yaml key);
//	  - next.config.ts sources basePath from NEXT_PUBLIC_BASE_PATH with
//	    the "/admin" literal default, emits basePath AND assetPrefix
//	    (same value), and carries the static-branch fail-loud guard;
//	  - src/lib/basepath_gen.ts exists, is Tier-1-certified (verifying
//	    embedded forge:hash marker), exports BASE_PATH defaulting to
//	    "/admin" and an idempotent joinBasePath;
//	  - generated src/ TS/TSX (nav, dashboard, hooks, mocks,
//	    pages) contains NO hand-prefixed "/admin" string literals —
//	    Link/router handle basePath; bare literals double-prefix or go
//	    stale the day the mount point moves.
//
//	build level (gated: the whole test is marked SKIPPED — never passed —
//	when FORGE_E2E_SKIP_NPM is set; a missing npm is a hard failure under
//	CI, where provisioning node is the workflow's job):
//	  - `npm run build` with NEXT_PUBLIC_BASE_PATH explicitly "" must
//	    FAIL with the template's refusal message (a static export would
//	    bake root-mounted URLs that 404 behind the proxy);
//	  - `npm run build` with NEXT_PUBLIC_BASE_PATH=/admin must pass and
//	    the exported HTML must reference /admin/_next assets — the
//	    field-reported hydration bug class (chunk URLs skipping the
//	    prefix → JS 404s → React never hydrates).
//
// The frontend is named "console" while the base path is "/admin" ON
// PURPOSE: no path under frontends/console/ contains the substring
// "/admin", so any "/admin" literal found in generated src/ is
// attributable to base-path machinery, not the directory layout.
func TestE2EFixtureCorpusFrontendBasePath(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()
	start := time.Now()

	runCmd(t, dir, forgeBin, "project", "new", "febp",
		"--mod", "example.com/febp",
		"--service", "api",
	)
	projectDir := filepath.Join(dir, "febp")
	addCorpusForgePkgReplace(t, projectDir)

	// Author convention-named CRUD RPCs over an UNANNOTATED message. That
	// combination is the whole point of this fixture, and it has to be
	// written here: `forge project new` ships an empty service stub (no
	// example RPC surface since the pb-through collapse), and an empty
	// service means zero hook files — which would make the prefix-clean
	// scan below scan nothing at all.
	//
	// RPCs but NO `// forge:entity` marker is exactly the F2 shape:
	//   - hooks/mocks ARE emitted (they follow the RPCs), so there is real
	//     generated frontend output for the base-path scan to judge;
	//   - NO entity pages are emitted (they follow the marker), so the
	//     nav must advertise NO entity routes — the 404-wall pin further
	//     down.
	// Each rpc is marked `// forge:custom`, which is what says so: an
	// unmarked CRUD-named rpc over a wire message with no table is refused
	// by generate (#962, codegen.UnmatchedCRUDRPCs).
	// Historically the RPCs were bare verbs (Create/Get/...) which matched
	// no entity at all and produced a hollow frontend; the convention names
	// here are the regression guard for that.
	apiProto := filepath.Join(projectDir, "proto", "services", "api", "v1", "api.proto")
	proto := readFileE2E(t, apiProto)
	proto = strings.Replace(proto, "  // TODO: Add your RPC methods here.",
		"  // forge:custom\n  rpc CreateItem(CreateItemRequest) returns (CreateItemResponse);\n"+
			"  // forge:custom\n  rpc GetItem(GetItemRequest) returns (GetItemResponse);\n"+
			"  // forge:custom\n  rpc UpdateItem(UpdateItemRequest) returns (UpdateItemResponse);\n"+
			"  // forge:custom\n  rpc DeleteItem(DeleteItemRequest) returns (DeleteItemResponse);\n"+
			"  // forge:custom\n  rpc ListItems(ListItemsRequest) returns (ListItemsResponse);", 1)
	if !strings.Contains(proto, "rpc ListItems(") {
		t.Fatalf("could not author RPCs into the scaffolded api.proto — the stub's TODO anchor moved:\n%s", proto)
	}
	proto += `
message Item {
  string id = 1;
  string name = 2;
}
message CreateItemRequest { Item item = 1; }
message CreateItemResponse { Item item = 1; }
message GetItemRequest { string id = 1; }
message GetItemResponse { Item item = 1; }
message UpdateItemRequest { Item item = 1; }
message UpdateItemResponse { Item item = 1; }
message DeleteItemRequest { string id = 1; }
message DeleteItemResponse {}
message ListItemsRequest { int32 page_size = 1; string page_token = 2; }
message ListItemsResponse { repeated Item items = 1; string next_page_token = 2; }
`
	if err := os.WriteFile(apiProto, []byte(proto), 0o644); err != nil {
		t.Fatalf("author api proto: %v", err)
	}

	// `forge scaffold frontend --base-path` is the user-facing entry point
	// for the feature. When npm is on PATH the add also runs
	// `npm install` in the frontend (forge's own behavior), which lets
	// `forge generate` exercise the local protoc-gen-es TS-stub pass —
	// the same path a real user hits.
	//
	// `--output static` is the scaffold default; it is spelled out because
	// the fixture's build-level pins (fail-loud base-path guard,
	// /admin-prefixed out/ export) are static-branch behavior, and a
	// future default change must not silently move this fixture off them.
	runCmd(t, projectDir, forgeBin, "scaffold", "frontend", "console", "--base-path", "/admin", "--output", "static")
	feDir := filepath.Join(projectDir, "frontends", "console")
	assertPathExistsE2E(t, filepath.Join(feDir, "package.json"))

	// ── 1. generate ×2 — idempotency. Covers basepath_gen.ts, nav/
	// dashboard/hooks/mocks/pages emission and (when node_modules
	// exists) the buf TS-stub pass. ───────────────────────────────────
	generateTwiceIdempotent(t, forgeBin, projectDir)

	// ── 2. render-level pins (no node required) ──────────────────────
	assertNextConfigBasePath(t, feDir, "/admin")
	assertBasePathGenHelper(t, projectDir, "frontends/console", "/admin")
	assertGeneratedSrcPrefixClean(t, feDir, "/admin")

	// Honest-routes pin (review F2): nav derives from the SAME entity
	// set that gates page emission. The Item message authored above carries
	// no (forge.v1.entity) annotation, so NO entity pages are emitted —
	// and the nav must therefore advertise NO entity routes. The old
	// behavior (nav claiming "/items" while no page existed) was the
	// 404-wall bug this pin now keeps dead. The annotated-entity frontend
	// path is exercised end-to-end by TestE2EScaffoldFrontendBuilds (one
	// CRUD entity, standalone default, npm build+test); the
	// static-export/dynamic-route conflict was resolved by making
	// standalone the scaffold default and static an explicit opt-in.
	//
	// The route list used to live in a Tier-1 nav_gen.tsx. It does not any
	// more: nav_gen.tsx.tmpl was deleted and its ALL_ROUTES table folded
	// into the scaffold-until-touched src/components/nav.tsx, which the
	// nav generator seeds from the same buildNavPages() set. The routes
	// moved; the honesty contract did not, so the pin follows the table to
	// its new home rather than being retired with the old filename.
	//
	// Asserting on ENTITY rows specifically is load-bearing. nav.tsx always
	// carries a `path: "/"` Dashboard row — a real page in every scaffold,
	// not an entity projection — so the old blanket `path: "/"` substring
	// check cannot simply be repointed here: it would fire on that
	// legitimate row and the pin would be red for every project forever.
	navPath := filepath.Join(feDir, "src", "components", "nav.tsx")
	assertPathExistsE2E(t, navPath)
	nav := readFileE2E(t, navPath)
	for _, line := range strings.Split(nav, "\n") {
		if !strings.Contains(line, `path: "/`) {
			continue
		}
		// The dashboard row is the one legitimate route in an
		// entity-less scaffold: `path: "/"`, slug "".
		if strings.Contains(line, `path: "/"`) {
			continue
		}
		t.Errorf("nav.tsx advertises an entity route but the unannotated scaffold emits no entity pages — "+
			"the F2 404-wall regression is back; offending row:\n%s\nfull file:\n%s", line, nav)
	}
	// Non-vacuousness for the loop above: a nav.tsx that lost its route
	// table entirely (or moved again) would satisfy "no entity rows"
	// while proving nothing. Pin the table's presence and its one
	// legitimate row, so this fails loudly if the routes relocate a
	// second time instead of silently passing.
	if !strings.Contains(nav, "ALL_ROUTES") {
		t.Errorf("nav.tsx has no ALL_ROUTES table — the F2 honest-routes pin above is scanning for rows that "+
			"can no longer appear there; find where the nav route list moved and repoint it. got:\n%s", nav)
	}
	if !strings.Contains(nav, `path: "/"`) {
		t.Errorf("nav.tsx does not carry the dashboard row (`path: \"/\"`) every scaffold ships — "+
			"the entity-route scan above is not proven to see route rows at all. got:\n%s", nav)
	}
	// Non-vacuousness for the prefix-clean scan above: files that DO
	// exist must be present and app-relative (basepath_gen carries the
	// only legitimate prefix literal; hooks must exist for the service).
	//
	// The hooks file carries a `_gen` suffix — forge owns it and rewrites
	// it every run. The un-suffixed `api-service-hooks.ts` this used to
	// name has not been emitted since the suffix landed, so the assertion
	// followed the file rather than being dropped: without it the
	// prefix-clean walk could scan an empty hooks tree and pass.
	assertPathExistsE2E(t, filepath.Join(feDir, "src", "hooks", "api-service-hooks_gen.ts"))
	assertPathExistsE2E(t, filepath.Join(feDir, "src", "lib", "basepath_gen.ts"))

	// Go-side compile/boot is pinned by fixtures 1–2; this fixture owns
	// the frontend surface, so no `go build` here (runtime budget).
	t.Logf("frontend-basepath fixture render phase: %s", time.Since(start))

	// ── 3. build-level pins (npm-gated) ──────────────────────────────
	requireCorpusNpm(t, start)
	npmStart := time.Now()

	// `forge scaffold frontend` already ran an install; re-running is a fast
	// no-op that also covers the npm-was-missing-at-add-time case. Same
	// flags as the scaffold-frontend e2e (forge's canonical install).
	//
	// It resolves @reliantlabs/forge-web-runtime over a file: link into the
	// shared repo checkout and runs its prepare script there; build it once
	// up front so parallel tests do not race that bootstrap.
	prebuildWebRuntimeE2E(t)
	runCmdTimeout(t, feDir, 5*time.Minute,
		"npm", "install", "--no-audit", "--no-fund", "--prefer-offline")

	// Generate again, AFTER that install. The buf TypeScript pass runs
	// protoc-gen-es out of the frontend's OWN node_modules and, when it is
	// absent, warns and SKIPS rather than failing — and the install that was
	// supposed to provide it, back at scaffold time, is best-effort by
	// design. So on a cold npm cache the earlier generate emitted no
	// src/gen/ at all and the build below died on
	// "Cannot find module '@/gen/services/api/v1/api_pb'", which says
	// nothing about base paths. A no-op once the stubs exist; the assertion
	// makes a future skip fail by name rather than as a wall of TS2307.
	runCmd(t, projectDir, forgeBin, "generate")
	assertPathExistsE2E(t, filepath.Join(feDir, "src", "gen", "services", "api", "v1", "api_pb.ts"))

	// 3a. Fail-loud guard FIRST (near-instant: the throw happens while
	// next.config.ts is evaluated, before any compilation).
	out, err := runCorpusCmdEnv(feDir, 2*time.Minute,
		[]string{"NEXT_PUBLIC_BASE_PATH="}, "npm", "run", "build")
	if err == nil {
		t.Fatalf("npm run build with NEXT_PUBLIC_BASE_PATH=\"\" must FAIL (static-export fail-loud guard); output:\n%s", out)
	}
	if !strings.Contains(out, "refusing to bake a root-mounted static export") {
		t.Errorf("empty-override build failed, but without the template's fail-loud message; output:\n%s", out)
	}

	// 3b. The real build, prefix explicit. The package.json build script
	// sets NODE_ENV=production, so the static branch emits out/.
	out, err = runCorpusCmdEnv(feDir, 8*time.Minute,
		[]string{"NEXT_PUBLIC_BASE_PATH=/admin"}, "npm", "run", "build")
	if err != nil {
		t.Fatalf("npm run build (NEXT_PUBLIC_BASE_PATH=/admin) failed: %v\n%s", err, out)
	}

	// 3c. The export must reference /admin-prefixed assets.
	assertStaticExportPrefixed(t, feDir, "/admin")

	t.Logf("frontend-basepath fixture npm phase: %s", time.Since(npmStart))
	t.Logf("frontend-basepath fixture total: %s", time.Since(start))
}

// assertNextConfigBasePath pins the rendered next.config.ts contract for
// a declared base_path: the single canonical env var with the forge.yaml
// literal as default, basePath AND assetPrefix from the same value, and
// the static-branch fail-loud guard for empty overrides.
func assertNextConfigBasePath(t *testing.T, feDir, basePath string) {
	t.Helper()
	cfg := readFileE2E(t, filepath.Join(feDir, "next.config.ts"))

	wantConst := fmt.Sprintf("const basePath = process.env.NEXT_PUBLIC_BASE_PATH ?? %q;", basePath)
	if !strings.Contains(cfg, wantConst) {
		t.Errorf("next.config.ts must source basePath from NEXT_PUBLIC_BASE_PATH with the %q literal default; want %q in:\n%s", basePath, wantConst, cfg)
	}
	wantSpread := "...(basePath ? { basePath, assetPrefix: basePath } : {}),"
	if !strings.Contains(cfg, wantSpread) {
		t.Errorf("next.config.ts must emit basePath AND assetPrefix (same value) — omitting assetPrefix lets chunk URLs skip the prefix and hydration dies; want %q in:\n%s", wantSpread, cfg)
	}
	// Static output (this fixture's explicit opt-in) must refuse to
	// bake a root-mounted export when the override empties the prefix.
	if !strings.Contains(cfg, `process.env.NODE_ENV === "production" && basePath === ""`) ||
		!strings.Contains(cfg, "throw new Error") {
		t.Errorf("next.config.ts (static output) must carry the fail-loud empty-basePath production guard; got:\n%s", cfg)
	}
}

// assertBasePathGenHelper pins the generated src/lib/basepath_gen.ts:
// exists, re-exports BASE_PATH and joinBasePath bound to the declared
// default, and is Tier-1-certified via its embedded forge:hash marker (so
// hand edits trip the stomp guard and `forge generate` keeps regenerating
// it when base_path changes).
//
// The IMPLEMENTATION of joinBasePath no longer lives in this file. It
// moved into @reliantlabs/forge-web-runtime (createBasePath), where the
// normalisation and idempotency rules are written and unit-tested once
// instead of being re-emitted into every project — see
// web-runtime/src/basepath.test.ts, which owns the behavioral cases this
// helper used to approximate by grepping for a `startsWith` clause.
//
// So what stays pinned HERE is what is genuinely this project's: the
// baked default from forge.yaml, the NEXT_PUBLIC_BASE_PATH env read
// (which cannot move into the library — Next.js inlines NEXT_PUBLIC_* by
// literal substitution into compiled project sources), and the two names
// the app imports. Asserting on the library's internals from here would
// pin someone else's implementation detail.
func assertBasePathGenHelper(t *testing.T, projectDir, feRel, basePath string) {
	t.Helper()
	rel := feRel + "/src/lib/basepath_gen.ts"
	bpPath := filepath.Join(projectDir, filepath.FromSlash(rel))
	assertPathExistsE2E(t, bpPath)
	bp := readFileE2E(t, bpPath)

	if !strings.Contains(bp, fmt.Sprintf("process.env.NEXT_PUBLIC_BASE_PATH ?? %q", basePath)) {
		t.Errorf("basepath_gen.ts must default BASE_PATH to the declared %q with NEXT_PUBLIC_BASE_PATH as the only override; got:\n%s", basePath, bp)
	}
	// The module must still hand the app BOTH names, whatever supplies
	// them: every generated import site and the base-path docs assume
	// `import { BASE_PATH, joinBasePath } from "@/lib/basepath_gen"`.
	for _, sym := range []string{"BASE_PATH", "joinBasePath"} {
		if !strings.Contains(bp, sym) {
			t.Errorf("basepath_gen.ts must export %s — generated code imports it from @/lib/basepath_gen; got:\n%s", sym, bp)
		}
	}
	if !strings.Contains(bp, "export const {") {
		t.Errorf("basepath_gen.ts must re-export the bound pair from the shared runtime; got:\n%s", bp)
	}
	// Pin the delegation itself. Without this the two name checks above
	// would pass on a file that merely MENTIONS the symbols (they appear
	// in the header comment), which is exactly the vacuous-pass shape
	// this fixture exists to prevent.
	if !strings.Contains(bp, `createBasePath`) || !strings.Contains(bp, `@reliantlabs/forge-web-runtime`) {
		t.Errorf("basepath_gen.ts must source BASE_PATH/joinBasePath from createBasePath in "+
			"@reliantlabs/forge-web-runtime — if that moved again, repoint this pin and the "+
			"behavioral tests in web-runtime/src/basepath.test.ts; got:\n%s", bp)
	}

	// Tier-1 certification is embedded in the file itself now (the
	// global manifest is dead): the forge:hash marker must be present
	// and verify against the recomputed body hash.
	if got := checksums.Verify([]byte(bp)); got != checksums.Pristine {
		t.Errorf("%s Verify = %v, want Pristine — without a verifying embedded forge:hash marker, hand edits would never trip the Tier-1 stomp guard", rel, got)
	}
}

// assertGeneratedSrcPrefixClean walks the frontend's src/ tree and fails
// on any TS/TSX string literal starting with the base path. Next.js
// <Link> and the router prepend the configured basePath automatically,
// and hand-built URLs go through joinBasePath — a bare "/admin..."
// literal in generated code is the double-prefix / stale-mount bug
// class. basepath_gen.ts is exempt: it legitimately carries the literal
// as its baked default.
func assertGeneratedSrcPrefixClean(t *testing.T, feDir, basePath string) {
	t.Helper()
	srcDir := filepath.Join(feDir, "src")
	litRe := regexp.MustCompile("[\"'`]" + regexp.QuoteMeta(basePath) + `\b`)
	exempt := filepath.ToSlash(filepath.Join("lib", "basepath_gen.ts"))

	scanned := 0
	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".ts", ".tsx", ".js", ".jsx":
		default:
			return nil
		}
		rel, rerr := filepath.Rel(srcDir, path)
		if rerr != nil {
			return rerr
		}
		if filepath.ToSlash(rel) == exempt {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		scanned++
		if loc := litRe.Find(body); loc != nil {
			t.Errorf("src/%s contains a hand-prefixed %q string literal (%q) — Link/router and joinBasePath own the prefix; bare literals double-prefix or go stale when the mount point moves", filepath.ToSlash(rel), basePath, string(loc))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan %s: %v", srcDir, err)
	}
	if scanned == 0 {
		t.Fatalf("prefix-clean scan saw zero TS/TSX files under %s — scaffold shape drifted", srcDir)
	}
}

// assertStaticExportPrefixed checks the static-export output (out/) for
// /admin-prefixed asset URLs: at least one exported HTML document must
// reference <basePath>/_next. This is the field-reported hydration bug
// class — chunk/asset URLs that skip the prefix 404 behind the proxy
// and React never hydrates.
func assertStaticExportPrefixed(t *testing.T, feDir, basePath string) {
	t.Helper()
	outDir := filepath.Join(feDir, "out")
	assertPathExistsE2E(t, outDir)

	htmlSeen := 0
	prefixed := false
	err := filepath.WalkDir(outDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if filepath.Ext(path) != ".html" {
			return nil
		}
		htmlSeen++
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(body), basePath+"/_next") {
			prefixed = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan static export %s: %v", outDir, err)
	}
	if htmlSeen == 0 {
		t.Fatalf("static export at %s contains no HTML files — `output: export` branch did not run", outDir)
	}
	if !prefixed {
		t.Errorf("no exported HTML references %s/_next — assets are root-mounted and would 404 behind the proxy (hydration bug class)", basePath)
	}
}

// requireCorpusNpm gates the corpus's npm build phase.
//
// It used to be `if reason := corpusNpmSkipReason(); reason != "" { t.Logf;
// return }` — a bare return that dropped the entire `next build` phase and
// left the test reporting PASS. This is the ONE e2e test CI has ever run, so
// that shape meant a broken setup-node step (or an inherited
// FORGE_E2E_SKIP_NPM) turned the corpus into a content-assertions-only run
// with nothing anywhere saying so.
//
// Two changes: the opt-out now marks the test SKIPPED rather than passed, so
// it is visible in every report; and a MISSING npm goes through requireTool,
// which is a hard failure under CI — provisioning node is the workflow's job.
func requireCorpusNpm(t *testing.T, start time.Time) {
	t.Helper()
	if os.Getenv("FORGE_E2E_SKIP_NPM") != "" {
		t.Logf("frontend-basepath fixture render phase total: %s", time.Since(start))
		t.Skip("FORGE_E2E_SKIP_NPM is set — npm build phase not run")
	}
	requireTool(t, "npm")
}

// runCorpusCmdEnv runs a command with extra environment entries and a
// hard timeout, returning combined output and the error (callers assert
// pass/fail themselves — the fail-loud guard EXPECTS a failure).
func runCorpusCmdEnv(dir string, timeout time.Duration, extraEnv []string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("timed out after %s: %w", timeout, ctx.Err())
	}
	return string(out), err
}

// ───────────────────── fixture 4: zero-service scaffold ─────────────────────

// TestE2EFixtureCorpusZeroService pins the bare-scaffold contract: a
// binary is a deployment unit, NOT a domain entity, so `forge project new`
// without --service must invent NO service from the binary name (the
// cp-forge field report: an admin-server binary grew an
// admin_server.v1.AdminServerService with five generic CRUD RPCs, zero
// callers ever, permanent 501s in every audit).
//
// Asserts, in order:
//
//  1. Bare `forge project new zerosvc` scaffolds zero services: no
//     proto/services/zerosvc/, no handlers/zerosvc/, no frontend (hence no
//     nav route anywhere), and no file in the tree mentions a zerosvc.v1
//     proto package. forge.yaml stays project-global, and the shell still
//     derives to the "service" kind off its on-disk sources.
//  2. `forge generate` is clean AND idempotent at zero services.
//  3. The zero-component appkit table compiles (`go build ./...`) and
//     BOOTS: /healthz 200, clean SIGTERM shutdown.
//  4. The documented first step actually works: `forge scaffold service
//     item` on the same project → generate → build.
func TestE2EFixtureCorpusZeroService(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()
	start := time.Now()

	// Bare scaffold: no --service. (--skip-tools: plugin install is
	// covered elsewhere; this fixture stays cheap.)
	runCmd(t, dir, forgeBin, "project", "new", "zerosvc",
		"--mod", "example.com/zerosvc",
		"--skip-tools",
	)
	projectDir := filepath.Join(dir, "zerosvc")
	addCorpusForgePkgReplace(t, projectDir)

	// ── 1. zero-service shape: nothing named after the binary ────────
	assertZeroServiceShape(t, projectDir, "zerosvc")
	// forge.yaml is project-global only: it never lists what the project
	// contains (that is read from the code — codegen.DiscoverProjectComponents)
	// and it never declares the kind. The kind is read off the TREE, so a bare
	// shell with zero services still derives to "service": the scaffold wrote
	// the KCL deploy tree and the pkg/app composition root, which forge emits
	// only for service projects.
	if strings.Contains(readFileE2E(t, filepath.Join(projectDir, "forge.yaml")), "components:") {
		t.Fatalf("forge.yaml must be global-only (no components: block)")
	}
	assertPathExistsE2E(t, filepath.Join(projectDir, "deploy", "kcl"))
	assertPathExistsE2E(t, filepath.Join(projectDir, "pkg", "app"))
	bareCfg, err := generator.ReadProjectConfig(filepath.Join(projectDir, "forge.yaml"))
	if err != nil {
		t.Fatalf("ReadProjectConfig on the bare scaffold: %v", err)
	}
	if bareCfg.EffectiveKind() != config.ProjectKindService {
		t.Fatalf("bare scaffold EffectiveKind() = %q, want %q — the on-disk service sources are what derive it",
			bareCfg.EffectiveKind(), config.ProjectKindService)
	}

	// ── 2. generate ×2 — clean and idempotent at zero services ───────
	generateTwiceIdempotent(t, forgeBin, projectDir)

	// Still nothing invented post-generate (descriptor pass, frontend
	// pass, cleanup pass all ran).
	assertZeroServiceShape(t, projectDir, "zerosvc")

	// ── 3. compiles + boots: /healthz 200, clean SIGTERM ─────────────
	runCmd(t, projectDir, "go", "build", "./...")
	bootHealthzAndShutdown(t, projectDir)

	// ── 4. documented first step: forge scaffold service item ─────────────
	// (`forge scaffold service` runs the generate pipeline itself; the
	// explicit generate after it pins that a follow-up run is clean.)
	runCmd(t, projectDir, forgeBin, "scaffold", "service", "item")
	assertPathExistsE2E(t, filepath.Join(projectDir, "proto", "services", "item", "v1", "item.proto"))
	assertPathExistsE2E(t, filepath.Join(projectDir, "internal", "handlers", "item", "service.go"))
	out := runCorpusCmdOK(t, projectDir, forgeBin, "generate")
	assertNoForkNoise(t, "post-add generate", out)
	runCmd(t, projectDir, "go", "build", "./...")

	t.Logf("zero-service fixture total: %s", time.Since(start))
}

// assertZeroServiceShape fails if any trace of an invented <name>
// service exists in the project: proto dir, handlers dir, frontend nav
// route, or a `services.<name>.v1` proto-package reference in any
// non-vendored file.
func assertZeroServiceShape(t *testing.T, projectDir, name string) {
	t.Helper()
	for _, p := range []string{
		filepath.Join("proto", "services", name),
		filepath.Join("internal", "handlers", name),
		filepath.Join("gen", "services", name),
		"frontends", // bare scaffold has no frontend → no nav route can exist
	} {
		if _, err := os.Stat(filepath.Join(projectDir, p)); !os.IsNotExist(err) {
			t.Errorf("bare scaffold must not create %s (stat err=%v)", p, err)
		}
	}
	// Sweep the tree for the invented proto package. Skips .forge-pkg
	// (vendored library) and git/npm machinery.
	needle := "services." + name + ".v1"
	err := filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if corpusSkipDir(d.Name()) || d.Name() == ".forge-pkg" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || info.Size() > 1<<20 {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(body), needle) {
			rel, _ := filepath.Rel(projectDir, path)
			t.Errorf("%s references %q — a %s service was invented from the binary name", rel, needle, name)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("zero-service tree sweep: %v", err)
	}
}

// ───────────────────────────── corpus helpers ─────────────────────────────

// addCorpusForgePkgReplace wires the unpublished forge module, whose pkg/*
// libraries a scaffolded project imports, to local source in BOTH module
// roots (project + gen/) so `go mod tidy` resolves on either side.
//
// Every scaffold e2e test calls it right after `forge project new`: the forge
// under test must be judged against forge/pkg from the SAME commit. The
// scaffold's own require is the newest release this checkout descends from
// (buildinfo.PublishedFloor), which by construction predates any package
// added since; `go mod tidy` ignores the go.work bridge, so without this it
// resolves the missing package from the retired forge/pkg module at
// @latest (v0.1.15) and fails. These tests used to SKIP instead, until a
// release caught up — which is never, before launch: consumers pin forge by
// commit, so a release tag always lags main.
//
// Under "Path A" proto unification the scaffolded proto/forge/v1/forge.proto
// declares `option go_package = ".../forge/pkg/forgepb;forgepb"`, and
// buf.gen.yaml excludes forge/v1 from output — so the project generates NO
// local gen/forge/v1 copy and every generated *.pb.go (root AND gen module)
// blank-imports forge/pkg/forgepb. forgepb already lives inside forge/pkg,
// which is vendored with the rest of the module to <project>/.forge-pkg, so a
// single forge replace in each module satisfies everything. (The old
// forge/gen shim module is gone: nothing imports
// github.com/reliant-labs/forge/gen anymore.)
func addCorpusForgePkgReplace(t *testing.T, projectDir string) {
	t.Helper()
	repoRoot := findRepoRoot(t)

	// Copy the in-tree forge MODULE into <project>/.forge-pkg as this test's
	// OWN local bridge to unpublished forge (the maintainer dev-loop, done
	// in-test — forge itself no longer vendors; it pins a published version).
	// A relative replace keeps the workspace off any absolute path and keeps
	// generate run 1 vs run 2 byte-identical for the tree-hash idempotency
	// assertion.
	//
	// It is the MODULE, not pkg/. forge/pkg stopped being a module, so the
	// old shape — copy pkg/ and `replace github.com/reliant-labs/forge/pkg`
	// — replaced a module path that no longer exists. The scaffold's real
	// requirement went unsatisfied, so nothing populated its go.sum and every
	// e2e shard died on `missing go.sum entry` for ordinary third-party deps
	// (connect, protobuf, pgx). The replace target must be a directory
	// holding forge's own go.mod.
	vendorCorpusForgeModule(t, repoRoot, projectDir)

	// The scaffold may pin no forge version at all: a forge built from a
	// working tree is not proxy-resolvable, so InstallableVersion() returns ""
	// and the templates omit the require. A directory replace still needs a
	// requirement to attach to, hence the explicit require alongside it.
	//
	// The exclude is not optional. `go mod tidy` will happily satisfy a
	// forge/pkg/* import from the still-published forge/pkg module, and a
	// graph holding both answers every such import with "ambiguous import:
	// found package ... in multiple modules".
	addModuleLines(t, filepath.Join(projectDir, "go.mod"),
		"require github.com/reliant-labs/forge v0.0.0",
		"replace github.com/reliant-labs/forge => ./.forge-pkg",
		"exclude github.com/reliant-labs/forge/pkg v0.1.15",
	)
	// gen module: the generated proto/config Go blank-imports
	// forge/pkg/forgepb, so it needs the same bridge — relative to gen/, the
	// vendored dir is one level up.
	if _, err := os.Stat(filepath.Join(projectDir, "gen", "go.mod")); err == nil {
		addModuleLines(t, filepath.Join(projectDir, "gen", "go.mod"),
			"require github.com/reliant-labs/forge v0.0.0",
			"replace github.com/reliant-labs/forge => ../.forge-pkg",
			"exclude github.com/reliant-labs/forge/pkg v0.1.15",
		)
	}
}

// addModuleLines appends each directive to the go.mod at path unless a
// directive naming the same module and verb is already present.
//
// It replaced an append-only `replace` helper: the single-module bridge needs
// require and exclude as well.
func addModuleLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := string(data)
	for _, line := range lines {
		verb, rest, ok := strings.Cut(line, " ")
		if !ok {
			t.Fatalf("malformed directive %q", line)
		}
		mod, _, _ := strings.Cut(rest, " ")
		// Already declared with this verb? Leave it alone.
		if strings.Contains(out, verb+" "+mod+" ") || strings.Contains(out, verb+" "+mod+"\n") {
			continue
		}
		out += "\n" + line + "\n"
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// vendorCorpusForgeModule assembles <project>/.forge-pkg as a MODULE
// declaring github.com/reliant-labs/forge — forge's own go.mod and go.sum,
// plus a copy of <repo>/pkg (skipping .git/ and testdata/). This is the
// TEST's in-tree bridge to unpublished forge, not a forge feature.
func vendorCorpusForgeModule(t *testing.T, repoRoot, projectDir string) {
	t.Helper()
	dst := filepath.Join(projectDir, ".forge-pkg")

	// forge's own go.mod/go.sum make this directory a MODULE declaring
	// github.com/reliant-labs/forge. Without them a directory replace has
	// nothing to resolve, which is the whole defect this replaced.
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("create %s: %v", dst, err)
	}
	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(repoRoot, name))
		if err != nil {
			t.Fatalf("read forge %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dst, name), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", filepath.Join(dst, name), err)
		}
	}

	// Only pkg/ is copied, not the whole repo: pkg/* is all a scaffold
	// imports, and copying internal/ and cmd/ would multiply every e2e
	// fixture by forge's entire source tree for no added coverage.
	src := filepath.Join(repoRoot, "pkg")
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, "pkg", rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(filepath.Join(dst, "pkg", rel), data, 0o644)
	})
	if err != nil {
		t.Fatalf("vendor the forge module into %s: %v", dst, err)
	}
}

// generateTwiceIdempotent runs `forge generate` twice and asserts the
// second run (a) succeeds, (b) changes ZERO files (full tree hash
// compare), and (c) emits zero ownership-machinery warnings.
// Idempotency is a first-class assertion: the kalshi matcher bug
// manifested as regen output flip-flopping between app.<Field> and nil
// across runs.
func generateTwiceIdempotent(t *testing.T, forgeBin, projectDir string) {
	t.Helper()

	out1 := runCorpusCmdOK(t, projectDir, forgeBin, "generate")
	assertNoForkNoise(t, "generate run 1", out1)

	before := hashProjectTree(t, projectDir)
	beforeContent := snapshotSmallFiles(t, projectDir)
	out2 := runCorpusCmdOK(t, projectDir, forgeBin, "generate")
	assertNoForkNoise(t, "generate run 2", out2)
	after := hashProjectTree(t, projectDir)

	var diffs []string
	for path, h := range after {
		if before[path] != h {
			diffs = append(diffs, path)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			diffs = append(diffs, path+" (deleted)")
		}
	}
	sort.Strings(diffs)
	if len(diffs) > 0 {
		var detail strings.Builder
		for _, p := range diffs {
			old, ok := beforeContent[p]
			if !ok {
				continue
			}
			cur, err := os.ReadFile(filepath.Join(projectDir, p))
			if err != nil {
				continue
			}
			fmt.Fprintf(&detail, "\n--- %s (run1 → run2) ---\n%s", p, corpusLineDiff(old, string(cur), 40))
		}
		t.Fatalf("second `forge generate` is not idempotent — %d file(s) changed:\n  %s\n%s",
			len(diffs), strings.Join(diffs, "\n  "), detail.String())
	}

	// Run 2 changed nothing, so it recorded the tree as a verified fixed
	// point. (A run with a failed step records nothing, but such a run exits
	// non-zero and runCorpusCmdOK has already failed the test.) Run 3 must
	// skip exactly when run 2 recorded (generate_cache.go). A miss after a
	// record means the cache never hits on a real scaffold; a skip without
	// one is a false skip.
	//
	// Run 2 may itself have skipped: when run 1 changed nothing, run 1 is
	// the run that recorded, and run 2 found it. The record is then still
	// valid, and run 3 must skip too.
	out3 := runCorpusCmdOK(t, projectDir, forgeBin, "generate")
	ran2 := !strings.Contains(out2, "nothing to do")
	verified := !ran2 || strings.Contains(out2, "Recorded as a verified fixed point")
	switch skipped := strings.Contains(out3, "nothing to do"); {
	case verified && !skipped:
		t.Fatalf("third `forge generate` over a verified, unchanged tree did not skip:\nrun 2:\n%s\nrun 3:\n%s", out2, out3)
	case !verified && skipped:
		t.Fatalf("third `forge generate` skipped although nothing was recorded:\nrun 2:\n%s\nrun 3:\n%s", out2, out3)
	case !verified:
		t.Fatalf("run 2 changed nothing and no step failed, yet it did not record a fixed point:\n%s", out2)
	}
	if after3 := hashProjectTree(t, projectDir); !maps.Equal(after, after3) {
		t.Fatal("a skipped `forge generate` changed the tree")
	}
}

// snapshotSmallFiles captures the content of textual files under 1MB so
// an idempotency failure can print a real diff instead of just paths.
func snapshotSmallFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && corpusSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || info.Size() > 1<<20 || d.Name() == "forge.lock" {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	return out
}

// corpusLineDiff is a crude unified-ish diff: lines present in only one
// of the two versions, capped at maxLines.
func corpusLineDiff(a, b string, maxLines int) string {
	aLines := strings.Split(a, "\n")
	bLines := strings.Split(b, "\n")
	aSet := map[string]int{}
	for _, l := range aLines {
		aSet[l]++
	}
	bSet := map[string]int{}
	for _, l := range bLines {
		bSet[l]++
	}
	var out []string
	for _, l := range aLines {
		if bSet[l] == 0 {
			out = append(out, "- "+l)
		}
	}
	for _, l := range bLines {
		if aSet[l] == 0 {
			out = append(out, "+ "+l)
		}
	}
	if len(out) > maxLines {
		out = append(out[:maxLines], fmt.Sprintf("… (%d more)", len(out)-maxLines))
	}
	if len(out) == 0 {
		return "(line-set identical — ordering or count difference)"
	}
	return strings.Join(out, "\n")
}

// assertNoForkNoise fails when a clean generate run prints ownership-
// machinery output: fork-era warnings (must never appear again) or
// disown/migration lines (must not appear on a clean tree).
func assertNoForkNoise(t *testing.T, label, out string) {
	t.Helper()
	for _, needle := range []string{"forked file(s)", "fork-coherence", "now forks", "disowned", "legacy forked"} {
		if strings.Contains(out, needle) {
			t.Errorf("%s: unexpected ownership-machinery output (%q) in:\n%s", label, needle, out)
		}
	}
}

// assertFieldWired pins the deps-matcher fix at the grep level: the
// name-matched Deps field must wire `Field: app.Field` and must NOT be
// a silent typed-zero (`Field: nil`) or carry an unresolved TODO.
// gofmt column-aligns struct-literal values, so match `Field:` followed
// by any run of spaces.
func assertFieldWired(t *testing.T, file, content, field string) {
	t.Helper()
	wired := regexp.MustCompile(regexp.QuoteMeta(field) + `: +app\.` + regexp.QuoteMeta(field))
	if !wired.MatchString(content) {
		t.Errorf("%s: missing `%s: app.%s` — name-matched dep was not wired", file, field, field)
	}
	nilWire := regexp.MustCompile(regexp.QuoteMeta(field) + `: +nil`)
	if nilWire.MatchString(content) {
		t.Errorf("%s: contains silent `%s: nil` — the deps-matcher downgrade bug class", file, field)
	}
	if strings.Contains(content, "TODO: wire "+field) {
		t.Errorf("%s: contains `TODO: wire %s` — field should have resolved", file, field)
	}
}

// corpusSkipDir reports whether a directory is excluded from the
// idempotency tree hash / content snapshot. `.git` is repo machinery;
// `node_modules` and `.next` are npm/Next-owned trees `forge generate`
// never writes — hashing them (tens of thousands of files once the
// frontend fixture installs dependencies) would dominate runtime and,
// for the content snapshot, memory, without guarding anything.
func corpusSkipDir(name string) bool {
	return name == ".git" || name == "node_modules" || name == ".next"
}

// hashProjectTree walks the project and returns rel-path → sha256 for
// every regular file. Nothing forge-owned is excluded: .forge state files,
// side renders, go.sum — a second generate must leave ALL of it
// untouched. (Only non-forge trees are skipped; see corpusSkipDir.)
//
// The exceptions are the generate lock (generateLockRel) and the generate
// cache's manifest (generateCacheRel). The manifest is machine-local
// bookkeeping, not output: run 2 is the run that verifies the fixed point,
// so it is the run that records it — generateTwiceIdempotent then checks the
// record works with a third run. The lock is not output either: it is the
// OS lock every run takes, and the holder writes its pid
// and start time into it on every acquisition (recordGenerateLockHolder)
// so a waiting run can name it. Two runs therefore always differ there,
// and hashing it would fail every idempotence check on the second run.
func hashProjectTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if corpusSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || d.Name() == "forge.lock" {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if filepath.ToSlash(rel) == generateLockRel || filepath.ToSlash(rel) == generateCacheRel {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		sum := sha256.Sum256(data)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("hash tree %s: %v", root, err)
	}
	return out
}

// corpusServerPkg returns the `go build` package pattern for the project's
// PRIMARY binary — the ONE main package under cmd/ — together with whether
// the project has a binary to boot at all.
//
// `./cmd/...` is NOT that pattern. A service scaffold emits
// cmd/<bin>/main.go PLUS the cmd/<bin>/cmd/ cobra tree and its
// services/workers/operators subpackages, so the wildcard matches five
// packages and `go build -o <file>` refuses them outright:
//
//	go: cannot write multiple packages to non-directory .../corpus-server
//
// The binary name is DERIVED, never spelled out here: the corpus scaffolds
// projects under several names, and the derivation mirrors forge's own
// (generator.resolveBinaryName + codegen.discoverSecondaryBinaries) — the
// cmd/<name>/ leaves carrying a main.go are the candidates, and when a
// project has more than one (`forge scaffold binary`) the primary is the
// one named by forge.yaml.
//
// ok=false ONLY for the library kind, which by construction has no cmd/
// tree; that is the single shape where booting a server is inapplicable. A
// missing main package under any bootable kind is a scaffold REGRESSION and
// fails loudly — silently skipping there would recreate exactly the class of
// dead test this helper exists to kill.
func corpusServerPkg(t *testing.T, projectDir string) (string, bool) {
	t.Helper()

	cfg, err := generator.ReadProjectConfig(filepath.Join(projectDir, "forge.yaml"))
	if err != nil {
		t.Fatalf("read forge.yaml under %s: %v", projectDir, err)
	}
	if cfg.EffectiveKind() == config.ProjectKindLibrary {
		return "", false
	}

	mains, err := filepath.Glob(filepath.Join(projectDir, "cmd", "*", "main.go"))
	if err != nil {
		t.Fatalf("glob cmd/*/main.go under %s: %v", projectDir, err)
	}
	if len(mains) == 0 {
		t.Fatalf("%s project at %s has no cmd/*/main.go — the scaffold must emit a composition root for a bootable kind",
			cfg.EffectiveKind(), projectDir)
	}
	bins := make([]string, 0, len(mains))
	for _, m := range mains {
		bins = append(bins, filepath.Base(filepath.Dir(m)))
	}
	sort.Strings(bins)
	if len(bins) == 1 {
		return "./cmd/" + bins[0], true
	}

	// Multiple main packages: the primary binary is the forge.yaml project
	// name verbatim (hyphens preserved), the same rule the scaffold and the
	// regenerate lane address cmd/<bin>/ by.
	primary := cfg.Name
	if primary == "" {
		primary = filepath.Base(projectDir)
	}
	for _, b := range bins {
		if b == primary {
			return "./cmd/" + b, true
		}
	}
	t.Fatalf("project at %s has main packages %v, none named %q — cannot tell which binary serves",
		projectDir, bins, primary)
	return "", false
}

// buildCorpusServer builds the project's primary binary to serverBin. It is
// the single build entry point every boot helper below goes through, so the
// package pattern is derived in exactly one place. A library-kind project
// has nothing to boot, so the fixture skips (see corpusServerPkg — every
// other shape is a hard failure, not a skip).
func buildCorpusServer(t *testing.T, projectDir, serverBin string) {
	t.Helper()
	pkg, ok := corpusServerPkg(t, projectDir)
	if !ok {
		t.Skipf("library-kind project at %s ships no binary — booting a server is inapplicable", projectDir)
	}
	runCmd(t, projectDir, "go", "build", "-o", serverBin, pkg)
}

// bootHealthzAndShutdown builds the server binary, boots it, verifies
// /healthz returns 200, then SIGTERMs it and requires a clean exit
// within a bounded wait — the appkit-merge boot recipe.
//
// The port is allocated via freePortE2E (scaffold_e2e_test.go): the
// corpus runs t.Parallel(), so a hard-coded per-fixture port would be a
// collision against any other test (or stray process) on the machine.
//
// DATABASE_URL: unless the caller names one, the boot gets its own
// ephemeral postgres. The scaffolded config declares database_url REQUIRED
// (config.checkRequired fires before bind) and the DB provider PINGS the database
// while wiring infra, so "no database" is not a bootable configuration for
// any service project — not even a zero-entity one. The database is left
// empty: a project with no entities has no migrations to apply, and the
// point of this helper is the boot, not the schema.
func bootHealthzAndShutdown(t *testing.T, projectDir string, extraEnv ...string) {
	t.Helper()
	port := freePortE2E(t)

	serverBin := filepath.Join(projectDir, "corpus-server")
	buildCorpusServer(t, projectDir, serverBin)

	dsn := ""
	for _, kv := range extraEnv {
		if k, v, ok := strings.Cut(kv, "="); ok && k == "DATABASE_URL" {
			dsn = v
		}
	}
	if dsn == "" {
		provisioned, cleanup, err := pgtest.NewURL()
		if err != nil {
			t.Fatalf("provision boot postgres: %v", err)
		}
		defer cleanup()
		dsn = provisioned
	}

	cmd := exec.Command(serverBin, "server")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		"DATABASE_URL="+dsn,
		"ENVIRONMENT=development", // local dev defaults
	)
	// Caller overrides REPLACE the defaults (duplicate-env behavior is
	// platform-dependent, so force-rewrite instead of appending) — the
	// CRUD-lifecycle fixture boots WITH a real postgres DATABASE_URL to
	// prove the generated bootstrap constructs the DB/ORM pair from
	// config.
	for _, kv := range extraEnv {
		if k, v, ok := strings.Cut(kv, "="); ok {
			cmd.Env = withForcedEnv(cmd.Env, k, v)
		}
	}
	var serverOut strings.Builder
	cmd.Stdout = &serverOut
	cmd.Stderr = &serverOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	killed := false
	defer func() {
		if !killed {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitForServing(t, base, 15*time.Second) {
		t.Fatalf("server did not become ready\noutput:\n%s", serverOut.String())
	}
	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200\noutput:\n%s", resp.StatusCode, serverOut.String())
	}

	// Clean shutdown: SIGTERM, bounded wait.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		killed = true
		if err != nil {
			t.Fatalf("server did not shut down cleanly on SIGTERM: %v\noutput:\n%s", err, serverOut.String())
		}
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		killed = true
		t.Fatalf("server did not exit within 20s of SIGTERM\noutput:\n%s", serverOut.String())
	}
	_ = os.Remove(serverBin)
}

// disownRoundTrip exercises the full ownership lifecycle on ONE
// Tier-1 generated file (rel, e.g. pkg/app/wire_gen.go or
// internal/db/item_orm.go):
//
//	hand-edit → generate (drift error, new option text) →
//	`forge project disown --reason` (one-way transfer) → generate leaves the
//	file alone with ZERO warnings → delete + generate re-adopts to the
//	pristine render (entry back to Tier-1).
func disownRoundTrip(t *testing.T, forgeBin, projectDir, rel string) {
	t.Helper()
	wireGenPath := filepath.Join(projectDir, rel)
	pristine := readFileE2E(t, wireGenPath)

	// Hand-edit: append a user marker.
	const marker = "func userDisownMarker() {}"
	appendCorpusFile(t, wireGenPath, "\n// user edit — must survive every generate after disown\n"+marker+"\n")

	// 1. Plain generate must trip the Tier-1 stomp guard, and the error
	// must teach the new option set: extension point first, then
	// --explain-drift / --force, with `forge project disown --reason` last —
	// described as a RECORDED GAP that freezes the file forever, never as a
	// sanctioned end state. No fork-era guidance.
	//
	// The disown needles mirror generate_drift_hints_test.go, which pins the
	// same report at unit level; keep the two lists in step so the wording
	// can only move in one place.
	out, err := runCorpusCmd(projectDir, forgeBin, "generate")
	if err == nil {
		t.Fatalf("generate over a hand-edited Tier-1 file must fail (drift guard); output:\n%s", out)
	}
	if !strings.Contains(out, "Tier-1") || !strings.Contains(out, rel) {
		t.Fatalf("drift error should name the Tier-1 guard and %s; got:\n%s", rel, out)
	}
	for _, want := range []string{
		"extension point",
		"--explain-drift",
		"--force",
		"forge project disown <path> --reason",
		"not a solution",
		"not an end state",
		"stops updating the file forever",
		"Expect to delete the entry",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("drift error missing option text %q; got:\n%s", want, out)
		}
	}
	// The old report taught the fork escape hatch; that guidance must be
	// gone. Needles are the old option-text phrases, not bare flag names.
	for _, stale := range []string{"Re-run with `--accept`", "unfork --merge", "fork the file"} {
		if strings.Contains(out, stale) {
			t.Errorf("drift error still teaches fork-era escape hatch %q; got:\n%s", stale, out)
		}
	}

	// 2. Disowning requires a reason — refuse without one.
	out, err = runCorpusCmd(projectDir, forgeBin, "project", "disown", rel)
	if err == nil {
		t.Fatalf("disown without --reason must refuse; output:\n%s", out)
	}
	if !strings.Contains(out, "--reason is required") {
		t.Errorf("reason-less disown should explain the requirement; got:\n%s", out)
	}

	// 3. `forge project disown --reason`: the one-way transfer. Confirms the
	// path and documents the delete + generate re-adoption flow.
	out = runCorpusCmdOK(t, projectDir, forgeBin, "project", "disown", rel,
		"--reason", "corpus e2e: custom wiring the generated file can't express")
	if !strings.Contains(out, "disowned "+rel) {
		t.Errorf("disown should confirm the path; got:\n%s", out)
	}
	if !strings.Contains(out, "delete it and run `forge generate`") {
		t.Errorf("disown should document the re-adoption path; got:\n%s", out)
	}

	// 4. Generate now leaves the file alone FOREVER — clean exit, the
	// user content survives, and there is no warning noise of any kind
	// (disowned files are a legitimate end state, not a nag target).
	// No side renders are parked either: there is no reconcile-later
	// limbo.
	for run := 1; run <= 2; run++ {
		out = runCorpusCmdOK(t, projectDir, forgeBin, "generate")
		assertNoForkNoise(t, fmt.Sprintf("post-disown generate run %d", run), out)
		if got := readFileE2E(t, wireGenPath); !strings.Contains(got, marker) {
			t.Fatalf("disowned %s was overwritten by generate run %d — disown not honored", rel, run)
		}
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".forge", "render", rel)); !os.IsNotExist(err) {
		t.Errorf("side render parked for a disowned file — the reconcile-later limbo should be gone (stat err=%v)", err)
	}

	// 5. Re-adoption: delete the file, run generate. The emitter
	// re-emits the pristine render and the entry returns to Tier-1.
	if err := os.Remove(wireGenPath); err != nil {
		t.Fatalf("delete disowned file: %v", err)
	}
	out = runCorpusCmdOK(t, projectDir, forgeBin, "generate")
	assertNoForkNoise(t, "re-adoption generate", out)
	readopted := readFileE2E(t, wireGenPath)
	if readopted != pristine {
		t.Errorf("re-adoption generate did not restore the pristine render of %s", rel)
	}
	// Tier-1 ownership is embedded in the file now: the re-emitted
	// render must carry a verifying forge:hash marker, and the disowned
	// record must be gone from .forge/disowned.json (the file itself is
	// deleted when the last record clears).
	if got := checksums.Verify([]byte(readopted)); got != checksums.Pristine {
		t.Errorf("%s Verify = %v after re-adoption, want Pristine (re-certified Tier-1)", rel, got)
	}
	if raw, err := os.ReadFile(filepath.Join(projectDir, checksums.DisownedFile)); err == nil {
		var disowned struct {
			Files map[string]any `json:"files"`
		}
		if jerr := json.Unmarshal(raw, &disowned); jerr != nil {
			t.Fatalf("parse %s: %v", checksums.DisownedFile, jerr)
		}
		if _, still := disowned.Files[rel]; still {
			t.Errorf("%s still recorded in %s after re-adoption", rel, checksums.DisownedFile)
		}
	}
}

// ─────────────────── fixture 5: executed CRUD lifecycle ───────────────────

// crudLifecycleProbeSrc is the in-project probe test the CRUD-lifecycle
// fixture writes into handlers/item/ and executes with the project's own
// `go test`. It drives the REAL generated stack — generated CRUD wiring →
// pkg/crud lifecycle → internal/db ORM → real postgres — against the
// project's OWN migration SQL (db/migrations/*.up.sql applied verbatim),
// with claims attached the same way the generated middleware would.
//
// Every assertion is a real semantic, not AnyOutcome:
//
//	create×2     → two rows, distinct non-empty IDs, created_at set
//	get          → returns the created row
//	list         → both rows visible
//	update       → actually mutates (re-read observes the change)
//	delete → get → NotFound with a CLEAN client message (no SQL text)
//	list+search  → filter finds the matching row (never silently empty,
//	               never an Internal error from a phantom column)
//	order_by     → declared column accepted; undeclared column rejected
//	               as InvalidArgument (allowlist, not identifier-shape)
//
// This is exactly the bug class the 2026-06 generated-output review
// found shipping: forge validated template RENDERING, never executed
// behavior.
const crudLifecycleProbeSrc = `package item

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/testkit"

	pb "example.com/crudlife/gen/services/item/v1"
	"example.com/crudlife/pkg/middleware"
)

// corpusProjectRoot walks up from the test's working directory (the package
// dir) to the directory holding forge.yaml. Counting "../.." is what rotted
// the first time: this probe lived at internal/<svc>/ when it was written and
// moved to internal/handlers/<svc>/ later, silently pointing the migrations
// read at a directory that does not exist. The root is found, not counted.
func corpusProjectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, serr := os.Stat(filepath.Join(dir, "forge.yaml")); serr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no forge.yaml in any parent of the package dir — cannot locate the project root")
		}
		dir = parent
	}
}

// corpusMigratedDB builds a real-postgres DB and applies the project's
// own migration SQL (db/migrations/*.up.sql, in order) — the schema the
// generated CRUD code is supposed to run against.
func corpusMigratedDB(t *testing.T) orm.Context {
	t.Helper()
	db := testkit.NewPostgresDB(t)
	migDir := filepath.Join(corpusProjectRoot(t), "db", "migrations")
	entries, err := os.ReadDir(migDir)
	if err != nil {
		t.Fatalf("read project migrations dir: %v", err)
	}
	var ups []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			ups = append(ups, e.Name())
		}
	}
	if len(ups) == 0 {
		t.Fatalf("project has no .up.sql migrations in %s", migDir)
	}
	sort.Strings(ups)
	for _, name := range ups {
		sql, rerr := os.ReadFile(filepath.Join(migDir, name))
		if rerr != nil {
			t.Fatalf("read migration %s: %v", name, rerr)
		}
		if _, xerr := db.Exec(context.Background(), string(sql)); xerr != nil {
			t.Fatalf("apply project migration %s: %v\nSQL:\n%s", name, xerr, sql)
		}
	}
	return db
}

func authedCtx() context.Context {
	return middleware.ContextWithClaims(context.Background(),
		&middleware.Claims{UserID: "corpus-user", Email: "corpus@example.com"})
}

// requireCleanClientError asserts an error surface fit for clients:
// the right code and NO leaked SQL/driver internals in the message.
func requireCleanClientError(t *testing.T, label string, err error, want connect.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected %v error, got nil", label, want)
	}
	if got := connect.CodeOf(err); got != want {
		t.Errorf("%s: code = %v, want %v (err: %v)", label, got, want, err)
	}
	msg := err.Error()
	for _, leak := range []string{"sql:", "SQL", "SELECT", "no rows in result set", "no such column"} {
		if strings.Contains(msg, leak) {
			t.Errorf("%s: client-visible error leaks driver/SQL text (%q): %s", label, leak, msg)
		}
	}
}

func TestCorpusCRUDLifecycle(t *testing.T) {
	db := corpusMigratedDB(t)
	svc := NewTestItem(t, WithDB(db))
	ctx := authedCtx()

	// ── create ×2: two rows, distinct non-empty IDs, created_at set ──
	r1, err := svc.CreateItem(ctx, connect.NewRequest(&pb.CreateItemRequest{Name: "first", Description: "alpha"}))
	if err != nil {
		t.Fatalf("create#1: %v", err)
	}
	id1 := r1.Msg.GetItem().GetId()
	if id1 == "" {
		t.Errorf("create#1 returned an EMPTY id — the generated create path never generates one")
	}
	if r1.Msg.GetItem().GetCreatedAt() == nil {
		t.Errorf("create#1: created_at is nil despite timestamps:true on the entity")
	}

	r2, err := svc.CreateItem(ctx, connect.NewRequest(&pb.CreateItemRequest{Name: "second", Description: "beta"}))
	if err != nil {
		t.Fatalf("create#2: %v", err)
	}
	id2 := r2.Msg.GetItem().GetId()
	if id2 == "" {
		t.Errorf("create#2 returned an EMPTY id")
	}
	if id1 == id2 {
		t.Errorf("create#1 and create#2 returned the SAME id (%q) — second create overwrote the first (upsert-as-create data loss)", id1)
	}

	lr, err := svc.ListItems(ctx, connect.NewRequest(&pb.ListItemsRequest{PageSize: 10}))
	if err != nil {
		t.Fatalf("list after two creates: %v", err)
	}
	if got := len(lr.Msg.GetItems()); got != 2 {
		t.Errorf("list after two creates: %d row(s), want 2 — creates are not inserts", got)
	}

	// ── get returns the created row ──────────────────────────────────
	if id1 != "" {
		gr, gerr := svc.GetItem(ctx, connect.NewRequest(&pb.GetItemRequest{Id: id1}))
		if gerr != nil {
			t.Fatalf("get created row: %v", gerr)
		}
		if gr.Msg.GetItem().GetName() != "first" {
			t.Errorf("get: name = %q, want %q", gr.Msg.GetItem().GetName(), "first")
		}

		// ── update actually mutates ──────────────────────────────────
		updated := gr.Msg.GetItem()
		updated.Name = "renamed"
		_, uerr := svc.UpdateItem(ctx, connect.NewRequest(&pb.UpdateItemRequest{Item: updated}))
		if uerr != nil {
			t.Fatalf("update: %v (the scaffold's own Update RPC must be wired, not a custom-read-shape stub)", uerr)
		}
		gr2, gerr2 := svc.GetItem(ctx, connect.NewRequest(&pb.GetItemRequest{Id: id1}))
		if gerr2 != nil {
			t.Fatalf("get after update: %v", gerr2)
		}
		if gr2.Msg.GetItem().GetName() != "renamed" {
			t.Errorf("update did not mutate: name = %q, want %q", gr2.Msg.GetItem().GetName(), "renamed")
		}
		if gr2.Msg.GetItem().GetCreatedAt() == nil {
			t.Errorf("update nulled created_at — timestamps must be managed, not round-tripped through the request")
		}

		// ── delete, then get-missing = clean NotFound ────────────────
		if _, derr := svc.DeleteItem(ctx, connect.NewRequest(&pb.DeleteItemRequest{Id: id1})); derr != nil {
			t.Fatalf("delete: %v", derr)
		}
		_, gerr3 := svc.GetItem(ctx, connect.NewRequest(&pb.GetItemRequest{Id: id1}))
		requireCleanClientError(t, "get-after-delete", gerr3, connect.CodeNotFound)

		// ── soft delete is REAL: the row survives in the database ────
		// deleted_at present means Delete is UPDATE ... SET deleted_at,
		// reads filter it out, and the data is still there. A hard
		// DELETE here means soft_delete was decorative again.
		var survivors int64
		row := db.QueryRow(ctx, "SELECT COUNT(*) FROM items WHERE id = $1 AND deleted_at IS NOT NULL", id1)
		if scanErr := row.Scan(&survivors); scanErr != nil {
			t.Fatalf("count soft-deleted row: %v", scanErr)
		}
		if survivors != 1 {
			t.Errorf("soft_delete:true must keep the row with deleted_at set; rows with deleted_at NOT NULL = %d, want 1 (hard DELETE = decorative soft delete)", survivors)
		}

		lr2, lerr2 := svc.ListItems(ctx, connect.NewRequest(&pb.ListItemsRequest{PageSize: 10}))
		if lerr2 != nil {
			t.Fatalf("list after delete: %v", lerr2)
		}
		if got := len(lr2.Msg.GetItems()); got != 1 {
			t.Errorf("list after delete: %d row(s), want 1", got)
		}
	}

	// ── get a never-existed id = clean NotFound ──────────────────────
	_, merr := svc.GetItem(ctx, connect.NewRequest(&pb.GetItemRequest{Id: "corpus-never-existed"}))
	requireCleanClientError(t, "get-missing", merr, connect.CodeNotFound)

	// ── list with the scaffold's own search filter finds the row ─────
	search := "second"
	sr, serr := svc.ListItems(ctx, connect.NewRequest(&pb.ListItemsRequest{PageSize: 10, Search: &search}))
	if serr != nil {
		t.Fatalf("list with search=%q errored: %v — the generated filter maps to a phantom column instead of the entity's string columns", search, serr)
	}
	if got := len(sr.Msg.GetItems()); got != 1 {
		t.Errorf("list with search=%q: %d row(s), want exactly 1 — search must not silently return nothing", search, got)
	} else if sr.Msg.GetItems()[0].GetName() != "second" {
		t.Errorf("list with search=%q returned the wrong row: %q", search, sr.Msg.GetItems()[0].GetName())
	}

	// ── order_by: declared column accepted, undeclared rejected ──────
	if _, oerr := svc.ListItems(ctx, connect.NewRequest(&pb.ListItemsRequest{PageSize: 10, OrderBy: "name"})); oerr != nil {
		t.Errorf("list order_by=name (a declared column) errored: %v", oerr)
	}
	_, berr := svc.ListItems(ctx, connect.NewRequest(&pb.ListItemsRequest{PageSize: 10, OrderBy: "password_hash"}))
	requireCleanClientError(t, "list-orderby-undeclared", berr, connect.CodeInvalidArgument)
}
`

// TestE2EFixtureCorpusCRUDLifecycle is the executed-lifecycle gate: the
// first corpus fixture that asserts what generated CRUD code DOES, not
// what it renders as.
//
// Shape (the schema-is-truth flow — NO entity protos anywhere):
//
//  1. `forge project new` scaffolds the Item service: CRUD RPCs + wire message,
//     no migration, no entity, no ORM. The schema starts empty.
//  2. `forge scaffold entity item ...` emits the create-table migration —
//     SQL is the schema declaration. The scaffold proto already carries
//     the Item wire contract, so add-entity leaves it alone.
//  3. `forge generate` shadow-applies db/migrations, introspects, and
//     projects entity struct + ORM + CRUD wiring from the REAL columns.
//     Conventions come off the columns: deleted_at ⇒ soft delete,
//     created_at+updated_at ⇒ managed timestamps, text ⇒ searchable.
//  4. A second entity (bookmark, with a repeated []string field) is
//     born through the FULL `forge scaffold entity` surface, including the
//     one-time schema→wire CRUD scaffold into the service proto.
//  5. A HAND-WRITTEN migration ladder evolves bookmarks (add column +
//     data movement splitting a column) and `forge generate` again —
//     the ORM must follow the applied schema with no proto change.
//
// Also pins the projection-vs-implementation split for CRUD:
//
//   - handlers_crud.go — the RPC implementations — is USER-OWNED from
//     line one (scaffold-once, no DO-NOT-EDIT banner) and THIN: it
//     never names an entity field, so schema changes never rot it.
//   - handlers_crud_ops_gen.go — the per-entity wiring (field mapping,
//     filters, proto<->struct conversion, packers) — stays Tier-1.
//   - handlers_crud_gen.go (the old Tier-1 implementation file) is DEAD
//     and must not be emitted.
//   - the scaffold's own proto must never trip the custom-read-shape stub
//     (the F2 root cause: the descriptor collapsed message-typed fields
//     to the literal string "message").
//   - the emitted migration guards the id invariant (CHECK (id <> ”))
//     and stores timestamps as TIMESTAMPTZ, not TEXT.
func TestE2EFixtureCorpusCRUDLifecycle(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()
	start := time.Now()

	runCmd(t, dir, forgeBin, "project", "new", "crudlife",
		"--mod", "example.com/crudlife",
		"--service", "item",
	)
	projectDir := filepath.Join(dir, "crudlife")
	addCorpusForgePkgReplace(t, projectDir)

	// ── 0. the schema starts EMPTY: no boilerplate migration, and a
	// pristine generate emits no entities (honest scaffold). ──────────
	if entries, err := os.ReadDir(filepath.Join(projectDir, "db", "migrations")); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".sql") {
				t.Errorf("pristine scaffold ships migration %s — tables must be born via `forge scaffold entity`", e.Name())
			}
		}
	}

	// Declare the entity IN THE PROTO — the only place an entity is
	// declared. `project new` ships an empty service stub, so the marked
	// message is authored here; bare `forge scaffold` then births it,
	// injecting the CRUD quintet forge itself defines (hand-authoring the
	// request messages would pin a shape forge does not emit).
	lifecycleProtoPath := filepath.Join(projectDir, "proto", "services", "item", "v1", "item.proto")
	lifecycleProto := readFileE2E(t, lifecycleProtoPath)
	lifecycleProto += `
// forge:entity
// forge:soft-delete
message Item {
  string id = 1;
  string name = 2;
  string description = 3;
  bool active = 4;
}
`
	writeFileE2E(t, lifecycleProtoPath, lifecycleProto)
	runCmd(t, projectDir, forgeBin, "scaffold")

	// The boot steps below start the REAL server against a real ephemeral
	// postgres (pkg/pgtest) so the generated bootstrap's DB+ORM
	// construction — and the executed CRUD lifecycle — run on the same
	// engine production does, not just rendered. postgres is the pinned
	// (and derived-default) driver, so no forge.yaml override is needed.

	// ── 1. generate ×2 — idempotent with an entity in play ───────────
	generateTwiceIdempotent(t, forgeBin, projectDir)

	// ── 1b. the custom-query seam (Phase 4 #2) is scaffolded ONCE and
	// survives regeneration untouched. internal/db/item_repo_ext.go is a
	// Tier-2 (scaffold-once, user-owned) sibling of the regenerated
	// item_orm.go — it documents where hand-written / raw-SQL queries go.
	// Prove the affordance exists AND is regen-stable: capture it now,
	// confirm later generates never rewrite it (a second generate already
	// ran inside generateTwiceIdempotent above; step 6/7's generates run
	// after, and the post-edit survival is re-checked there).
	repoExtPath := filepath.Join(projectDir, "internal", "db", "item_repo_ext.go")
	repoExtAfterGen := readFileE2E(t, repoExtPath)
	if !strings.Contains(repoExtAfterGen, "package db") {
		t.Errorf("item_repo_ext.go is not a valid package db file:\n%s", repoExtAfterGen)
	}
	if strings.Contains(repoExtAfterGen, "DO NOT EDIT") {
		t.Errorf("item_repo_ext.go carries a DO-NOT-EDIT banner — the custom-query seam must be user-owned from line one")
	}
	if !strings.Contains(repoExtAfterGen, "db.Bun()") || !strings.Contains(repoExtAfterGen, "itemRepo") {
		t.Errorf("item_repo_ext.go must document the db.Bun() raw-SQL handle and the generated itemRepo; got:\n%s", repoExtAfterGen)
	}
	// Simulate the user taking ownership: append a real hand-written
	// method. A later generate must leave THIS content intact.
	repoExtUserOwned := repoExtAfterGen + `
func itemSeamProbe() string { return "user-owned" }
`
	writeCorpusFile(t, repoExtPath, repoExtUserOwned)

	// features.deploy is shape-derived for service projects: the scaffold
	// ships deploy/kcl/dev/main.k on the KCL-native config path (it
	// imports `config_gen` + the per-env `.config`), so a pristine
	// generate MUST emit those files or the scaffold's own KCL imports
	// are unresolvable and `forge env up` can't compose per-env config (the
	// J1 features.deploy catch-22: gate said deploy=false, schema
	// rejected features.deploy, main.k imported the never-generated
	// file).
	for _, rel := range []string{
		filepath.Join("deploy", "kcl", "config_gen.k"),
		filepath.Join("deploy", "kcl", "dev", "config.k"),
	} {
		if _, err := os.Stat(filepath.Join(projectDir, rel)); err != nil {
			t.Errorf("pristine generate did not emit %s — the scaffold's own deploy/kcl/dev/main.k import is unresolvable (features.deploy catch-22): %v", rel, err)
		}
	}

	handlerDir := filepath.Join(projectDir, "internal", "handlers", "item")

	// ── 2. projection vs implementation split ────────────────────────
	// (Non-fatal: a missing file here must not hide the executed-
	// lifecycle results below — this gate's whole point is maximum
	// behavioral signal per run.)
	// The implementation file: user-owned from line one, thin.
	shimPath := filepath.Join(handlerDir, "handlers_crud.go")
	if _, err := os.Stat(shimPath); os.IsNotExist(err) {
		t.Errorf("handlers_crud.go (the user-owned thin CRUD implementation file) was not scaffolded")
	} else {
		shim := readFileE2E(t, shimPath)
		if strings.Contains(shim, "DO NOT EDIT") {
			t.Errorf("handlers_crud.go carries a DO-NOT-EDIT banner — the CRUD implementation file must be user-owned from line one")
		}
		if !strings.Contains(shim, "crud.Handle") {
			t.Errorf("handlers_crud.go does not delegate to pkg/crud; got:\n%s", shim)
		}
		// THIN IS LOAD-BEARING: the owned file must never name entity
		// fields, or schema changes rot it. "Description" only exists as
		// an Item field name.
		if strings.Contains(shim, "Description") {
			t.Errorf("handlers_crud.go names entity fields — field mapping belongs in the generated ops file, not the owned shim:\n%s", shim)
		}
	}
	// The projection file: Tier-1, regenerated, names the fields.
	if _, err := os.Stat(filepath.Join(handlerDir, "handlers_crud_ops_gen.go")); os.IsNotExist(err) {
		t.Errorf("handlers_crud_ops_gen.go (the Tier-1 CRUD wiring projection) was not generated")
	}
	// The old Tier-1 implementation file is dead.
	if _, err := os.Stat(filepath.Join(handlerDir, "handlers_crud_gen.go")); !os.IsNotExist(err) {
		t.Errorf("handlers_crud_gen.go still emitted — the Tier-1 CRUD implementation file must be dead (stat err=%v)", err)
	}
	// The scaffold's own conventions must never mismatch themselves.
	if entries, err := os.ReadDir(handlerDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			body := readFileE2E(t, filepath.Join(handlerDir, e.Name()))
			// Check both spellings: the current marker and the legacy
			// one (which fresh output must never re-grow either).
			if strings.Contains(body, "forge:custom-read-shape") || strings.Contains(body, "FORGE_CRUD_SHAPE_MISMATCH") {
				t.Errorf("%s contains a custom-read-shape marker — the scaffold's own proto failed the shape matcher (F2)", e.Name())
			}
		}
	}

	// ── 3. migration semantics ───────────────────────────────────────
	// Located by glob, not by a hardcoded "00001_" prefix: scaffolded
	// migrations carry a UTC timestamp version (internal/migrationver), so
	// the number is different on every run and no literal can name it.
	migration := readBornMigrationE2E(t, projectDir, "items")
	if !strings.Contains(migration, "CHECK (id <> '')") {
		t.Errorf("migration lacks CHECK (id <> '') on the string PK — empty-id rows were the silent-upsert data-loss vector; got:\n%s", migration)
	}
	if !strings.Contains(migration, "created_at TIMESTAMPTZ") {
		t.Errorf("migration stores created_at as something other than TIMESTAMPTZ; got:\n%s", migration)
	}

	// The ORM is a projection of the schema, not of any proto: the
	// entity struct lives in internal/db and uses time.Time (the
	// timestamppb impedance is confined to the wire seam).
	ormPath := filepath.Join(projectDir, "internal", "db", "item_orm_gen.go")
	if orm := readFileE2E(t, ormPath); !strings.Contains(orm, "type Item struct") {
		t.Errorf("internal/db/item_orm.go does not declare the Item entity struct")
	} else if strings.Contains(orm, "timestamppb") {
		t.Errorf("entity ORM still references timestamppb — db structs must be schema projections (time.Time)")
	}

	// ── 4. compiles ───────────────────────────────────────────────────
	runCmd(t, projectDir, "go", "build", "./...")

	// ── 5. the executed lifecycle ─────────────────────────────────────
	writeCorpusFile(t, filepath.Join(handlerDir, "crud_lifecycle_corpus_test.go"), crudLifecycleProbeSrc)
	out, err := runCorpusCmd(projectDir, "go", "test", "-count=1", "-run", "TestCorpusCRUDLifecycle", "-v", "./internal/handlers/item/")
	if err != nil {
		t.Errorf("EXECUTED CRUD lifecycle failed:\n%s", out)
	} else {
		t.Logf("executed CRUD lifecycle passed:\n%s", out)
	}

	// ── 6. second entity through the FULL add-entity surface ─────────
	// bookmark exercises: proto injection (CRUD messages + RPCs into the
	// service proto), a repeated []string column (native TEXT[] in the
	// migration, native TEXT[] on postgres), soft delete.
	bookmarkProto := readFileE2E(t, lifecycleProtoPath)
	bookmarkProto += `
// forge:entity
// forge:soft-delete
message Bookmark {
  string id = 1;
  string url = 2;
  string title = 3;
  repeated string tags = 4;
  bool done = 5;
}
`
	writeFileE2E(t, lifecycleProtoPath, bookmarkProto)
	runCmd(t, projectDir, forgeBin, "scaffold")
	itemProto := readFileE2E(t, lifecycleProtoPath)
	if !strings.Contains(itemProto, "rpc CreateBookmark(CreateBookmarkRequest)") {
		t.Fatalf("scaffold entity did not scaffold the Bookmark CRUD RPCs into the service proto")
	}
	if strings.Contains(itemProto, "forge.v1.entity") {
		t.Errorf("scaffold entity emitted a forge.v1.entity annotation — entity protos are dead, SQL is the schema")
	}
	bookmarkMig := readBornMigrationE2E(t, projectDir, "bookmarks")
	if !strings.Contains(bookmarkMig, "tags TEXT[] NOT NULL DEFAULT '{}'") {
		t.Errorf("bookmark migration lacks the native array column; got:\n%s", bookmarkMig)
	}
	runCmd(t, projectDir, forgeBin, "generate")

	// The Phase-4 #2 seam must survive regeneration UNTOUCHED: a second
	// entity + another full `forge generate` ran above, and the
	// user-owned item_repo_ext.go (with the hand-added itemSeamProbe) must
	// be byte-identical to what we wrote. Scaffold-once means scaffold
	// once — forge never overwrites it. A new entity also gets its OWN
	// seam.
	if got := readFileE2E(t, repoExtPath); got != repoExtUserOwned {
		t.Errorf("item_repo_ext.go was rewritten by a later generate — the scaffold-once seam must survive untouched.\nwant:\n%s\n---got:\n%s", repoExtUserOwned, got)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "internal", "db", "bookmark_repo_ext.go")); err != nil {
		t.Errorf("the second entity (bookmark) did not get its own custom-query seam: %v", err)
	}

	runCmd(t, projectDir, "go", "build", "./...")

	writeCorpusFile(t, filepath.Join(handlerDir, "bookmark_lifecycle_corpus_test.go"), bookmarkLifecycleProbeSrc)
	out, err = runCorpusCmd(projectDir, "go", "test", "-count=1", "-run", "TestCorpusBookmarkLifecycle", "-v", "./internal/handlers/item/")
	if err != nil {
		t.Errorf("EXECUTED bookmark (repeated-field + soft-delete) lifecycle failed:\n%s", out)
	} else {
		t.Logf("executed bookmark lifecycle passed:\n%s", out)
	}

	// ── 7. the schema EVOLVES by hand and the ORM follows ────────────
	// Add a column + move data (split url into a derived column) in a
	// hand-written migration — no proto change anywhere — and re-run
	// generate. The projection must pick up the new column end to end:
	// struct field, column allowlist (order_by=domain accepted via the
	// real RPC), scan/insert.
	writeCorpusFile(t, nextMigrationPathE2E(t, projectDir, "bookmark_domain"), `
ALTER TABLE bookmarks ADD COLUMN domain TEXT NOT NULL DEFAULT '';
UPDATE bookmarks SET domain = substr(url, position('//' in url) + 2);
`)
	runCmd(t, projectDir, forgeBin, "generate")
	bookmarkORM := readFileE2E(t, filepath.Join(projectDir, "internal", "db", "bookmark_orm_gen.go"))
	// Collapse whitespace: the entity struct is gofmt-aligned, so the
	// field/type pair is separated by run-of-spaces, not a single space.
	if !strings.Contains(strings.Join(strings.Fields(bookmarkORM), " "), "Domain string") {
		t.Errorf("hand-written migration added `domain` but the regenerated entity struct doesn't carry it — the ORM is not following the applied schema")
	}
	runCmd(t, projectDir, "go", "build", "./...")
	writeCorpusFile(t, filepath.Join(handlerDir, "bookmark_evolve_corpus_test.go"), bookmarkEvolveProbeSrc)
	out, err = runCorpusCmd(projectDir, "go", "test", "-count=1", "-run", "TestCorpusBookmarkEvolved", "-v", "./internal/handlers/item/")
	if err != nil {
		t.Errorf("EXECUTED schema-evolution lifecycle failed:\n%s", out)
	} else {
		t.Logf("executed schema-evolution lifecycle passed:\n%s", out)
	}

	// ── 7b. legacy TEXT timestamps (M3, kalshi fr-3fba9166ba) ────────
	// A pre-forge schema stores created_at/updated_at as TEXT. The old
	// emitter stamped time.Now().UTC()/IsZero() unconditionally, so
	// `forge generate` emitted a trade_orm.go that could NEVER compile
	// (`undefined: time`, `msg.CreatedAt.IsZero undefined (type
	// string)`) — and the only "fix" was hand-patching a do-not-edit
	// file. Pin the whole path: generate exits 0, the projection keeps
	// the columns as strings, the project builds, and the executed ORM
	// stamps real RFC3339Nano text.
	tradeProto := readFileE2E(t, lifecycleProtoPath)
	tradeProto += `
// forge:entity
message Trade {
  string id = 1;
  string ticker = 2;
}
`
	writeFileE2E(t, lifecycleProtoPath, tradeProto)
	runCmd(t, projectDir, forgeBin, "scaffold")
	tradeMigPath := bornMigrationPathE2E(t, projectDir, "trades")
	tradeMig := readFileE2E(t, tradeMigPath)
	tradeMig = strings.ReplaceAll(tradeMig,
		"created_at TIMESTAMPTZ NOT NULL DEFAULT (now())", "created_at TEXT NOT NULL DEFAULT ''")
	tradeMig = strings.ReplaceAll(tradeMig,
		"updated_at TIMESTAMPTZ NOT NULL DEFAULT (now())", "updated_at TEXT NOT NULL DEFAULT ''")
	if !strings.Contains(tradeMig, "created_at TEXT") {
		t.Fatalf("failed to rewrite the trade migration to legacy TEXT timestamps; got:\n%s", tradeMig)
	}
	writeCorpusFile(t, tradeMigPath, tradeMig)
	runCmd(t, projectDir, forgeBin, "generate")
	tradeORM := readFileE2E(t, filepath.Join(projectDir, "internal", "db", "trade_orm_gen.go"))
	// Whitespace-collapsed: the struct field is gofmt-aligned.
	if !strings.Contains(strings.Join(strings.Fields(tradeORM), " "), "CreatedAt string") {
		t.Errorf("TEXT created_at should project as a string struct field; got:\n%s", tradeORM)
	}
	if strings.Contains(tradeORM, "msg.CreatedAt.IsZero()") {
		t.Errorf("trade_orm.go calls IsZero on a string created_at — the fr-3fba9166ba shape is back")
	}
	runCmd(t, projectDir, "go", "build", "./...")
	writeCorpusFile(t, filepath.Join(handlerDir, "trade_text_timestamps_corpus_test.go"), tradeTextTimestampsProbeSrc)
	out, err = runCorpusCmd(projectDir, "go", "test", "-count=1", "-run", "TestCorpusTradeTextTimestamps", "-v", "./internal/handlers/item/")
	if err != nil {
		t.Errorf("EXECUTED legacy-TEXT-timestamps lifecycle failed:\n%s", out)
	} else {
		t.Logf("executed legacy-TEXT-timestamps lifecycle passed:\n%s", out)
	}

	// ── 7c. WIRED custom-read-shape RPC (Phase 4 #1) ─────────────────
	// Flip the scaffold's own ListItems into a custom (non-AIP-158) read:
	// rename its response's repeated field `items` → `results` so the
	// shape matcher routes it to the custom path. Re-scaffold the shim
	// fresh (delete handlers_crud.go so forge re-emits every CRUD method)
	// and ListItems lands as a WIRED starting point: before Phase 4 that
	// was a blank CodeUnimplemented stub; now it builds a best-effort
	// filter, runs a real db.ListItem query, and projects rows via the
	// generated itemToProto. Prove it compiles out of the box (go build),
	// is NOT Unimplemented, and a real HTTP call returns 200 (the query
	// executed against postgres).
	itemProtoPath := filepath.Join(projectDir, "proto", "services", "item", "v1", "item.proto")
	itemProtoSrc := readFileE2E(t, itemProtoPath)
	if !strings.Contains(itemProtoSrc, "repeated Item items = 1;") {
		t.Fatalf("ListItemsResponse does not carry the expected `repeated Item items` field; got:\n%s", itemProtoSrc)
	}
	itemProtoSrc = strings.Replace(itemProtoSrc, "repeated Item items = 1;", "repeated Item results = 1;", 1)
	writeCorpusFile(t, itemProtoPath, itemProtoSrc)
	// Re-scaffold the owned shim from scratch so ListItems re-emits as the
	// wired custom body (the append path never rewrites an existing
	// method; deleting the file is the clean "re-scaffold all" lever).
	if err := os.Remove(filepath.Join(handlerDir, "handlers_crud.go")); err != nil {
		t.Fatalf("remove handlers_crud.go to force re-scaffold: %v", err)
	}
	runCmd(t, projectDir, forgeBin, "generate")

	customShim := readFileE2E(t, filepath.Join(handlerDir, "handlers_crud.go"))
	if !strings.Contains(customShim, "func (s *Service) ListItems(") {
		t.Fatalf("ListItems shim not re-scaffolded; got:\n%s", customShim)
	}
	// Locate the ListItems body and assert it is the WIRED custom shape.
	if idx := strings.Index(customShim, "func (s *Service) ListItems("); idx >= 0 {
		body := customShim[idx:]
		if !strings.Contains(body[:min(len(body), 1200)], "db.ListItem(") {
			t.Errorf("ListItems body must be WIRED (a real db.ListItem query), not an Unimplemented stub; got:\n%s", customShim)
		}
	}
	if !strings.Contains(customShim, "forge:custom-read-shape") {
		t.Errorf("ListItems must carry the custom-read-shape marker after the response rename; got:\n%s", customShim)
	}
	if strings.Contains(customShim, "ListItems is not implemented") {
		t.Errorf("wired ListItems must not be an Unimplemented stub; got:\n%s", customShim)
	}
	if !strings.Contains(customShim, "// TODO: refine this query") {
		t.Errorf("wired custom body must carry the refine TODO; got:\n%s", customShim)
	}
	// Compiles out of the box.
	runCmd(t, projectDir, "go", "build", "./...")
	// And RUNS a real query: a tokenless dev-mode HTTP call returns 200
	// (the wired db.ListItem executed against real postgres without error).
	bootCustomReadRPC(t, projectDir)

	// ── 8. boots WITH a database: the Tier-1 bootstrap constructs the
	// DB + ORM pair from cfg.DatabaseUrl (ensureDatabase). Previously
	// NOTHING constructed app.ORM for a project that grew its first
	// entity post-scaffold (setup.go is scaffold-once and was rendered
	// with HasDatabase=false), so the typed-nil panicked on the first
	// RPC. /healthz 200 + clean SIGTERM against a real postgres DSN pins
	// the constructed path end-to-end.
	bootDSN, bootCleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision boot postgres: %v", err)
	}
	defer bootCleanup()
	bootHealthzAndShutdown(t, projectDir, "DATABASE_URL="+bootDSN)

	// ── 8b. JWT validation is REAL end-to-end: over real HTTP, a garbage
	// bearer token is rejected while a valid RS256 token (validated against
	// JWT_SECRET) authenticates and the row round-trips. Pins the
	// real-validation path the generated CRUD runs behind.
	bootCRUDAuthEnforced(t, projectDir)

	// ── 9. and WITHOUT a database it fails AT BOOT, loudly ───────────
	// The process exits non-zero naming DATABASE_URL before it ever serves
	// a request — never a typed-nil orm.Context panicking mid-RPC.
	bootMustFailWithoutDatabase(t, projectDir)

	// ── 10. disown lifecycle on a generated ORM file (M3, kalshi
	// fr-4dfef712e9, reproduced verbatim): the escape hatch every
	// *_orm.go header advertises ("forge project disown to take ownership")
	// must actually work. Before M3 the ORM emitter bypassed the
	// ownership chokepoint (its outputs carried no forge certification),
	// so disown refused on
	// internal/db/*_orm.go. Full round-trip: hand-edit → drift error →
	// disown → generate leaves it alone → delete + generate re-adopts.
	disownRoundTrip(t, forgeBin, projectDir, "internal/db/item_orm_gen.go")

	t.Logf("crud-lifecycle fixture total: %s", time.Since(start))
}

// bootMustFailWithoutDatabase starts the already-built corpus server
// with an empty DATABASE_URL and asserts the boot FAILS fast with an
// actionable message that NAMES the missing setting. The absence must be
// caught at boot, never as a typed-nil orm.Context panicking on the first
// RPC.
//
// The gate is config.checkRequired: config.proto declares database_url
// `required: true`, so the process refuses before it binds a port or wires
// anything — strictly earlier and louder than the injected `Deps.DB is
// required` check, which this path can no longer reach. The assertion is on
// the SETTING name, which is what an operator can act on and is stable
// across whichever layer refuses first.
func bootMustFailWithoutDatabase(t *testing.T, projectDir string) {
	t.Helper()
	serverBin := filepath.Join(projectDir, "corpus-server")
	buildCorpusServer(t, projectDir, serverBin)

	cmd := exec.Command(serverBin, "server")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", freePortE2E(t)),
		"DATABASE_URL=",
		"ENVIRONMENT=development",
	)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Errorf("server BOOTED with no DATABASE_URL while serving DB-backed CRUD — the absence must be rejected at boot\noutput:\n%s", out.String())
		}
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("server still running 20s after boot with no DATABASE_URL — the absence must fail AT BOOT, not on the first RPC\noutput:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "DATABASE_URL") {
		t.Errorf("boot failure must name the missing setting (DATABASE_URL); output:\n%s", out.String())
	}
	_ = os.Remove(serverBin)
}

// applyProjectMigrationsPostgres applies the project's db/migrations
// *.up.sql files, in order, to the postgres database at dsn — the schema
// the generated server expects but does not create itself.
func applyProjectMigrationsPostgres(t *testing.T, projectDir, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres db: %v", err)
	}
	defer db.Close()

	migDir := filepath.Join(projectDir, "db", "migrations")
	entries, err := os.ReadDir(migDir)
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	var ups []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			ups = append(ups, e.Name())
		}
	}
	if len(ups) == 0 {
		t.Fatalf("no .up.sql migrations in %s", migDir)
	}
	sort.Strings(ups)
	for _, name := range ups {
		sqlSrc, rerr := os.ReadFile(filepath.Join(migDir, name))
		if rerr != nil {
			t.Fatalf("read migration %s: %v", name, rerr)
		}
		if _, xerr := db.Exec(string(sqlSrc)); xerr != nil {
			t.Fatalf("apply migration %s: %v", name, xerr)
		}
	}
}

// mintDevJWT generates a throwaway RSA keypair and returns the PEM-encoded
// public key (for JWT_SECRET) plus a Bearer token string signed with the
// matching private key. RS256 matches the scaffold's default signing method,
// so a server started with JWT_SECRET set to this public key validates the
// token for real — this is the TEST-ONLY way to obtain a genuine principal.
func mintDevJWT(t *testing.T) (pubPEM string, bearer string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	pubPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))

	b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	header := b64([]byte(`{"alg":"RS256","typ":"JWT"}`))
	// Identity claims ONLY. This test-support minter used to add
	// org_id=dev-org-001 and role=admin, which is forge inventing a role
	// vocabulary and a scoping model in the one artifact agents read to
	// learn what a forge token looks like. What a caller MAY do is
	// application policy forge does not generate; an app that needs such a
	// claim puts it in the token its own issuer mints.
	payloadJSON, err := json.Marshal(map[string]any{
		"sub":   "dev-user-001",
		"email": "dev@localhost",
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("marshal JWT payload: %v", err)
	}
	signingInput := header + "." + b64(payloadJSON)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("sign JWT: %v", err)
	}
	return pubPEM, "Bearer " + signingInput + "." + b64(sig)
}

// bootCRUDAuthEnforced boots the corpus server against a real postgres
// database with REAL JWT auth (JWT_SECRET set to a throwaway RS256 public
// key — no dev-auth bypass) and drives CreateItem over real HTTP three
// times:
//   - with NO Authorization header → rejected. CreateItem's proto declares
//     auth_required: true, and the scaffolded serve.go builds
//     `middleware.AuthDeps{AnonymousOK: false}`, so the interceptor turns
//     the caller away before any handler runs.
//   - with a MALFORMED bearer token → rejected. A present credential is
//     always validated for real; a bad one is an error, never a silent
//     downgrade to anonymous.
//   - with a valid RS256 bearer token → the row is created and round-trips.
//
// The token-less leg is the one that could not previously be asserted. The
// interceptor was non-gating: a request with no Authorization header reached
// the handler claim-less, enforcing identity was every handler's job, and
// `auth_required` gated nothing at runtime. One measured app shipped 17 of
// 20 CRUD RPCs open that way, all 20 of them declaring auth_required: true.
// The declaration is now projected into pkg/middleware/procedures_gen.go and
// enforced, so this is a shape forge generates.
func bootCRUDAuthEnforced(t *testing.T, projectDir string) {
	t.Helper()
	port := freePortE2E(t)

	serverBin := filepath.Join(projectDir, "corpus-server")
	buildCorpusServer(t, projectDir, serverBin)

	// The generated server does not auto-migrate; give it a postgres
	// database with the project's own schema applied (same SQL the
	// in-process lifecycle tests run against).
	devDSN, devCleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision dev postgres: %v", err)
	}
	defer devCleanup()
	applyProjectMigrationsPostgres(t, projectDir, devDSN)

	pubPEM, bearer := mintDevJWT(t)

	cmd := exec.Command(serverBin, "server")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		"DATABASE_URL="+devDSN,
		"ENVIRONMENT=development",
		// Real JWT validation: the scaffold's SetupAuth builds a JWT
		// validator (default RS256) that reads JWT_SECRET as the PEM public
		// key. A token is REQUIRED to obtain a principal — there is no bypass.
		"JWT_SECRET="+pubPEM,
	)
	var serverOut strings.Builder
	cmd.Stdout = &serverOut
	cmd.Stderr = &serverOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("start auth-enforced server: %v", err)
	}
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		_ = os.Remove(serverBin)
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitForServing(t, base, 15*time.Second) {
		t.Fatalf("auth-enforced server did not become ready\noutput:\n%s", serverOut.String())
	}

	createBody := `{"name":"auth-proof","description":"auth-gated create"}`
	url := base + "/services.item.v1.ItemService/CreateItem"

	// (1) NO credential → rejected before the handler. CreateItem never
	// declared itself public, so nothing published it.
	anonReq, err := http.NewRequest(http.MethodPost, url, strings.NewReader(createBody))
	if err != nil {
		t.Fatalf("build anonymous CreateItem request: %v", err)
	}
	anonReq.Header.Set("Content-Type", "application/json")
	anon, err := http.DefaultClient.Do(anonReq)
	if err != nil {
		t.Fatalf("POST CreateItem (no credential): %v", err)
	}
	anonBody, _ := io.ReadAll(anon.Body)
	anon.Body.Close()
	if anon.StatusCode == http.StatusOK {
		t.Fatalf("CreateItem with NO Authorization header returned 200 and created a row — the RPC declares "+
			"auth_required: true and nothing enforced it\nresponse: %s\nserver output:\n%s",
			anonBody, serverOut.String())
	}

	// (2) MALFORMED token → rejected. A presented credential is validated
	// for real against JWT_SECRET; a bad one is an error, never a silent
	// downgrade to anonymous. This is the leg that proves the validator is
	// actually wired (AnonymousOK only relaxes the token-LESS case).
	badReq, err := http.NewRequest(http.MethodPost, url, strings.NewReader(createBody))
	if err != nil {
		t.Fatalf("build bad-token CreateItem request: %v", err)
	}
	badReq.Header.Set("Content-Type", "application/json")
	badReq.Header.Set("Authorization", "Bearer not.a.jwt")
	bad, err := http.DefaultClient.Do(badReq)
	if err != nil {
		t.Fatalf("POST CreateItem (malformed token): %v", err)
	}
	badBody, _ := io.ReadAll(bad.Body)
	bad.Body.Close()
	if bad.StatusCode == http.StatusOK {
		t.Fatalf("CreateItem with a MALFORMED bearer token returned 200 — a presented credential must be validated, not ignored\nresponse: %s\nserver output:\n%s",
			badBody, serverOut.String())
	}

	// (3) valid bearer token → 200, row round-trips (real JWT validation).
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(createBody))
	if err != nil {
		t.Fatalf("build authed CreateItem request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST CreateItem (bearer token): %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authed CreateItem = HTTP %d, want 200 — the real JWT validation path is broken\nresponse: %s\nserver output:\n%s",
			resp.StatusCode, body, serverOut.String())
	}
	var created struct {
		Item struct {
			Id   string `json:"id"`
			Name string `json:"name"`
		} `json:"item"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("parse CreateItem response %q: %v", string(body), err)
	}
	if created.Item.Id == "" || created.Item.Name != "auth-proof" {
		t.Errorf("authed CreateItem response missing the created row: %s", body)
	}
}

// bootCustomReadRPC boots the corpus server against real postgres and
// calls the WIRED custom-read-shape RPC (the flipped ListItems) over
// HTTP. A 200 proves the scaffolded custom body runs its real db.ListItem
// query out of the box — not a CodeUnimplemented stub.
func bootCustomReadRPC(t *testing.T, projectDir string) {
	t.Helper()
	port := freePortE2E(t)

	serverBin := filepath.Join(projectDir, "corpus-custom-server")
	buildCorpusServer(t, projectDir, serverBin)

	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision custom-read postgres: %v", err)
	}
	defer cleanup()
	applyProjectMigrationsPostgres(t, projectDir, dsn)

	pubPEM, bearer := mintDevJWT(t)

	cmd := exec.Command(serverBin, "server")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		"DATABASE_URL="+dsn,
		"ENVIRONMENT=development",
		// Real JWT validation (no bypass): the custom read is auth-gated,
		// so the call must carry a valid bearer token to reach the handler.
		"JWT_SECRET="+pubPEM,
	)
	var serverOut strings.Builder
	cmd.Stdout = &serverOut
	cmd.Stderr = &serverOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("start custom-read server: %v", err)
	}
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		_ = os.Remove(serverBin)
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitForServing(t, base, 15*time.Second) {
		t.Fatalf("custom-read server did not become ready\noutput:\n%s", serverOut.String())
	}

	req, err := http.NewRequest(
		http.MethodPost,
		base+"/services.item.v1.ItemService/ListItems",
		strings.NewReader(`{}`),
	)
	if err != nil {
		t.Fatalf("build ListItems request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST ListItems (wired custom read): %v", err)
	}
	defer resp.Body.Close()
	var body strings.Builder
	_, _ = io.Copy(&body, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("wired custom-read ListItems = HTTP %d, want 200 — the scaffolded custom body did not run its real query out of the box\nresponse: %s\nserver output:\n%s",
			resp.StatusCode, body.String(), serverOut.String())
	}
}

// appendToCorpusFile appends content to an existing file — used to add
// explicit override sections (e.g. database.driver) to the minimal
// scaffolded forge.yaml, whose derived-default sections aren't written
// to disk and so can't be string-replaced.
func appendToCorpusFile(t *testing.T, path, content string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(data, []byte(content)...), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// bookmarkLifecycleProbeSrc exercises the second entity born through
// the FULL `forge scaffold entity` surface: repeated-field round trip
// (tags []string — native TEXT[] on postgres) and the soft-delete
// conventions read off the deleted_at
// column (delete is an UPDATE; reads filter; ListAll sees the corpse).
// tradeTextTimestampsProbeSrc executes the generated ORM against the
// legacy-TEXT-timestamps schema (created_at/updated_at TEXT): Create
// must stamp RFC3339Nano strings (caller-provided created_at wins),
// the row must round-trip, and Update must re-stamp updated_at while
// leaving created_at immutable. This is the runtime half of the
// fr-3fba9166ba pin — the compile half is the `go build` above.
const tradeTextTimestampsProbeSrc = `package item

import (
	"testing"
	"time"

	"example.com/crudlife/internal/db"
)

func TestCorpusTradeTextTimestamps(t *testing.T) {
	dbc := corpusMigratedDB(t)
	ctx := authedCtx()

	tr := &db.Trade{Ticker: "KXBTC"}
	if err := db.CreateTrade(ctx, dbc, tr); err != nil {
		t.Fatalf("CreateTrade against the TEXT-timestamp schema: %v", err)
	}
	if tr.ID == "" {
		t.Error("CreateTrade should mint a ULID id")
	}
	if tr.CreatedAt == "" || tr.UpdatedAt == "" {
		t.Fatalf("managed TEXT timestamps not stamped: created_at=%q updated_at=%q", tr.CreatedAt, tr.UpdatedAt)
	}
	if _, err := time.Parse(time.RFC3339Nano, tr.CreatedAt); err != nil {
		t.Errorf("created_at is not RFC3339Nano text: %q (%v)", tr.CreatedAt, err)
	}
	if _, err := time.Parse(time.RFC3339Nano, tr.UpdatedAt); err != nil {
		t.Errorf("updated_at is not RFC3339Nano text: %q (%v)", tr.UpdatedAt, err)
	}

	got, err := db.GetTradeByID(ctx, dbc, tr.ID)
	if err != nil {
		t.Fatalf("GetTradeByID: %v", err)
	}
	if got.Ticker != "KXBTC" || got.CreatedAt != tr.CreatedAt {
		t.Errorf("round-trip mismatch: ticker=%q created_at=%q (want %q / %q)", got.Ticker, got.CreatedAt, "KXBTC", tr.CreatedAt)
	}

	prevUpdated := got.UpdatedAt
	time.Sleep(5 * time.Millisecond) // RFC3339Nano resolution: force a distinct stamp
	got.Ticker = "KXETH"
	if err := db.UpdateTrade(ctx, dbc, got); err != nil {
		t.Fatalf("UpdateTrade: %v", err)
	}
	re, err := db.GetTradeByID(ctx, dbc, tr.ID)
	if err != nil {
		t.Fatalf("re-read after update: %v", err)
	}
	if re.Ticker != "KXETH" {
		t.Errorf("update did not mutate: ticker=%q", re.Ticker)
	}
	if re.UpdatedAt == prevUpdated {
		t.Errorf("updated_at was not re-stamped on update: %q", re.UpdatedAt)
	}
	if re.CreatedAt != tr.CreatedAt {
		t.Errorf("created_at must be immutable across updates: %q -> %q", tr.CreatedAt, re.CreatedAt)
	}
}
`

const bookmarkLifecycleProbeSrc = `package item

import (
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	pb "example.com/crudlife/gen/services/item/v1"
	"example.com/crudlife/internal/db"
)

func TestCorpusBookmarkLifecycle(t *testing.T) {
	dbc := corpusMigratedDB(t)
	svc := NewTestItem(t, WithDB(dbc))
	ctx := authedCtx()

	// ── repeated-field round trip through the real RPC stack ─────────
	// The embedded comma is load-bearing: values must come back EXACTLY,
	// not flattened into a comma-joined string (the old non-compiling-ORM
	// bug's sibling failure mode).
	tags := []string{"go", "sql", "comma,inside"}
	cr, err := svc.CreateBookmark(ctx, connect.NewRequest(&pb.CreateBookmarkRequest{
		Url: "https://example.com/a", Title: "alpha", Tags: tags,
	}))
	if err != nil {
		t.Fatalf("create bookmark: %v", err)
	}
	id := cr.Msg.GetBookmark().GetId()
	if id == "" {
		t.Fatal("create returned empty id")
	}
	if got := cr.Msg.GetBookmark().GetTags(); len(got) != 3 {
		t.Errorf("create response tags = %v, want %v", got, tags)
	}
	gr, err := svc.GetBookmark(ctx, connect.NewRequest(&pb.GetBookmarkRequest{Id: id}))
	if err != nil {
		t.Fatalf("get bookmark: %v", err)
	}
	got := gr.Msg.GetBookmark().GetTags()
	if len(got) != len(tags) {
		t.Fatalf("repeated field did not round-trip: got %v want %v", got, tags)
	}
	for i := range tags {
		if got[i] != tags[i] {
			t.Errorf("tags[%d] = %q, want %q", i, got[i], tags[i])
		}
	}
	if gr.Msg.GetBookmark().GetCreatedAt() == nil {
		t.Error("created_at nil — managed-timestamp convention (created_at+updated_at columns) not honored")
	}

	// ── AIP-134 masked update: ONLY fields named in update_mask are
	// written. The submitted entity deliberately carries clobber values
	// for url and tags; the mask names only title — a masked
	// title-update must leave url/tags intact. This is the silent-data-
	// loss vector the J1 journey hit (updating title NULLED url+tags).
	if _, err := svc.UpdateBookmark(ctx, connect.NewRequest(&pb.UpdateBookmarkRequest{
		Bookmark: &pb.Bookmark{
			Id:    id,
			Title: "masked-title",
			Url:   "https://evil.example/clobber",
			Tags:  []string{"clobbered"},
		},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
	})); err != nil {
		t.Fatalf("masked update (paths=[title]): %v", err)
	}
	grm, err := svc.GetBookmark(ctx, connect.NewRequest(&pb.GetBookmarkRequest{Id: id}))
	if err != nil {
		t.Fatalf("get after masked update: %v", err)
	}
	if got := grm.Msg.GetBookmark().GetTitle(); got != "masked-title" {
		t.Errorf("masked update did not write the masked field: title = %q, want %q", got, "masked-title")
	}
	if got := grm.Msg.GetBookmark().GetUrl(); got != "https://example.com/a" {
		t.Errorf("masked title-update CLOBBERED url: %q, want %q — update_mask is being ignored (whole-row write)", got, "https://example.com/a")
	}
	if got := grm.Msg.GetBookmark().GetTags(); len(got) != 3 {
		t.Errorf("masked title-update CLOBBERED tags: %v, want the original 3 — update_mask is being ignored (whole-row write)", got)
	}

	// A mask naming an unknown/immutable path is INVALID_ARGUMENT, not a
	// silent partial write.
	if _, err := svc.UpdateBookmark(ctx, connect.NewRequest(&pb.UpdateBookmarkRequest{
		Bookmark:   &pb.Bookmark{Id: id, Title: "x"},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"no_such_field"}},
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("masked update with unknown path: err = %v, want InvalidArgument", err)
	}

	// ── unmasked update = documented full-object replace ─────────────
	upd := grm.Msg.GetBookmark()
	upd.Tags = []string{"only-one"}
	if _, err := svc.UpdateBookmark(ctx, connect.NewRequest(&pb.UpdateBookmarkRequest{Bookmark: upd})); err != nil {
		t.Fatalf("update bookmark: %v", err)
	}
	gr2, err := svc.GetBookmark(ctx, connect.NewRequest(&pb.GetBookmarkRequest{Id: id}))
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if g := gr2.Msg.GetBookmark().GetTags(); len(g) != 1 || g[0] != "only-one" {
		t.Errorf("update did not mutate tags: %v", g)
	}

	// ── soft delete: UPDATE-not-DELETE, reads filter, ListAll sees ────
	if _, err := svc.DeleteBookmark(ctx, connect.NewRequest(&pb.DeleteBookmarkRequest{Id: id})); err != nil {
		t.Fatalf("delete bookmark: %v", err)
	}
	if _, err := svc.GetBookmark(ctx, connect.NewRequest(&pb.GetBookmarkRequest{Id: id})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("get after soft delete: err = %v, want NotFound", err)
	}
	lr, err := svc.ListBookmarks(ctx, connect.NewRequest(&pb.ListBookmarksRequest{PageSize: 10}))
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if n := len(lr.Msg.GetBookmarks()); n != 0 {
		t.Errorf("list after soft delete: %d rows, want 0", n)
	}
	// ListAll bypasses ONLY the soft-delete filter: the corpse is
	// visible with deleted_at set — delete was an UPDATE, not a DELETE.
	all, err := db.ListAllBookmark(ctx, dbc)
	if err != nil {
		t.Fatalf("ListAllBookmark: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("ListAll after soft delete: %d rows, want 1 (the soft-deleted row)", len(all))
	}
	if all[0].DeletedAt == nil {
		t.Error("soft-deleted row has nil deleted_at — delete did not stamp the column")
	}
}
`

// bookmarkEvolveProbeSrc runs AFTER the hand-written migration ladder
// (add column + data movement) and `forge generate`: the ORM followed
// the applied schema, so the new `domain` column is a declared column —
// order_by=domain is accepted by the real RPC, and the struct carries
// the field (compile-time proof via direct ORM use).
const bookmarkEvolveProbeSrc = `package item

import (
	"testing"

	"connectrpc.com/connect"

	pb "example.com/crudlife/gen/services/item/v1"
	"example.com/crudlife/internal/db"
)

func TestCorpusBookmarkEvolved(t *testing.T) {
	dbc := corpusMigratedDB(t)
	svc := NewTestItem(t, WithDB(dbc))
	ctx := authedCtx()

	cr, err := svc.CreateBookmark(ctx, connect.NewRequest(&pb.CreateBookmarkRequest{
		Url: "https://example.com/b", Title: "beta",
	}))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The new column is a first-class struct field (schema projection).
	row, err := db.GetBookmarkByID(ctx, dbc, cr.Msg.GetBookmark().GetId())
	if err != nil {
		t.Fatalf("orm get: %v", err)
	}
	if row.Domain != "" {
		t.Logf("domain = %q (DEFAULT '' for new rows is fine)", row.Domain)
	}

	// order_by on the migration-added column must be ACCEPTED — the
	// column allowlist is regenerated from the applied schema.
	if _, err := svc.ListBookmarks(ctx, connect.NewRequest(&pb.ListBookmarksRequest{PageSize: 10, OrderBy: "domain"})); err != nil {
		t.Errorf("order_by=domain (a column added by hand-written migration) rejected: %v — the ORM did not follow the applied schema", err)
	}
	// ...and a phantom column must still be REJECTED.
	if _, err := svc.ListBookmarks(ctx, connect.NewRequest(&pb.ListBookmarksRequest{PageSize: 10, OrderBy: "phantom"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("order_by=phantom: err = %v, want InvalidArgument", err)
	}
}
`

// ── small file/exec utilities (corpus-local; no t.Fatal-free variants
// exist in the sibling e2e files) ──────────────────────────────────────

func writeCorpusFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func appendCorpusFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s for append: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
}

// mustReplaceInFile replaces old with new in path, failing if old is
// absent — a scaffold-shape drift signal, not a soft skip.
func mustReplaceInFile(t *testing.T, path, old, new string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	content := string(data)
	if !strings.Contains(content, old) {
		t.Fatalf("scaffold drift: %s does not contain anchor:\n%s", path, old)
	}
	content = strings.Replace(content, old, new, 1)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// mustReplaceFirstInFile is mustReplaceInFile for anchors that appear
// multiple times — only the FIRST occurrence is replaced.
func mustReplaceFirstInFile(t *testing.T, path, old, new string) {
	mustReplaceInFile(t, path, old, new)
}

// runCorpusCmd runs a command and returns its combined output and error
// (callers assert on failure modes themselves).
func runCorpusCmd(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runCorpusCmdOK runs a command and fails the test on error, returning
// the combined output for content assertions.
func runCorpusCmdOK(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	out, err := runCorpusCmd(dir, name, args...)
	if err != nil {
		t.Fatalf("command %q in %s failed: %v\n%s", append([]string{name}, args...), dir, err, out)
	}
	return out
}
