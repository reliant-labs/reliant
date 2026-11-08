package codegen

import (
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/naming"
)

// writeComponentDeps writes a minimal component package (contract.go with a
// Deps struct) under the given role root so ParseServiceDeps + disk
// resolution see it. body is the Deps struct field block.
func writeComponentDeps(t *testing.T, projectDir, roleRoot, leaf, pkg, depsBody string) {
	t.Helper()
	dir := filepath.Join(projectDir, filepath.FromSlash(roleRoot), leaf)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := "package " + pkg + "\n\ntype Service interface{ Do() }\n\ntype Deps struct {\n" + depsBody + "\n}\n\nfunc New(d Deps) Service { return nil }\n"
	if err := os.WriteFile(filepath.Join(dir, "contract.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
}

func newInjectProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	yaml := "name: proj\nmodule_path: example.com/proj\n"
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	return dir
}

// writeInfra writes a minimal internal/app/providers.go with an Infra
// struct whose fields are the given body.
func writeInfra(t *testing.T, projectDir, fieldsBody string) {
	t.Helper()
	appDir := filepath.Join(projectDir, "internal", "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("mkdir app: %v", err)
	}
	src := "package app\n\ntype Repository interface{ Get() }\n\ntype Infra struct {\n" + fieldsBody + "\n}\n"
	if err := os.WriteFile(filepath.Join(appDir, "providers.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write infra: %v", err)
	}
}

func readInject(t *testing.T, projectDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(projectDir, "internal", "app", "compose.go"))
	if err != nil {
		t.Fatalf("read compose.go: %v", err)
	}
	return string(data)
}

// TestGenerateInject_TypeTopoOrder: billing.Deps.Users typed user.Service
// means user is constructed before billing — by TYPE, not field name.
func TestGenerateInject_TypeTopoOrder(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal/handlers", "user", "user", "\tRepo Repository")
	writeComponentDeps(t, dir, "internal/handlers", "billing", "billing", "\tUsers user.Service")
	// user's Deps references a local Repository type; declare it so the dir
	// parses. (ParseServiceDeps is AST-only; the type need not resolve.)
	appendType(t, dir, "internal/handlers/user", "type Repository interface{ Get() }")
	// Infra provides Repo by exact name — the compile-time backstop fills
	// User.Deps.Repo (the temp project doesn't type-check, so the matcher
	// can't PROVE assignability; the exact-name backstop is the loud,
	// deterministic policy).
	writeInfra(t, dir, "\tRepo Repository")

	services := []ServiceDef{
		{Name: "UserService", ModulePath: "example.com/proj"},
		{Name: "BillingService", ModulePath: "example.com/proj"},
	}
	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   services,
	})
	if err != nil {
		t.Fatalf("GenerateInject: %v", err)
	}
	out := readInject(t, dir)
	ui := strings.Index(out, "c.User =")
	bi := strings.Index(out, "c.Billing =")
	if ui < 0 || bi < 0 {
		t.Fatalf("missing assignments in:\n%s", out)
	}
	if ui > bi {
		t.Fatalf("User must be constructed before Billing:\n%s", out)
	}
	// Billing's Users field is filled by the producer's local var (suffixed
	// "Inst" so it never shadows the package import alias), not infra. (The
	// fixtures declare no middleware_gen.go, so nothing is wrapped.)
	if !strings.Contains(out, "Users: userInst,") {
		t.Fatalf("Billing.Users should be wired to the user producer var:\n%s", out)
	}
}

// TestGenerateInject_MissingProviderIsLoud: a required collaborator field
// (interface, no producer, no Infra) raises a generate-time error naming
// the type + component + field. (No providers.go on disk, so the matcher
// can't prove an Infra field; but the field is also not a producer and not
// scalar — it must be loud.)
func TestGenerateInject_MissingProviderIsLoud(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal/handlers", "billing", "billing", "\tStripe StripeClient")
	appendType(t, dir, "internal/handlers/billing", "type StripeClient interface{ Charge() }")

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "BillingService", ModulePath: "example.com/proj"}},
	})
	if err == nil {
		t.Fatalf("expected MissingProvider error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"Billing", "Stripe", "StripeClient", "providers.go",
		// F14: the enriched error carries a copy-paste-correct remedy — the
		// Infra field, the OpenInfra construction, and the automatic compose
		// wiring — so the forced hand-wiring is correct-by-construction rather
		// than a silent nil.
		"add a field to the Infra struct",
		"Stripe StripeClient",
		"construct it in OpenInfra",
		"infra.Stripe = ",
		"NewComponents sets Billing.Deps.Stripe = infra.Stripe",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q:\n%s", want, msg)
		}
	}
}

// TestGenerateInject_OptionalMissingIsSilent: an optional collaborator
// with no provider takes a typed nil and does NOT raise.
func TestGenerateInject_OptionalMissingIsSilent(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal/handlers", "billing", "billing",
		"\t// forge:optional-dep\n\tStripe StripeClient")
	appendType(t, dir, "internal/handlers/billing", "type StripeClient interface{ Charge() }")

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "BillingService", ModulePath: "example.com/proj"}},
	})
	if err != nil {
		t.Fatalf("optional missing dep must not raise: %v", err)
	}
	out := readInject(t, dir)
	if !strings.Contains(out, "Stripe: nil,") {
		t.Fatalf("optional dep should be typed nil:\n%s", out)
	}
}

// TestGenerateInject_ClockIDGenSeam: the framework Clock / IDGen seam.
// A required Deps field typed `func() time.Time` wires to `time.Now` and one
// typed `func() string` wires to a ULID id generator — BY TYPE, without the
// author declaring an Infra field or hand-wiring the composition (the
// Clock/NewID DI-saga fix). No MissingProvider is raised, and the `time` +
// ulid imports are emitted.
func TestGenerateInject_ClockIDGenSeam(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal/handlers", "orders", "orders",
		"\tNow func() time.Time\n\tNewID func() string")

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "OrdersService", ModulePath: "example.com/proj"}},
	})
	if err != nil {
		t.Fatalf("framework func seam must not raise MissingProvider: %v", err)
	}
	out := readInject(t, dir)
	for _, want := range []string{
		"Now: time.Now,",
		"NewID: func() string { return ulid.Make().String() },",
		`"time"`,
		`"github.com/oklog/ulid/v2"`,
	} {
		if !containsNormalized(out, want) {
			t.Fatalf("compose.go missing %q:\n%s", want, out)
		}
	}
	assertGofmtFixedPoint(t, filepath.Join(dir, "internal", "app", "compose.go"))
}

// containsNormalized is substring containment with runs of whitespace collapsed
// on both sides. Alignment inside an emitted struct literal is DERIVED by the
// writer's gofmt pass — it depends on the longest key in the literal, which the
// assertion has no business predicting. Asserting the exact column is asserting
// the formatter's arithmetic, and it breaks the moment a sibling field is added.
func containsNormalized(haystack, needle string) bool {
	collapse := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	return strings.Contains(collapse(haystack), collapse(needle))
}

// TestGenerateInject_OptionalFuncDepIsNil: an OPTIONAL func-typed dep is left
// to the optional/nil tail (the `//forge:optional-dep` contract wins over the
// framework seam) — codegen accepts it without raising, so the escape hatch
// works end-to-end for func-typed deps too (Fix 1(a)).
func TestGenerateInject_OptionalFuncDepIsNil(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal/handlers", "orders", "orders",
		"\t// forge:optional-dep\n\tNow func() time.Time")

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "OrdersService", ModulePath: "example.com/proj"}},
	})
	if err != nil {
		t.Fatalf("optional func dep must not raise: %v", err)
	}
	out := readInject(t, dir)
	if !strings.Contains(out, "Now: nil,") {
		t.Fatalf("optional func dep should be typed nil, not the framework clock:\n%s", out)
	}
	if strings.Contains(out, "time.Now") {
		t.Fatalf("optional func dep must NOT get the framework clock:\n%s", out)
	}
}

// TestGenerateInject_SameNameInfraFieldOverridesSeam: an author who declares
// an Infra field of the SAME NAME as the func Deps field keeps control — the
// seam yields to the exact-name Infra path.
func TestGenerateInject_SameNameInfraFieldOverridesSeam(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal/handlers", "orders", "orders",
		"\tNow func() time.Time")
	writeInfra(t, dir, "\tNow func() time.Time")

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "OrdersService", ModulePath: "example.com/proj"}},
	})
	if err != nil {
		t.Fatalf("GenerateCompose: %v", err)
	}
	out := readInject(t, dir)
	if !strings.Contains(out, "Now: infra.Now,") {
		t.Fatalf("same-name Infra field should override the framework clock seam:\n%s", out)
	}
}

// TestGenerateInject_ConventionalDeps: Logger/Config wire to infra.Log /
// infra.Cfg, never a producer or a MissingProvider.
func TestGenerateInject_ConventionalDeps(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal/handlers", "user", "user",
		"\tLogger *slog.Logger\n\tConfig *config.Config")

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "UserService", ModulePath: "example.com/proj"}},
	})
	if err != nil {
		t.Fatalf("GenerateInject: %v", err)
	}
	out := readInject(t, dir)
	if !strings.Contains(out, "Logger: infra.Log,") {
		t.Fatalf("Logger should wire to infra.Log:\n%s", out)
	}
	if !strings.Contains(out, "Config: infra.Cfg,") {
		t.Fatalf("Config should wire to infra.Cfg:\n%s", out)
	}
}

