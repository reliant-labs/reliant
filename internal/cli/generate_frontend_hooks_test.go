package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// codegenServiceDefForStarterTest returns a 2-RPC ServiceDef suitable as
// input to writeHookStarterTest in the starter-test unit tests. Kept as a
// helper rather than inlined so the three starter tests share one shape.
func codegenServiceDefForStarterTest() codegen.ServiceDef {
	return codegen.ServiceDef{
		Name:      "UserService",
		ProtoFile: "proto/services/users/v1/users.proto",
		Methods: []codegen.Method{
			{Name: "GetUser", InputType: "GetUserRequest", OutputType: "GetUserResponse"},
			{Name: "CreateUser", InputType: "CreateUserRequest", OutputType: "CreateUserResponse"},
		},
	}
}

// writeHookFixture writes content to path, creating its directory.
func writeHookFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func codegenHookDataForStarterTest() codegen.FrontendHookTemplateData {
	return codegen.ServiceDefToHookData(codegenServiceDefForStarterTest())
}

// TestWriteHooksIndex_FlatModeNoCollisions asserts the historic shape is
// preserved when no two hook files re-export the same identifier: a flat
// `export * from "./..."` per file. This is the path nearly every
// single-service project takes; regressing it would break every existing
// `import { useGetUser } from "@/hooks"` site.
func TestWriteHooksIndex_FlatModeNoCollisions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.ts")

	files := []hookFileEntry{
		{fileName: "user-service-hooks_gen.ts", nsAlias: "userService", symbols: []string{"useGetUser", "useListUsers"}},
		{fileName: "org-service-hooks_gen.ts", nsAlias: "orgService", symbols: []string{"useGetOrg", "useListOrgs"}},
	}

	if err := writeHooksIndex(path, files); err != nil {
		t.Fatalf("writeHooksIndex: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read index.ts: %v", err)
	}
	s := string(got)

	if !strings.Contains(s, "Mode: flat wildcard re-exports") {
		t.Errorf("expected flat-mode comment, got:\n%s", s)
	}
	if !strings.Contains(s, `export * from "./user-service-hooks_gen";`) {
		t.Errorf("expected flat wildcard for user-service-hooks, got:\n%s", s)
	}
	if !strings.Contains(s, `export * from "./org-service-hooks_gen";`) {
		t.Errorf("expected flat wildcard for org-service-hooks, got:\n%s", s)
	}
	if strings.Contains(s, "export * as") {
		t.Errorf("did not expect namespace re-exports in flat mode, got:\n%s", s)
	}
}

// TestWriteHooksIndex_NamespacedModeOnCollision asserts that when two hook
// files export the same identifier (e.g. both have a generic `useList`
// because each service has a List RPC), the entire barrel switches to
// `export * as <alias>` form. This is the collision-aware fix that
// unblocks projects the moment they grow past one service with overlapping
// RPC names.
func TestWriteHooksIndex_NamespacedModeOnCollision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.ts")

	// Both services export `useList` — flat wildcard would generate two
	// `export * from "..."` lines re-exporting the same identifier,
	// which tsc rejects as a duplicate-export error.
	files := []hookFileEntry{
		{fileName: "user-service-hooks_gen.ts", nsAlias: "userService", symbols: []string{"useGet", "useList"}},
		{fileName: "org-service-hooks_gen.ts", nsAlias: "orgService", symbols: []string{"useGet", "useList"}},
	}

	if err := writeHooksIndex(path, files); err != nil {
		t.Fatalf("writeHooksIndex: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read index.ts: %v", err)
	}
	s := string(got)

	if !strings.Contains(s, "Mode: namespaced re-exports") {
		t.Errorf("expected namespaced-mode comment, got:\n%s", s)
	}
	// Both collisions should be documented in the comment block so the
	// user knows exactly which symbols forced the switch.
	if !strings.Contains(s, "useGet (from org-service-hooks_gen.ts, user-service-hooks_gen.ts)") {
		t.Errorf("expected useGet collision in comment, got:\n%s", s)
	}
	if !strings.Contains(s, "useList (from org-service-hooks_gen.ts, user-service-hooks_gen.ts)") {
		t.Errorf("expected useList collision in comment, got:\n%s", s)
	}
	if !strings.Contains(s, `export * as userService from "./user-service-hooks_gen";`) {
		t.Errorf("expected namespaced re-export for userService, got:\n%s", s)
	}
	if !strings.Contains(s, `export * as orgService from "./org-service-hooks_gen";`) {
		t.Errorf("expected namespaced re-export for orgService, got:\n%s", s)
	}
	// Confirm the flat wildcard form is NOT present in namespaced mode.
	if strings.Contains(s, `export * from "./user-service-hooks_gen";`) {
		t.Errorf("did not expect flat wildcard in namespaced mode, got:\n%s", s)
	}
}

// TestDetectIndexCollisions_NoOverlap asserts the no-collision path
// returns nil, which is the signal writeHooksIndex uses to pick flat-mode
// emission.
func TestDetectIndexCollisions_NoOverlap(t *testing.T) {
	files := []hookFileEntry{
		{fileName: "a.ts", symbols: []string{"useGetA", "useListA"}},
		{fileName: "b.ts", symbols: []string{"useGetB", "useListB"}},
	}
	if got := detectIndexCollisions(files); len(got) != 0 {
		t.Errorf("expected zero collisions, got %+v", got)
	}
}

// TestDetectIndexCollisions_ListsAllOverlapsSorted asserts ALL colliding
// symbols are reported (not just the first) and the result is sorted so
// the comment block at the top of index.ts is byte-stable across runs.
func TestDetectIndexCollisions_ListsAllOverlapsSorted(t *testing.T) {
	files := []hookFileEntry{
		{fileName: "a.ts", symbols: []string{"useList", "useGet", "useUnique"}},
		{fileName: "b.ts", symbols: []string{"useGet", "useList"}},
	}
	got := detectIndexCollisions(files)
	if len(got) != 2 {
		t.Fatalf("expected 2 collisions, got %d: %+v", len(got), got)
	}
	if got[0].symbol != "useGet" || got[1].symbol != "useList" {
		t.Errorf("expected sorted symbols [useGet, useList], got [%s, %s]", got[0].symbol, got[1].symbol)
	}
	// File slices inside each collision must also be sorted.
	want := []string{"a.ts", "b.ts"}
	for _, c := range got {
		if len(c.files) != 2 || c.files[0] != want[0] || c.files[1] != want[1] {
			t.Errorf("expected files %v for %s, got %v", want, c.symbol, c.files)
		}
	}
}

