package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// importedFooProto is the vendored module's proto: a FooService with the same
// short name as the project's own, a BarService no frontend allowlists, and a
// `// forge:mutation` marker the hooks must read from the MODULE's source.
const importedFooProto = `syntax = "proto3";
package other.v1;

message GetThingRequest { string id = 1; }
message GetThingResponse { string name = 1; }
message ListThingsRequest { string page_token = 1; }
message ListThingsResponse { string next_page_token = 1; }

service FooService {
  // forge:mutation — claims the thing it reads.
  rpc GetThing(GetThingRequest) returns (GetThingResponse);
  rpc ListThings(ListThingsRequest) returns (ListThingsResponse);
}

service BarService {
  rpc GetThing(GetThingRequest) returns (GetThingResponse);
}
`

// importedBillingProto is a second package of the same module: one entry's
// allowlist may name services of any package the module holds.
const importedBillingProto = `syntax = "proto3";
package billing.v1;

message GetInvoiceRequest { string id = 1; }
message GetInvoiceResponse { string total = 1; }

service BillingService {
  rpc GetInvoice(GetInvoiceRequest) returns (GetInvoiceResponse);
}
`

func importedFixtureServices() []codegen.ServiceDef {
	method := func(name string) codegen.Method {
		return codegen.Method{Name: name, InputType: name + "Request", OutputType: name + "Response"}
	}
	return []codegen.ServiceDef{
		{Name: "FooService", Package: "other.v1", ProtoFile: "other/v1/foo.proto", Methods: []codegen.Method{method("GetThing"), method("ListThings")}},
		{Name: "BarService", Package: "other.v1", ProtoFile: "other/v1/foo.proto", Methods: []codegen.Method{method("GetThing")}},
		{Name: "BillingService", Package: "billing.v1", ProtoFile: "billing/v1/billing.proto", Methods: []codegen.Method{method("GetInvoice")}},
	}
}

// importedHooksFixture lays down a project whose web frontend has its own
// FooService hooks and a vendored module at vendor/other/ (own buf.yaml).
func importedHooksFixture(t *testing.T) (dir string, cfg *config.ProjectConfig, imp config.ImportedHooks) {
	t.Helper()
	dir = t.TempDir()
	writeHookFixture(t, filepath.Join(dir, "vendor/other/buf.yaml"), "version: v2\n")
	writeHookFixture(t, filepath.Join(dir, "vendor/other/other/v1/foo.proto"), importedFooProto)
	writeHookFixture(t, filepath.Join(dir, "vendor/other/billing/v1/billing.proto"), importedBillingProto)
	imp = config.ImportedHooks{
		ProtoRoot: "vendor/other",
		GenDir:    "other",
		Services:  []string{"other.v1.FooService", "billing.v1.BillingService"},
	}
	fe := config.FrontendConfig{Name: "web", Type: "vite-spa", ImportedHooks: []config.ImportedHooks{imp}}.WithDir("frontends/web")
	cfg = &config.ProjectConfig{Name: "myapp", Frontends: []config.FrontendConfig{fe}}
	return dir, cfg, imp
}

// TestImportedServiceHookData pins the pure part of an imported service's
// hooks, without buf: imports rooted under gen_dir, the fully-qualified key
// root, and markers read from <proto_root>/<ProtoFile>.
func TestImportedServiceHookData(t *testing.T) {
	dir, _, imp := importedHooksFixture(t)
	foo := importedFixtureServices()[0]

	data, err := importedServiceHookData(foo, dir, imp)
	if err != nil {
		t.Fatal(err)
	}
	if data.ImportPath != "other/other/v1/foo_pb" {
		t.Errorf("ImportPath = %q, want it under gen_dir", data.ImportPath)
	}
	for _, g := range append(append([]codegen.HookImportGroup(nil), data.SchemaImports...), data.TypeImports...) {
		if !strings.HasPrefix(g.ImportPath, "other/") {
			t.Errorf("import %q is not under gen_dir", g.ImportPath)
		}
	}
	if got := data.QueryKeyRoot(); got != "other.v1.FooService" {
		t.Errorf("QueryKeyRoot = %q, want the fully-qualified name", got)
	}
	for _, m := range data.Methods {
		if m.Name == "GetThing" && m.IsQuery {
			t.Error("GetThing's // forge:mutation marker in the module's proto was not read")
		}
		if m.Name == "ListThings" && !m.IsQuery {
			t.Error("ListThings should stay a query")
		}
	}

	// A project service keeps its historical key root, so every existing
	// hooks file renders byte-identically.
	own := codegen.ServiceDefToHookData(fakeService("FooService", "proto/services/foo/v1/foo.proto"))
	if own.QueryKeyRoot() != "fooService" {
		t.Errorf("own QueryKeyRoot = %q, want the camel service name", own.QueryKeyRoot())
	}
}