// TestGenerateCompose_ReconcileAddsNewService is the F3#1 regression: a
// service added AFTER the initial compose.go scaffold must be wired into the
// (write-once, user-owned) compose.go additively — its import, its Components
// field, and its NewComponents construction — so the regenerated
// mounts_services.go's `c.<Field>` reference resolves instead of failing to
// compile. The existing service must be left intact and the result must be
// valid Go; re-running with the same set must be a no-op (idempotent).
func TestGenerateCompose_ReconcileAddsNewService(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal/handlers", "user", "user",
		"\tLogger *slog.Logger\n\tConfig *config.Config")
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config")

	// First emit: only the user service.
	if err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "UserService", ModulePath: "example.com/proj"}},
	}); err != nil {
		t.Fatalf("first GenerateCompose: %v", err)
	}
	first := readInject(t, dir)
	if !strings.Contains(first, "c.User =") {
		t.Fatalf("first emit missing c.User:\n%s", first)
	}
	if strings.Contains(first, "c.Billing =") {
		t.Fatalf("first emit must not carry billing yet:\n%s", first)
	}

	// Add a second service on disk and regenerate over BOTH. compose.go
	// already exists (write-once), so this exercises the reconciler.
	writeComponentDeps(t, dir, "internal/handlers", "billing", "billing",
		"\tLogger *slog.Logger\n\tConfig *config.Config")
	if err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services: []ServiceDef{
			{Name: "UserService", ModulePath: "example.com/proj"},
			{Name: "BillingService", ModulePath: "example.com/proj"},
		},
	}); err != nil {
		t.Fatalf("reconcile GenerateCompose: %v", err)
	}
	out := readInject(t, dir)

	// Billing is now wired: construction, struct field, and import.
	if !strings.Contains(out, "c.Billing =") {
		t.Fatalf("reconcile did not add the c.Billing construction:\n%s", out)
	}
	if !strings.Contains(out, "example.com/proj/internal/handlers/billing") {
		t.Fatalf("reconcile did not add the billing import:\n%s", out)
	}
	if !strings.Contains(out, "Billing billing.Service") {
		t.Fatalf("reconcile did not add the Billing Components field:\n%s", out)
	}
	// The pre-existing user service must be untouched.
	if !strings.Contains(out, "c.User =") {
		t.Fatalf("reconcile clobbered the existing user wiring:\n%s", out)
	}
	// The whole file must remain valid Go.
	if _, err := parser.ParseFile(token.NewFileSet(), "compose.go", out, parser.SkipObjectResolution); err != nil {
		t.Fatalf("reconciled compose.go is not valid Go: %v\n----\n%s", err, out)
	}

	// Idempotent: regenerating with the same set changes nothing.
	if err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services: []ServiceDef{
			{Name: "UserService", ModulePath: "example.com/proj"},
			{Name: "BillingService", ModulePath: "example.com/proj"},
		},
	}); err != nil {
		t.Fatalf("idempotent GenerateCompose: %v", err)
	}
	if again := readInject(t, dir); again != out {
		t.Fatalf("reconcile is not idempotent; second run diverged:\n%s", again)
	}
}

// TestGenerateCompose_ReconcileAddsClockService proves the `forge scaffold
// service`-with-Clock path: a service carrying a `func() time.Time` dep,
// added AFTER the compose.go scaffold, gets its Clock seam assignment AND the
// `time` import injected additively (format.Source is gofmt-only, so the
// reconciler adds the import itself), leaving valid Go.
func TestGenerateCompose_ReconcileAddsClockService(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal/handlers", "user", "user",
		"\tLogger *slog.Logger")
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config")

	// First emit: only the user service (no time import).
	if err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "UserService", ModulePath: "example.com/proj"}},
	}); err != nil {
		t.Fatalf("first GenerateCompose: %v", err)
	}
	if first := readInject(t, dir); strings.Contains(first, `"time"`) {
		t.Fatalf("first emit should not import time yet:\n%s", first)
	}

	// Add a Clock-bearing service and reconcile over both.
	writeComponentDeps(t, dir, "internal/handlers", "orders", "orders",
		"\tNow func() time.Time")
	if err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services: []ServiceDef{
			{Name: "UserService", ModulePath: "example.com/proj"},
			{Name: "OrdersService", ModulePath: "example.com/proj"},
		},
	}); err != nil {
		t.Fatalf("reconcile GenerateCompose: %v", err)
	}
	out := readInject(t, dir)
	if !strings.Contains(out, "Now: time.Now,") {
		t.Fatalf("reconcile did not wire the Clock seam:\n%s", out)
	}
	if !strings.Contains(out, `"time"`) {
		t.Fatalf("reconcile did not add the time import for the Clock seam:\n%s", out)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "compose.go", out, parser.SkipObjectResolution); err != nil {
		t.Fatalf("reconciled compose.go is not valid Go: %v\n%s", err, out)
	}
}

// TestGenerateCompose_ReconcileWiresDBOnExistingService is the F4 regression:
// a service ALREADY wired into compose.go that later GAINS a DB dep (the
// `DB orm.Context` field the first entity injects) must have that dep wired
// into its existing construction — a stale, write-once compose.go must not
// leave the by-type resolution un-applied (boot-time nil DB). The reconciler
// injects the assignment into the existing New(Deps{…}) literal.
func TestGenerateCompose_ReconcileWiresDBOnExistingService(t *testing.T) {
	dir := newInjectProject(t)
	// Infra exposes a DB field by exact name so the assignment resolves
	// deterministically without type-checking the temp project.
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config\n\tDB Repository")

	// settings service WITHOUT a DB dep initially.
	writeComponentDeps(t, dir, "internal/handlers", "settings", "settings",
		"\tLogger *slog.Logger\n\tConfig *config.Config")
	if err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "SettingsService", ModulePath: "example.com/proj"}},
	}); err != nil {
		t.Fatalf("first GenerateCompose: %v", err)
	}
	if first := readInject(t, dir); strings.Contains(first, "DB:") {
		t.Fatalf("first emit must not carry a DB assignment yet:\n%s", first)
	}

	// The service gains a DB dep — exactly what ensureDepsDBField does when the
	// first entity appears. Rewrite its Deps to include DB.
	writeComponentDeps(t, dir, "internal/handlers", "settings", "settings",
		"\tLogger *slog.Logger\n\tConfig *config.Config\n\tDB Repository")
	if err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "SettingsService", ModulePath: "example.com/proj"}},
	}); err != nil {
		t.Fatalf("reconcile GenerateCompose: %v", err)
	}
	out := readInject(t, dir)
	// gofmt aligns struct-literal values, so collapse whitespace before
	// matching the field/expr pairs.
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if !strings.Contains(flat(out), "DB: infra.DB") {
		t.Fatalf("reconcile did not wire the gained DB dep into the existing construction:\n%s", out)
	}
	// The prior Logger/Config wiring must remain.
	if !strings.Contains(flat(out), "Logger: infra.Log") || !strings.Contains(flat(out), "Config: infra.Cfg") {
		t.Fatalf("reconcile disturbed the existing assignments:\n%s", out)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "compose.go", out, parser.SkipObjectResolution); err != nil {
		t.Fatalf("reconciled compose.go is not valid Go: %v\n----\n%s", err, out)
	}
	// Idempotent: the assignment is not injected twice.
	if err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "SettingsService", ModulePath: "example.com/proj"}},
	}); err != nil {
		t.Fatalf("idempotent GenerateCompose: %v", err)
	}
	if again := readInject(t, dir); strings.Count(flat(again), "DB: infra.DB") != 1 {
		t.Fatalf("DB assignment must be wired exactly once (idempotent):\n%s", again)
	}
}

