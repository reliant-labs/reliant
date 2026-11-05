package codegen

import (
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/templates"
)

func TestGenerateServiceStub_ZeroMethods_NoUnusedImports(t *testing.T) {
	// Create a temp directory to act as the target
	targetDir := filepath.Join(t.TempDir(), "ordersservice")

	svc := ServiceDef{
		Name:       "OrdersService",
		Package:    "orders.v1",
		GoPackage:  "github.com/test/proj/gen/proto/services/orders/v1",
		PkgName:    "ordersv1",
		Methods:    nil, // zero RPCs
		ProtoFile:  "proto/services/orders/v1/orders.proto",
		ModulePath: "github.com/test/proj",
	}

	if err := GenerateServiceStub(svc, targetDir); err != nil {
		t.Fatalf("GenerateServiceStub() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(targetDir, "service.go"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	content := string(data)

	// Must NOT contain "context" import (unused when no methods)
	if strings.Contains(content, `"context"`) {
		t.Errorf("generated stub with zero methods should not import \"context\", got:\n%s", content)
	}

	// The embedded template always imports pb for the keep-alive reference
	if !strings.Contains(content, `pb "`) {
		t.Error("generated stub should import pb (used for keep-alive reference)")
	}

	// Must still contain the connect package (used by struct embedding + Register)
	if !strings.Contains(content, `"connectrpc.com/connect"`) {
		t.Error("generated stub should still import connectrpc.com/connect")
	}

	// Must still contain the service connect package
	if !strings.Contains(content, `ordersv1connect`) {
		t.Error("generated stub should still import the service connect package")
	}

	// Must contain Deps struct with Logger
	if !strings.Contains(content, `type Deps struct`) {
		t.Error("generated stub should contain Deps struct")
	}
	if !strings.Contains(content, "Logger") || !strings.Contains(content, "*slog.Logger") {
		t.Error("generated stub should contain Logger field of type *slog.Logger in Deps")
	}

	// Must contain new fallible New() signature accepting Deps. The
	// service constructor is now always (*Service, error) so per-RPC
	// nil-check guards are unnecessary — bare-Deps validation runs once
	// at construction time inside validateDeps().
	if !strings.Contains(content, `func New(deps Deps) (*Service, error)`) {
		t.Error("generated stub should have New(deps Deps) (*Service, error) signature")
	}
	if !strings.Contains(content, `validateDeps`) {
		t.Error("generated stub should declare a validateDeps() helper used by New")
	}
	// (2026-05-07 wire-gen migration) ApplyDeps is gone; validateDeps
	// runs at New() time and the codegen'd wire_gen.go assembles the
	// full Deps before the call. No mutation method should remain.
	if strings.Contains(content, "func (s *Service) ApplyDeps(") {
		t.Error("generated stub should not define ApplyDeps anymore — wire_gen feeds full Deps into New()")
	}

	// Must NOT contain init() or registry.Register
	if strings.Contains(content, `func init()`) {
		t.Error("generated stub should not contain init() function")
	}
	if strings.Contains(content, `registry.Register`) {
		t.Error("generated stub should not contain registry.Register call")
	}
}

func TestGenerateServiceStub_WithMethods_IncludesImports(t *testing.T) {
	targetDir := filepath.Join(t.TempDir(), "echoservice")

	svc := ServiceDef{
		Name:      "EchoService",
		Package:   "echo.v1",
		GoPackage: "github.com/test/proj/gen/proto/services/echo/v1",
		PkgName:   "echov1",
		Methods: []Method{
			{Name: "Echo", InputType: "EchoRequest", OutputType: "EchoResponse"},
		},
		ProtoFile:  "proto/services/echo/v1/echo.proto",
		ModulePath: "github.com/test/proj",
	}

	if err := GenerateServiceStub(svc, targetDir); err != nil {
		t.Fatalf("GenerateServiceStub() error = %v", err)
	}

	serviceData, err := os.ReadFile(filepath.Join(targetDir, "service.go"))
	if err != nil {
		t.Fatalf("ReadFile(service.go) error = %v", err)
	}

	serviceContent := string(serviceData)

	if !strings.Contains(serviceContent, `pb "`) {
		t.Error("service.go should import pb")
	}

	// The Echo RPC gets its own file, complete with its own imports.
	stub := RPCHandlerFileName("Echo")
	handlersData, err := os.ReadFile(filepath.Join(targetDir, stub))
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", stub, err)
	}

	handlersContent := string(handlersData)

	if !strings.Contains(handlersContent, `"context"`) {
		t.Errorf("%s should import \"context\"", stub)
	}

	if !strings.Contains(handlersContent, `func (s *Service) Echo(`) {
		t.Errorf("%s should contain the Echo RPC stub", stub)
	}
}

func TestGenerateMock_ZeroMethods_Skipped(t *testing.T) {
	mockDir := filepath.Join(t.TempDir(), "mocks")

	svc := ServiceDef{
		Name:       "OrdersService",
		Package:    "orders.v1",
		GoPackage:  "github.com/test/proj/gen/proto/services/orders/v1",
		PkgName:    "ordersv1",
		Methods:    nil,
		ProtoFile:  "proto/services/orders/v1/orders.proto",
		ModulePath: "github.com/test/proj",
	}

	written, err := GenerateMock(svc, "", mockDir, nil)
	if err != nil {
		t.Fatalf("GenerateMock() error = %v", err)
	}
	if written.Wrote || written.Skipped != "no RPCs" {
		t.Errorf("expected a no-RPCs skip for zero-RPC service, got %+v", written)
	}

	mockFile := filepath.Join(mockDir, "orders_mock_gen.go")
	if _, err := os.Stat(mockFile); !os.IsNotExist(err) {
		t.Errorf("expected no mock file for zero-RPC service, but %s exists", mockFile)
	}
}

// TestGenerateMock_CrossPackageMessageImports covers the proto-split shape
// where a thin service-surface proto file reuses request/response messages
// from an IMPORTED proto file that generates into a DIFFERENT Go package.
// The descriptor records the foreign provenance on each method via
// Input/OutputProtoFile; the mock generator must import that foreign package
// under its own alias and qualify the type with it instead of the service's
// own `pb`. Before the fix every type was hardcoded to `pb.`, producing
// `undefined: pb.X` build failures for every cross-package reference.
func TestGenerateMock_Tier1WriterRecordsTarget(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "forge.yaml"), []byte("name: proj\n"), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	checksums.ResetSkipWrite()
	t.Cleanup(checksums.ResetSkipWrite)

	svc := ServiceDef{
		Name:       "OrdersService",
		Package:    "orders.v1",
		GoPackage:  "github.com/test/proj/gen/proto/services/orders/v1",
		PkgName:    "ordersv1",
		ProtoFile:  "proto/services/orders/v1/orders.proto",
		ModulePath: "github.com/test/proj",
		Methods: []Method{{
			Name:       "CreateOrder",
			InputType:  "CreateOrderRequest",
			OutputType: "CreateOrderResponse",
		}},
	}

	written, err := GenerateMock(svc, projectDir, filepath.Join(projectDir, "internal", "handlers", "mocks"), nil)
	if err != nil {
		t.Fatalf("GenerateMock() error = %v", err)
	}
	if !written.Wrote || !written.Changed {
		t.Fatal("expected mock file to be written")
	}

	const rel = "internal/handlers/mocks/orders_mock_gen.go"
	if !checksums.IsTier1Target(rel) {
		t.Fatalf("handler mock was not recorded as a Tier-1 target; targets=%v", checksums.Tier1Targets())
	}
	b, err := os.ReadFile(filepath.Join(projectDir, rel))
	if err != nil {
		t.Fatalf("read mock: %v", err)
	}
	if got := checksums.Verify(b); got != checksums.Pristine {
		t.Fatalf("generated handler mock is not self-certified (Verify=%v):\n%s", got, string(b))
	}
}

func TestGenerateMock_CrossPackageMessageImports(t *testing.T) {
	mockDir := filepath.Join(t.TempDir(), "mocks")

	const module = "github.com/test/proj"
	svc := ServiceDef{
		Name:       "DaemonTokenService",
		Package:    "reliant.v1",
		GoPackage:  module + "/gen/services/daemon_token/v1",
		PkgName:    "daemon_tokenv1",
		ProtoFile:  "services/daemon_token/v1/daemon_token.proto",
		ModulePath: module,
		Methods: []Method{
			{
				// Request/response come from an IMPORTED proto file →
				// gen/reliant/v1, NOT the service's own package.
				Name:            "CreateDaemonToken",
				InputType:       "CreateDaemonTokenRequest",
				OutputType:      "CreateDaemonTokenResponse",
				InputProtoFile:  "reliant/v1/daemon_registry.proto",
				OutputProtoFile: "reliant/v1/daemon_registry.proto",
			},
			{
				// Declared in the service's own proto file → stays `pb`.
				Name:            "MintManagedDaemonToken",
				InputType:       "MintManagedDaemonTokenRequest",
				OutputType:      "MintManagedDaemonTokenResponse",
				InputProtoFile:  "services/daemon_token/v1/daemon_token.proto",
				OutputProtoFile: "services/daemon_token/v1/daemon_token.proto",
			},
		},
	}

	written, err := GenerateMock(svc, "", mockDir, nil)
	if err != nil {
		t.Fatalf("GenerateMock() error = %v", err)
	}
	if !written.Wrote || !written.Changed {
		t.Fatal("expected mock file to be written")
	}

	b, err := os.ReadFile(filepath.Join(mockDir, "daemon_token_mock_gen.go"))
	if err != nil {
		t.Fatalf("read mock: %v", err)
	}
	got := string(b)

	// The foreign package must be imported under a distinct alias...
	if !strings.Contains(got, `reliantv1 "`+module+`/gen/reliant/v1"`) {
		t.Errorf("expected aliased foreign import for gen/reliant/v1, got:\n%s", got)
	}
	// ...and the cross-package message must be qualified with that alias,
	// never with the service's own `pb`.
	if !strings.Contains(got, "reliantv1.CreateDaemonTokenRequest") {
		t.Errorf("expected reliantv1.CreateDaemonTokenRequest, got:\n%s", got)
	}
	if strings.Contains(got, "pb.CreateDaemonTokenRequest") {
		t.Errorf("cross-package message must NOT be qualified with pb., got:\n%s", got)
	}
	// Same-file messages stay on `pb`.
	if !strings.Contains(got, "pb.MintManagedDaemonTokenRequest") {
		t.Errorf("expected same-file message to stay pb.MintManagedDaemonTokenRequest, got:\n%s", got)
	}

	// The emitted file must be valid, gofmt-able Go (catches malformed
	// import blocks / dangling aliases).
	if _, err := format.Source(b); err != nil {
		t.Errorf("generated mock is not valid Go: %v\n%s", err, got)
	}
}

