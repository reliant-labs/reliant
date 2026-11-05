// Tests for registration-in-code (the user-owned pkg/app/services.go
// row list): the registry parser + classification chokepoint, the audit
// surfaces (shape served:false additive marker
// + codegen unregistered_services finding), and the stale-cleanup
// retirement path. The full end-to-end flow (real `forge generate` on a
// scaffolded project) lives in serve_types_only_e2e_test.go behind the
// e2e build tag.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/generator"
)

// writeServiceRegistry drops a pkg/app/services.go into dir with the
// given body. The body mirrors the scaffold shape: api registered,
// project tombstoned (comment only), anything else unlisted.
func writeServiceRegistry(t *testing.T, dir, body string) {
	t.Helper()
	appDir := filepath.Join(dir, "pkg", "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("mkdir pkg/app: %v", err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "services.go"), []byte(body), 0o644); err != nil {
		t.Fatalf("write services.go: %v", err)
	}
}

const registryFixture = `package app

import (
	"log/slog"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/appkit"

	"github.com/example/demo/pkg/config"
)

// RegisteredServices lists what THIS binary serves.
func RegisteredServices(app *App, cfg *config.Config, logger *slog.Logger, opts ...connect.HandlerOption) []appkit.ServiceDef {
	return []appkit.ServiceDef{
		serviceRowAPI(app, cfg, logger, opts...),
		// project: types-only — served by control-plane
	}
}
`

func serveTestServiceDefs() []codegen.ServiceDef {
	return []codegen.ServiceDef{
		{Name: "ApiService", Package: "api.v1", Methods: []codegen.Method{
			{Name: "Get", InputType: "GetRequest", OutputType: "GetResponse"},
		}},
		{Name: "ProjectService", Package: "reliant.v1", Methods: []codegen.Method{
			{Name: "CreateProject", InputType: "CreateProjectRequest", OutputType: "CreateProjectResponse"},
			{Name: "GetProject", InputType: "GetProjectRequest", OutputType: "GetProjectResponse"},
		}},
		{Name: "LedgerService", Package: "ledger.v1", Methods: []codegen.Method{
			{Name: "Post", InputType: "PostRequest", OutputType: "PostResponse"},
		}},
	}
}

func TestServiceRegistry_Classification(t *testing.T) {
	dir := t.TempDir()
	writeServiceRegistry(t, dir, registryFixture)

	reg, err := loadServiceRegistry(dir)
	if err != nil {
		t.Fatalf("loadServiceRegistry: %v", err)
	}
	if !reg.Exists {
		t.Fatalf("registry must report Exists=true")
	}

	// Spelling-agnostic: proto, kebab/CLI, and snake forms all resolve.
	for _, spelling := range []string{"ApiService", "api", "API"} {
		if got := reg.state(spelling); got != registrationRegistered {
			t.Errorf("state(%q) = %v, want registered", spelling, got)
		}
	}
	for _, spelling := range []string{"ProjectService", "project"} {
		if got := reg.state(spelling); got != registrationTombstoned {
			t.Errorf("state(%q) = %v, want tombstoned (comment mention)", spelling, got)
		}
	}
	// Ledger appears nowhere — newly added.
	if got := reg.state("LedgerService"); got != registrationUnlisted {
		t.Errorf("state(LedgerService) = %v, want unlisted", got)
	}
}

func TestServiceRegistry_MissingFileServesEverything(t *testing.T) {
	reg, err := loadServiceRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("loadServiceRegistry on empty dir: %v", err)
	}
	if reg.Exists {
		t.Fatalf("Exists must be false when pkg/app/services.go is absent")
	}
	for _, name := range []string{"ApiService", "anything-at-all"} {
		if !reg.registered(name) {
			t.Errorf("missing registry must fail open to registered for %q", name)
		}
	}
}

func TestServiceRegistry_ParseErrorIsLoud(t *testing.T) {
	dir := t.TempDir()
	writeServiceRegistry(t, dir, "package app\nfunc broken( {")
	if _, err := loadServiceRegistry(dir); err == nil || !strings.Contains(err.Error(), serviceRegistryRelPath) {
		t.Fatalf("parse failure must name the registration file, got err=%v", err)
	}

	// The pipeline accessor propagates (and memoizes) the failure.
	ctx := &pipelineContext{ProjectDir: dir}
	if _, err := ctx.serviceRegistry(); err == nil {
		t.Fatalf("ctx.serviceRegistry must propagate the parse error")
	}
	if _, err := ctx.rowServiceDefs(); err == nil {
		t.Fatalf("rowServiceDefs must propagate the parse error")
	}
}

func TestServiceRegistry_CollisionPrefixedRowResolves(t *testing.T) {
	dir := t.TempDir()
	// A cross-role collision (service "billing" + internal/billing)
	// renames the FieldName to SvcBilling; the registered detection must
	// still resolve the underlying service.
	writeServiceRegistry(t, dir, `package app

func RegisteredServices() {
	_ = serviceRowSvcBilling
}
`)
	reg, err := loadServiceRegistry(dir)
	if err != nil {
		t.Fatalf("loadServiceRegistry: %v", err)
	}
	if !reg.registered("billing") || !reg.registered("BillingService") {
		t.Errorf("serviceRowSvcBilling reference must register billing in all spellings")
	}
}