// writeFallibleComponentDeps writes a component whose New returns (Service,
// error) — the real scaffold shape. Its compose
// construction is therefore the fallible `xInst, err := pkg.New(pkg.Deps{…})`
// … `c.X = xInst` form, where the Deps literal PRECEDES the `c.X =` assignment.
func writeFallibleComponentDeps(t *testing.T, projectDir, roleRoot, leaf, pkg, depsBody string) {
	t.Helper()
	dir := filepath.Join(projectDir, filepath.FromSlash(roleRoot), leaf)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := "package " + pkg + "\n\ntype Service interface{ Do() }\n\ntype Deps struct {\n" + depsBody +
		"\n}\n\nfunc New(d Deps) (Service, error) { return nil, nil }\n"
	if err := os.WriteFile(filepath.Join(dir, "contract.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
}

// TestGenerateCompose_ReconcileWiresDBOnFallibleService is the runtime-crash
// regression the earlier non-fallible DB test never reached: the REAL scaffold
// emits a fallible service, whose construction is
// `xInst, err := pkg.New(pkg.Deps{…})` … `c.X = xInst` — the Deps literal sits
// BEFORE the `c.X =` assignment. The field-level additive pass anchors its
// Deps-literal search AFTER that assignment, so it silently no-ops on this
// shape and the gained `DB` dep is never wired: compose.go constructs the
// handler with a nil DB and the app crashes at boot ("Deps.DB is required").
// The pristine-stale converge closes the gap by re-rendering the whole file
// once nothing user-owned would be lost.
func TestGenerateCompose_ReconcileWiresDBOnFallibleService(t *testing.T) {
	dir := newInjectProject(t)
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config\n\tDB Repository")

	// A fallible service WITHOUT a DB dep initially.
	writeFallibleComponentDeps(t, dir, "internal/handlers", "orders", "orders",
		"\tLogger *slog.Logger\n\tConfig *config.Config")
	if err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "OrdersService", ModulePath: "example.com/proj"}},
	}); err != nil {
		t.Fatalf("first GenerateCompose: %v", err)
	}
	first := readInject(t, dir)
	// Precondition: the fallible shape (so this test exercises the anchor gap)
	// and no DB assignment yet.
	if !strings.Contains(first, "ordersInst, err := orders.New(orders.Deps{") {
		t.Fatalf("expected the fallible construction shape in the first emit:\n%s", first)
	}
	if strings.Contains(first, "DB:") {
		t.Fatalf("first emit must not carry a DB assignment yet:\n%s", first)
	}

	// The service gains a DB dep (what the first entity's ensureDepsDBField does).
	writeFallibleComponentDeps(t, dir, "internal/handlers", "orders", "orders",
		"\tLogger *slog.Logger\n\tConfig *config.Config\n\tDB Repository")
	if err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "OrdersService", ModulePath: "example.com/proj"}},
	}); err != nil {
		t.Fatalf("reconcile GenerateCompose: %v", err)
	}
	out := readInject(t, dir)
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if !strings.Contains(flat(out), "DB: infra.DB") {
		t.Fatalf("reconcile did not wire the gained DB dep into the fallible construction:\n%s", out)
	}
	// Existing wiring must remain intact.
	for _, want := range []string{"Logger: infra.Log", "Config: infra.Cfg", "c.Orders = ordersInst"} {
		if !strings.Contains(flat(out), want) {
			t.Fatalf("reconcile disturbed existing wiring (missing %q):\n%s", want, out)
		}
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "compose.go", out, parser.SkipObjectResolution); err != nil {
		t.Fatalf("reconciled compose.go is not valid Go: %v\n----\n%s", err, out)
	}
	// Idempotent: the DB assignment is wired exactly once.
	if err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "OrdersService", ModulePath: "example.com/proj"}},
	}); err != nil {
		t.Fatalf("idempotent GenerateCompose: %v", err)
	}
	if again := readInject(t, dir); strings.Count(flat(again), "DB: infra.DB") != 1 {
		t.Fatalf("DB assignment must be wired exactly once (idempotent):\n%s", again)
	}
}

// TestGenerateCompose_ReconcilePreservesCustomizationOnStaleFile locks the
// safety gate on the pristine-stale converge: a compose.go that carries ANY
// line the fresh render lacks (here a hand-added statement) is a customized /
// disowned file the user owns. Even when it is ALSO stale (a fallible service
// has gained a DB dep it lacks), the converge MUST decline and leave the file
// to the additive/preserve path — the customization survives byte-for-byte and
// forge never clobbers hand-owned wiring.
func TestGenerateCompose_ReconcilePreservesCustomizationOnStaleFile(t *testing.T) {
	dir := newInjectProject(t)
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config\n\tDB Repository")

	writeFallibleComponentDeps(t, dir, "internal/handlers", "orders", "orders",
		"\tLogger *slog.Logger\n\tConfig *config.Config")
	if err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "OrdersService", ModulePath: "example.com/proj"}},
	}); err != nil {
		t.Fatalf("first GenerateCompose: %v", err)
	}

	// Hand-customize: inject a sentinel line the fresh render never emits.
	const sentinel = "\t_ = infra // forge-test: HAND-ADDED sentinel, must survive"
	composePath := filepath.Join(dir, "internal", "app", "compose.go")
	raw := readInject(t, dir)
	customized := strings.Replace(raw, "c := &Components{}\n", "c := &Components{}\n"+sentinel+"\n", 1)
	if customized == raw {
		t.Fatalf("failed to inject sentinel into compose.go:\n%s", raw)
	}
	if err := os.WriteFile(composePath, []byte(customized), 0o644); err != nil {
		t.Fatalf("write customized compose.go: %v", err)
	}

	// Make it stale too: the service gains a DB dep the file does not wire.
	writeFallibleComponentDeps(t, dir, "internal/handlers", "orders", "orders",
		"\tLogger *slog.Logger\n\tConfig *config.Config\n\tDB Repository")
	if err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "OrdersService", ModulePath: "example.com/proj"}},
	}); err != nil {
		t.Fatalf("reconcile GenerateCompose: %v", err)
	}
	out := readInject(t, dir)
	if !strings.Contains(out, "HAND-ADDED sentinel, must survive") {
		t.Fatalf("customization was clobbered by the reconcile:\n%s", out)
	}
	// The converge declined (customized file left to the additive path); the
	// fallible additive pass cannot retrofit the DB, so it stays absent — the
	// correct conservative outcome for a hand-owned file.
	if strings.Contains(out, "DB:") {
		t.Fatalf("a customized/disowned compose.go must not be wholesale-converged:\n%s", out)
	}
}

// TestGenerateProviders_RetrofitsORMOnFirstEntity is the F4 provider-side
// regression: a providers.go scaffolded BEFORE the first entity has a
// `DB *sql.DB` pool but no ORM client. When the first entity turns on the ORM
// (ormEnabled), the write-once providers.go must be retrofitted with the
// `ORM *orm.Client` field + its OpenInfra construction so the service's
// `DB orm.Context` dep resolves — instead of a build break from *sql.DB not
// satisfying orm.Context.
func TestGenerateProviders_RetrofitsORMOnFirstEntity(t *testing.T) {
	dir := t.TempDir()

	// First emit: has a database driver but no entities yet (ormEnabled=false).
	if err := GenerateProviders("example.com/proj", "postgres", false, dir); err != nil {
		t.Fatalf("first GenerateProviders: %v", err)
	}
	path := filepath.Join(dir, "internal", "app", "providers.go")
	before, _ := os.ReadFile(path)
	if strings.Contains(string(before), "ORM *orm.Client") {
		t.Fatalf("pre-entity providers.go must not carry the ORM client yet:\n%s", before)
	}
	if !strings.Contains(string(before), "DB *sql.DB") {
		t.Fatalf("pre-entity providers.go should carry the *sql.DB pool:\n%s", before)
	}

	// First entity arrives → ormEnabled becomes true; retrofit fires.
	if err := GenerateProviders("example.com/proj", "postgres", true, dir); err != nil {
		t.Fatalf("retrofit GenerateProviders: %v", err)
	}
	after, _ := os.ReadFile(path)
	got := string(after)
	for _, want := range []string{
		"ORM *orm.Client",
		"orm.NewClientWithDB(db, \"postgres\")",
		"infra.ORM = ormClient",
		"github.com/reliant-labs/forge/pkg/orm",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("retrofitted providers.go missing %q:\n%s", want, got)
		}
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "providers.go", got, parser.SkipObjectResolution); err != nil {
		t.Fatalf("retrofitted providers.go is not valid Go: %v\n----\n%s", err, got)
	}

	// Idempotent: a second retrofit run injects nothing more.
	if err := GenerateProviders("example.com/proj", "postgres", true, dir); err != nil {
		t.Fatalf("idempotent GenerateProviders: %v", err)
	}
	again, _ := os.ReadFile(path)
	if strings.Count(string(again), "ORM *orm.Client") != 1 {
		t.Fatalf("ORM field must be present exactly once (idempotent):\n%s", again)
	}
}

// TestGenerateInject_ScalarIsConfigNotMissing: a scalar Deps field is
// configuration, not a collaborator — it takes the typed-zero and never
// raises MissingProvider.
func TestGenerateInject_ScalarIsConfigNotMissing(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal/handlers", "user", "user", "\tMaxRetries int")

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "UserService", ModulePath: "example.com/proj"}},
	})
	if err != nil {
		t.Fatalf("scalar dep must not raise MissingProvider: %v", err)
	}
	out := readInject(t, dir)
	if !strings.Contains(out, "MaxRetries: 0,") {
		t.Fatalf("scalar should take typed zero:\n%s", out)
	}
}

// TestGenerateInject_ScalarResolvesFromConfig: a scalar Deps field that
// matches a typed Config field resolves from infra.Cfg.<field>, not a
// typed-zero. An unmatched scalar still takes the typed-zero. (Regression
// for kalshi's WTI EIAKey/FREDKey being reset to "" + TODO.)
func TestGenerateInject_ScalarResolvesFromConfig(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal/handlers", "wti", "wti",
		"\tEIAKey string\n\tUnmapped string")
	// Config struct carries EIAKey (matches) but not Unmapped.
	cfgDir := filepath.Join(dir, "pkg", "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	cfgSrc := "package config\n\ntype Config struct {\n\tEIAKey string\n\tPort int32\n}\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.go"), []byte(cfgSrc), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "WtiService", ModulePath: "example.com/proj"}},
	})
	if err != nil {
		t.Fatalf("GenerateInject: %v", err)
	}
	out := readInject(t, dir)
	if !containsNormalized(out, "EIAKey: infra.Cfg.EIAKey,") {
		t.Fatalf("EIAKey should resolve from infra.Cfg:\n%s", out)
	}
	if !containsNormalized(out, "Unmapped: \"\",") {
		t.Fatalf("Unmapped scalar should take typed-zero:\n%s", out)
	}
}