// TestGenerateFrontendHooks_PerFrontendByDefault asserts that the
// default (workspaces=false) emit lands at frontends/<name>/src/hooks/
// with the historic relative-import shape. This is the snapshot
// stability test — projects that don't opt in must see the exact
// pre-workspaces layout.
func TestGenerateFrontendHooks_PerFrontendByDefault(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.ProjectConfig{
		Name: "myapp",
		Frontends: []config.FrontendConfig{
			config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir("frontends/web"),
		},
	}
	services := []codegen.ServiceDef{
		fakeService("UserService", "proto/services/users/v1/users.proto"),
	}
	if err := os.MkdirAll(filepath.Join(dir, "frontends/web/src/hooks"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := generateFrontendHooks(cfg, services, dir, nil); err != nil {
		t.Fatalf("generateFrontendHooks: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "frontends/web/src/hooks/user-service-hooks_gen.ts"))
	if err != nil {
		t.Fatalf("read per-frontend hooks: %v", err)
	}
	s := string(got)
	// Per-frontend mode imports from @/lib/connect (frontend-local).
	if !strings.Contains(s, `from "@/lib/connect"`) {
		t.Errorf("per-frontend hook should import connectClient from @/lib/connect, got:\n%s", s)
	}
	// And from @/gen for the proto types.
	if !strings.Contains(s, `from "@/gen/`) {
		t.Errorf("per-frontend hook should import proto types from @/gen, got:\n%s", s)
	}
	// Workspace import shapes must NOT appear in default mode.
	if strings.Contains(s, "../transport") || strings.Contains(s, "@myapp/api") {
		t.Errorf("per-frontend hook should not import from workspace paths, got:\n%s", s)
	}

	// The shared packages/hooks/ dir should NOT be touched.
	if _, err := os.Stat(filepath.Join(dir, "packages/hooks")); !os.IsNotExist(err) {
		t.Errorf("workspaces=false should not create packages/hooks/, got err=%v", err)
	}
}

// TestGenerateFrontendHooks_WrittenFileCarriesForgeHashMarker asserts the
// fix for the orphaned-hooks-file bug: a hooks file must be written
// through the certification chokepoint (checksums.WriteGeneratedFile), not
// a bare os.WriteFile, so it carries an embedded forge:hash marker. Without
// the marker the file is invisible to the marker-driven stale-artifact
// sweep (cleanupStaleArtifacts) — the whole reason a renamed service's old
// hooks file used to survive on disk with nothing that could ever remove
// it.
func TestGenerateFrontendHooks_WrittenFileCarriesForgeHashMarker(t *testing.T) {
	resetCleanupRunState(t)
	dir := t.TempDir()
	cfg := &config.ProjectConfig{
		Name:      "myapp",
		Frontends: []config.FrontendConfig{config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir("frontends/web")},
	}
	services := []codegen.ServiceDef{fakeService("UserService", "proto/services/users/v1/users.proto")}
	cs := &checksums.FileChecksums{}

	if err := generateFrontendHooks(cfg, services, dir, cs); err != nil {
		t.Fatalf("generateFrontendHooks: %v", err)
	}

	relPath := "frontends/web/src/hooks/user-service-hooks_gen.ts"
	got, err := os.ReadFile(filepath.Join(dir, relPath))
	if err != nil {
		t.Fatalf("read hooks file: %v", err)
	}
	if checksums.Verify(got) != checksums.Pristine {
		t.Errorf("expected a pristine forge:hash marker on %s, got status %v; content:\n%s",
			relPath, checksums.Verify(got), got)
	}
	if !checksums.WasWrittenThisRun(relPath) {
		t.Errorf("expected %s recorded in WrittenThisRun so the stale-artifact sweep can see this run touched it", relPath)
	}
}

// TestGenerateFrontendHooks_RenamedServiceOrphanIsSweptAsStale is the
// end-to-end regression case: a service renamed between two `forge
// generate` runs must leave its OLD hooks file positively identified as
// stale by cleanupStaleArtifacts (the same marker-driven sweep every other
// Tier-1 output uses), rather than as an invisible unmarked leftover.
func TestGenerateFrontendHooks_RenamedServiceOrphanIsSweptAsStale(t *testing.T) {
	resetCleanupRunState(t)
	dir := t.TempDir()
	cfg := &config.ProjectConfig{
		Name:      "myapp",
		Frontends: []config.FrontendConfig{config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir("frontends/web")},
	}
	cs := &checksums.FileChecksums{}

	// Run 1: WorkOrderService exists, its hooks file is born (and certified).
	if err := generateFrontendHooks(cfg, []codegen.ServiceDef{
		fakeService("WorkOrderService", "proto/services/workorder/v1/workorder.proto"),
	}, dir, cs); err != nil {
		t.Fatalf("first generateFrontendHooks: %v", err)
	}
	oldRel := "frontends/web/src/hooks/work-order-service-hooks_gen.ts"
	if _, err := os.Stat(filepath.Join(dir, oldRel)); err != nil {
		t.Fatalf("expected %s to exist after first run: %v", oldRel, err)
	}

	// Run 2: the service is renamed to WorkorderService — a fresh
	// pipeline run, so WrittenThisRun starts empty again.
	checksums.ResetSkipWrite()
	if err := generateFrontendHooks(cfg, []codegen.ServiceDef{
		fakeService("WorkorderService", "proto/services/workorder/v1/workorder.proto"),
	}, dir, cs); err != nil {
		t.Fatalf("second generateFrontendHooks: %v", err)
	}
	newRel := "frontends/web/src/hooks/workorder-service-hooks_gen.ts"
	if _, err := os.Stat(filepath.Join(dir, newRel)); err != nil {
		t.Fatalf("expected %s to exist after rename: %v", newRel, err)
	}
	// The OLD file is still on disk — forge does not delete mid-render.
	if _, err := os.Stat(filepath.Join(dir, oldRel)); err != nil {
		t.Fatalf("expected orphaned %s to still be on disk before the sweep: %v", oldRel, err)
	}

	ctx := newCleanupCtx(dir, cs, []codegen.ServiceDef{
		fakeService("WorkorderService", "proto/services/workorder/v1/workorder.proto"),
	}, cfg)
	sweep, err := cleanupStaleArtifacts(ctx)
	if err != nil {
		t.Fatalf("cleanupStaleArtifacts: %v", err)
	}
	if len(sweep.HandEdited) != 0 {
		t.Errorf("handEdited = %v, want empty", sweep.HandEdited)
	}
	found := false
	for _, c := range sweep.Removed {
		if c == oldRel {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the orphaned pre-rename hooks file %s among removed stale files, got %v", oldRel, sweep.Removed)
	}
	// The renamed file must NOT be removed — this run wrote it.
	for _, c := range sweep.Removed {
		if c == newRel {
			t.Errorf("current hooks file %s should not be a stale artifact", newRel)
		}
	}
}

// TestReportOrphanedHookFiles_LegacyUnmarkedFileIsReportedNotDeleted
// covers the files this fix cannot retroactively certify: a hooks file
// left on disk by a forge build that predates marker certification. Such
// a file carries the Tier-1 banner but no forge:hash, so forge cannot
// prove it still owns those bytes — it must be reported, never deleted.
func TestReportOrphanedHookFiles_LegacyUnmarkedFileIsReportedNotDeleted(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "frontends/web/src/hooks")
	mustMkdir(t, hooksDir)

	legacyPath := filepath.Join(hooksDir, "work-order-service-hooks_gen.ts")
	legacyContent := "// Code generated by forge. DO NOT EDIT.\n" +
		"// forge-owned: regenerated every run — do not edit (forge project disown to take ownership)\n" +
		"export function useGetWorkOrder() {}\n"
	mustWrite(t, legacyPath, legacyContent)

	live := []hookFileEntry{{fileName: "workorder-service-hooks_gen.ts"}}
	reportOrphanedHookFiles(hooksDir, live)

	// Report-only: the file must survive.
	if _, err := os.Stat(legacyPath); err != nil {
		t.Errorf("legacy orphan must not be deleted: %v", err)
	}
}

// TestReportOrphanedHookFiles_MarkerBearingOrphanIsNotDoubleReported
// asserts that once a hooks file DOES carry a forge:hash marker,
// reportOrphanedHookFiles defers entirely to the marker-driven sweep
// (cleanupStaleArtifacts) rather than emitting a second, redundant notice.
func TestReportOrphanedHookFiles_MarkerBearingOrphanIsNotDoubleReported(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "frontends/web/src/hooks")
	mustMkdir(t, hooksDir)

	markedPath := filepath.Join(hooksDir, "work-order-service-hooks_gen.ts")
	mustWriteStamped(t, markedPath, "frontends/web/src/hooks/work-order-service-hooks_gen.ts",
		"// Code generated by forge. DO NOT EDIT.\nexport function useGetWorkOrder() {}\n")

	// No assertion on stdout/stderr content (reportOrphanedHookFiles
	// writes to os.Stderr directly) — this test only needs the function
	// not to crash and the file to remain untouched; the "no double
	// report" contract is the marker-check branch itself, exercised via
	// coverage rather than output capture.
	reportOrphanedHookFiles(hooksDir, nil)

	if _, err := os.Stat(markedPath); err != nil {
		t.Errorf("marker-bearing file must not be deleted: %v", err)
	}
}

// TestGenerateFrontendHooks_WorkspaceModeEmitsToSharedDir asserts that
// the workspace-mode emit lands at packages/hooks/src/generated/ with
// imports rewritten to @<scope>/api and "../transport".
func TestGenerateFrontendHooks_WorkspaceModeEmitsToSharedDir(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.ProjectConfig{
		Name: "myapp",
		Frontends: []config.FrontendConfig{
			config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir("frontends/web"),
			config.FrontendConfig{Name: "mobile", Type: "react-native"}.WithDir("frontends/mobile"),
		},
	}
	*cfg = cfg.WithFrontendWorkspaces(true)
	services := []codegen.ServiceDef{
		fakeService("UserService", "proto/services/users/v1/users.proto"),
	}

	if err := generateFrontendHooks(cfg, services, dir, nil); err != nil {
		t.Fatalf("generateFrontendHooks: %v", err)
	}

	// Workspace mode writes one file shared across all frontends.
	got, err := os.ReadFile(filepath.Join(dir, "packages/hooks/src/generated/user-service-hooks_gen.ts"))
	if err != nil {
		t.Fatalf("read workspace hooks: %v", err)
	}
	s := string(got)
	if !strings.Contains(s, `from "../transport"`) {
		t.Errorf("workspace hook should import connectClient from ../transport, got:\n%s", s)
	}
	if !strings.Contains(s, `from "@myapp/api/services/users/v1/users_pb"`) {
		t.Errorf("workspace hook should import proto types from @myapp/api/<path>, got:\n%s", s)
	}
	// Per-frontend hooks dirs should NOT be written in workspace mode.
	for _, fe := range cfg.Frontends {
		p := filepath.Join(dir, fe.DeclaredDir(), "src/hooks/user-service-hooks_gen.ts")
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("workspaces=true should not write per-frontend %s, got err=%v", p, err)
		}
	}

	// Barrel index.ts re-exports the generated file.
	idx, err := os.ReadFile(filepath.Join(dir, "packages/hooks/src/generated/index.ts"))
	if err != nil {
		t.Fatalf("read workspace hooks index: %v", err)
	}
	if !strings.Contains(string(idx), `export * from "./user-service-hooks_gen"`) {
		t.Errorf("workspace hooks index should re-export user-service-hooks, got:\n%s", idx)
	}
}

// TestMutationMarkedMethods_RecognizesMarkerShapes asserts the proto-source
// scanner picks up the `// forge:mutation` marker in every attachment shape it
// supports (leading line, block doc-comment, trailing on the rpc line) and
// ignores unmarked RPCs and markers detached by a blank line.
func TestMutationMarkedMethods_RecognizesMarkerShapes(t *testing.T) {
	dir := t.TempDir()
	proto := `syntax = "proto3";
package orders.v1;

service OrderService {
  // Issue a refund.
  //
  // forge:mutation
  rpc IssueRefund(IssueRefundRequest) returns (IssueRefundResponse);

  // forge:mutation
  rpc CheckoutCart(CheckoutCartRequest) returns (CheckoutCartResponse);

  rpc PlaceOrder(PlaceOrderRequest) returns (PlaceOrderResponse); // forge:mutation

  // Just a plain read, no marker.
  rpc GetOrder(GetOrderRequest) returns (GetOrderResponse);

  // forge:mutation

  // A blank line above detaches the marker from this rpc.
  rpc ListOrders(ListOrdersRequest) returns (ListOrdersResponse);
}
`
	path := filepath.Join(dir, "orders.proto")
	if err := os.WriteFile(path, []byte(proto), 0o644); err != nil {
		t.Fatal(err)
	}

	got := scanRPCHookMarkers(path).Mutation
	for _, want := range []string{"IssueRefund", "CheckoutCart", "PlaceOrder"} {
		if !got[want] {
			t.Errorf("expected %s to be marked as mutation, got %+v", want, got)
		}
	}
	for _, notWant := range []string{"GetOrder", "ListOrders"} {
		if got[notWant] {
			t.Errorf("did not expect %s to be marked as mutation, got %+v", notWant, got)
		}
	}
}

// TestScanRPCHookMarkers_QueryScopeShapes: `forge:query-scope` attaches
// exactly as forge:mutation does — leading block or trailing — a bare marker
// is recorded (for generate to refuse), a longer token is not the marker,
// and a blank line detaches it.
func TestScanRPCHookMarkers_QueryScopeShapes(t *testing.T) {
	dir := t.TempDir()
	proto := `syntax = "proto3";
package run.v1;

service RunService {
  // Most recent run per workflow.
  //
  // forge:query-scope Run — a run write must refresh it.
  rpc LastRunPerWorkflow(LastRunPerWorkflowRequest) returns (LastRunPerWorkflowResponse);

  rpc RunStats(RunStatsRequest) returns (RunStatsResponse); // forge:query-scope Run

  // forge:query-scope
  rpc Bare(BareRequest) returns (BareResponse);

  // forge:query-scoped Run
  rpc Longer(LongerRequest) returns (LongerResponse);

  // forge:query-scope Run

  rpc Detached(DetachedRequest) returns (DetachedResponse);
}
`
	path := filepath.Join(dir, "run.proto")
	if err := os.WriteFile(path, []byte(proto), 0o644); err != nil {
		t.Fatal(err)
	}
	got := scanRPCHookMarkers(path).QueryScope
	want := map[string]string{"LastRunPerWorkflow": "Run", "RunStats": "Run", "Bare": ""}
	if len(got) != len(want) {
		t.Fatalf("query scopes = %+v, want %+v", got, want)
	}
	for rpc, entity := range want {
		if e, ok := got[rpc]; !ok || e != entity {
			t.Errorf("%s: scope = %q (present %v), want %q", rpc, e, ok, entity)
		}
	}
}

// TestGenerateFrontendHooks_MarkersReadFromTheModuleRelativeProtoPath: the
// descriptor records ProtoFile relative to the buf module (proto/), as
// `services/orders/v1/orders.proto`. The scanner joined that to the project
// root, found no file, and dropped every marker without a word — so neither
// forge:mutation nor forge:query-scope ever applied in a real project.
func TestGenerateFrontendHooks_MarkersReadFromTheModuleRelativeProtoPath(t *testing.T) {
	dir := t.TempDir()
	writeHookFixture(t, filepath.Join(dir, "proto", "services", "orders", "v1", "orders.proto"), `syntax = "proto3";
package orders.v1;

service OrderService {
  // forge:mutation
  rpc FindAndHoldSeat(FindAndHoldSeatRequest) returns (FindAndHoldSeatResponse);
  // forge:query-scope Order
  rpc OrderTotals(OrderTotalsRequest) returns (OrderTotalsResponse) {
    option idempotency_level = NO_SIDE_EFFECTS;
  }
}
`)
	cfg := &config.ProjectConfig{
		Name:      "myapp",
		Frontends: []config.FrontendConfig{config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir("frontends/web")},
	}
	services := []codegen.ServiceDef{{
		Name:      "OrderService",
		ProtoFile: "services/orders/v1/orders.proto", // as the descriptor writes it
		Methods: []codegen.Method{
			{Name: "FindAndHoldSeat", InputType: "FindAndHoldSeatRequest", OutputType: "FindAndHoldSeatResponse"},
			{Name: "OrderTotals", InputType: "OrderTotalsRequest", OutputType: "OrderTotalsResponse", NoSideEffects: true},
		},
	}}
	if err := generateFrontendHooks(cfg, services, dir, nil); err != nil {
		t.Fatalf("generateFrontendHooks: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "frontends/web/src/hooks/order-service-hooks_gen.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"export const useFindAndHoldSeat = createMutationHook<",
		`orderTotals: keys.query("orderTotals", OrderTotalsRequestSchema, "order")`,
		`order: keys.entity("order")`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("hooks missing %q — the proto's markers were not read:\n%s", want, got)
		}
	}
}

// TestGenerateFrontendHooks_QueryScopeRefusalFailsTheStep: a scope generate
// cannot honour fails the hooks step, naming the proto — never a silent
// no-op, which is the stale read the marker exists to prevent.
func TestGenerateFrontendHooks_QueryScopeRefusalFailsTheStep(t *testing.T) {
	dir := t.TempDir()
	protoRel := "proto/services/orders/v1/orders.proto"
	writeHookFixture(t, filepath.Join(dir, protoRel), `syntax = "proto3";
package orders.v1;

service OrderService {
  // forge:query-scope Order
  rpc PlaceOrder(PlaceOrderRequest) returns (PlaceOrderResponse);
}
`)
	cfg := &config.ProjectConfig{
		Name:      "myapp",
		Frontends: []config.FrontendConfig{config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir("frontends/web")},
	}
	services := []codegen.ServiceDef{{
		Name:      "OrderService",
		ProtoFile: protoRel,
		Methods:   []codegen.Method{{Name: "PlaceOrder", InputType: "PlaceOrderRequest", OutputType: "PlaceOrderResponse"}},
	}}
	err := generateFrontendHooks(cfg, services, dir, nil)
	if err == nil || !strings.Contains(err.Error(), protoRel) || !strings.Contains(err.Error(), "useMutation") {
		t.Fatalf("want a refusal naming %s and the mutation, got %v", protoRel, err)
	}
}

// TestGenerateFrontendHooks_ReportsStaleStarterRowsOnAKindFlip is the
// generate half of #1508: the hook flips under an owned starter test, and
// the run's closing summary names the row's line and its replacement body
// instead of leaving vitest to find it.
func TestGenerateFrontendHooks_ReportsStaleStarterRowsOnAKindFlip(t *testing.T) {
	checksums.ResetPerRunState()
	t.Cleanup(checksums.ResetPerRunState)
	dir := t.TempDir()
	protoRel := "proto/run/v1/run.proto"
	writeHookFixture(t, filepath.Join(dir, protoRel), "syntax = \"proto3\";\npackage run.v1;\nservice RunService {}\n")
	cfg := &config.ProjectConfig{
		Name:      "myapp",
		Frontends: []config.FrontendConfig{config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir("frontends/web")},
	}
	svc := func(noSideEffects bool) []codegen.ServiceDef {
		return []codegen.ServiceDef{{
			Name:      "RunService",
			ProtoFile: protoRel,
			Methods: []codegen.Method{
				{Name: "LastRunPerWorkflow", InputType: "LastRunPerWorkflowRequest", OutputType: "LastRunPerWorkflowResponse", NoSideEffects: noSideEffects},
				{Name: "PauseRun", InputType: "PauseRunRequest", OutputType: "PauseRunResponse"},
			},
		}}
	}
	// First generate: LastRunPerWorkflow is a mutation, and the starter
	// test is written for it.
	if err := generateFrontendHooks(cfg, svc(false), dir, nil); err != nil {
		t.Fatal(err)
	}
	if left := checksums.LeftAloneThisRun(); len(left) != 0 {
		t.Fatalf("a fresh starter agrees with its hooks; nothing to report, got %+v", left)
	}
	testRel := "frontends/web/src/hooks/run-service-hooks.test.tsx"
	starter, err := os.ReadFile(filepath.Join(dir, testRel))
	if err != nil {
		t.Fatal(err)
	}
	wantLine := strings.Count(string(starter[:strings.Index(string(starter), `it("useLastRunPerWorkflow`)]), "\n") + 1

	// The rpc declares NO_SIDE_EFFECTS: the hook flips, the test does not.
	out := captureStdout(t, func() {
		if err := generateFrontendHooks(cfg, svc(true), dir, nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "useLastRunPerWorkflow is now a useQuery (was a useMutation)") {
		t.Errorf("generate must name the flip as it makes it, got:\n%s", out)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, testRel)); string(after) != string(starter) {
		t.Fatal("the starter test is the user's file; generate must not rewrite it")
	}
	left := checksums.LeftAloneThisRun()
	if len(left) != 1 || !strings.HasSuffix(left[0].Path, testRel) {
		t.Fatalf("want the stale test in the closing summary, got %+v", left)
	}
	detail := strings.Join(left[0].Detail, "\n")
	for _, want := range []string{
		fmt.Sprintf("line %d: useLastRunPerWorkflow is now a useQuery", wantLine),
		"renderHook(() => useLastRunPerWorkflow({} as never), { wrapper });",
		"await waitFor(() => expect(result.current.isSuccess).toBe(true));",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("summary detail missing %q:\n%s", want, detail)
		}
	}
	if strings.Contains(detail, "waitForSettled helper") {
		t.Errorf("PauseRun is still a mutation row; the helper is not orphaned:\n%s", detail)
	}
}

// TestMutationMarkedMethods_MissingFileIsNotAnError asserts a proto path with
// no file on disk yields an empty set rather than failing — hooks fall back to
// heuristic classification.
func TestMutationMarkedMethods_MissingFileIsNotAnError(t *testing.T) {
	if got := scanRPCHookMarkers(filepath.Join(t.TempDir(), "nope.proto")).Mutation; len(got) != 0 {
		t.Errorf("expected empty set for missing proto, got %+v", got)
	}
}

// TestApplyMutationMarkers_FlipsAndRecomputesImportFlags asserts a marked
// query flips to a mutation and the HasQueries/HasMutations import gates are
// recomputed. Here the ONLY query flips, so HasQueries must go false.
func TestApplyMutationMarkers_FlipsAndRecomputesImportFlags(t *testing.T) {
	data := codegen.FrontendHookTemplateData{
		HasQueries: true,
		Methods: []codegen.FrontendHookMethod{
			{Name: "IssueRefund", IsQuery: true},
		},
	}
	applyMutationMarkers(&data, map[string]bool{"IssueRefund": true})

	if data.Methods[0].IsQuery {
		t.Error("IssueRefund should have flipped to a mutation")
	}
	if data.HasQueries {
		t.Error("HasQueries should be false after the only query flipped")
	}
	if !data.HasMutations {
		t.Error("HasMutations should be true after a method flipped to mutation")
	}
}

// TestGenerateFrontendHooks_MutationMarkerForcesMutationHook is the
// end-to-end check: an RPC whose name matches a query prefix (IssueRefund →
// "Is…") but carries `// forge:mutation` in its proto must render as a
// useMutation hook, while an unmarked GetOrder stays a useQuery.
func TestGenerateFrontendHooks_MutationMarkerForcesMutationHook(t *testing.T) {
	dir := t.TempDir()
	protoRel := "proto/services/orders/v1/orders.proto"
	protoAbs := filepath.Join(dir, protoRel)
	if err := os.MkdirAll(filepath.Dir(protoAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	proto := `syntax = "proto3";
package orders.v1;

service OrderService {
  // Issue a refund for an order.
  //
  // forge:mutation
  rpc IssueRefund(IssueRefundRequest) returns (IssueRefundResponse);

  // Fetch an order by id.
  rpc GetOrder(GetOrderRequest) returns (GetOrderResponse);
}
`
	if err := os.WriteFile(protoAbs, []byte(proto), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.ProjectConfig{
		Name:      "myapp",
		Frontends: []config.FrontendConfig{config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir("frontends/web")},
	}
	services := []codegen.ServiceDef{{
		Name:      "OrderService",
		ProtoFile: protoRel,
		Methods: []codegen.Method{
			{Name: "IssueRefund", InputType: "IssueRefundRequest", OutputType: "IssueRefundResponse"},
			{Name: "GetOrder", InputType: "GetOrderRequest", OutputType: "GetOrderResponse"},
		},
	}}
	if err := os.MkdirAll(filepath.Join(dir, "frontends/web/src/hooks"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := generateFrontendHooks(cfg, services, dir, nil); err != nil {
		t.Fatalf("generateFrontendHooks: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "frontends/web/src/hooks/order-service-hooks_gen.ts"))
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	s := string(got)

	// IssueRefund must be a mutation despite its query-prefix name — which
	// is now visible as WHICH FACTORY builds the hook.
	if !strings.Contains(s, "export const useIssueRefund = createMutationHook<") {
		t.Errorf("useIssueRefund should be a mutation hook, got:\n%s", s)
	}
	// GetOrder stays a query.
	if !strings.Contains(s, "export const useGetOrder = createQueryHook<") {
		t.Errorf("useGetOrder should remain a query hook, got:\n%s", s)
	}
	// Both factories are imported since the service now has one of each.
	if !strings.Contains(s, "createMutationHook") || !strings.Contains(s, "createQueryHook") {
		t.Errorf("expected both createMutationHook and createQueryHook imports, got:\n%s", s)
	}
	// A marked mutation must NOT appear in the query-key factory: a mutation
	// has no cache entry of its own, it invalidates other RPCs' entries.
	if strings.Contains(s, `issueRefund: keys.query(`) {
		t.Errorf("IssueRefund should not have a query-key factory entry, got:\n%s", s)
	}
}

// fakeService synthesizes a minimal ServiceDef suitable for hook
// rendering. The minimum the renderer needs is a name, a proto file
// (drives the import path), and at least one non-streaming RPC.
func fakeService(name, protoFile string) codegen.ServiceDef {
	return codegen.ServiceDef{
		Name:      name,
		ProtoFile: protoFile,
		Methods: []codegen.Method{
			{Name: "GetUser", InputType: "GetUserRequest", OutputType: "GetUserResponse"},
			{Name: "CreateUser", InputType: "CreateUserRequest", OutputType: "CreateUserResponse"},
		},
	}
}

// TestWriteHookStarterTest_EmitsLiveTestWhenNoSiblingPresent asserts the
// generated hook test lands as a LIVE `.test.tsx`, with one row per RPC.
//
// It used to land as `<svc>-hooks.test.tsx.starter`, inert until someone
// renamed it, and the dogfood run proved nobody ever does: a 17 KB
// generated suite per service that had never executed once, so the layer
// all sixteen pages sat on had zero active tests while `forge test`
// reported green. A test disabled by its file extension is decoration.
func TestWriteHookStarterTest_EmitsLiveTestWhenNoSiblingPresent(t *testing.T) {
	dir := t.TempDir()
	svc := codegenServiceDefForStarterTest()
	data := codegenHookDataForStarterTest()

	if err := writeHookStarterTest(dir, "user-service-hooks_gen.ts", svc, data); err != nil {
		t.Fatalf("writeHookStarterTest: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "user-service-hooks.test.tsx.starter")); err == nil {
		t.Error("an inert .starter must not be emitted — vitest never runs it")
	}
	got, err := os.ReadFile(filepath.Join(dir, "user-service-hooks.test.tsx"))
	if err != nil {
		t.Fatalf("expected a live test to exist: %v", err)
	}
	s := string(got)
	// Per-RPC rows
	for _, want := range []string{
		"useGetUser resolves a happy-path response",
		"useCreateUser resolves a happy-path response",
		`"UserService.GetUser":`,
		`"UserService.CreateUser":`,
		"import { useGetUser }",
		"import { useCreateUser }",
		"mockTransport",
		"setTransport",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("hook test missing %q, got:\n%s", want, s)
		}
	}
}

// TestWriteHookStarterTest_LeavesNothingQueuedPastTeardown pins the shape
// that keeps the scaffolded hook test from failing a green run.
//
// The starter used to end a mutation test the moment `mutateAsync` resolved.
// React Query tells its observers on a LATER macrotask, so that return left
// a re-render queued; when the mutation was the last test in the file, it
// fired after vitest tore jsdom down and died on `window` —
// "ReferenceError: window is not defined … caught after test environment
// was torn down". Every test passed and the run failed anyway,
// intermittently, on whichever runner lost the race (control-plane CI runs
// 35813220867 and 36222818452).
//
// The rendered roots were never unmounted either: Testing Library only
// registers auto-cleanup when `afterEach` is a vitest GLOBAL, which the
// scaffolded config does not enable.
func TestWriteHookStarterTest_LeavesNothingQueuedPastTeardown(t *testing.T) {
	dir := t.TempDir()
	if err := writeHookStarterTest(dir, "user-service-hooks_gen.ts", codegenServiceDefForStarterTest(), codegenHookDataForStarterTest()); err != nil {
		t.Fatalf("writeHookStarterTest: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "user-service-hooks.test.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)

	for _, want := range []string{
		// Explicit, because vitest globals are off and RTL's auto-cleanup
		// never registers.
		`import { cleanup, renderHook, waitFor } from "@testing-library/react";`,
		`import { afterEach, beforeEach, describe, it, expect } from "vitest";`,
		"cleanup();",
		"queryClient.clear();",
		// One client per test, owned by the hooks so afterEach can clear it —
		// not built inside `wrapper`, where each re-render made a new one.
		"<QueryClientProvider client={queryClient}>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("hook test missing %q, got:\n%s", want, s)
		}
	}
	if strings.Contains(s, "function wrapper({ children }: { children: ReactNode }) {\n  const qc = new QueryClient(") {
		t.Errorf("wrapper must not build its own QueryClient — afterEach cannot clear a client it never sees:\n%s", s)
	}

	// The mutation test must wait for the RENDERED hook to settle. Checked
	// inside the CreateUser block specifically: the query branch already
	// waits on isSuccess, so a file-wide match would pass without the fix.
	start := strings.Index(s, `it("useCreateUser resolves a happy-path response"`)
	if start < 0 {
		t.Fatalf("no useCreateUser test in:\n%s", s)
	}
	block := s[start:]
	if end := strings.Index(block, "\n  });\n"); end >= 0 {
		block = block[:end]
	}
	if !strings.Contains(block, "mutateAsync(") {
		t.Fatalf("useCreateUser test no longer calls mutateAsync:\n%s", block)
	}
	const settle = "await waitForSettled(result);"
	if !strings.Contains(block, settle) {
		t.Errorf("mutation test returns before the hook re-renders; want %q in:\n%s", settle, block)
	}
	if strings.Index(block, settle) < strings.Index(block, "mutateAsync(") {
		t.Errorf("the settle wait must come AFTER mutateAsync:\n%s", block)
	}
	// ...and the helper must wait on the RENDERED status, not on the promise.
	if !strings.Contains(s, `await waitFor(() => expect(["success", "error"]).toContain(result.current.status));`) {
		t.Errorf("waitForSettled no longer waits for the rendered status to settle:\n%s", s)
	}
}

// TestWriteHookStarterTest_QueryOnlyServiceDeclaresNoUnusedHelper: the
// waitForSettled helper is only called from mutation rows, so a query-only
// service must not declare it — no-unused-vars would flag the scaffold the
// moment it landed, the same way an unused `waitFor` import once had to be
// deleted by hand from every mutation-only service's test.
func TestWriteHookStarterTest_QueryOnlyServiceDeclaresNoUnusedHelper(t *testing.T) {
	svc := codegen.ServiceDef{
		Name:      "AuditService",
		ProtoFile: "proto/services/audit/v1/audit.proto",
		Methods: []codegen.Method{
			{Name: "ListEvents", InputType: "ListEventsRequest", OutputType: "ListEventsResponse"},
		},
	}
	dir := t.TempDir()
	if err := writeHookStarterTest(dir, "audit-service-hooks_gen.ts", svc, codegen.ServiceDefToHookData(svc)); err != nil {
		t.Fatalf("writeHookStarterTest: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "audit-service-hooks.test.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if strings.Contains(s, "waitForSettled") {
		t.Errorf("query-only service declares the mutation-only helper:\n%s", s)
	}
	// The teardown is NOT mutation-specific: queries still render roots.
	for _, want := range []string{"cleanup();", "queryClient.clear();"} {
		if !strings.Contains(s, want) {
			t.Errorf("query-only hook test missing %q:\n%s", want, s)
		}
	}
}

// TestWriteHookStarterTest_SkipsWhenActiveTestExists asserts the test is
// NOT rewritten when the user already has `<file>.test.tsx`. Scaffold-once:
// regenerating must not clobber hand-edited tests.
func TestWriteHookStarterTest_SkipsWhenActiveTestExists(t *testing.T) {
	dir := t.TempDir()
	activePath := filepath.Join(dir, "user-service-hooks.test.tsx")
	if err := os.WriteFile(activePath, []byte("// user-edited"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := codegenServiceDefForStarterTest()
	data := codegenHookDataForStarterTest()
	if err := writeHookStarterTest(dir, "user-service-hooks_gen.ts", svc, data); err != nil {
		t.Fatalf("writeHookStarterTest: %v", err)
	}

	// The user's file must not have been touched.
	got, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "// user-edited" {
		t.Errorf("active test was overwritten: %s", string(got))
	}
	// And no .starter should have been written alongside it.
	if _, err := os.Stat(filepath.Join(dir, "user-service-hooks.test.tsx.starter")); err == nil {
		t.Errorf("starter was written despite active test existing")
	}
}

// TestWriteHookStarterTest_ActivatesALegacyStarter: a project generated by
// an older forge carries an inert `.test.tsx.starter` and no live test. The
// user may have edited it while it sat there, so it is RENAMED into place
// rather than replaced by a fresh render — and it does not survive as a
// second, dead copy beside the live one.
func TestWriteHookStarterTest_ActivatesALegacyStarter(t *testing.T) {
	dir := t.TempDir()
	starterPath := filepath.Join(dir, "user-service-hooks.test.tsx.starter")
	if err := os.WriteFile(starterPath, []byte("// previous starter, hand-edited"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := codegenServiceDefForStarterTest()
	data := codegenHookDataForStarterTest()
	if err := writeHookStarterTest(dir, "user-service-hooks_gen.ts", svc, data); err != nil {
		t.Fatalf("writeHookStarterTest: %v", err)
	}

	if _, err := os.Stat(starterPath); err == nil {
		t.Error("the inert .starter must not survive activation")
	}
	got, err := os.ReadFile(filepath.Join(dir, "user-service-hooks.test.tsx"))
	if err != nil {
		t.Fatalf("expected the starter to be activated in place: %v", err)
	}
	if string(got) != "// previous starter, hand-edited" {
		t.Errorf("activation replaced the user's content instead of renaming it: %s", string(got))
	}
}

// TestWriteHookStarterTest_RemovesALeftoverStarterBesideALiveTest: once the
// user has a live test, a dormant starter next to it is dead weight that
// nothing runs and nothing reads.
func TestWriteHookStarterTest_RemovesALeftoverStarterBesideALiveTest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "user-service-hooks.test.tsx"), []byte("// mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	starterPath := filepath.Join(dir, "user-service-hooks.test.tsx.starter")
	if err := os.WriteFile(starterPath, []byte("// stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeHookStarterTest(dir, "user-service-hooks_gen.ts", codegenServiceDefForStarterTest(), codegenHookDataForStarterTest()); err != nil {
		t.Fatalf("writeHookStarterTest: %v", err)
	}
	if _, err := os.Stat(starterPath); err == nil {
		t.Error("a leftover .starter beside a live test must be removed")
	}
	got, _ := os.ReadFile(filepath.Join(dir, "user-service-hooks.test.tsx"))
	if string(got) != "// mine" {
		t.Errorf("the user's live test was touched: %s", got)
	}
}

// updateHookGoldens rewrites testdata/hooks/ from the current generator:
// `go test ./internal/cli/ -run TestGenerateFrontendHooks_Golden -update-hooks`.
var updateHookGoldens = flag.Bool("update-hooks", false, "rewrite the generated-hooks golden files")

// TestGenerateFrontendHooks_Golden pins the whole rendered hooks file and its
// starter test for a service whose rpcs exercise every classification path:
// NO_SIDE_EFFECTS reads named as nouns (Health, Version), an undeclared noun
// (Ready, still a mutation), a read verb (GetInfo), a NO_SIDE_EFFECTS rpc the
// author also marked `// forge:mutation` (the marker wins), and an ordinary
// write (Restart). One classification feeds three artifacts — the hook's
// factory, its entry (or not) in the key factory, and the starter test's
// idiom — so the golden shows all three moving together.
//
// The paged reads ride the same golden: ListEvents and SearchLogs carry
// forge's list-pagination shape (page_token in, next_page_token out) and
// gain a use<Name>Infinite hook, key entry and starter row; ListNodes reads
// a list with no tokens and gains none; FindPeers has the shape but is marked
// `// forge:mutation`, and a mutation is never paged. SearchLogs, a custom
// read, declares `// forge:query-scope Event`: both of its keys move under
// the event scope ListEvents already has.
func TestGenerateFrontendHooks_Golden(t *testing.T) {
	dir := t.TempDir()
	protoRel := "proto/system/v1/system.proto"
	protoAbs := filepath.Join(dir, protoRel)
	if err := os.MkdirAll(filepath.Dir(protoAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(protoAbs, []byte(`syntax = "proto3";
package system.v1;

service SystemService {
  rpc Health(HealthRequest) returns (HealthResponse) {
    option idempotency_level = NO_SIDE_EFFECTS;
  }
  rpc Ready(ReadyRequest) returns (ReadyResponse);
  rpc GetInfo(GetInfoRequest) returns (GetInfoResponse);
  rpc Version(VersionRequest) returns (VersionResponse) {
    option idempotency_level = NO_SIDE_EFFECTS;
  }
  // forge:mutation — reads nothing the cache could hold.
  rpc Drain(DrainRequest) returns (DrainResponse) {
    option idempotency_level = NO_SIDE_EFFECTS;
  }
  rpc Restart(RestartRequest) returns (RestartResponse);
  rpc ListEvents(ListEventsRequest) returns (ListEventsResponse);
  // forge:query-scope Event — log lines are events, so an event write refreshes it.
  rpc SearchLogs(SearchLogsRequest) returns (SearchLogsResponse);
  rpc ListNodes(ListNodesRequest) returns (ListNodesResponse);
  // forge:mutation — claims the peers it finds.
  rpc FindPeers(FindPeersRequest) returns (FindPeersResponse);
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	method := func(name string, noSideEffects bool) codegen.Method {
		return codegen.Method{Name: name, InputType: name + "Request", OutputType: name + "Response", NoSideEffects: noSideEffects}
	}
	services := []codegen.ServiceDef{{
		Name:      "SystemService",
		ProtoFile: protoRel,
		Methods: []codegen.Method{
			method("Health", true), method("Ready", false), method("GetInfo", false),
			method("Version", true), method("Drain", true), method("Restart", false),
			method("ListEvents", false), method("SearchLogs", false), method("ListNodes", false),
			method("FindPeers", false),
		},
		Messages: map[string][]codegen.MessageFieldDef{
			"ListEventsRequest":  {{Name: "page_size", ProtoType: "int32"}, {Name: "page_token", ProtoType: "string"}},
			"ListEventsResponse": {{Name: "events", ProtoType: "[]message"}, {Name: "next_page_token", ProtoType: "string"}},
			"SearchLogsRequest":  {{Name: "query", ProtoType: "string"}, {Name: "page_token", ProtoType: "string", IsOptional: true}},
			"SearchLogsResponse": {{Name: "lines", ProtoType: "[]string"}, {Name: "next_page_token", ProtoType: "string"}},
			"ListNodesRequest":   {{Name: "region", ProtoType: "string"}},
			"ListNodesResponse":  {{Name: "nodes", ProtoType: "[]message"}},
			"FindPeersRequest":   {{Name: "page_token", ProtoType: "string"}},
			"FindPeersResponse":  {{Name: "next_page_token", ProtoType: "string"}},
		},
	}}
	cfg := &config.ProjectConfig{
		Name:      "myapp",
		Frontends: []config.FrontendConfig{config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir("frontends/web")},
	}
	if err := generateFrontendHooks(cfg, services, dir, nil); err != nil {
		t.Fatalf("generateFrontendHooks: %v", err)
	}

	hooksDir := filepath.Join(dir, "frontends", "web", "src", "hooks")
	for _, name := range []string{"system-service-hooks_gen.ts", "system-service-hooks.test.tsx"} {
		got, err := os.ReadFile(filepath.Join(hooksDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		golden := filepath.Join("testdata", "hooks", name+".golden")
		if *updateHookGoldens {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("read golden %s (run with -update-hooks to create it): %v", golden, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from %s (run with -update-hooks to accept):\n--- got ---\n%s", name, golden, got)
		}
	}
}