func TestSplitServiceDefs_AndViews(t *testing.T) {
	dir := t.TempDir()
	writeServiceRegistry(t, dir, registryFixture)
	ctx := &pipelineContext{ProjectDir: dir, Services: serveTestServiceDefs()}

	rows, err := ctx.rowServiceDefs()
	if err != nil {
		t.Fatalf("rowServiceDefs: %v", err)
	}
	// Registered (api) + unlisted/newly-added (ledger) get rows;
	// tombstoned (project) does not.
	if len(rows) != 2 || rows[0].Name != "ApiService" || rows[1].Name != "LedgerService" {
		t.Errorf("rows = %+v, want [ApiService LedgerService]", rows)
	}

	registered, err := ctx.registeredServiceDefs()
	if err != nil {
		t.Fatalf("registeredServiceDefs: %v", err)
	}
	if len(registered) != 1 || registered[0].Name != "ApiService" {
		t.Errorf("registered = %+v, want [ApiService]", registered)
	}

}

func TestAllServiceRuntimeNames(t *testing.T) {
	got := allServiceRuntimeNames([]codegen.ServiceDef{
		{Name: "AdminServerService"}, {Name: "ApiService"},
	})
	if len(got) != 2 || got[0] != "admin-server" || got[1] != "api" {
		t.Errorf("allServiceRuntimeNames = %v, want [admin-server api]", got)
	}
}

// TestCleanupStale_TombstonedHandlerFilesBecomeCandidates pins the
// retirement flow's cleanup half: certified (forge:hash-stamped) Tier-1
// files under a retired handlers dir (not re-written this run because
// the emitters are gated) are deleted; user-owned (unmarked Tier-2)
// files in the same dir are never candidates.
func TestCleanupStale_TombstonedHandlerFilesBecomeCandidates(t *testing.T) {
	checksums.ResetPerRunState()
	checksums.ResetSkipWrite()
	defer checksums.ResetPerRunState()
	defer checksums.ResetSkipWrite()

	dir := t.TempDir()
	handlerDir := filepath.Join(dir, "internal", "handlers", "project")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Tier-1: stamped pristine forge render — now gated off, so stale.
	genBody, ok := checksums.Stamp("handlers/project/handlers_gen.go",
		[]byte("// Code generated by forge. DO NOT EDIT.\npackage project\n"))
	if !ok {
		t.Fatal("handlers_gen.go should be stampable")
	}
	// Tier-2: scaffold-once user-owned (no marker) — never a candidate.
	userBody := "package project\n// user-owned handler logic\n"
	if err := os.WriteFile(filepath.Join(handlerDir, "handlers_gen.go"), genBody, 0o644); err != nil {
		t.Fatalf("write handlers_gen.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(handlerDir, "handlers.go"), []byte(userBody), 0o644); err != nil {
		t.Fatalf("write handlers.go: %v", err)
	}

	cs := &generator.FileChecksums{}
	ctx := &pipelineContext{
		ProjectDir:  dir,
		AbsPath:     dir,
		Checksums:   cs,
		HasServices: true, // owner-step gate for handlers paths
	}

	sweep, err := cleanupStaleArtifacts(ctx)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if len(sweep.HandEdited) != 0 {
		t.Errorf("handEdited = %v, want none", sweep.HandEdited)
	}
	if want := []string{"internal/handlers/project/handlers_gen.go"}; strings.Join(sweep.Removed, ",") != strings.Join(want, ",") {
		t.Fatalf("removed = %v, want exactly the Tier-1 handlers_gen.go", sweep.Removed)
	}

	// The pristine stale file goes, but the user-written Tier-2 file is
	// never touched. (The manifest-prune assertion is gone with the
	// manifest: the marker that recorded forge's authorship is deleted
	// WITH the file — nothing else to prune.)
	if _, statErr := os.Stat(filepath.Join(handlerDir, "handlers_gen.go")); !os.IsNotExist(statErr) {
		t.Errorf("the sweep must delete handlers_gen.go, stat err = %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(handlerDir, "handlers.go")); statErr != nil {
		t.Errorf("user-written handlers.go must survive the sweep: %v", statErr)
	}
}

// TestGenerateWebhookRoutes_UnregisteredServiceIsHardError pins the
// reworked validation rule: webhooks on a service without a serviceRow
// in pkg/app/services.go is a generate-time error naming the
// registration file (F1 enforced this as a forge.yaml LoadStrict rule;
// the yaml surface is gone).
func TestGenerateWebhookRoutes_UnregisteredServiceIsHardError(t *testing.T) {
	dir := t.TempDir()
	writeServiceRegistry(t, dir, registryFixture) // project tombstoned
	yamlBody := `name: demo
module_path: github.com/example/demo
`
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(yamlBody), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	// Webhooks are discovered from the real source: a webhook_<name>.go file
	// in the service's handler dir. "project" is tombstoned (types-only) in
	// the registry, so a webhook handler there must be a hard error.
	handlerDir := filepath.Join(dir, "internal", "handlers", "project")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatalf("mkdir handler dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(handlerDir, "webhook_stripe.go"), []byte("package project\n"), 0o644); err != nil {
		t.Fatalf("write webhook handler: %v", err)
	}
	reg, err := loadServiceRegistry(dir)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	err = generateWebhookRoutes(reg, dir, nil)
	if err == nil {
		t.Fatalf("webhooks on an unregistered service must be a hard error")
	}
	for _, want := range []string{serviceRegistryRelPath, "webhooks", codegen.ServiceRowFuncName("project")} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}