// TestParseInfraFields reads the Infra struct fields from internal/app.
func TestParseInfraFields(t *testing.T) {
	dir := t.TempDir()
	appDir := filepath.Join(dir, "internal", "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := "package app\n\nimport \"log/slog\"\n\ntype Infra struct {\n\tLog *slog.Logger\n\tRepo *PostgresRepo\n\tunexported int\n}\n"
	if err := os.WriteFile(filepath.Join(appDir, "providers.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	fields, err := parseInfraFields(appDir)
	if err != nil {
		t.Fatalf("parseInfraFields: %v", err)
	}
	if _, ok := fields["Log"]; !ok {
		t.Fatalf("Log field missing: %v", fields)
	}
	if f, ok := fields["Repo"]; !ok || f.Type != "*PostgresRepo" {
		t.Fatalf("Repo field wrong: %+v", fields)
	}
	if _, ok := fields["unexported"]; ok {
		t.Fatalf("unexported field should be skipped")
	}
}

// appendType appends a type declaration to a component's contract.go so a
// referenced local type parses (AST-only; need not type-check).
func appendType(t *testing.T, projectDir, rolePath, decl string) {
	t.Helper()
	path := filepath.Join(projectDir, filepath.FromSlash(rolePath), "contract.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read for append: %v", err)
	}
	if err := os.WriteFile(path, append(data, []byte("\n"+decl+"\n")...), 0o644); err != nil {
		t.Fatalf("append: %v", err)
	}
}

// ─── HTTP-client seam + observed-wrapper emission ────────────────────────

// TestGenerateInject_HTTPClientSeam: a required Deps field typed exactly
// `*http.Client` (the adapter/client scaffold shape) resolves to the
// scaffold-once providers.go method `infra.DefaultClient()` — the
// instrumented outbound default — instead of raising MissingProvider.
func TestGenerateInject_HTTPClientSeam(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal", "stripe", "stripe", "\tHTTPClient *http.Client")

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Packages: []BootstrapPackageData{
			{Name: "stripe", Package: "stripe", ImportPath: "stripe", FieldName: "Stripe", VarName: "stripe"},
		},
	})
	if err != nil {
		t.Fatalf("HTTP-client seam must not raise MissingProvider: %v", err)
	}
	out := readInject(t, dir)
	if !strings.Contains(out, "HTTPClient: infra.DefaultClient(),") {
		t.Fatalf("HTTPClient should wire to infra.DefaultClient():\n%s", out)
	}
	if _, ferr := format.Source([]byte(out)); ferr != nil {
		t.Fatalf("generated compose.go is not gofmt-valid: %v\n%s", ferr, out)
	}
}

// TestGenerateInject_SameNameInfraFieldOverridesHTTPClientSeam: an author who
// declares Infra.HTTPClient keeps control — the exact-name Infra path wins
// over the DefaultClient() seam (same override contract as Clock/IDGen).
func TestGenerateInject_SameNameInfraFieldOverridesHTTPClientSeam(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal", "stripe", "stripe", "\tHTTPClient *http.Client")
	writeInfra(t, dir, "\tHTTPClient *http.Client")

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Packages: []BootstrapPackageData{
			{Name: "stripe", Package: "stripe", ImportPath: "stripe", FieldName: "Stripe", VarName: "stripe"},
		},
	})
	if err != nil {
		t.Fatalf("GenerateCompose: %v", err)
	}
	out := readInject(t, dir)
	if !strings.Contains(out, "HTTPClient: infra.HTTPClient,") {
		t.Fatalf("same-name Infra field should override the DefaultClient seam:\n%s", out)
	}
	if strings.Contains(out, "infra.DefaultClient()") {
		t.Fatalf("seam must yield to the explicit Infra field:\n%s", out)
	}
}

// TestGenerateInject_OptionalHTTPClientIsNil: `//forge:optional-dep` wins
// over the seam — the marker's "nil is acceptable" contract is respected
// (the scaffolds default a nil HTTPClient internally).
func TestGenerateInject_OptionalHTTPClientIsNil(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal", "stripe", "stripe",
		"\t// forge:optional-dep\n\tHTTPClient *http.Client")

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Packages: []BootstrapPackageData{
			{Name: "stripe", Package: "stripe", ImportPath: "stripe", FieldName: "Stripe", VarName: "stripe"},
		},
	})
	if err != nil {
		t.Fatalf("optional HTTPClient must not raise: %v", err)
	}
	out := readInject(t, dir)
	if !strings.Contains(out, "HTTPClient: nil,") {
		t.Fatalf("optional HTTPClient should be typed nil:\n%s", out)
	}
	if strings.Contains(out, "infra.DefaultClient()") {
		t.Fatalf("optional HTTPClient must NOT get the DefaultClient seam:\n%s", out)
	}
}

// writeObservedPackage writes an internal contract package whose New returns
// the Service interface, plus the OWNED observe_chain.go seam — the shape
// `forge scaffold package` scaffolds. The seam's presence (its
// newObserveChain builder) is the opt-in signal DetectObserveChainSeam keys
// on; the middleware wrapper constructor itself is generated
// (middleware_gen.go, named off the concrete return type — here `service` →
// NewServiceWithForgeMiddleware) and not needed for this compose-emission test.
func writeObservedPackage(t *testing.T, projectDir, leaf, pkg string, fallible bool) {
	t.Helper()
	dir := filepath.Join(projectDir, "internal", leaf)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// New returns a concrete `service` impl (the shape the real interactor /
	// service / adapter scaffolds emit), so ResolveMiddlewareWrapper resolves the
	// wrapper name off the concrete type: `service` → `NewServiceWithForgeMiddleware`.
	newFn := "func New(d Deps) Service { return &service{} }"
	if fallible {
		newFn = "func New(d Deps) (Service, error) { return &service{}, nil }"
	}
	contract := "package " + pkg + "\n\ntype Service interface{ Do() }\n\ntype service struct{}\n\nfunc (s *service) Do() {}\n\ntype Deps struct {\n\tLogger *slog.Logger\n}\n\n" + newFn + "\n"
	if err := os.WriteFile(filepath.Join(dir, "contract.go"), []byte(contract), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	seam := "package " + pkg + "\n\nfunc newObserveChain() interface{} { return nil }\n"
	if err := os.WriteFile(filepath.Join(dir, "observe_chain.go"), []byte(seam), 0o644); err != nil {
		t.Fatalf("write observe_chain: %v", err)
	}
	// The decorator the contracts step writes before compose is assembled;
	// compose wraps only a component whose decorator is declared.
	decorator := "package " + pkg + "\n\nfunc NewServiceWithForgeMiddleware(inner Service) Service { return &forgeMiddlewareService{inner: inner} }\n\ntype forgeMiddlewareService struct{ Service; inner Service }\n"
	if err := os.WriteFile(filepath.Join(dir, "middleware_gen.go"), []byte(decorator), 0o644); err != nil {
		t.Fatalf("write middleware_gen: %v", err)
	}
}

// TestGenerateCompose_WrappedObservedPackage: a package with an observe_chain.go
// seam and an interface-returning New is constructed in WRAPPED form.
func TestGenerateCompose_WrappedObservedPackage(t *testing.T) {
	dir := newInjectProject(t)
	writeObservedPackage(t, dir, "stripe", "stripe", false)
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config")

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Packages: []BootstrapPackageData{
			{Name: "stripe", Package: "stripe", ImportPath: "stripe", FieldName: "Stripe", VarName: "stripe"},
		},
	})
	if err != nil {
		t.Fatalf("GenerateCompose: %v", err)
	}
	out := readInject(t, dir)
	if !strings.Contains(out, "c.Stripe = stripe.NewServiceWithForgeMiddleware(stripe.New(stripe.Deps{") {
		t.Fatalf("expected wrapped construction:\n%s", out)
	}
	if _, ferr := format.Source([]byte(out)); ferr != nil {
		t.Fatalf("wrapped compose.go is not gofmt-valid: %v\n%s", ferr, out)
	}
}

// TestGenerateCompose_WrappedFalliblePackage: the fallible two-step keeps the
// error branch and wraps the constructed local var.
func TestGenerateCompose_WrappedFalliblePackage(t *testing.T) {
	dir := newInjectProject(t)
	writeObservedPackage(t, dir, "stripe", "stripe", true)
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config")

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Packages: []BootstrapPackageData{
			{Name: "stripe", Package: "stripe", ImportPath: "stripe", FieldName: "Stripe", VarName: "stripe", Fallible: true},
		},
	})
	if err != nil {
		t.Fatalf("GenerateCompose: %v", err)
	}
	out := readInject(t, dir)
	if !strings.Contains(out, "stripeInst, err := stripe.New(stripe.Deps{") {
		t.Fatalf("fallible construction missing:\n%s", out)
	}
	if !strings.Contains(out, "c.Stripe = stripe.NewServiceWithForgeMiddleware(stripeInst)") {
		t.Fatalf("fallible wrapped assignment missing:\n%s", out)
	}
	if _, ferr := format.Source([]byte(out)); ferr != nil {
		t.Fatalf("wrapped compose.go is not gofmt-valid: %v\n%s", ferr, out)
	}
}