// TestWriteImportedHookFiles: an imported FooService lands in
// src/hooks/<gen_dir>/, beside — not over — the project's own FooService
// hooks, outside the barrel; a service the allowlist omits gets nothing.
func TestWriteImportedHookFiles(t *testing.T) {
	resetCleanupRunState(t)
	dir, cfg, imp := importedHooksFixture(t)
	if err := generateFrontendHooks(cfg, []codegen.ServiceDef{fakeService("FooService", "proto/services/foo/v1/foo.proto")}, dir, nil); err != nil {
		t.Fatal(err)
	}
	hooksDir := filepath.Join(dir, "frontends/web/src/hooks")
	ownBefore, err := os.ReadFile(filepath.Join(hooksDir, "foo-service-hooks_gen.ts"))
	if err != nil {
		t.Fatal(err)
	}
	barrelBefore, err := os.ReadFile(filepath.Join(hooksDir, "index.ts"))
	if err != nil {
		t.Fatal(err)
	}

	tmpl, err := parseHooksTemplate()
	if err != nil {
		t.Fatal(err)
	}
	n, err := writeImportedHookFiles(tmpl, cfg.Frontends[0], imp, importedFixtureServices(), dir, "frontends/web/src/hooks/other", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("wrote %d files, want 2 (FooService, BillingService)", n)
	}

	got, err := os.ReadFile(filepath.Join(hooksDir, "other", "foo-service-hooks_gen.ts"))
	if err != nil {
		t.Fatalf("imported hooks not at src/hooks/<gen_dir>/: %v", err)
	}
	s := string(got)
	for _, want := range []string{
		`from "@/gen/other/other/v1/foo_pb"`,
		`serviceKeys("other.v1.FooService")`,
		`from "@/lib/connect"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("imported hooks lack %s:\n%s", want, s)
		}
	}
	if strings.Contains(s, `from "@/gen/other/v1/`) {
		t.Errorf("an import is not rooted under gen_dir:\n%s", s)
	}
	billing, err := os.ReadFile(filepath.Join(hooksDir, "other", "billing-service-hooks_gen.ts"))
	if err != nil || !strings.Contains(string(billing), `serviceKeys("billing.v1.BillingService")`) ||
		!strings.Contains(string(billing), `from "@/gen/other/billing/v1/billing_pb"`) {
		t.Errorf("a second package of the module: err=%v\n%s", err, billing)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "other", "bar-service-hooks_gen.ts")); !os.IsNotExist(err) {
		t.Errorf("BarService is not allowlisted, but its hooks exist (err=%v)", err)
	}
	starter, err := os.ReadFile(filepath.Join(hooksDir, "other", "foo-service-hooks.test.tsx"))
	if err != nil || !strings.Contains(string(starter), `from "./foo-service-hooks_gen"`) {
		t.Errorf("starter test beside the imported hooks: err=%v\n%s", err, starter)
	}

	if ownAfter, _ := os.ReadFile(filepath.Join(hooksDir, "foo-service-hooks_gen.ts")); string(ownAfter) != string(ownBefore) {
		t.Error("the project's own FooService hooks changed")
	}
	if !strings.Contains(string(ownBefore), `serviceKeys("fooService")`) {
		t.Errorf("own hooks key root changed:\n%s", ownBefore)
	}
	if barrelAfter, _ := os.ReadFile(filepath.Join(hooksDir, "index.ts")); string(barrelAfter) != string(barrelBefore) {
		t.Errorf("imported hooks joined the barrel:\n%s", barrelAfter)
	}
}

// TestGenerateImportedFrontendHooksRefusesWorkspaces: the shared
// packages/hooks layout has no per-frontend gen_dir to root imports under.
func TestGenerateImportedFrontendHooksRefusesWorkspaces(t *testing.T) {
	dir, cfg, _ := importedHooksFixture(t)
	ws := cfg.WithFrontendWorkspaces(true)
	err := generateImportedFrontendHooks(&ws, dir, nil)
	if err == nil || !strings.Contains(err.Error(), "not supported with frontend workspaces") {
		t.Fatalf("err = %v, want the workspaces refusal", err)
	}
}

// TestGenerateFrontendHooksGateSeesImportedHooks: a project with no service
// of its own still runs the hooks step for an imported module.
func TestGenerateFrontendHooksGateSeesImportedHooks(t *testing.T) {
	_, cfg, _ := importedHooksFixture(t)
	config.ApplyDerivedDefaults(cfg)
	ctx := &pipelineContext{Cfg: cfg}
	if !gateFrontendHooks(ctx) {
		t.Error("imported_hooks alone should open the hooks gate")
	}
	cfg.Frontends[0].ImportedHooks = nil
	if gateFrontendHooks(ctx) {
		t.Error("no services and no imported_hooks: the gate stays shut")
	}
}

// TestGenerateImportedFrontendHooksEndToEnd runs the real buf and
// protoc-gen-forge over the vendored module: the allowlisted services of both
// its packages get hooks, and nothing is written into the project's own
// gen/forge_descriptor/.
func TestGenerateImportedFrontendHooksEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("runs buf and protoc-gen-forge over a vendored proto module; runs in task test")
	}
	if _, err := exec.LookPath("buf"); err != nil {
		t.Skip("buf is not on PATH")
	}
	resetCleanupRunState(t)
	dir, cfg, _ := importedHooksFixture(t)
	if err := generateImportedFrontendHooks(cfg, dir, nil); err != nil {
		t.Fatalf("generateImportedFrontendHooks: %v", err)
	}
	hooksDir := filepath.Join(dir, "frontends/web/src/hooks/other")
	foo, err := os.ReadFile(filepath.Join(hooksDir, "foo-service-hooks_gen.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(foo), `serviceKeys("other.v1.FooService")`) || !strings.Contains(string(foo), `from "@/gen/other/other/v1/foo_pb"`) {
		t.Errorf("imported FooService hooks:\n%s", foo)
	}
	if !strings.Contains(string(foo), "useListThingsInfinite") {
		t.Errorf("the descriptor's message fields did not reach the paging classifier:\n%s", foo)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "billing-service-hooks_gen.ts")); err != nil {
		t.Errorf("BillingService (second package of the module): %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "bar-service-hooks_gen.ts")); !os.IsNotExist(err) {
		t.Errorf("BarService is not allowlisted (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, codegen.DescriptorDir)); !os.IsNotExist(err) {
		t.Errorf("an imported module must not reach %s (err=%v)", codegen.DescriptorDir, err)
	}
}