// TestGenerateMock_UnprogrammedRPCReturnsSvcerr pins the unprogrammed-rpc
// error: svcerr.Unimplemented, wrapped so it is a *connect.Error as well, and
// never a hand-built connect.NewError — which forgeconv-handler-connect-error
// flags, and which a project with hundreds of mocks would otherwise carry as
// that many generated findings. The message is the one the connect.NewError
// spelling produced, so a test pinning it keeps passing.
func TestGenerateMock_UnprogrammedRPCReturnsSvcerr(t *testing.T) {
	mockDir := filepath.Join(t.TempDir(), "mocks")
	svc := ServiceDef{
		Name:       "OrdersService",
		Package:    "orders.v1",
		GoPackage:  "github.com/test/proj/gen/proto/services/orders/v1",
		PkgName:    "ordersv1",
		ProtoFile:  "proto/services/orders/v1/orders.proto",
		ModulePath: "github.com/test/proj",
		Methods: []Method{
			{Name: "CreateOrder", InputType: "CreateOrderRequest", OutputType: "CreateOrderResponse"},
			{Name: "TailOrders", InputType: "TailOrdersRequest", OutputType: "TailOrdersResponse", ServerStreaming: true},
		},
	}
	if _, err := GenerateMock(svc, "", mockDir, nil); err != nil {
		t.Fatalf("GenerateMock() error = %v", err)
	}
	b, err := os.ReadFile(filepath.Join(mockDir, "orders_mock_gen.go"))
	if err != nil {
		t.Fatalf("read mock: %v", err)
	}
	got := string(b)
	for _, want := range []string{
		`"github.com/reliant-labs/forge/pkg/svcerr"`,
		`return nil, svcerr.Wrap(svcerr.Unimplemented("handler for CreateOrder not yet implemented"))`,
		`return svcerr.Wrap(svcerr.Unimplemented("handler for TailOrders not yet implemented"))`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("mock missing %s:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"connect.NewError", `"fmt"`} {
		if strings.Contains(got, unwanted) {
			t.Errorf("mock must not contain %s:\n%s", unwanted, got)
		}
	}
	if _, err := format.Source(b); err != nil {
		t.Errorf("generated mock is not valid Go: %v\n%s", err, got)
	}
}

// TestInspectComponentDepsShape_DomainLocalConfig asserts that a
// package whose Deps.Config field is typed as a package-local Config
// (e.g. `enforcement.Config`) does NOT get HasConfig=true, because the
// bootstrap template would otherwise emit `Config: cfg` — where cfg is
// the project's `*config.Config` — and the codegen would fail to
// compile with "cannot use *config.Config as enforcement.Config".
//
// FRICTION 2026-06-02 (cp-forge layer-2 enforcement): the well-known
// name shortcut for "Config" bypassed type-matching entirely, forcing
// every package declaring its own Config struct to rename the field
// (Caps, EnforcementCaps, ...).
func TestInspectComponentDepsShape_DomainLocalConfig(t *testing.T) {
	projectDir := t.TempDir()

	// pkg/app/app_extras.go — empty AppExtras (no Config field there).
	appDir := filepath.Join(projectDir, "pkg", "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "app_extras.go"),
		[]byte("package app\n\ntype AppExtras struct{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// internal/enforcement/contract.go — Deps with a domain-local Config.
	pkgDir := filepath.Join(projectDir, "internal", "enforcement")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := `package enforcement

import "log/slog"

type Config struct{}

type Deps struct {
	Logger *slog.Logger
	Config Config
}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "contract.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	components := []BootstrapComponentData{
		{Name: "enforcement", Package: "enforcement", ImportPath: "enforcement"},
	}
	inspectComponentDepsShape(components, projectDir, "internal")

	if !components[0].HasLogger {
		t.Error("HasLogger should be true (Logger is *slog.Logger)")
	}
	if components[0].HasConfig {
		t.Error("HasConfig must be false when Deps.Config is a domain-local type (not *config.Config); bootstrap template would emit `Config: cfg` and fail to compile")
	}
}

// TestInspectComponentDepsShape_ProjectConfig asserts the canonical
// case still works: Deps.Config typed as *config.Config gets
// HasConfig=true so bootstrap emits `Config: cfg`.
func TestInspectComponentDepsShape_ProjectConfig(t *testing.T) {
	projectDir := t.TempDir()

	appDir := filepath.Join(projectDir, "pkg", "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "app_extras.go"),
		[]byte("package app\n\ntype AppExtras struct{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pkgDir := filepath.Join(projectDir, "internal", "cache")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := `package cache

import (
	"log/slog"
	"example.com/proj/pkg/config"
)

type Deps struct {
	Logger *slog.Logger
	Config *config.Config
}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "contract.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	components := []BootstrapComponentData{
		{Name: "cache", Package: "cache", ImportPath: "cache"},
	}
	inspectComponentDepsShape(components, projectDir, "internal")

	if !components[0].HasLogger {
		t.Error("HasLogger should be true for *slog.Logger")
	}
	if !components[0].HasConfig {
		t.Error("HasConfig should be true for *config.Config")
	}
}

// canonicalAliasFixture writes a minimal project shape for the
// CanonicalAppField tests: pkg/app/app_extras.go with the given source,
// internal/<pkg>/contract.go with the given source, and a go.mod naming
// the module so import-path resolution is exact.
func canonicalAliasFixture(t *testing.T, appExtrasSrc, pkgName, contractSrc string) string {
	t.Helper()
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "go.mod"),
		[]byte("module example.com/proj\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	appDir := filepath.Join(projectDir, "pkg", "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "app_extras.go"), []byte(appExtrasSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	pkgDir := filepath.Join(projectDir, "internal", pkgName)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "contract.go"), []byte(contractSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	return projectDir
}

// TestInspectComponentDepsShape_CanonicalServiceAlias mirrors the
// cp-forge svcdaemon shape: the package's Deps declares collaborator
// fields that have NO name match on App/AppExtras (DaemonRepo,
// URLBuilder — they are constructed inline in user-owned setup.go), so
// bootstrap's auto-wire cannot express the construction and the
// generated `daemon.New(Deps{...})` panics at boot ("Deps.DaemonRepo
// is required"). AppExtras DOES hold the canonical, fully-wired
// instance (`DaemonService daemon.Service`, assigned in Setup, which
// appkit runs before the package table). Expect CanonicalAppField to
// name it so the template emits an alias instead of a second,
// half-built instance.
func TestInspectComponentDepsShape_CanonicalServiceAlias(t *testing.T) {
	projectDir := canonicalAliasFixture(t, `package app

import daemon "example.com/proj/internal/daemon"

type AppExtras struct {
	Conn          string
	DaemonService daemon.Service
}
`, "daemon", `package daemon

import "log/slog"

type Repository interface{ Get() string }

type Service interface{ Do() }

type Deps struct {
	Logger     *slog.Logger
	Conn       string
	DaemonRepo Repository
}

func New(deps Deps) Service { return nil }
`)

	components := []BootstrapComponentData{
		{Name: "daemon", Package: "daemon", ImportPath: "daemon", FieldName: "Daemon", Alias: "daemon"},
	}
	inspectComponentDepsShape(components, projectDir, "internal")

	if got := components[0].CanonicalAppField; got != "DaemonService" {
		t.Errorf("CanonicalAppField = %q, want %q (Deps.DaemonRepo is unwireable and AppExtras.DaemonService holds the canonical instance)", got, "DaemonService")
	}
	// The name+type matches must still be recorded — testing.go and the
	// non-alias fallback paths read them.
	if !components[0].HasLogger {
		t.Error("HasLogger should still be true")
	}
}

// TestInspectComponentDepsShape_CanonicalAlias_FullyWiredStaysConstructed
// mirrors cp-forge's enforcement/Checker shape: every Deps field
// auto-wires by name+type, so even though AppExtras holds a field of
// the package's Service type, bootstrap keeps constructing its own
// instance — the alias mechanism is strictly for unexpressible wirings.
func TestInspectComponentDepsShape_CanonicalAlias_FullyWiredStaysConstructed(t *testing.T) {
	projectDir := canonicalAliasFixture(t, `package app

import enforcement "example.com/proj/internal/enforcement"

type AppExtras struct {
	EnforcementRepo string
	Checker         enforcement.Service
}
`, "enforcement", `package enforcement

import "log/slog"

type Service interface{ Check() }

type Deps struct {
	Logger          *slog.Logger
	EnforcementRepo string
}

func New(deps Deps) Service { return nil }
`)

	components := []BootstrapComponentData{
		{Name: "enforcement", Package: "enforcement", ImportPath: "enforcement", FieldName: "Enforcement", Alias: "enforcement"},
	}
	inspectComponentDepsShape(components, projectDir, "internal")

	if got := components[0].CanonicalAppField; got != "" {
		t.Errorf("CanonicalAppField = %q, want \"\" (all Deps fields auto-wire; no alias)", got)
	}
	if len(components[0].AppFieldRefs) != 1 || components[0].AppFieldRefs[0].DepsField != "EnforcementRepo" {
		t.Errorf("AppFieldRefs = %+v, want exactly [EnforcementRepo]", components[0].AppFieldRefs)
	}
}

// TestInspectComponentDepsShape_CanonicalAlias_ScalarOnlyGapStaysConstructed
// mirrors cp-forge's billing/APIKey shape: the only unwired Deps fields
// are configuration scalars (string / []byte / numeric). Scalars are
// never auto-wired and their zero value is the package's documented
// degraded mode — not the panic/no-op collaborator class — so the
// package keeps constructing even when a canonical instance exists.
func TestInspectComponentDepsShape_CanonicalAlias_ScalarOnlyGapStaysConstructed(t *testing.T) {
	projectDir := canonicalAliasFixture(t, `package app

import billing "example.com/proj/internal/billing"

type AppExtras struct {
	Stripe billing.Service
}
`, "billing", `package billing

type Service interface{ Charge() }

type Deps struct {
	APIKey    string
	PlansData []byte
}

func New(deps Deps) Service { return nil }
`)

	components := []BootstrapComponentData{
		{Name: "billing", Package: "billing", ImportPath: "billing", FieldName: "Billing", Alias: "billing"},
	}
	inspectComponentDepsShape(components, projectDir, "internal")

	if got := components[0].CanonicalAppField; got != "" {
		t.Errorf("CanonicalAppField = %q, want \"\" (only config scalars are unwired)", got)
	}
}

// TestInspectComponentDepsShape_CanonicalAlias_AliasedImport mirrors
// cp-forge's internal/user shape: app_extras.go imports the package
// under a renamed qualifier (internaluser "…/internal/user") so a
// package-name string compare would miss the field. The resolver must
// follow the file's import table.
func TestInspectComponentDepsShape_CanonicalAlias_AliasedImport(t *testing.T) {
	projectDir := canonicalAliasFixture(t, `package app

import internaluser "example.com/proj/internal/user"

type AppExtras struct {
	UserService internaluser.Service
}
`, "user", `package user

type AuditLogger interface{ Log() }

type Service interface{ Get() }

type Deps struct {
	Audit AuditLogger
}

func New(deps Deps) Service { return nil }
`)

	components := []BootstrapComponentData{
		{Name: "user", Package: "user", ImportPath: "user", FieldName: "PkgUser", Alias: "pkgUser"},
	}
	inspectComponentDepsShape(components, projectDir, "internal")

	if got := components[0].CanonicalAppField; got != "UserService" {
		t.Errorf("CanonicalAppField = %q, want %q (aliased import must resolve)", got, "UserService")
	}
}

// TestInspectComponentDepsShape_CanonicalAlias_AmbiguousSkips: two
// App/AppExtras fields of the package's Service type — there is no
// deterministic canonical instance, so fall back to construction (the
// deps-coverage lint surfaces the unwired field).
func TestInspectComponentDepsShape_CanonicalAlias_AmbiguousSkips(t *testing.T) {
	projectDir := canonicalAliasFixture(t, `package app

import daemon "example.com/proj/internal/daemon"

type AppExtras struct {
	DaemonService daemon.Service
	DaemonShadow  daemon.Service
}
`, "daemon", `package daemon

type Repository interface{ Get() string }

type Service interface{ Do() }

type Deps struct {
	DaemonRepo Repository
}

func New(deps Deps) Service { return nil }
`)

	components := []BootstrapComponentData{
		{Name: "daemon", Package: "daemon", ImportPath: "daemon", FieldName: "Daemon", Alias: "daemon"},
	}
	inspectComponentDepsShape(components, projectDir, "internal")

	if got := components[0].CanonicalAppField; got != "" {
		t.Errorf("CanonicalAppField = %q, want \"\" (two candidate fields is ambiguous)", got)
	}
}

// TestInspectComponentDepsShape_CanonicalAlias_OptionalDepDoesNotTrigger:
// a collaborator explicitly marked `// forge:optional-dep` is designed
// to be nil at construction — it must not count toward the
// "construction is unexpressible" trigger.
func TestInspectComponentDepsShape_CanonicalAlias_OptionalDepDoesNotTrigger(t *testing.T) {
	projectDir := canonicalAliasFixture(t, `package app

import notifier "example.com/proj/internal/notifier"

type AppExtras struct {
	NotifierService notifier.Service
}
`, "notifier", `package notifier

type Sink interface{ Send() }

type Service interface{ Notify() }

type Deps struct {
	// forge:optional-dep
	Sink Sink
}

func New(deps Deps) Service { return nil }
`)

	components := []BootstrapComponentData{
		{Name: "notifier", Package: "notifier", ImportPath: "notifier", FieldName: "Notifier", Alias: "notifier"},
	}
	inspectComponentDepsShape(components, projectDir, "internal")

	if got := components[0].CanonicalAppField; got != "" {
		t.Errorf("CanonicalAppField = %q, want \"\" (only optional-marked deps are unwired)", got)
	}
}

func TestGenerateBootstrapTesting_MultipleServices(t *testing.T) {
	targetDir := t.TempDir()

	// Scaffold real handler dirs with a minimal Deps shape (Logger only) —
	// the test harness renders one factory per discovered service.
	svcSrc := func(pkg string) string {
		return `package ` + pkg + `

import "log/slog"

type Deps struct {
	Logger *slog.Logger
}

type Service struct{ deps Deps }

func New(deps Deps) (*Service, error) { return &Service{deps: deps}, nil }
`
	}
	writeFileT(t, filepath.Join(targetDir, "internal", "handlers", "api", "service.go"), svcSrc("api"))
	writeFileT(t, filepath.Join(targetDir, "internal", "handlers", "orders", "service.go"), svcSrc("orders"))

	services := []ServiceDef{
		{Name: "APIService", ModulePath: "example.com/proj"},
		{Name: "OrdersService", ModulePath: "example.com/proj"},
	}

	if err := GenerateBootstrapTesting(BootstrapTestingGenInput{
		GenContext: GenContext{ProjectDir: targetDir, ModulePath: "example.com/proj", Checksums: nil},
		Services:   services,
		Packages:   nil,
		Workers:    nil,
		Operators:  nil,
	}); err != nil {
		t.Fatalf("GenerateBootstrapTesting() error = %v", err)
	}

	// One helper file per service, each in that service's OWN package and
	// directory. The per-service option TYPES the aggregate file needed
	// (APITestOption vs OrdersTestOption) are gone: they existed only to make
	// passing service A's option to service B's factory a compile error, and
	// separate files in separate packages make that mistake unrepresentable.
	apiBody, err := os.ReadFile(filepath.Join(targetDir, ComponentTestHelperRelPath("api")))
	if err != nil {
		t.Fatalf("ReadFile(api helpers) error = %v", err)
	}
	ordersBody, err := os.ReadFile(filepath.Join(targetDir, ComponentTestHelperRelPath("orders")))
	if err != nil {
		t.Fatalf("ReadFile(orders helpers) error = %v", err)
	}
	apiContent, ordersContent := string(apiBody), string(ordersBody)

	// The `_test.go` suffix is the whole point of the split: it keeps package
	// `testing` out of the production binary's dependency graph.
	for _, rel := range []string{ComponentTestHelperRelPath("api"), ComponentTestHelperRelPath("orders")} {
		if !strings.HasSuffix(rel, "_test.go") {
			t.Errorf("%s must end in _test.go or `testing` leaks into cmd/", rel)
		}
	}

	for _, tc := range []struct {
		name    string
		content string
		wants   []string
	}{
		{
			name:    "api",
			content: apiContent,
			wants: []string{
				"Code generated by forge. DO NOT EDIT.",
				// package CLAUSE is the service's own, NOT api_test: Go
				// compiles in-package _test.go files INTO the package, so
				// both api and api_test files can use these helpers.
				"package api\n",
				"type TestOption func(*testConfig)",
				"func WithAPIDeps(deps Deps) TestOption",
				"func NewTestAPI(t *testing.T, opts ...TestOption) *Service",
				"func NewTestAPIServer(t *testing.T, opts ...TestOption)",
				`"example.com/proj/gen/services/api/v1/apiv1connect"`,
				"apiv1connect.APIServiceClient",
				"testkit.DiscardLogger()",
				`"github.com/reliant-labs/forge/pkg/testkit"`,
				// AuthedContext: claims-bearing ctx via the project's own
				// middleware.ContextWithClaims setter.
				"func AuthedContext(t *testing.T, opts ...testkit.ClaimsOption) context.Context",
				"testkit.AuthedContext(t, middleware.ContextWithClaims, opts...)",
				// The server factory installs the principal SERVER-side: a
				// client-side AuthedContext never crosses HTTP, so without
				// this every auth-gated RPC called through the typed client
				// answers Unauthenticated before reaching the handler.
				"testkit.ServerPrincipal(middleware.ContextWithClaims, cfg.principal...)",
				"func WithPrincipal(opts ...testkit.ClaimsOption) TestOption",
				"func WithoutPrincipal() TestOption",
			},
		},
		{
			name:    "orders",
			content: ordersContent,
			wants: []string{
				"package orders\n",
				"func WithOrdersDeps(deps Deps) TestOption",
				"func NewTestOrders(t *testing.T, opts ...TestOption) *Service",
				"func NewTestOrdersServer(t *testing.T, opts ...TestOption)",
				`"example.com/proj/gen/services/orders/v1/ordersv1connect"`,
				"ordersv1connect.OrdersServiceClient",
			},
		},
	} {
		for _, want := range tc.wants {
			if !strings.Contains(tc.content, want) {
				t.Errorf("%s helpers_gen_test.go missing %q", tc.name, want)
			}
		}
	}

	// Each file is scoped to ITS OWN service: api's helpers must not mention
	// orders' factory, and vice versa. This is the property the per-service
	// option interfaces used to buy at the cost of an interface per service.
	if strings.Contains(apiContent, "NewTestOrders") {
		t.Error("api helpers must not declare the orders factory — one file per service")
	}
	if strings.Contains(ordersContent, "NewTestAPI") {
		t.Error("orders helpers must not declare the api factory — one file per service")
	}

	// The helpers live INSIDE the handler package now, so they must NOT
	// import it (that would be a self-import).
	if strings.Contains(apiContent, `"example.com/proj/internal/handlers/api"`) {
		t.Error("api helpers must not import their own package")
	}
}

func TestGenerateBootstrapTesting_WithPackages(t *testing.T) {
	targetDir := t.TempDir()

	services := []ServiceDef{
		{Name: "APIService", ModulePath: "example.com/proj"},
	}

	packages := []BootstrapPackageData{
		{Name: "cache", Package: "cache", ImportPath: "cache", FieldName: "Cache", VarName: "cache"},
	}

	// The helper file lands IN the package's own directory, so that
	// directory has to exist. (A component declared in forge.yaml but not
	// yet on disk is skipped rather than conjuring a stray dir.)
	cacheDir := filepath.Join(targetDir, "internal", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	cacheSrc := "package cache\n\ntype Deps struct{}\n\ntype Service interface{ Get() string }\n\nfunc New(d Deps) Service { return nil }\n"
	if err := os.WriteFile(filepath.Join(cacheDir, "contract.go"), []byte(cacheSrc), 0o644); err != nil {
		t.Fatalf("write cache contract: %v", err)
	}

	if err := GenerateBootstrapTesting(BootstrapTestingGenInput{
		GenContext: GenContext{ProjectDir: targetDir, ModulePath: "example.com/proj", Checksums: nil},
		Services:   services,
		Packages:   packages,
		Workers:    nil,
		Operators:  nil,
	}); err != nil {
		t.Fatalf("GenerateBootstrapTesting() error = %v", err)
	}

	// An internal package gets its helper file in ITS OWN directory
	// (internal/cache), in its own package clause — so the package's types
	// are unqualified and nothing imports internal/cache to reach them.
	data, err := os.ReadFile(filepath.Join(targetDir, "internal", "cache", componentTestHelperFile))
	if err != nil {
		t.Fatalf("ReadFile(cache helpers) error = %v", err)
	}

	content := string(data)

	if !strings.Contains(content, "package cache\n") {
		t.Error("cache helpers should be in `package cache`")
	}
	// Living inside the package means NOT importing it.
	if strings.Contains(content, `"example.com/proj/internal/cache"`) {
		t.Error("cache helpers must not import their own package")
	}

	// Deps/Service are unqualified now that the file is in-package. The
	// package-scoped option TYPE is gone — one file per component makes a
	// cross-component option mixup unrepresentable rather than a compile
	// error (review DI#8's concern, solved structurally).
	if !strings.Contains(content, `func WithCacheDeps(deps Deps) TestOption`) {
		t.Error("cache helpers should contain WithCacheDeps taking the local Deps")
	}
	if !strings.Contains(content, `func NewTestCache(t *testing.T, opts ...TestOption) Service`) {
		t.Error("cache helpers should contain NewTestCache returning the local Service")
	}
}

// TestGenerateBootstrapTesting_ExternalComponentPackageExcluded is the
// FIX #3 regression: an `//forge:external-component` domain package that
// shares its package clause with a handler service (domain
// internal/billing + handler internal/handlers/billing, both `package
// billing`) must NOT get a NewTest<Pkg> factory, must NOT drive a
// Svc-prefix rename on the HANDLER service's factory, and must NOT
// duplicate-declare. testing.go must emit the plain NewTestBilling for the
// handler service and import the domain billing only if a stub needs it.
func TestGenerateBootstrapTesting_ExternalComponentPackageExcluded(t *testing.T) {
	targetDir := t.TempDir()

	// Handler service billing on disk (package billing).
	handlerDir := filepath.Join(targetDir, "internal", "handlers", "billing")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatalf("mkdir handler: %v", err)
	}
	handlerSrc := "package billing\n\ntype Deps struct{}\n\ntype Service struct{}\n\nfunc New(d Deps) (*Service, error) { return &Service{}, nil }\n"
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte(handlerSrc), 0o644); err != nil {
		t.Fatalf("write handler: %v", err)
	}

	// External-component domain billing on disk (package billing) — has a
	// contract.go so discoverPackages would pick it up, marked external.
	domainDir := filepath.Join(targetDir, "internal", "billing")
	if err := os.MkdirAll(domainDir, 0o755); err != nil {
		t.Fatalf("mkdir domain: %v", err)
	}
	domainSrc := "//forge:external-component\npackage billing\n\ntype Service interface{ Charge() error }\n\ntype Deps struct{}\n\nfunc New(d Deps) Service { return nil }\n"
	if err := os.WriteFile(filepath.Join(domainDir, "contract.go"), []byte(domainSrc), 0o644); err != nil {
		t.Fatalf("write domain: %v", err)
	}

	// The domain billing is passed in Packages (as discoverPackages would
	// supply it) so the generator must filter it out itself.
	packages := []BootstrapPackageData{
		{Name: "billing", Package: "billing", ImportPath: "billing", FieldName: "Billing", VarName: "billing"},
	}
	if err := GenerateBootstrapTesting(BootstrapTestingGenInput{
		GenContext: GenContext{ProjectDir: targetDir, ModulePath: "example.com/proj", Checksums: nil},
		Services:   []ServiceDef{{Name: "BillingService", ModulePath: "example.com/proj"}},
		Packages:   packages,
	}); err != nil {
		t.Fatalf("GenerateBootstrapTesting() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(targetDir, ComponentTestHelperRelPath("billing")))
	if err != nil {
		t.Fatalf("ReadFile(billing helpers) error = %v", err)
	}
	content := string(data)

	// Handler service gets the PLAIN factory (no Svc prefix — the external
	// domain pkg doesn't count as a collision).
	if !strings.Contains(content, "func NewTestBilling(t *testing.T") {
		t.Errorf("helpers should emit plain NewTestBilling for the handler service:\n%s", content)
	}
	if strings.Contains(content, "func NewTestPkgBilling(") || strings.Contains(content, "func NewTestSvcBilling(") {
		t.Errorf("helpers must not emit a factory keyed off the external-component domain pkg:\n%s", content)
	}
	if strings.Contains(content, "WithPkgBillingDeps(") {
		t.Errorf("helpers must not emit a Deps option for the external-component domain pkg:\n%s", content)
	}

	// The external-component domain package gets NO helper file at all —
	// previously this was "absent from the shared file"; now it is the
	// absence of the file itself.
	if _, err := os.Stat(filepath.Join(targetDir, "internal", "billing", componentTestHelperFile)); err == nil {
		t.Error("an //forge:external-component package must not get a helpers_gen_test.go")
	}
}

// TestGenerateBootstrapTesting_MigratedDBOptIn pins the DB harness
// contract for projects with embedded migrations: the DEFAULT test DB is
// a bare (schema-less) real-postgres database, and a NewMigratedTestDB
// helper is emitted so tests opt in to the real schema via
// WithDB(NewMigratedTestDB(t)).
func TestGenerateBootstrapTesting_MigratedDBOptIn(t *testing.T) {
	projectDir := t.TempDir()

	// A service whose Deps carry a DB field (AnyServiceHasDB → true).
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "api")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	serviceGo := `package api

import (
	"log/slog"

	"github.com/reliant-labs/forge/pkg/orm"
)

type Deps struct {
	Logger *slog.Logger
	DB     orm.Context
}

type Service struct{ deps Deps }

func New(deps Deps) (*Service, error) { return &Service{deps: deps}, nil }
`
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte(serviceGo), 0o644); err != nil {
		t.Fatal(err)
	}

	// Embedded migrations present (the GenerateMigrate predicate).
	migDir := filepath.Join(projectDir, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "00001_init.up.sql"), []byte("CREATE TABLE items (id TEXT);"), 0o644); err != nil {
		t.Fatal(err)
	}

	services := []ServiceDef{{Name: "APIService", ModulePath: "example.com/proj"}}
	if err := GenerateBootstrapTesting(BootstrapTestingGenInput{
		GenContext: GenContext{ProjectDir: projectDir, ModulePath: "example.com/proj", Checksums: nil},
		Services:   services,
		Packages:   nil,
		Workers:    nil,
		Operators:  nil,
	}); err != nil {
		t.Fatalf("GenerateBootstrapTesting() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(projectDir, ComponentTestHelperRelPath("api")))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	content := string(data)

	// Alignment-insensitive: the write chokepoint canonical-formats Go
	// output, so the composite-literal key padding is gofmt's call.
	if !regexp.MustCompile(`db:\s+testkit\.NewPostgresDB\(t\)`).MatchString(content) {
		t.Error("default test DB must be the BARE real-postgres DB (migrations are opt-in)")
	}
	if !strings.Contains(content, `func NewMigratedTestDB(t *testing.T) orm.Context`) {
		t.Error("testing.go should emit the NewMigratedTestDB opt-in helper when migrations exist")
	}
	if !strings.Contains(content, `testkit.NewMigratedPostgresDB(t, forgedb.MigrationsFS)`) {
		t.Error("NewMigratedTestDB should delegate to testkit.NewMigratedPostgresDB over forgedb.MigrationsFS")
	}
	if !strings.Contains(content, `forgedb "example.com/proj/db"`) {
		t.Error("testing.go should import the project db package as forgedb")
	}
}

func TestPackageDataFromNames(t *testing.T) {
	pkgs, err := PackageDataFromNames([]string{"cache", "db", "notifications"}, t.TempDir())
	if err != nil {
		t.Fatalf("PackageDataFromNames: %v", err)
	}

	if len(pkgs) != 3 {
		t.Fatalf("expected 3 packages, got %d", len(pkgs))
	}

	if pkgs[0].Name != "cache" || pkgs[0].FieldName != "Cache" {
		t.Errorf("expected cache/Cache, got %s/%s", pkgs[0].Name, pkgs[0].FieldName)
	}
	if pkgs[1].Name != "db" || pkgs[1].FieldName != "DB" {
		t.Errorf("expected db/DB, got %s/%s", pkgs[1].Name, pkgs[1].FieldName)
	}
	if pkgs[2].Name != "notifications" || pkgs[2].FieldName != "Notifications" {
		t.Errorf("expected notifications/Notifications, got %s/%s", pkgs[2].Name, pkgs[2].FieldName)
	}
}

// Bug #19 regression: nested package names ("mcp/database") must produce
// distinct ImportPath / FieldName / VarName so two nested packages with the
// same leaf don't collide in the bootstrap struct.
func TestPackageDataFromNames_Nested(t *testing.T) {
	pkgs, err := PackageDataFromNames([]string{"mcp/database", "cache"}, t.TempDir())
	if err != nil {
		t.Fatalf("PackageDataFromNames: %v", err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(pkgs))
	}
	got := pkgs[0]
	if got.Package != "database" {
		t.Errorf("nested Package leaf = %q, want \"database\"", got.Package)
	}
	if got.ImportPath != "mcp/database" {
		t.Errorf("nested ImportPath = %q, want \"mcp/database\"", got.ImportPath)
	}
	// "MCP" is a registered Go initialism, so PascalCase upper-cases it.
	if got.FieldName != "MCPDatabase" {
		t.Errorf("nested FieldName = %q, want \"MCPDatabase\"", got.FieldName)
	}
	// VarName lowercases only the first rune (preserves the rest of the
	// initialism as-is — "mCPDatabase" is awkward but valid Go and unique).
	if got.VarName != "mCPDatabase" {
		t.Errorf("nested VarName = %q, want \"mCPDatabase\"", got.VarName)
	}
	// Flat names still work the same way.
	flat := pkgs[1]
	if flat.Package != "cache" || flat.ImportPath != "cache" || flat.FieldName != "Cache" || flat.VarName != "cache" {
		t.Errorf("flat = %+v, want all=cache/Cache", flat)
	}
}

func TestToExportedFieldName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"api", "API"},
		{"db", "DB"},
		{"orders", "Orders"},
		{"notifications", "Notifications"},
	}

	for _, tt := range tests {
		got := naming.ToExportedFieldName(tt.input)
		if got != tt.want {
			t.Errorf("naming.ToExportedFieldName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGenerateMissingHandlerStubs_GeneratesOnlyMissing(t *testing.T) {
	targetDir := filepath.Join(t.TempDir(), "echoservice")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Simulate an existing handlers.go with only the Echo method implemented
	existingHandlers := `package echo

import (
	"context"
	"connectrpc.com/connect"
)

func (s *Service) Echo(
	ctx context.Context,
	req *connect.Request[any],
) (*connect.Response[any], error) {
	return nil, nil
}
`
	if err := os.WriteFile(filepath.Join(targetDir, "handlers.go"), []byte(existingHandlers), 0644); err != nil {
		t.Fatal(err)
	}

	svc := ServiceDef{
		Name:      "EchoService",
		Package:   "echo.v1",
		GoPackage: "github.com/test/proj/gen/proto/services/echo/v1",
		PkgName:   "echov1",
		Methods: []Method{
			{Name: "Echo", InputType: "EchoRequest", OutputType: "EchoResponse"},
			{Name: "Ping", InputType: "PingRequest", OutputType: "PingResponse"},
			{Name: "Health", InputType: "HealthRequest", OutputType: "HealthResponse"},
		},
		ProtoFile:  "proto/services/echo/v1/echo.proto",
		ModulePath: "github.com/test/proj",
	}

	result, err := GenerateMissingHandlerStubs(svc, t.TempDir(), targetDir, nil, nil)
	if err != nil {
		t.Fatalf("GenerateMissingHandlerStubs() error = %v", err)
	}

	if result.AllUpToDate {
		t.Fatal("expected new methods to be generated, got AllUpToDate", nil)
	}

	if len(result.NewMethods) != 2 {
		t.Fatalf("expected 2 new methods, got %d: %v", len(result.NewMethods), result.NewMethods)
	}

	// Verify the method names
	expected := map[string]bool{"Ping": true, "Health": true}
	for _, name := range result.NewMethods {
		if !expected[name] {
			t.Errorf("unexpected method %q in new methods", name)
		}
	}

	// Stubs land in per-RPC user-owned files — no handlers_gen.go.
	if _, statErr := os.Stat(filepath.Join(targetDir, "handlers_gen.go")); !os.IsNotExist(statErr) {
		t.Error("handlers_gen.go must never be created (scaffold-and-forget model)")
	}

	// Each new method gets its OWN file, self-contained: package clause plus
	// every import its stub body references (the pb alias goimports cannot
	// infer among them).
	for _, m := range []string{"Ping", "Health"} {
		stub := RPCHandlerFileName(m)
		data, rerr := os.ReadFile(filepath.Join(targetDir, stub))
		if rerr != nil {
			t.Fatalf("ReadFile(%s) error = %v", stub, rerr)
		}
		content := string(data)
		mustParseGo(t, stub, data)
		if !strings.Contains(content, "func (s *Service) "+m+"(") {
			t.Errorf("%s should contain the %s stub; got:\n%s", stub, m, content)
		}
		if !strings.Contains(content, `pb "github.com/test/proj/gen/proto/services/echo/v1"`) {
			t.Errorf("%s must carry its own pb import; got:\n%s", stub, content)
		}
		// No fmt: svcerr.ScaffoldStub composes the message from the RPC
		// name, so the stub body formats nothing. Asserted negatively
		// because an unused import does not compile — mustParseGo above
		// parses, it does not type-check, so this is what catches it.
		if strings.Contains(content, `"fmt"`) {
			t.Errorf("%s carries an unused fmt import; got:\n%s", stub, content)
		}
	}

	// The user's existing handlers.go is left exactly as it was — forge never
	// appends into a file it did not write for this RPC.
	data, err := os.ReadFile(filepath.Join(targetDir, "handlers.go"))
	if err != nil {
		t.Fatalf("ReadFile(handlers.go) error = %v", err)
	}
	if string(data) != existingHandlers {
		t.Errorf("the user's handlers.go was modified:\n%s", data)
	}
}

// TestGenerateMissingHandlerStubs_HandwrittenImplInCrudFile reproduces
// kalshi fr-fba0c4be8d: a user hand-implements a non-CRUD RPC inside the
// user-owned handlers_crud.go (the scaffold header says it's their file).
// ScanExistingMethods skips handlers_crud.go wholesale so its delegating
// CRUD shims don't suppress ops regen — but that also hid the hand impl,
// so GenerateMissingHandlerStubs re-emitted a DUPLICATE stub into
// handlers.go and the package failed to compile. The fix scans
// handlers_crud.go for methods whose name is NOT a CRUD method (i.e. a
// hand impl) and treats those as already implemented.
func TestGenerateMissingHandlerStubs_HandwrittenImplInCrudFile(t *testing.T) {
	targetDir := filepath.Join(t.TempDir(), "settlements")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	// handlers_crud.go: a forge-scaffolded shim that DELEGATES for the
	// CRUD-shaped GetSettlement, plus a HAND-WRITTEN custom-shape
	// ListSettlements (no entity behind it — not a CRUD method).
	crudFile := `package settlements

import (
	"context"
	"connectrpc.com/connect"
)

// GetSettlement is a generated CRUD shim that delegates to the ops layer.
func (s *Service) GetSettlement(
	ctx context.Context,
	req *connect.Request[any],
) (*connect.Response[any], error) {
	return s.getSettlementOp(ctx, req)
}

// ListSettlements is HAND-WRITTEN: a custom read shape with no entity.
func (s *Service) ListSettlements(
	ctx context.Context,
	req *connect.Request[any],
) (*connect.Response[any], error) {
	// custom query, hand-rolled
	return nil, nil
}
`
	if err := os.WriteFile(filepath.Join(targetDir, "handlers_crud.go"), []byte(crudFile), 0644); err != nil {
		t.Fatal(err)
	}

	svc := ServiceDef{
		Name:       "SettlementsService",
		Package:    "settlements.v1",
		GoPackage:  "github.com/test/proj/gen/proto/services/settlements/v1",
		PkgName:    "settlementsv1",
		ProtoFile:  "proto/services/settlements/v1/settlements.proto",
		ModulePath: "github.com/test/proj",
		Methods: []Method{
			{Name: "GetSettlement", InputType: "GetSettlementRequest", OutputType: "GetSettlementResponse"},
			{Name: "ListSettlements", InputType: "ListSettlementsRequest", OutputType: "ListSettlementsResponse"},
			{Name: "GetTradeable", InputType: "GetTradeableRequest", OutputType: "GetTradeableResponse"},
		},
	}

	// GetSettlement is a CRUD method (owned by CRUD gen); ListSettlements is
	// NOT — it's the hand impl. GetTradeable is a genuinely-missing RPC.
	crudMethodNames := map[string]bool{"GetSettlement": true}

	result, err := GenerateMissingHandlerStubs(svc, t.TempDir(), targetDir, crudMethodNames, nil)
	if err != nil {
		t.Fatalf("GenerateMissingHandlerStubs() error = %v", err)
	}

	// Only GetTradeable should be stubbed — NOT ListSettlements (hand impl)
	// and NOT GetSettlement (CRUD-owned).
	if len(result.NewMethods) != 1 || result.NewMethods[0] != "GetTradeable" {
		t.Fatalf("expected only GetTradeable stubbed, got %v", result.NewMethods)
	}

	// The missing GetTradeable stub is scaffolded into its own user-owned
	// file (never a handlers_gen.go); the two suppressed RPCs get no file
	// at all.
	if _, statErr := os.Stat(filepath.Join(targetDir, "handlers_gen.go")); !os.IsNotExist(statErr) {
		t.Error("handlers_gen.go must never be created (scaffold-and-forget model)")
	}
	for _, m := range []string{"ListSettlements", "GetSettlement"} {
		if _, statErr := os.Stat(filepath.Join(targetDir, RPCHandlerFileName(m))); !os.IsNotExist(statErr) {
			t.Errorf("%s must NOT be re-stubbed — it is already implemented (duplicate method → compile error)",
				RPCHandlerFileName(m))
		}
	}
	stub := RPCHandlerFileName("GetTradeable")
	data, err := os.ReadFile(filepath.Join(targetDir, stub))
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", stub, err)
	}
	if !strings.Contains(string(data), "func (s *Service) GetTradeable(") {
		t.Errorf("%s should stub the genuinely-missing GetTradeable:\n%s", stub, data)
	}
}

func TestGenerateMissingHandlerStubs_AllUpToDate(t *testing.T) {
	targetDir := filepath.Join(t.TempDir(), "echoservice")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	// All methods already implemented
	existingHandlers := `package echo

func (s *Service) Echo() {}
func (s *Service) Ping() {}
`
	if err := os.WriteFile(filepath.Join(targetDir, "handlers.go"), []byte(existingHandlers), 0644); err != nil {
		t.Fatal(err)
	}

	svc := ServiceDef{
		Name:      "EchoService",
		Package:   "echo.v1",
		GoPackage: "github.com/test/proj/gen/proto/services/echo/v1",
		PkgName:   "echov1",
		Methods: []Method{
			{Name: "Echo", InputType: "EchoRequest", OutputType: "EchoResponse"},
			{Name: "Ping", InputType: "PingRequest", OutputType: "PingResponse"},
		},
		ProtoFile:  "proto/services/echo/v1/echo.proto",
		ModulePath: "github.com/test/proj",
	}

	result, err := GenerateMissingHandlerStubs(svc, t.TempDir(), targetDir, nil, nil)
	if err != nil {
		t.Fatalf("GenerateMissingHandlerStubs() error = %v", err)
	}

	if !result.AllUpToDate {
		t.Fatalf("expected AllUpToDate, got new methods: %v", result.NewMethods)
	}

	// Should NOT create handlers_gen.go
	if _, err := os.Stat(filepath.Join(targetDir, "handlers_gen.go")); !os.IsNotExist(err) {
		t.Error("handlers_gen.go should not be created when all methods exist")
	}
}

func TestGenerateMissingHandlerStubs_SkipsTestFiles(t *testing.T) {
	targetDir := filepath.Join(t.TempDir(), "echoservice")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Method implemented only in test file — should NOT count as existing
	testFile := `package echo

func (s *Service) Echo() {}
`
	if err := os.WriteFile(filepath.Join(targetDir, "handlers_test.go"), []byte(testFile), 0644); err != nil {
		t.Fatal(err)
	}
	// Real handler dirs always carry a service.go; the disk-first
	// resolver reads the package clause from it.
	if err := os.WriteFile(filepath.Join(targetDir, "service.go"), []byte("package echo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := ServiceDef{
		Name:      "EchoService",
		Package:   "echo.v1",
		GoPackage: "github.com/test/proj/gen/proto/services/echo/v1",
		PkgName:   "echov1",
		Methods: []Method{
			{Name: "Echo", InputType: "EchoRequest", OutputType: "EchoResponse"},
		},
		ProtoFile:  "proto/services/echo/v1/echo.proto",
		ModulePath: "github.com/test/proj",
	}

	result, err := GenerateMissingHandlerStubs(svc, t.TempDir(), targetDir, nil, nil)
	if err != nil {
		t.Fatalf("GenerateMissingHandlerStubs() error = %v", err)
	}

	if result.AllUpToDate {
		t.Fatal("expected Echo to be generated since it's only in test files")
	}
	if len(result.NewMethods) != 1 || result.NewMethods[0] != "Echo" {
		t.Fatalf("expected [Echo], got %v", result.NewMethods)
	}
}

// TestGenerateMissingHandlerStubs_AllUpToDateIsANoOp asserts the AllUpToDate
// branch is now a pure no-op. In the scaffold-and-forget model forge no longer
// owns (or removes) any handlers_gen.go: teardown of a legacy gen file lives in
// the generate cleanup pass, not here. So when every RPC is implemented, this
// function touches nothing on disk.
func TestGenerateMissingHandlerStubs_AllUpToDateIsANoOp(t *testing.T) {
	targetDir := filepath.Join(t.TempDir(), "echoservice")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	existingHandlers := `package echo

func (s *Service) Echo() {}
func (s *Service) Ping() {}
`
	if err := os.WriteFile(filepath.Join(targetDir, "handlers.go"), []byte(existingHandlers), 0644); err != nil {
		t.Fatal(err)
	}
	// A legacy handlers_gen.go left over from an older forge version: this
	// function must NOT touch it (cleanup is another pass's job).
	legacy := []byte("package echo\nfunc (s *Service) legacy() {}\n")
	if err := os.WriteFile(filepath.Join(targetDir, "handlers_gen.go"), legacy, 0644); err != nil {
		t.Fatal(err)
	}

	svc := ServiceDef{
		Name:      "EchoService",
		Package:   "echo.v1",
		GoPackage: "github.com/test/proj/gen/proto/services/echo/v1",
		PkgName:   "echov1",
		Methods: []Method{
			{Name: "Echo", InputType: "EchoRequest", OutputType: "EchoResponse"},
			{Name: "Ping", InputType: "PingRequest", OutputType: "PingResponse"},
		},
		ProtoFile:  "proto/services/echo/v1/echo.proto",
		ModulePath: "github.com/test/proj",
	}

	result, err := GenerateMissingHandlerStubs(svc, t.TempDir(), targetDir, nil, nil)
	if err != nil {
		t.Fatalf("GenerateMissingHandlerStubs() error = %v", err)
	}
	if !result.AllUpToDate {
		t.Fatalf("expected AllUpToDate, got new methods: %v", result.NewMethods)
	}
	// The legacy gen file is left exactly as it was — this pass no longer
	// manages handlers_gen.go at all.
	got, err := os.ReadFile(filepath.Join(targetDir, "handlers_gen.go"))
	if err != nil || string(got) != string(legacy) {
		t.Fatalf("legacy handlers_gen.go must be left untouched; err=%v got=%q", err, string(got))
	}
}

func TestGenerateMissingHandlerStubs_IgnoresGeneratedStubsWhenDetectingMissing(t *testing.T) {
	targetDir := filepath.Join(t.TempDir(), "echoservice")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(targetDir, "handlers_gen.go"), []byte("package echo\nfunc (s *Service) Echo() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	svc := ServiceDef{
		Name:      "EchoService",
		Package:   "echo.v1",
		GoPackage: "github.com/test/proj/gen/proto/services/echo/v1",
		PkgName:   "echov1",
		Methods: []Method{
			{Name: "Echo", InputType: "EchoRequest", OutputType: "EchoResponse"},
			{Name: "Ping", InputType: "PingRequest", OutputType: "PingResponse"},
		},
		ProtoFile:  "proto/services/echo/v1/echo.proto",
		ModulePath: "github.com/test/proj",
	}

	result, err := GenerateMissingHandlerStubs(svc, t.TempDir(), targetDir, nil, nil)
	if err != nil {
		t.Fatalf("GenerateMissingHandlerStubs() error = %v", err)
	}
	if result.AllUpToDate {
		t.Fatal("expected missing methods to be regenerated when only handlers_gen.go exists")
	}
	if len(result.NewMethods) != 2 {
		t.Fatalf("expected 2 regenerated methods, got %v", result.NewMethods)
	}

	// ScanExistingMethods skips handlers_gen.go, so both RPCs read as missing
	// and each is scaffolded into its own fresh user-owned file.
	for _, m := range []string{"Echo", "Ping"} {
		stub := RPCHandlerFileName(m)
		data, rerr := os.ReadFile(filepath.Join(targetDir, stub))
		if rerr != nil {
			t.Fatalf("ReadFile(%s) error = %v", stub, rerr)
		}
		if !strings.Contains(string(data), "func (s *Service) "+m+"(") {
			t.Fatalf("%s should be scaffolded with the still-missing %s:\n%s", stub, m, data)
		}
	}
}

// TestToServicePackage_MovedToNaming notes the canonical test moved to
// internal/naming/naming_test.go (TestServicePackage). The codegen
// helper now delegates to naming.ServicePackage; the table-driven cases
// live with the canonical implementation.
// TestGenerateServiceStub_HandlersTestMatchesBootstrapTestingHelper covers
// the cross-role collision case: when an internal/<svc> directory exists,
// GenerateBootstrapTesting emits NewTestSvc<Pascal> rather than NewTest<Pascal>.
// The scaffolded per-RPC test must reference the same identifier.
func TestGenerateServiceStub_HandlersTestMatchesBootstrapTestingHelper(t *testing.T) {
	projectDir := t.TempDir()
	// Simulate the colliding internal package — a WIRED component (the
	// Service/Deps/New triple in contract.go) is what flips the
	// disambiguation in ComputeTestHelperName / GenerateBootstrapTesting.
	// The fixture used to be a bare empty directory, which the old
	// existence-based rule accepted; that shorthand is what let a types-only
	// package masquerade as a component. The assertion below is unchanged —
	// only the fixture now actually is the thing the comment claims.
	if err := os.MkdirAll(filepath.Join(projectDir, "internal", "billing"), 0755); err != nil {
		t.Fatalf("setup internal/billing: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "internal", "billing", "contract.go"),
		[]byte("package billing\n\ntype Service interface{ Charge() error }\n\ntype Deps struct{}\n\nfunc New(d Deps) Service { return nil }\n"), 0644); err != nil {
		t.Fatalf("setup internal/billing contract: %v", err)
	}
	targetDir := filepath.Join(projectDir, "internal", "handlers", "billing")

	svc := ServiceDef{
		Name:      "BillingService",
		Package:   "billing.v1",
		GoPackage: "example.com/proj/gen/services/billing/v1",
		PkgName:   "billingv1",
		Methods: []Method{
			{Name: "GetBill", InputType: "GetBillRequest", OutputType: "GetBillResponse"},
		},
		ProtoFile:  "proto/services/billing/v1/billing.proto",
		ModulePath: "example.com/proj",
	}

	if err := GenerateServiceStub(svc, targetDir); err != nil {
		t.Fatalf("GenerateServiceStub: %v", err)
	}

	name := ScaffoldTestFileName("GetBill")
	if name != "handlers_scaffold_get_bill_test.go" {
		t.Fatalf("ScaffoldTestFileName(GetBill) = %q", name)
	}
	got, err := os.ReadFile(filepath.Join(targetDir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	content := string(got)

	if !strings.Contains(content, "billing.NewTestSvcBilling(t)") {
		t.Errorf("%s should reference billing.NewTestSvcBilling on internal/billing collision, content:\n%s", name, content)
	}
	if strings.Contains(content, "billing.NewTestBilling(t)") {
		t.Errorf("%s should NOT reference billing.NewTestBilling on collision, content:\n%s", name, content)
	}

	// And the no-collision case: another service without an internal dir
	// keeps the simple form.
	noCollisionDir := filepath.Join(projectDir, "internal", "handlers", "echo")
	echoSvc := ServiceDef{
		Name:      "EchoService",
		Package:   "echo.v1",
		GoPackage: "example.com/proj/gen/services/echo/v1",
		PkgName:   "echov1",
		Methods: []Method{
			{Name: "Echo", InputType: "EchoRequest", OutputType: "EchoResponse"},
		},
		ProtoFile:  "proto/services/echo/v1/echo.proto",
		ModulePath: "example.com/proj",
	}
	if err := GenerateServiceStub(echoSvc, noCollisionDir); err != nil {
		t.Fatalf("GenerateServiceStub (echo): %v", err)
	}
	echoTest, err := os.ReadFile(filepath.Join(noCollisionDir, ScaffoldTestFileName("Echo")))
	if err != nil {
		t.Fatalf("read echo scaffold test: %v", err)
	}
	if !strings.Contains(string(echoTest), "echo.NewTestEcho(t)") {
		t.Errorf("echo scaffold test should reference echo.NewTestEcho (no collision)")
	}
}

func TestComputeTestHelperName(t *testing.T) {
	projectDir := t.TempDir()
	// internal/billing is a WIRED component: contract.go with the
	// Service/Deps/New triple. That — not the bare existence of a directory —
	// is what makes it occupy a test-factory slot and collide with the
	// handler service of the same name. See IsWiredComponentDir.
	if err := os.MkdirAll(filepath.Join(projectDir, "internal", "billing"), 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "internal", "billing", "contract.go"),
		[]byte("package billing\n\ntype Service interface{ Charge() error }\n\ntype Deps struct{}\n\nfunc New(d Deps) Service { return nil }\n"), 0644); err != nil {
		t.Fatalf("setup billing contract: %v", err)
	}

	// An external-component domain dir must NOT trigger the Svc prefix: it
	// is not a forge-wired component, so the handler service keeps its plain
	// factory name (FIX #3). internal/user/ carries the directive.
	if err := os.MkdirAll(filepath.Join(projectDir, "internal", "user"), 0755); err != nil {
		t.Fatalf("setup user: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "internal", "user", "contract.go"),
		[]byte("//forge:external-component\npackage user\n"), 0644); err != nil {
		t.Fatalf("setup user contract: %v", err)
	}

	// A types-only package: real code, no component to construct, opted out
	// with //forge:exclude-contract. It holds no test-factory slot, so it must
	// not drive a Svc-prefix on a same-named handler service. This shape
	// (internal/db, internal/deploy) is what the existence-based rule broke.
	if err := os.MkdirAll(filepath.Join(projectDir, "internal", "deploy"), 0755); err != nil {
		t.Fatalf("setup deploy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "internal", "deploy", "types.go"),
		[]byte("//forge:exclude-contract\npackage deploy\n\ntype Target struct{ Name string }\n"), 0644); err != nil {
		t.Fatalf("setup deploy types: %v", err)
	}

	// A directory with no Go component at all — existence alone proves nothing.
	if err := os.MkdirAll(filepath.Join(projectDir, "internal", "assets"), 0755); err != nil {
		t.Fatalf("setup assets: %v", err)
	}

	cases := []struct {
		pkg, project, want string
	}{
		{"billing", projectDir, "SvcBilling"}, // plain internal/billing dir -> collision
		{"users", projectDir, "Users"},
		{"admin_server", projectDir, "AdminServer"},
		{"billing", "", "Billing"},       // no project context -> no-collision form
		{"user", projectDir, "User"},     // external-component domain dir -> NOT a collision
		{"deploy", projectDir, "Deploy"}, // types-only (exclude-contract) dir -> NOT a collision
		{"assets", projectDir, "Assets"}, // bare dir, no contract.go -> NOT a collision
	}
	for _, c := range cases {
		if got := ComputeTestHelperName(c.pkg, c.project); got != c.want {
			t.Errorf("ComputeTestHelperName(%q, %q) = %q, want %q", c.pkg, c.project, got, c.want)
		}
	}
}

// TestWorkerDataFromNames_PascalCaseFieldName locks in the
// snake_case-worker fix: `forge scaffold worker calibrator_refit` must yield
// the idiomatic Go identifier `CalibratorRefit` for the exported
// Workers struct field + the `wireWorkerCalibratorRefitDeps` function name,
// not the underscore-preserving `Calibrator_refit` form that revive /
// staticcheck ST1003 would flag.
//
// Post-2026-06-08: the on-disk Package + Go package identifier is
// snake_case ("calibrator_refit" stays "calibrator_refit", "email-sender"
// becomes "email_sender"). Snake_case is a valid Go package identifier
// and matches the universal on-disk dir convention proto buf emits for
// multi-word proto packages. FieldName still derives from the original
// name via ToPascalCase so the exported Go identifier reads as multiple
// words.
func TestWorkerDataFromNames_PascalCaseFieldName(t *testing.T) {
	cases := []struct {
		name          string
		wantPackage   string
		wantFieldName string
		wantVarName   string
	}{
		// Snake-case → snake pkg; PascalCase via ToPascalCase from the
		// original name so word boundaries survive.
		{"calibrator_refit", "calibrator_refit", "CalibratorRefit", "calibratorRefit"},
		// Hyphenated → normalized to snake.
		{"email-sender", "email_sender", "EmailSender", "emailSender"},
		// Single-word stays as-is (just upper-cased first letter).
		{"refresh", "refresh", "Refresh", "refresh"},
		// Initialism — ToPascalCase recognizes API and uppercases it.
		{"api_poll", "api_poll", "APIPoll", "aPIPoll"},
	}
	for _, c := range cases {
		got, err := WorkerDataFromNames([]string{c.name}, "")
		if err != nil {
			t.Fatalf("WorkerDataFromNames(%q): %v", c.name, err)
		}
		if len(got) != 1 {
			t.Fatalf("WorkerDataFromNames(%q) returned %d entries, want 1", c.name, len(got))
		}
		w := got[0]
		if w.Package != c.wantPackage {
			t.Errorf("WorkerDataFromNames(%q).Package = %q, want %q", c.name, w.Package, c.wantPackage)
		}
		if w.FieldName != c.wantFieldName {
			t.Errorf("WorkerDataFromNames(%q).FieldName = %q, want %q", c.name, w.FieldName, c.wantFieldName)
		}
		if w.VarName != c.wantVarName {
			t.Errorf("WorkerDataFromNames(%q).VarName = %q, want %q", c.name, w.VarName, c.wantVarName)
		}
		// Sanity: FieldName must NOT contain an underscore.
		if strings.Contains(w.FieldName, "_") {
			t.Errorf("WorkerDataFromNames(%q).FieldName = %q must not contain '_'", c.name, w.FieldName)
		}
	}
}

// TestOperatorDataFromNames_PascalCaseFieldName mirrors the worker
// regression test — operators share the snake_case → PascalCase rule.
func TestOperatorDataFromNames_PascalCaseFieldName(t *testing.T) {
	got, err := OperatorDataFromNames([]string{"cert_rotator"}, "")
	if err != nil {
		t.Fatalf("OperatorDataFromNames: %v", err)
	}
	if len(got) != 1 || got[0].FieldName != "CertRotator" {
		t.Errorf("OperatorDataFromNames(\"cert_rotator\")[0].FieldName = %q, want \"CertRotator\"", got[0].FieldName)
	}
}

// TestWorkerDataFromSpecs_HonorsExplicitPath locks in the path-
// honoring rule: when forge.yaml declares
// `path: workers/climatology_refresh`, the generated bootstrap import must
// be `"<module>/workers/climatology_refresh"` (matching the on-disk dir).
// Same rule applies to the Alias — it must equal the `package X`
// declaration in the dir's .go file so call sites like `<Alias>.New(...)`
// resolve correctly.
//
// Coverage:
//   - Explicit snake_case path → ImportPath + Package + Alias all
//     preserve the underscore.
//   - Empty path (legacy entry point) → falls back to
//     `naming.GoPackage(name)` which canonicalises to snake_case.
//   - On-disk `package X` declaration overrides the path-derived alias —
//     ground truth wins when the user renamed the package after scaffolding
//     (e.g. legacy `package widgetv2` from the pre-2026-06-08 compact-form
//     interlude).
func TestWorkerDataFromSpecs_HonorsExplicitPath(t *testing.T) {
	projectDir := t.TempDir()
	// Seed an on-disk worker dir with the snake_case package declaration
	// so the ground-truth alias detection has something to read.
	workerDir := filepath.Join(projectDir, "internal", "workers", "climatology_refresh")
	if err := os.MkdirAll(workerDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	src := "package climatology_refresh\n\ntype Worker struct{}\n\nfunc New(Deps) *Worker { return &Worker{} }\n\ntype Deps struct{}\n"
	if err := os.WriteFile(filepath.Join(workerDir, "worker.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Mismatched on-disk dir for the "ground truth overrides path leaf" case:
	// path says workers/widget_v2 but the actual `package X` is `widgetv2`.
	mismatchDir := filepath.Join(projectDir, "internal", "workers", "widget_v2")
	if err := os.MkdirAll(mismatchDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	mismatchSrc := "package widgetv2\n\ntype Worker struct{}\n"
	if err := os.WriteFile(filepath.Join(mismatchDir, "worker.go"), []byte(mismatchSrc), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	cases := []struct {
		desc           string
		spec           WorkerSpec
		projectDir     string
		wantPackage    string
		wantImportPath string
		wantAlias      string
		wantFieldName  string
	}{
		{
			desc:           "explicit snake_case path preserves underscore for import + alias",
			spec:           WorkerSpec{Name: "climatology_refresh", Path: "workers/climatology_refresh"},
			projectDir:     projectDir,
			wantPackage:    "climatology_refresh",
			wantImportPath: "climatology_refresh",
			wantAlias:      "climatology_refresh",
			wantFieldName:  "ClimatologyRefresh",
		},
		{
			desc:           "empty path falls back to snake_case Go-style form",
			spec:           WorkerSpec{Name: "calibrator_refit"},
			projectDir:     "",
			wantPackage:    "calibrator_refit",
			wantImportPath: "calibrator_refit",
			wantAlias:      "calibrator_refit",
			wantFieldName:  "CalibratorRefit",
		},
		{
			desc:           "on-disk package declaration overrides path-derived alias",
			spec:           WorkerSpec{Name: "widget_v2", Path: "workers/widget_v2"},
			projectDir:     projectDir,
			wantPackage:    "widgetv2", // overridden by ground truth
			wantImportPath: "widget_v2",
			wantAlias:      "widgetv2", // overridden by ground truth
			wantFieldName:  "WidgetV2",
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			got, err := WorkerDataFromSpecs([]WorkerSpec{c.spec}, c.projectDir)
			if err != nil {
				t.Fatalf("WorkerDataFromSpecs: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("WorkerDataFromSpecs returned %d entries, want 1", len(got))
			}
			w := got[0]
			if w.Package != c.wantPackage {
				t.Errorf("Package = %q, want %q", w.Package, c.wantPackage)
			}
			if w.ImportPath != c.wantImportPath {
				t.Errorf("ImportPath = %q, want %q", w.ImportPath, c.wantImportPath)
			}
			if w.Alias != c.wantAlias {
				t.Errorf("Alias = %q, want %q", w.Alias, c.wantAlias)
			}
			if w.FieldName != c.wantFieldName {
				t.Errorf("FieldName = %q, want %q", w.FieldName, c.wantFieldName)
			}
		})
	}
}

// TestOperatorDataFromSpecs_HonorsExplicitPath mirrors the worker test —
// the path-honoring rule applies equally to operators.
func TestOperatorDataFromSpecs_HonorsExplicitPath(t *testing.T) {
	projectDir := t.TempDir()
	opDir := filepath.Join(projectDir, "internal", "operators", "cert_rotator")
	if err := os.MkdirAll(opDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	src := "package cert_rotator\n\ntype Controller struct{}\n"
	if err := os.WriteFile(filepath.Join(opDir, "controller.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := OperatorDataFromSpecs([]OperatorSpec{
		{Name: "cert_rotator", Path: "operators/cert_rotator"},
	}, projectDir)
	if err != nil {
		t.Fatalf("OperatorDataFromSpecs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("OperatorDataFromSpecs returned %d entries, want 1", len(got))
	}
	op := got[0]
	if op.ImportPath != "cert_rotator" {
		t.Errorf("ImportPath = %q, want %q", op.ImportPath, "cert_rotator")
	}
	if op.Alias != "cert_rotator" {
		t.Errorf("Alias = %q, want %q", op.Alias, "cert_rotator")
	}
	if op.FieldName != "CertRotator" {
		t.Errorf("FieldName = %q, want %q", op.FieldName, "CertRotator")
	}
}

// TestGenerateMissingHandlerStubs_UnitTestSkipsCRUDMethods pins the
// per-RPC test owner-rule: handlers_crud_gen_test.go owns CRUD-method
// rows (shape-aware: AIP-158 Id/PageSize/update_mask literals), so the
// stub path must NOT also emit a Test<CRUDMethod>_Generated row for the
// same method. Without this filter the user sees two scaffold tests per
// CRUD RPC, one in each file, and any future shape change has to be
// applied twice.
//
// It also pins the file split: each non-CRUD RPC gets its OWN
// handlers_scaffold_<rpc>_test.go, so two owners implementing two RPCs in
// one package share no file and no package-level helper.
//
// Regression guard: the previous implementation passed `fullData` (every
// RPC) into unit_test.go.tmpl, producing the overlap that this test
// pins against.
func TestGenerateMissingHandlerStubs_UnitTestSkipsCRUDMethods(t *testing.T) {
	projectDir := t.TempDir()
	targetDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Seed a placeholder integration_test.go and a minimal handlers.go so
	// ScanExistingMethods doesn't trip.
	intPlaceholder := `// Code generated by forge. DO NOT EDIT.
package patients_test

// forge-integration-test-placeholder
`
	if err := os.WriteFile(filepath.Join(targetDir, "integration_test.go"), []byte(intPlaceholder), 0o644); err != nil {
		t.Fatal(err)
	}
	// Real handler dirs always carry a service.go; the disk-first
	// resolver reads the package clause from it (test files are skipped).
	if err := os.WriteFile(filepath.Join(targetDir, "service.go"), []byte("package patients\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := ServiceDef{
		Name:       "PatientsService",
		Package:    "patients.v1",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		ProtoFile:  "proto/services/patients/v1/patients.proto",
		Methods: []Method{
			{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse"},
			{Name: "GetPatient", InputType: "GetPatientRequest", OutputType: "GetPatientResponse"},
			{Name: "Echo", InputType: "EchoRequest", OutputType: "EchoResponse"},
		},
	}

	crudMethodNames := map[string]bool{
		"CreatePatient": true,
		"GetPatient":    true,
	}

	if _, err := GenerateMissingHandlerStubs(svc, projectDir, targetDir, crudMethodNames, nil); err != nil {
		t.Fatalf("GenerateMissingHandlerStubs() error = %v", err)
	}

	// CRUD methods get no scaffold test file at all — handlers_crud_test.go
	// owns them.
	for _, crud := range []string{"CreatePatient", "GetPatient"} {
		if _, err := os.Stat(filepath.Join(targetDir, ScaffoldTestFileName(crud))); !os.IsNotExist(err) {
			t.Errorf("%s must not be born (CRUD rows are owned by handlers_crud_test.go)", ScaffoldTestFileName(crud))
		}
	}

	// The non-CRUD method gets its own file, named after the RPC alone.
	echoPath := filepath.Join(targetDir, "handlers_scaffold_echo_test.go")
	content, err := os.ReadFile(echoPath)
	if err != nil {
		t.Fatalf("read handlers_scaffold_echo_test.go: %v", err)
	}
	got := string(content)
	if !strings.Contains(got, "TestEcho_Generated") {
		t.Errorf("handlers_scaffold_echo_test.go should contain TestEcho_Generated; got:\n%s", got)
	}
	// No package-level helper: `setup` was the identifier two owners
	// collided on, and it bought exactly one line of typing.
	if strings.Contains(got, "func setup(") {
		t.Errorf("the born scaffold test must declare no package-level helper; got:\n%s", got)
	}
	if !strings.Contains(got, "svc := patients.NewTestPatients(t)") {
		t.Errorf("the scaffold should construct the service inline; got:\n%s", got)
	}
}

// TestUnitTestScaffold_SelfDestructingRows pins the scaffold-test contract:
// every generated row must be able to FAIL. The scaffold row asserts
// WantErr: connect.CodeUnimplemented against the stub handler, so it goes
// red the moment the handler is implemented — forcing the row to be
// rewritten with a real assertion. The permissive AnyOutcome knob is gone
// from pkg/tdd entirely; a test that cannot fail teaches green-means-nothing.
//
// The scaffold must also emit Ctx: app.AuthedContext(t) so handlers that
// read claims via middleware.GetUser see an authenticated context
// (review F4).
func TestUnitTestScaffold_SelfDestructingRows(t *testing.T) {
	data := ServiceTemplateData{
		ServiceName:         "EchoService",
		ServicePackage:      "echo",
		Module:              "example.com/test",
		ProtoPackage:        "services/echo",
		ProtoImportPath:     "services/echo",
		ProtoConnectPackage: "echov1connect",
		HandlerName:         "EchoService",
		TestHelperName:      "Echo",
		ServiceImportPath:   "echo",
		Methods: []MethodTemplateData{
			{Name: "Echo", InputType: "EchoRequest", OutputType: "EchoResponse"},
		},
	}
	content, err := templates.ServiceTemplates().Render("unit_test.go.tmpl", data)
	if err != nil {
		t.Fatalf("render unit_test.go.tmpl: %v", err)
	}
	got := string(content)

	if strings.Contains(got, "AnyOutcome") {
		t.Errorf("scaffold must not reference AnyOutcome (deleted from pkg/tdd — permissive rows belong in no library); got:\n%s", got)
	}
	// WantScaffoldStub, not WantErr: connect.CodeUnimplemented. The row has
	// to key on the sentinel only forge's own untouched stub emits, or a
	// finished handler that answers Unimplemented for its own reasons —
	// most commonly a nil-guard on a dep the harness leaves unset — keeps
	// the row green forever. Asserting the bare code is exactly the defect
	// that left 78 of 78 rows passing against implemented RPCs.
	if !strings.Contains(got, "WantScaffoldStub: true") {
		t.Errorf("scaffold row must self-destruct via WantScaffoldStub; got:\n%s", got)
	}
	if strings.Contains(got, "WantErr: connect.CodeUnimplemented") {
		t.Errorf("scaffold row must NOT assert the bare CodeUnimplemented — an implemented handler can return it; got:\n%s", got)
	}
	if !strings.Contains(got, "Ctx:") || !strings.Contains(got, ".AuthedContext(t)") {
		t.Errorf("scaffold row must emit Ctx: <pkg>.AuthedContext(t); got:\n%s", got)
	}
}

// TestInspectComponentDepsShape_ORMContextDB asserts that an interactor
// package declaring `DB orm.Context` gets HasDB=true, so the generated
// test harness emits `DB: cfg.db` for it.
//
// The service factory in the same generated file has always merged the DB
// in (`if deps.DB == nil { deps.DB = cfg.db }`); the package factory
// emitted only Logger and Config. A `forge scaffold package --type
// interactor` whose workflow touches the database therefore got a
// nil DB from NewTest<Package> and nil-panicked on first use — inside
// pkg/app/testing.go, a DO NOT EDIT file the author cannot correct.
//
// A domain-local DB type must NOT set the flag, for the same reason
// HasConfig gates on *config.Config: the template would emit `DB: cfg.db`
// (an orm.Context) into a field of an incompatible type.
func TestInspectComponentDepsShape_ORMContextDB(t *testing.T) {
	projectDir := t.TempDir()

	appDir := filepath.Join(projectDir, "pkg", "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "app_extras.go"),
		[]byte("package app\n\ntype AppExtras struct{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pkgDir := filepath.Join(projectDir, "internal", "fulfillment")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := `package fulfillment

import (
	"log/slog"

	"github.com/reliant-labs/forge/pkg/orm"
)

type Deps struct {
	DB     orm.Context
	Logger *slog.Logger
}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "contract.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	components := []BootstrapComponentData{
		{Name: "fulfillment", Package: "fulfillment", ImportPath: "fulfillment"},
	}
	inspectComponentDepsShape(components, projectDir, "internal")

	if !components[0].HasLogger {
		t.Error("HasLogger should be true (Logger is *slog.Logger)")
	}
	if !components[0].HasDB {
		t.Error("HasDB must be true when Deps.DB is orm.Context; without it NewTest<Package> builds the interactor with a nil DB and panics on first database call")
	}
}

// TestInspectComponentDepsShape_DomainLocalDB is the negative half: a
// package-local DB type must not trip HasDB, or the generated
// `DB: cfg.db` would be a type mismatch at codegen time.
func TestInspectComponentDepsShape_DomainLocalDB(t *testing.T) {
	projectDir := t.TempDir()

	appDir := filepath.Join(projectDir, "pkg", "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "app_extras.go"),
		[]byte("package app\n\ntype AppExtras struct{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pkgDir := filepath.Join(projectDir, "internal", "ledger")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := `package ledger

import "log/slog"

type DB struct{}

type Deps struct {
	DB     DB
	Logger *slog.Logger
}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "contract.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	components := []BootstrapComponentData{
		{Name: "ledger", Package: "ledger", ImportPath: "ledger"},
	}
	inspectComponentDepsShape(components, projectDir, "internal")

	if components[0].HasDB {
		t.Error("HasDB must be false for a domain-local DB type; the template would emit `DB: cfg.db` (an orm.Context) into an incompatible field")
	}
}