// TestGenerateCompose_FallibleErrorStringIsLintClean: the fallible
// construction wraps New's error. The error string must not START with the
// capitalized component FieldName ("Stripe: %w") — staticcheck's ST1005
// ("error strings should not be capitalized") flags that on forge's own
// emitted compose.go, breaking the invariant that `forge lint` exits 0 on a
// fresh scaffold. The emitted form is "construct <FieldName>: %w" —
// lowercase first word, component name preserved.
func TestGenerateCompose_FallibleErrorStringIsLintClean(t *testing.T) {
	dir := newInjectProject(t)
	writeObservedPackage(t, dir, "stripe", "stripe", true)
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config")

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Packages: []BootstrapPackageData{
			{Name: "stripe", Package: "stripe", ImportPath: "stripe", FieldName: "Stripe", VarName: "stripe", Fallible: true},
		},
	})
	if err != nil {
		t.Fatalf("GenerateCompose: %v", err)
	}
	out := readInject(t, dir)
	if !strings.Contains(out, `fmt.Errorf("construct Stripe: %w", err)`) {
		t.Fatalf("fallible error wrap should read `construct Stripe: %%w`:\n%s", out)
	}
	if strings.Contains(out, `fmt.Errorf("Stripe: %w", err)`) {
		t.Fatalf("ST1005 regression — capitalized error string emitted:\n%s", out)
	}
}

// TestGenerateCompose_ConcreteConstructorNotWrapped: a handler-shaped package
// (New returns *Service) is NEVER wrapped, even when an observe_chain.go seam
// is present — otelconnect owns the RPC edge and the Components field type is
// the concrete pointer.
func TestGenerateCompose_ConcreteConstructorNotWrapped(t *testing.T) {
	dir := newInjectProject(t)
	pkgDir := filepath.Join(dir, "internal", "things")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := "package things\n\ntype Service struct{}\n\ntype Deps struct {\n\tLogger *slog.Logger\n}\n\nfunc New(d Deps) *Service { return &Service{} }\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "service.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write service: %v", err)
	}
	seam := "package things\n\nfunc newObserveChain() interface{} { return nil }\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "observe_chain.go"), []byte(seam), 0o644); err != nil {
		t.Fatalf("write observe_chain: %v", err)
	}
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config")

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Packages: []BootstrapPackageData{
			{Name: "things", Package: "things", ImportPath: "things", FieldName: "Things", VarName: "things"},
		},
	})
	if err != nil {
		t.Fatalf("GenerateCompose: %v", err)
	}
	out := readInject(t, dir)
	if strings.Contains(out, "WithForgeMiddleware") {
		t.Fatalf("concrete-returning constructor must not be wrapped:\n%s", out)
	}
}

// TestGenerateCompose_ReconcileAddsWrappedPackage: a wrapped package added
// AFTER the initial compose.go scaffold is injected additively in wrapped
// form, the result stays valid Go, and the reconcile is idempotent.
func TestGenerateCompose_ReconcileAddsWrappedPackage(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal/handlers", "user", "user",
		"\tLogger *slog.Logger\n\tConfig *config.Config")
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config")

	if err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "UserService", ModulePath: "example.com/proj"}},
	}); err != nil {
		t.Fatalf("first GenerateCompose: %v", err)
	}

	writeObservedPackage(t, dir, "stripe", "stripe", false)
	in := InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "UserService", ModulePath: "example.com/proj"}},
		Packages: []BootstrapPackageData{
			{Name: "stripe", Package: "stripe", ImportPath: "stripe", FieldName: "Stripe", VarName: "stripe"},
		},
	}
	if err := GenerateCompose(in); err != nil {
		t.Fatalf("reconcile GenerateCompose: %v", err)
	}
	out := readInject(t, dir)
	if !strings.Contains(out, "c.Stripe = stripe.NewServiceWithForgeMiddleware(stripe.New(stripe.Deps{") {
		t.Fatalf("reconcile did not inject the wrapped construction:\n%s", out)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "compose.go", out, parser.SkipObjectResolution); err != nil {
		t.Fatalf("reconciled compose.go is not valid Go: %v\n%s", err, out)
	}
	// Idempotent.
	if err := GenerateCompose(in); err != nil {
		t.Fatalf("idempotent GenerateCompose: %v", err)
	}
	if again := readInject(t, dir); again != out {
		t.Fatalf("wrapped reconcile is not idempotent:\n%s", again)
	}
}

// TestGenerateCompose_WrappedProducerInjectsDecorated: a consumer of an
// INSTRUMENTED producer must receive the DECORATED value, not the raw
// constructed instance.
//
// The regression this pins: compose emitted
//
//	c.Stripe = stripe.NewServiceWithForgeMiddleware(stripeInst)
//	billingInst, err := billing.New(billing.Deps{Payments: stripeInst})
//
// — the decorator was assigned to a Components field nothing reads, while the
// one real caller got the undecorated instance. Every call through the
// producer bypassed the observability chain the package was opted into,
// although a decorator HAD been constructed. Silent capability loss.
func TestGenerateCompose_WrappedProducerInjectsDecorated(t *testing.T) {
	dir := newInjectProject(t)
	writeObservedPackage(t, dir, "stripe", "stripe", false)
	writeComponentDeps(t, dir, "internal/handlers", "billing", "billing", "\tPayments stripe.Service")
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config")

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "BillingService", ModulePath: "example.com/proj"}},
		Packages: []BootstrapPackageData{
			{Name: "stripe", Package: "stripe", ImportPath: "stripe", FieldName: "Stripe", VarName: "stripe"},
		},
	})
	if err != nil {
		t.Fatalf("GenerateCompose: %v", err)
	}
	out := readInject(t, dir)

	if !strings.Contains(out, "c.Stripe = stripe.NewServiceWithForgeMiddleware(") {
		t.Fatalf("producer should be constructed in wrapped form:\n%s", out)
	}
	if !strings.Contains(out, "Payments: c.Stripe,") {
		t.Fatalf("consumer must receive the DECORATED producer (Payments: c.Stripe), got:\n%s", out)
	}
	if strings.Contains(out, "Payments: stripeInst,") {
		t.Fatalf("consumer received the UNDECORATED instance — the observe chain is bypassed:\n%s", out)
	}
	if _, ferr := format.Source([]byte(out)); ferr != nil {
		t.Fatalf("compose.go is not gofmt-valid: %v\n%s", ferr, out)
	}
}

// TestGenerateCompose_ReconcileDropsRemovedDepsField is the converge regression
// the additive-only reconciler could not reach: removing a field from a
// component's Deps struct is ordinary refactoring, but compose.go keeps
// constructing it, so `<alias>.Deps{…}` names a field the struct no longer has
// and the project stops compiling — `unknown field DB in struct literal`. Every
// subsequent `forge generate` re-ran the same additive passes and left the dead
// key in place, so generate could not heal a state generate created; the only
// recovery an agent found in a live run was `rm -f internal/app/compose.go`.
//
// The key set of a `<pkg>.Deps{…}` literal is not a degree of freedom the
// author owns — Go fixes it to the Deps struct's field set. So a key outside
// that set is DERIVABLY dead (proven from the struct, not from compose.go) and
// dropping it destroys nothing that could ever have compiled.
func TestGenerateCompose_ReconcileDropsRemovedDepsField(t *testing.T) {
	dir := newInjectProject(t)
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config\n\tDB Repository")

	writeFallibleComponentDeps(t, dir, "internal/handlers", "orders", "orders",
		"\tLogger *slog.Logger\n\tConfig *config.Config\n\tDB Repository")
	in := InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "OrdersService", ModulePath: "example.com/proj"}},
	}
	if err := GenerateCompose(in); err != nil {
		t.Fatalf("first GenerateCompose: %v", err)
	}
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if !strings.Contains(flat(readInject(t, dir)), "DB: infra.DB") {
		t.Fatalf("precondition: first emit must wire DB:\n%s", readInject(t, dir))
	}

	// The author removes the DB dependency from the Deps struct.
	writeFallibleComponentDeps(t, dir, "internal/handlers", "orders", "orders",
		"\tLogger *slog.Logger\n\tConfig *config.Config")
	if err := GenerateCompose(in); err != nil {
		t.Fatalf("reconcile GenerateCompose: %v", err)
	}
	out := readInject(t, dir)
	if strings.Contains(flat(out), "DB:") {
		t.Fatalf("compose.go still constructs the REMOVED Deps field — the project cannot build:\n%s", out)
	}
	for _, want := range []string{"Logger: infra.Log", "Config: infra.Cfg", "c.Orders = ordersInst"} {
		if !strings.Contains(flat(out), want) {
			t.Fatalf("drop disturbed the surviving wiring (missing %q):\n%s", want, out)
		}
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "compose.go", out, parser.SkipObjectResolution); err != nil {
		t.Fatalf("reconciled compose.go is not valid Go: %v\n----\n%s", err, out)
	}
	// Idempotent: a second generate over the converged file changes nothing.
	if err := GenerateCompose(in); err != nil {
		t.Fatalf("idempotent GenerateCompose: %v", err)
	}
	if again := readInject(t, dir); again != out {
		t.Fatalf("reconcile is not idempotent:\n--- first ---\n%s\n--- second ---\n%s", out, again)
	}
}

// TestGenerateCompose_DropsRemovedDepsKeyButNotTheExpression is the ownership
// line stated as a test: forge owns the KEY SET of a `<pkg>.Deps{…}` literal
// (Go fixes it to the Deps struct's fields) and the author owns the EXPRESSION.
// A hand-wired value on a surviving field must come through the drop
// byte-for-byte, in the same edit that removes the key whose field is gone.
func TestGenerateCompose_DropsRemovedDepsKeyButNotTheExpression(t *testing.T) {
	dir := newInjectProject(t)
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config\n\tDB Repository\n\tAuditLog *slog.Logger")

	writeFallibleComponentDeps(t, dir, "internal/handlers", "orders", "orders",
		"\tLogger *slog.Logger\n\tConfig *config.Config\n\tDB Repository")
	in := InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "OrdersService", ModulePath: "example.com/proj"}},
	}
	if err := GenerateCompose(in); err != nil {
		t.Fatalf("first GenerateCompose: %v", err)
	}

	// The author re-wires Logger by hand to a different Infra source.
	composePath := filepath.Join(dir, "internal", "app", "compose.go")
	handWired := strings.Replace(readInject(t, dir), "Logger: infra.Log,", "Logger: infra.AuditLog,", 1)
	if !strings.Contains(handWired, "infra.AuditLog") {
		t.Fatalf("failed to hand-wire Logger:\n%s", readInject(t, dir))
	}
	if err := os.WriteFile(composePath, []byte(handWired), 0o644); err != nil {
		t.Fatalf("write hand-wired compose.go: %v", err)
	}

	// ...and removes DB from the Deps struct.
	writeFallibleComponentDeps(t, dir, "internal/handlers", "orders", "orders",
		"\tLogger *slog.Logger\n\tConfig *config.Config")
	if err := GenerateCompose(in); err != nil {
		t.Fatalf("reconcile GenerateCompose: %v", err)
	}
	out := readInject(t, dir)
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if strings.Contains(flat(out), "DB:") {
		t.Fatalf("the removed field's key survived:\n%s", out)
	}
	if !strings.Contains(flat(out), "Logger: infra.AuditLog") {
		t.Fatalf("the hand-wired EXPRESSION was clobbered — the drop must touch keys only:\n%s", out)
	}
}

// TestGenerateCompose_DropSurvivesCustomizedFile: a compose.go the author has
// customized is exactly the file the additive passes and the pristine-stale
// converge both DECLINE to touch — so before this, a customized file that lost
// a Deps field was permanently unbuildable by generate. The drop is not one of
// those passes and is not gated by them: the customization survives AND the
// dead key goes.
func TestGenerateCompose_DropSurvivesCustomizedFile(t *testing.T) {
	dir := newInjectProject(t)
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config\n\tDB Repository")

	writeFallibleComponentDeps(t, dir, "internal/handlers", "orders", "orders",
		"\tLogger *slog.Logger\n\tConfig *config.Config\n\tDB Repository")
	in := InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "OrdersService", ModulePath: "example.com/proj"}},
	}
	if err := GenerateCompose(in); err != nil {
		t.Fatalf("first GenerateCompose: %v", err)
	}
	const sentinel = "\t_ = infra // forge-test: HAND-ADDED sentinel, must survive"
	composePath := filepath.Join(dir, "internal", "app", "compose.go")
	raw := readInject(t, dir)
	customized := strings.Replace(raw, "c := &Components{}\n", "c := &Components{}\n"+sentinel+"\n", 1)
	if customized == raw {
		t.Fatalf("failed to inject sentinel:\n%s", raw)
	}
	if err := os.WriteFile(composePath, []byte(customized), 0o644); err != nil {
		t.Fatalf("write customized compose.go: %v", err)
	}

	writeFallibleComponentDeps(t, dir, "internal/handlers", "orders", "orders",
		"\tLogger *slog.Logger\n\tConfig *config.Config")
	if err := GenerateCompose(in); err != nil {
		t.Fatalf("reconcile GenerateCompose: %v", err)
	}
	out := readInject(t, dir)
	if !strings.Contains(out, "HAND-ADDED sentinel, must survive") {
		t.Fatalf("customization was clobbered:\n%s", out)
	}
	if strings.Contains(strings.Join(strings.Fields(out), " "), "DB:") {
		t.Fatalf("a customized file must still shed the dead key:\n%s", out)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "compose.go", out, parser.SkipObjectResolution); err != nil {
		t.Fatalf("reconciled compose.go is not valid Go: %v\n----\n%s", err, out)
	}
}

// TestGenerateCompose_ReconcileDropsStaleKeyOfExternalComponent is the
// regression test for #747. The stale-key drop measured only DISCOVERED
// components' literals, so a package that left the discovered set while
// compose.go — owned, only ever added to — kept constructing it was never
// checked again. Marking a composed adapter //forge:external-component and
// then removing a Deps field left a key naming nothing, and every generate
// after that failed its validate build and rolled back:
//
//	internal/app/compose.go:62:3: unknown field BaseURL in struct literal of type alpaca.Deps
//
// Go fixes that literal's key set to the Deps struct exactly as it does a
// discovered component's, so the drop now covers every project package the
// file constructs. The construction itself is the author's to delete: it is
// kept, and reported, when the package's own marker says the server no
// longer composes it.
func TestGenerateCompose_ReconcileDropsStaleKeyOfExternalComponent(t *testing.T) {
	cases := []struct {
		name string
		// header is prepended to the package's contract.go once it leaves
		// the composition; discovered says whether it is still in the
		// discovered package set (a marked package is; an excluded one is not).
		header, warning string
		discovered      bool
	}{
		{
			name:       "external-component",
			header:     "//forge:external-component\n",
			warning:    "internal/app/compose.go still constructs internal/alpaca, which is now //forge:external-component — delete its import, Components field and construction, and put the instance on Infra",
			discovered: true,
		},
		{
			name:       "not-a-component",
			header:     "//forge:not-a-component: the CLI builds it\n",
			warning:    "internal/app/compose.go still constructs internal/alpaca, which is now //forge:not-a-component — delete its import, Components field and construction: the server does not have it",
			discovered: true,
		},
		{
			// No marker to report: the package simply left discovery (its
			// contract opted out). The key is still dead, so it still goes.
			name:   "excluded",
			header: "//forge:exclude-contract\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newInjectProject(t)
			writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config\n\tAuditLog *slog.Logger")
			writeFallibleComponentDeps(t, dir, "internal", "alpaca", "alpaca",
				"\tLogger *slog.Logger\n\tBaseURL string")
			alpaca := BootstrapPackageData{Name: "alpaca", Package: "alpaca", ImportPath: "alpaca", FieldName: "Alpaca", VarName: "alpaca", Fallible: true}
			var warnings strings.Builder
			in := InjectGenInput{
				GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
				Packages:   []BootstrapPackageData{alpaca},
				Warnings:   &warnings,
			}
			if err := GenerateCompose(in); err != nil {
				t.Fatalf("first GenerateCompose: %v", err)
			}
			flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
			if first := flat(readInject(t, dir)); !strings.Contains(first, "BaseURL:") || !strings.Contains(first, "Logger: infra.Log,") {
				t.Fatalf("precondition: the first emit composes alpaca with Logger and BaseURL:\n%s", readInject(t, dir))
			}

			// The author re-wires Logger by hand…
			composePath := filepath.Join(dir, "internal", "app", "compose.go")
			handWired := regexp.MustCompile(`Logger:\s+infra\.Log,`).ReplaceAllString(readInject(t, dir), "Logger: infra.AuditLog,")
			if !strings.Contains(handWired, "infra.AuditLog") {
				t.Fatalf("failed to hand-wire Logger:\n%s", readInject(t, dir))
			}
			if err := os.WriteFile(composePath, []byte(handWired), 0o644); err != nil {
				t.Fatalf("write hand-wired compose.go: %v", err)
			}
			// …takes the package out of the composition, and removes BaseURL.
			writeContract(t, dir, "internal/alpaca", tc.header+"package alpaca\n\ntype Service interface{ Do() }\n\n"+
				"type Deps struct {\n\tLogger *slog.Logger\n}\n\nfunc New(d Deps) (Service, error) { return nil, nil }\n")
			if !tc.discovered {
				in.Packages = nil
			}
			warnings.Reset()
			if err := GenerateCompose(in); err != nil {
				t.Fatalf("reconcile GenerateCompose: %v", err)
			}
			out := readInject(t, dir)
			if strings.Contains(flat(out), "BaseURL") {
				t.Fatalf("compose.go still keys the removed BaseURL field — the project cannot build:\n%s", out)
			}
			if !strings.Contains(flat(out), "Logger: infra.AuditLog,") {
				t.Fatalf("the hand-wired expression on the surviving key changed — the drop must touch keys only:\n%s", out)
			}
			if !strings.Contains(flat(out), "alpaca.New(alpaca.Deps{") {
				t.Fatalf("the construction is the author's code and must be kept, not deleted:\n%s", out)
			}
			if _, err := format.Source([]byte(out)); err != nil {
				t.Fatalf("reconciled compose.go is not gofmt-valid: %v\n----\n%s", err, out)
			}

			got := strings.TrimSpace(warnings.String())
			switch {
			case tc.warning == "" && got != "":
				t.Errorf("no marker takes the package out of the composition, so nothing to report; got:\n%s", got)
			case tc.warning != "" && got != "⚠️  "+tc.warning:
				t.Errorf("warning:\n got: %q\nwant: %q", got, "⚠️  "+tc.warning)
			}

			// Idempotent: the converged file is left as it is.
			if err := GenerateCompose(in); err != nil {
				t.Fatalf("idempotent GenerateCompose: %v", err)
			}
			if again := readInject(t, dir); again != out {
				t.Fatalf("reconcile is not idempotent:\n--- first ---\n%s\n--- second ---\n%s", out, again)
			}
		})
	}
}

// TestComposeDropStaleDepsKeys_ProjectPackagesOnly pins the boundary of the
// widened drop: an undiscovered literal is measured only when its alias
// resolves, through the file's own imports, to a package of THIS project
// whose Deps struct parses. A dependency's literal, or a package that does
// not parse, is never touched.
func TestComposeDropStaleDepsKeys_ProjectPackagesOnly(t *testing.T) {
	dir := newInjectProject(t)
	writeContract(t, dir, "internal/broken", "package broken\n\ntype Deps struct {\n\tLogger *slog.Logger\n")
	src := `package app

import (
	"example.com/other/vendorpkg"
	"example.com/proj/internal/broken"
)

func build() {
	_ = vendorpkg.Deps{Gone: 1}
	_ = broken.Deps{Gone: 1}
}
`
	got, changed := composeDropStaleDepsKeys(src, InjectGenData{Module: "example.com/proj", ProjectDir: dir})
	if changed || got != src {
		t.Fatalf("only a parsed Deps struct of a project package may drop keys; got:\n%s", got)
	}
}

// TestGenerateCompose_DropsTheLastDepsField is why the drop needs POSITIVE
// proof of the key set rather than a non-empty one. Emptying a Deps struct
// entirely is ordinary refactoring and leaves every key in the literal dead; a
// `len(keys) > 0` gate would have quietly refused to heal it. DepsFound is what
// separates "the struct has no fields" from "the package did not parse".
func TestGenerateCompose_DropsTheLastDepsField(t *testing.T) {
	dir := newInjectProject(t)
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config")
	writeComponentDeps(t, dir, "internal/handlers", "orders", "orders", "\tLogger *slog.Logger")
	in := InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "OrdersService", ModulePath: "example.com/proj"}},
	}
	if err := GenerateCompose(in); err != nil {
		t.Fatalf("first GenerateCompose: %v", err)
	}
	if !strings.Contains(readInject(t, dir), "Logger: infra.Log") {
		t.Fatalf("precondition: first emit must wire Logger:\n%s", readInject(t, dir))
	}

	writeComponentDeps(t, dir, "internal/handlers", "orders", "orders", "")
	if err := GenerateCompose(in); err != nil {
		t.Fatalf("reconcile GenerateCompose: %v", err)
	}
	out := readInject(t, dir)
	if strings.Contains(out, "Logger:") {
		t.Fatalf("emptying the Deps struct must empty the literal:\n%s", out)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "compose.go", out, parser.SkipObjectResolution); err != nil {
		t.Fatalf("reconciled compose.go is not valid Go: %v\n----\n%s", err, out)
	}
}

// TestGenerateCompose_DropSkipsUnparsableComponent is the guard that keeps the
// drop from becoming a licence to destroy. A component package mid-edit yields
// the SAME empty key set as a component with no deps, and acting on it would
// empty a perfectly good literal on the strength of source forge could not
// read. Nothing may be dropped without a Deps struct that positively parsed.
func TestGenerateCompose_DropSkipsUnparsableComponent(t *testing.T) {
	dir := newInjectProject(t)
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config\n\tDB Repository")
	writeFallibleComponentDeps(t, dir, "internal/handlers", "orders", "orders",
		"\tLogger *slog.Logger\n\tConfig *config.Config\n\tDB Repository")
	in := InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "OrdersService", ModulePath: "example.com/proj"}},
	}
	if err := GenerateCompose(in); err != nil {
		t.Fatalf("first GenerateCompose: %v", err)
	}
	before := readInject(t, dir)

	// Mid-edit: the component's only source file no longer parses.
	broken := "package orders\n\ntype Service interface{ Do() }\n\ntype Deps struct {\n\tLogger *slog.Logger\n"
	if err := os.WriteFile(filepath.Join(dir, "internal", "handlers", "orders", "contract.go"), []byte(broken), 0o644); err != nil {
		t.Fatalf("write broken contract: %v", err)
	}
	if err := GenerateCompose(in); err != nil {
		t.Fatalf("reconcile GenerateCompose: %v", err)
	}
	after := readInject(t, dir)
	for _, want := range []string{"Logger: infra.Log", "Config: infra.Cfg", "DB: infra.DB"} {
		if !strings.Contains(strings.Join(strings.Fields(after), " "), want) {
			t.Fatalf("unreadable component source must never authorize a drop (lost %q):\n--- before ---\n%s\n--- after ---\n%s", want, before, after)
		}
	}
}

// TestGenerateCompose_DropKeepsEmbeddedDepsKey: the key set forge measures
// against is the literal's FULL legal key set, not the subset forge knows how
// to wire. ParseServiceDeps drops embedded fields because it cannot address
// them — so measuring against it would delete an embedded key the author wired
// by hand, which is a field that exists.
func TestGenerateCompose_DropKeepsEmbeddedDepsKey(t *testing.T) {
	dir := newInjectProject(t)
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config")
	depsBody := "\tBase\n\tLogger *slog.Logger\n\tConfig *config.Config"
	writeFallibleComponentDeps(t, dir, "internal/handlers", "orders", "orders", depsBody)
	in := InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "OrdersService", ModulePath: "example.com/proj"}},
	}
	if err := GenerateCompose(in); err != nil {
		t.Fatalf("first GenerateCompose: %v", err)
	}
	// The author wires the embedded field by hand — forge never emits it.
	composePath := filepath.Join(dir, "internal", "app", "compose.go")
	raw := readInject(t, dir)
	withEmbedded := strings.Replace(raw, "orders.Deps{", "orders.Deps{\n\t\tBase: orders.Base{},", 1)
	if withEmbedded == raw {
		t.Fatalf("failed to hand-wire the embedded key:\n%s", raw)
	}
	if err := os.WriteFile(composePath, []byte(withEmbedded), 0o644); err != nil {
		t.Fatalf("write compose.go: %v", err)
	}

	if err := GenerateCompose(in); err != nil {
		t.Fatalf("reconcile GenerateCompose: %v", err)
	}
	if out := readInject(t, dir); !strings.Contains(out, "Base: orders.Base{}") {
		t.Fatalf("an embedded Deps key IS a field — it must survive the drop:\n%s", out)
	}
}

// TestGenerateCompose_RemovedDepsFieldStillCompiles is the same defect proven
// where it actually bites: `go build ./...` on a real module, not a string
// match. The pinning unit tests assert the dead key is gone; this one asserts
// the thing the user experiences — a project whose Deps struct lost a field
// still compiles after `forge generate`, with no `rm -f internal/app/compose.go`
// in between. The sandbox module has no external requires, so the build needs
// nothing from the network.
func TestGenerateCompose_RemovedDepsFieldStillCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks or builds fixture modules via the go toolchain; runs in task test")
	}
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("go.mod", "module example.com/proj\n\ngo 1.26.2\n")
	write("forge.yaml", "name: proj\nmodule_path: example.com/proj\n")
	write("internal/app/providers.go", `package app

import (
	"log/slog"

	"example.com/proj/internal/handlers/orders"
)

type Infra struct {
	Log *slog.Logger
	DB  orders.Store
}
`)
	ordersSrc := func(withDB bool) string {
		db := ""
		if withDB {
			db = "\n\tDB     Store"
		}
		return `package orders

import "log/slog"

type Store interface{ Get() }

type Service interface{ Do() }

type Deps struct {
	Logger *slog.Logger` + db + `
}

func New(d Deps) (Service, error) { return nil, nil }
`
	}
	write("internal/handlers/orders/contract.go", ordersSrc(true))

	in := InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "OrdersService", ModulePath: "example.com/proj"}},
	}
	goBuild := func(stage string) {
		t.Helper()
		cmd := exec.Command("go", "build", "./...")
		cmd.Dir = dir
		cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: go build failed: %v\n%s\n--- compose.go ---\n%s",
				stage, err, out, readInject(t, dir))
		}
	}

	if err := GenerateCompose(in); err != nil {
		t.Fatalf("first GenerateCompose: %v", err)
	}
	if !strings.Contains(strings.Join(strings.Fields(readInject(t, dir)), " "), "DB: infra.DB") {
		t.Fatalf("precondition: first emit must wire DB:\n%s", readInject(t, dir))
	}
	goBuild("before the refactor")

	// The author drops the DB dependency. Nothing else changes.
	write("internal/handlers/orders/contract.go", ordersSrc(false))
	if err := GenerateCompose(in); err != nil {
		t.Fatalf("reconcile GenerateCompose: %v", err)
	}
	goBuild("after removing the Deps field")
}

// writeStoreGen writes a minimal internal/db/store_gen.go — just the aggregate
// interface the resolver reads its accessor map from. The real file is emitted
// by the ORM generator; this is the shape that matters to composition.
func writeStoreGen(t *testing.T, projectDir string, entities ...[2]string) {
	t.Helper()
	writeStoreFiles(t, filepath.Join(projectDir, "internal", "db"), "db", entities...)
}

// writeStoreFiles writes the generator's store layout into dir: the
// aggregate store_gen.go, and one <entity>_store_gen.go per entity carrying
// the aggregate's accessor for it.
func writeStoreFiles(t *testing.T, dir, pkg string, entities ...[2]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "store_gen.go"), []byte("package "+pkg+"\n\ntype Store struct{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, e := range entities {
		src := "package " + pkg + "\n\nfunc (s Store) " + e[1] + "() " + e[0] + "Store { return nil }\n"
		if err := os.WriteFile(filepath.Join(dir, strings.ToLower(e[0])+"_store_gen.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A generated store must RESOLVE, not merely exist.
//
// Generating db.<Entity>Store without teaching composition to provide it is
// worse than not generating it: `forge generate` reports "has no provider" and
// instructs the author to add an Infra field and construct "your
// implementation" — which is the hand-written passthrough adapter the store
// exists to delete. Two measured forge-one-shot runs wrote 723 and 464 lines
// of exactly that layer.
//
// Every store is constructible from the ORM client Infra already owns, so
// there is nothing for the author to supply.
func TestGenerateInject_GeneratedStoreSeam(t *testing.T) {
	dir := newInjectProject(t)
	writeStoreGen(t, dir, [2]string{"Estimate", "Estimates"}, [2]string{"Job", "Jobs"})
	writeComponentDeps(t, dir, "internal/handlers", "orders", "orders",
		"\tEstimates db.EstimateStore\n\tAll db.Store")

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "OrdersService", ModulePath: "example.com/proj"}},
	})
	if err != nil {
		t.Fatalf("a generated store must resolve without hand-wiring; got: %v", err)
	}
	out := readInject(t, dir)
	for _, want := range []string{
		"Estimates: db.NewStore(infra.ORM).Estimates(),",
		"All: db.NewStore(infra.ORM),",
		`"example.com/proj/internal/db"`,
	} {
		if !containsNormalized(out, want) {
			t.Fatalf("compose.go missing %q:\n%s", want, out)
		}
	}
	assertGofmtFixedPoint(t, filepath.Join(dir, "internal", "app", "compose.go"))
}

// The accessor set comes from the GENERATED file, so a type that merely looks
// like a store in a project that has no such entity must NOT pick up a
// provider — emitting db.NewStore(...).Widgets() there would not compile.
// It falls through to the ordinary loud missing-provider path instead.
func TestGenerateInject_UnknownStoreTypeIsStillMissing(t *testing.T) {
	dir := newInjectProject(t)
	writeStoreGen(t, dir, [2]string{"Estimate", "Estimates"})
	writeComponentDeps(t, dir, "internal/handlers", "orders", "orders",
		"\tWidgets db.WidgetStore")

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "OrdersService", ModulePath: "example.com/proj"}},
	})
	if err == nil {
		t.Fatal("db.WidgetStore has no Widget entity behind it — resolving it would emit code that does not compile")
	}
}

// A service named like an identifier compose.go itself uses — its locals (`c`,
// `infra`, `err`), its imports (`fmt`, `slog`) — used to be imported under
// that same name, so `forge project new x --service c` failed its first
// generate: `c.New undefined (type *Components has no field or method New)`.
// Such a component is imported as `svc<Name>`; every other keeps its package
// name, so no compose.go that compiled before changes. Compiled for real: the
// fixture is its own module, and `go build` is the only judge of "collides".
func TestGenerateCompose_ServiceNamedLikeAGeneratedIdentifier(t *testing.T) {
	dir := newInjectProject(t)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/proj\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	colliding := []string{"c", "infra", "err", "fmt", "slog"}
	var services []ServiceDef
	for _, name := range append([]string{"orders"}, colliding...) {
		writeFallibleComponentDeps(t, dir, "internal/handlers", name, name, "")
		services = append(services, ServiceDef{Name: name, ModulePath: "example.com/proj"})
	}
	writeInfra(t, dir, "")
	if err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   services,
	}); err != nil {
		t.Fatalf("GenerateCompose: %v", err)
	}
	out := readInject(t, dir)
	for _, name := range colliding {
		alias := "svc" + strings.ToUpper(name[:1]) + name[1:]
		if !strings.Contains(out, alias+` "example.com/proj/internal/handlers/`+name+`"`) ||
			!strings.Contains(out, alias+".New("+alias+".Deps{") {
			t.Errorf("service %q must be imported and constructed as %s:\n%s", name, alias, out)
		}
	}
	if !strings.Contains(out, `orders "example.com/proj/internal/handlers/orders"`) {
		t.Errorf("a service whose name collides with nothing keeps its package name:\n%s", out)
	}
	if testing.Short() {
		return
	}
	cmd := exec.Command("go", "build", "./internal/...")
	cmd.Dir = dir
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod"))
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compose.go does not compile with services named %v: %v\n%s\n--- compose.go ---\n%s", colliding, err, b, out)
	}
}

// Every name compose.go's own template uses for something else — read off the
// template by appTemplateIdentifiers, so a local or import added later is
// swept here the day it is written — is given to a service, and in a second
// project to a worker. compose.go must compile for each. Names forge refuses
// for a component (keywords, predeclared identifiers, main) are skipped, as
// are names that are not their own package name ("Components" is package
// components, which nothing in the file names).
func TestGenerateCompose_EveryTemplateIdentifierAsAComponentName(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles two generated projects")
	}
	var names []string
	for name := range appTemplateIdentifiers() {
		if token.IsKeyword(name) || types.Universe.Lookup(name) != nil || name == "main" ||
			naming.ServicePackage(name) != name {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) < 10 {
		t.Fatalf("appTemplateIdentifiers yielded only %v — the sweep would prove nothing", names)
	}
	for _, role := range []string{"internal/handlers", "internal/workers"} {
		t.Run(role, func(t *testing.T) {
			dir := newInjectProject(t)
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/proj\n\ngo 1.22\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			writeInfra(t, dir, "")
			in := InjectGenInput{GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"}}
			for _, name := range names {
				writeFallibleComponentDeps(t, dir, role, name, name, "")
				if role == "internal/handlers" {
					in.Services = append(in.Services, ServiceDef{Name: name, ModulePath: "example.com/proj"})
				} else {
					in.Workers = append(in.Workers, BootstrapWorkerData{
						Name: name, Package: name, ImportPath: name, FieldName: naming.ToPascalCase(name), VarName: name, Fallible: true,
					})
				}
			}
			if err := GenerateCompose(in); err != nil {
				t.Fatalf("GenerateCompose: %v", err)
			}
			cmd := exec.Command("go", "build", "./internal/...")
			cmd.Dir = dir
			cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod"))
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("compose.go does not compile with components named %v: %v\n%s\n--- compose.go ---\n%s",
					names, err, b, readInject(t, dir))
			}
		})
	}
}

// The identifiers forge's internal/app templates use are read off the
// templates, so they include what compose.go and lifecycle.go actually write —
// and nothing a component's own package supplies.
func TestAppTemplateIdentifiers(t *testing.T) {
	got := appTemplateIdentifiers()
	for _, want := range []string{"c", "infra", "err", "fmt", "slog", "time", "ulid", "db", "context", "ctrl", "runtime", "serverkit", "lifecyclekit", "nil", "error"} {
		if !got[want] {
			t.Errorf("appTemplateIdentifiers lacks %q", want)
		}
	}
	for name := range got {
		if strings.HasPrefix(name, "zzforgeplaceholder") {
			t.Errorf("a placeholder component alias leaked into the template identifiers: %q", name)
		}
	}
	for _, field := range []string{"New", "Deps", "Log", "AddToScheme"} {
		if got[field] {
			t.Errorf("%q is a selector or field name, not a file-scope identifier", field)
		}
	}
}

// database.orm.package moves the store out of internal/db (reliant P5b,
// F12.2): the seam is spelled with the ORM package's own name, and a
// `db.Store` in that project is the project's own internal/db — which the
// seam must leave to the ordinary resolution paths.
func TestGenerateInject_StoreSeamFollowsTheORMPackage(t *testing.T) {
	dir := newInjectProject(t)
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"),
		[]byte("name: proj\nmodule_path: example.com/proj\ndatabase:\n  orm:\n    package: internal/store\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	storeDir := filepath.Join(dir, "internal", "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStoreFiles(t, storeDir, "store", [2]string{"Estimate", "Estimates"})
	writeComponentDeps(t, dir, "internal/handlers", "orders", "orders",
		"\tEstimates store.EstimateStore\n\tAll store.Store")

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services:   []ServiceDef{{Name: "OrdersService", ModulePath: "example.com/proj"}},
	})
	if err != nil {
		t.Fatalf("a relocated generated store must resolve; got: %v", err)
	}
	out := readInject(t, dir)
	for _, want := range []string{
		"Estimates: store.NewStore(infra.ORM).Estimates(),",
		"All: store.NewStore(infra.ORM),",
		`"example.com/proj/internal/store"`,
	} {
		if !containsNormalized(out, want) {
			t.Fatalf("compose.go missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"example.com/proj/internal/db"`) {
		t.Errorf("compose.go imports internal/db for a store that lives in internal/store:\n%s", out)
	}

	// The project's own internal/db is not the ORM any more: its Store is
	// not the seam's to provide.
	if (storeSeam{pkg: "store", dir: "internal/store"}).owns("db.Store") {
		t.Error("the store seam claimed db.Store although the ORM package is internal/store")
	}
}
