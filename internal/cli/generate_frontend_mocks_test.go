package cli

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"text/template"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/templates"
)

// renderMockTransport parses and executes the frontend mocks/mock-transport
// template with the given entity data. Helper for the regression tests below.
func renderMockTransport(t *testing.T, entities []codegen.MockTransportEntity) string {
	t.Helper()
	tmplContent, err := templates.FrontendTemplates().Get(filepath.Join("mocks", "mock-transport.ts.tmpl"))
	if err != nil {
		t.Fatalf("read mock-transport template: %v", err)
	}
	tmpl, err := template.New("mock-transport.ts.tmpl").Funcs(templates.FuncMap()).Parse(string(tmplContent))
	if err != nil {
		t.Fatalf("parse mock-transport template: %v", err)
	}
	var buf bytes.Buffer
	data := codegen.NewMockTransportTemplateData(entities)
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("execute mock-transport template: %v", err)
	}
	return buf.String()
}

// TestMockTransport_ListOnlyEntity_DoesNotEmitEmptySchemaImports is the
// regression test for the kalshi-trader friction round's blocker: when an
// entity exposes only a List RPC (no Get / Create / Update / Delete), the
// template used to emit
//
//	import { ListTradesResponseSchema, Schema, Schema } from "@/gen/...";
//
// because `.GetResponseType` and `.CreateResponseType` were empty strings
// and the template concatenated `{{ .X }}Schema` unconditionally. The
// duplicate `Schema` identifier blocked the dashboard's Next.js build with
// TS2300 + TS2305 errors before any user code ran. Fix: gate each schema
// import on the matching `Has*` flag.
func TestMockTransport_ListOnlyEntity_DoesNotEmitEmptySchemaImports(t *testing.T) {
	entities := []codegen.MockTransportEntity{
		{
			EntityName:       "Trade",
			EntityNamePlural: "Trades",
			EntitySlug:       "trades",
			ServiceName:      "TradingService",
			ServiceTypeName:  "kalshi.v1.TradingService",
			ListRPC:          "ListTrades",
			HasList:          true,
			ImportPath:       "services/api/v1/api_pb",
			ListResponseType: "ListTradesResponse",
		},
		{
			EntityName:       "Hypothesis",
			EntityNamePlural: "Hypotheses",
			EntitySlug:       "hypotheses",
			ServiceName:      "TradingService",
			ServiceTypeName:  "kalshi.v1.TradingService",
			ListRPC:          "ListHypotheses",
			HasList:          true,
			ImportPath:       "services/api/v1/api_pb",
			ListResponseType: "ListHypothesesResponse",
		},
	}

	got := renderMockTransport(t, entities)

	// The exact pre-fix substring that broke the kalshi-trader build —
	// `Schema, Schema` was a duplicate-identifier TS2300 error AND was not
	// exported from api_pb (TS2305).
	if strings.Contains(got, "Schema, Schema") {
		t.Errorf("rendered template still contains duplicate `Schema, Schema` import — TS2300/TS2305 regression. Output:\n%s", got)
	}

	// The named imports should appear — these ARE exported and ARE used.
	for _, want := range []string{
		"ListTradesResponseSchema",
		"ListHypothesesResponseSchema",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected named import %q in rendered output, got:\n%s", want, got)
		}
	}

	// Symbols for absent RPCs must NOT appear — an entity with HasGet=false
	// has no GetResponseType, and importing a non-existent symbol breaks
	// the next.js build.
	for _, unwanted := range []string{
		"GetTradeResponseSchema",
		"CreateTradeResponseSchema",
		"GetHypothesisResponseSchema",
		"CreateHypothesisResponseSchema",
	} {
		if strings.Contains(got, unwanted) {
			t.Errorf("did not expect %q (entity has no Get/Create RPC) in rendered output, got:\n%s", unwanted, got)
		}
	}
}

// TestMockTransport_FullCRUD_EmitsAllSchemaImports asserts the canonical CRUD
// path still emits every needed Schema import. Guards against an overzealous
// fix to the import-gating block above breaking the standard scaffold path
// (Patient/Clinic-style projects where every entity has List/Get/Create/
// Update/Delete).
func TestMockTransport_FullCRUD_EmitsAllSchemaImports(t *testing.T) {
	entities := []codegen.MockTransportEntity{
		{
			EntityName:         "Patient",
			EntityNamePlural:   "Patients",
			EntitySlug:         "patients",
			ServiceName:        "ClinicService",
			ServiceTypeName:    "demo.v1.ClinicService",
			ListRPC:            "ListPatients",
			GetRPC:             "GetPatient",
			CreateRPC:          "CreatePatient",
			UpdateRPC:          "UpdatePatient",
			DeleteRPC:          "DeletePatient",
			HasList:            true,
			HasGet:             true,
			HasCreate:          true,
			HasUpdate:          true,
			HasDelete:          true,
			ImportPath:         "services/clinic/v1/clinic_pb",
			ListResponseType:   "ListPatientsResponse",
			GetResponseType:    "GetPatientResponse",
			CreateResponseType: "CreatePatientResponse",
		},
	}

	got := renderMockTransport(t, entities)

	for _, want := range []string{
		"ListPatientsResponseSchema",
		"GetPatientResponseSchema",
		"CreatePatientResponseSchema",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in full-CRUD output, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Schema, Schema") {
		t.Errorf("full-CRUD output unexpectedly contains `Schema, Schema`:\n%s", got)
	}
}

// TestMockTransport_BindsTransportVariableNotCastAtReturn is the regression
// test for the second kalshi-trader friction: the template used to return
// `{ async unary(...) {...}, async stream(...) {...} } as unknown as Transport`,
// and that trailing cast does NOT propagate Connect's `Transport` interface
// backwards into the callback parameter types. Under strict tsc every
// `unary` / `stream` parameter (method, signal, timeoutMs, header, input,
// contextValues) errored with TS7006 "Parameter X implicitly has an any
// type" — 12 errors per file. The fix: bind the object literal to a
// `const transport: Transport = { ... }` variable up front, then return it,
// so tsc has the interface available when checking the method bodies.
func TestMockTransport_BindsTransportVariableNotCastAtReturn(t *testing.T) {
	entities := []codegen.MockTransportEntity{
		{
			EntityName:       "Trade",
			EntityNamePlural: "Trades",
			EntitySlug:       "trades",
			ServiceName:      "TradingService",
			ServiceTypeName:  "kalshi.v1.TradingService",
			ListRPC:          "ListTrades",
			HasList:          true,
			ImportPath:       "services/api/v1/api_pb",
			ListResponseType: "ListTradesResponse",
		},
	}

	got := renderMockTransport(t, entities)

	// The Transport literal — and with it the TS7006 hazard — now lives in
	// @reliantlabs/forge-web-runtime/mock-transport, where
	// `const transport: Transport` binding is asserted by the package's own
	// tests. What the PROJECT file must still do is expose the same
	// entry point with the same signature, since connect.ts (scaffold-once,
	// never rewritten) calls it as `createMockTransport()` and
	// `createMockTransport(real)`.
	if !strings.Contains(got, "export function createMockTransport(fallback?: Transport): Transport {") {
		t.Errorf("expected the createMockTransport(fallback?) entry point connect.ts calls. Got:\n%s", got)
	}

	// The engine must arrive through the SUBPATH, never the barrel: the
	// fixtures and the dispatch engine have to be tree-shakeable out of a
	// production bundle.
	if !strings.Contains(got, `from "@reliantlabs/forge-web-runtime/mock-transport"`) {
		t.Errorf("expected the engine to be imported from the /mock-transport subpath. Got:\n%s", got)
	}
	if strings.Contains(got, `from "@reliantlabs/forge-web-runtime"`) {
		t.Errorf("mock transport must not import the barrel — that would anchor the fixtures in every bundle. Got:\n%s", got)
	}

	// The engine is re-exported under a local alias so the project's own
	// exported symbol keeps the name its callers use.
	if strings.Contains(got, "as unknown as Transport") {
		t.Errorf("template should not use `as unknown as Transport` cast (TS7006 on callback params). Got:\n%s", got)
	}
}

// TestMockTransport_StreamMethodHasNoExplicitReturnTypeAnnotation is the
// regression test for the kalshi-trader friction round's stream-typing
// blocker. The previous template annotated the stream method as
//
//	async stream(...): Promise<StreamResponse<never, never>> { ... }
//
// but the passthrough branch returned `fallback!.stream(...)` whose type
// is the generic `Promise<StreamResponse<I, O>>` — not assignable to the
// narrower `never, never` instantiation. Result: every `forge generate`
// re-rendered the file with a TS2322 error in the passthrough branch and
// blocked `npm run typecheck`. Fix: drop the explicit return-type
// annotation so tsc infers the per-callback signature from the outer
// `const transport: Transport = { ... }` binding (which was already in
// place for the unrelated TS7006 fix).
func TestMockTransport_StreamMethodHasNoExplicitReturnTypeAnnotation(t *testing.T) {
	entities := []codegen.MockTransportEntity{
		{
			EntityName:       "Trade",
			EntityNamePlural: "Trades",
			EntitySlug:       "trades",
			ServiceName:      "TradingService",
			ServiceTypeName:  "kalshi.v1.TradingService",
			ListRPC:          "ListTrades",
			HasList:          true,
			ImportPath:       "services/api/v1/api_pb",
			ListResponseType: "ListTradesResponse",
		},
	}

	got := renderMockTransport(t, entities)

	// The stream() method — and the TS2322 hazard its missing annotation
	// avoids — is library code now, guarded by the engine's own tests. The
	// project file must not carry a hand-rolled Transport implementation at
	// all: a re-inlined one is exactly the ~300 lines this extraction
	// removed, and it would drift from the tested engine silently.
	for _, reinlined := range []string{
		"async stream(method, signal, timeoutMs, header, input, contextValues)",
		"async unary(method, signal, timeoutMs, header, input, contextValues)",
		"const transport: Transport = {",
	} {
		if strings.Contains(got, reinlined) {
			t.Errorf("the project file must delegate to the engine, not re-implement Transport (%q). Got:\n%s", reinlined, got)
		}
	}

	// Streaming still has to WORK — it is the engine's job, reached through
	// the single entry point.
	if !strings.Contains(got, "createEngineTransport({") {
		t.Errorf("expected the project file to delegate to the engine's createMockTransport. Got:\n%s", got)
	}
}

// TestMockTransport_GroupsImportsByModule is the regression test for the
// kalshi-trader friction round's import-grouping nit: three entities
// (Trade, Hypothesis, Settlement) whose response schemas all lived in
// `@/gen/services/api/v1/api_pb` rendered as three separate back-to-
// back import statements rather than one merged one. Not a compile
// error (tsc dedups), but it tripped `import/order` and
// `import/no-duplicates` lint rules and bloated the diff. Fix: pre-
// aggregate the schema imports by ImportPath in
// NewMockTransportTemplateData so the template emits one
// merged `import { A, B, C } from "@/gen/<path>"` per source module.
func TestMockTransport_GroupsImportsByModule(t *testing.T) {
	entities := []codegen.MockTransportEntity{
		{
			EntityName:       "Trade",
			EntityNamePlural: "Trades",
			EntitySlug:       "trades",
			ListRPC:          "ListTrades",
			HasList:          true,
			ImportPath:       "services/api/v1/api_pb",
			ListResponseType: "ListTradesResponse",
		},
		{
			EntityName:       "Hypothesis",
			EntityNamePlural: "Hypotheses",
			EntitySlug:       "hypotheses",
			ListRPC:          "ListHypotheses",
			HasList:          true,
			ImportPath:       "services/api/v1/api_pb",
			ListResponseType: "ListHypothesesResponse",
		},
		{
			EntityName:       "Settlement",
			EntityNamePlural: "Settlements",
			EntitySlug:       "settlements",
			ListRPC:          "ListSettlements",
			HasList:          true,
			ImportPath:       "services/api/v1/api_pb",
			ListResponseType: "ListSettlementsResponse",
		},
	}

	got := renderMockTransport(t, entities)

	// Exactly one schema-import line should reference the shared
	// api_pb module — three would be a regression.
	const wantPath = `from "@/gen/services/api/v1/api_pb"`
	occurrences := strings.Count(got, wantPath)
	if occurrences != 1 {
		t.Errorf("expected exactly one `from \"@/gen/services/api/v1/api_pb\"` line (merged import), got %d. Output:\n%s", occurrences, got)
	}

	// And that single line must list all three schemas, regardless
	// of order (NewMockTransportTemplateData sorts symbols
	// alphabetically for deterministic output).
	for _, sym := range []string{"ListTradesResponseSchema", "ListHypothesesResponseSchema", "ListSettlementsResponseSchema"} {
		if !strings.Contains(got, sym) {
			t.Errorf("merged import should contain %q. Output:\n%s", sym, got)
		}
	}

	// Per-entity mock fixtures stay 1:1 — distinct modules per entity.
	for _, sym := range []string{"tradesMocks", "hypothesesMocks", "settlementsMocks"} {
		if !strings.Contains(got, sym) {
			t.Errorf("expected mock fixture import alias %q in output. Got:\n%s", sym, got)
		}
	}
}

// TestMockTransport_DistinctModules_KeepsImportsSeparate guards against
// an overzealous fix to NewMockTransportTemplateData: when two
// entities live in different proto modules, they must produce two
// separate import lines, not one merged super-import.
func TestMockTransport_DistinctModules_KeepsImportsSeparate(t *testing.T) {
	entities := []codegen.MockTransportEntity{
		{
			EntityName:       "Trade",
			EntityNamePlural: "Trades",
			EntitySlug:       "trades",
			ListRPC:          "ListTrades",
			HasList:          true,
			ImportPath:       "services/api/v1/api_pb",
			ListResponseType: "ListTradesResponse",
		},
		{
			EntityName:       "Daemon",
			EntityNamePlural: "Daemons",
			EntitySlug:       "daemons",
			ListRPC:          "ListDaemons",
			HasList:          true,
			ImportPath:       "services/control/v1/control_pb",
			ListResponseType: "ListDaemonsResponse",
		},
	}

	got := renderMockTransport(t, entities)

	if !strings.Contains(got, `from "@/gen/services/api/v1/api_pb"`) {
		t.Errorf("expected separate api_pb import line. Got:\n%s", got)
	}
	if !strings.Contains(got, `from "@/gen/services/control/v1/control_pb"`) {
		t.Errorf("expected separate control_pb import line. Got:\n%s", got)
	}
}

// TestMockTransport_SurrogatePk_KeyAndLookupsUseSameField is the regression
// test for the half-applied PK generalization (commit 329dc78): the store KEY
// was generalized to `e.{{ .PkFieldCamel }}`, but the Get/Update/Delete/Create
// handler LOOKUPS still hardcoded `id`. For an entity whose wire PK is a
// surrogate domain key (`usage_event_id` → `usageEventId`, NOT `id`), the store
// keyed on `usageEventId` while lookups read `String(req?.id)` ===
// `"undefined"`, so every Get/Update/Delete missed at runtime. It passed tsc
// and the daemon fixture (whose PK *is* `id`), staying green in CI while broken
// in the mock UI.
//
// This asserts the store-key field and ALL four handler lookups reference the
// SAME camelCase field for a non-`id` PK entity.
func TestMockTransport_SurrogatePk_KeyAndLookupsUseSameField(t *testing.T) {
	const pk = "usageEventId"
	entities := []codegen.MockTransportEntity{
		{
			EntityName:             "UsageEvent",
			EntityNamePlural:       "UsageEvents",
			EntitySlug:             "usage-events",
			ServiceName:            "BillingService",
			ServiceTypeName:        "controlplane.v1.BillingService",
			ListRPC:                "ListUsageEvents",
			GetRPC:                 "GetUsageEvent",
			CreateRPC:              "CreateUsageEvent",
			UpdateRPC:              "UpdateUsageEvent",
			DeleteRPC:              "DeleteUsageEvent",
			HasList:                true,
			HasGet:                 true,
			HasCreate:              true,
			HasUpdate:              true,
			HasDelete:              true,
			ItemsField:             "usageEvents",
			PkFieldCamel:           pk,
			GetEntityFieldCamel:    "usageEvent",
			CreateEntityFieldCamel: "usageEvent",
			ImportPath:             "services/billing/v1/billing_pb",
			EntityImportPath:       "controlplane/v1/shared_pb",
			TypeImport:             "UsageEvent",
			SchemaImport:           "UsageEventSchema",
			ListResponseType:       "ListUsageEventsResponse",
			GetResponseType:        "GetUsageEventResponse",
			CreateResponseType:     "CreateUsageEventResponse",
		},
	}

	got := renderMockTransport(t, entities)

	// The engine reads ONE field name — pkField — and uses it for the store
	// key, the Get/Delete lookup, the Update key resolution and the Create
	// mint. That single-source-of-truth is what makes the whole class of
	// surrogate-PK bugs unrepresentable: there is no longer a second place
	// to hardcode `id`. So the assertion moves to the declaration.
	if !strings.Contains(got, `pkField: "`+pk+`",`) {
		t.Fatalf("expected the descriptor to declare pkField %q. Got:\n%s", pk, got)
	}

	// And it must be the ONLY pk declaration for this entity — a second one
	// naming `id` would mean the template grew a fallback.
	pkRe := regexp.MustCompile(`pkField: "([A-Za-z0-9_]+)"`)
	for _, m := range pkRe.FindAllStringSubmatch(got, -1) {
		if m[1] != pk {
			t.Errorf("descriptor declares pkField %q, want %q — a surrogate-PK entity has no `id` field to fall back to. Got:\n%s", m[1], pk, got)
		}
	}

	// The fixtures the store is built from must come from the entity's own
	// mock module, and the CRUD arms must all be declared.
	if !strings.Contains(got, "fixtures: usageEventsMocks.usageEvents,") {
		t.Errorf("expected the descriptor to carry the entity's fixtures. Got:\n%s", got)
	}
	for _, want := range []string{
		`list: { rpc: "ListUsageEvents"`,
		`get: { rpc: "GetUsageEvent"`,
		`create: { rpc: "CreateUsageEvent"`,
		`update: { rpc: "UpdateUsageEvent"`,
		`delete: { rpc: "DeleteUsageEvent" }`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected the descriptor to declare %q. Got:\n%s", want, got)
		}
	}
}

// TestMockTransport_ResponseEntityField_WrapsOnDescriptorField pins that the
// Get/Create/Update response builders set the entity on the field NAMED BY THE
// RESPONSE DESCRIPTOR (GetEntityFieldCamel / CreateEntityFieldCamel), not the
// camelCased entity name. A proto is free to name the wrapper anything
// (`GetLLMKeyResponse { LLMKey key = 1; }` → `key`), and writing
// `{ camelCase(EntityName): record }` instead fails tsc with "object literal
// may only specify known properties". This is the previously-uncovered
// `responseEntityField` path (root cause #3 of commit 329dc78).
func TestMockTransport_ResponseEntityField_WrapsOnDescriptorField(t *testing.T) {
	entities := []codegen.MockTransportEntity{
		{
			EntityName:       "LLMKey",
			EntityNamePlural: "LLMKeys",
			EntitySlug:       "llm-keys",
			ServiceName:      "LLMKeyService",
			ServiceTypeName:  "controlplane.v1.LLMKeyService",
			GetRPC:           "GetLLMKey",
			CreateRPC:        "CreateLLMKey",
			UpdateRPC:        "UpdateLLMKey",
			HasGet:           true,
			HasCreate:        true,
			HasUpdate:        true,
			PkFieldCamel:     "id",
			// The freely-named wrapper field: `key`, NOT `lLMKey`.
			GetEntityFieldCamel:    "key",
			CreateEntityFieldCamel: "key",
			ImportPath:             "services/llmkey/v1/llmkey_pb",
			EntityImportPath:       "services/llmkey/v1/llmkey_pb",
			TypeImport:             "LLMKey",
			SchemaImport:           "LLMKeySchema",
			GetResponseType:        "GetLLMKeyResponse",
			CreateResponseType:     "CreateLLMKeyResponse",
		},
	}

	got := renderMockTransport(t, entities)

	// Each CRUD arm must name the descriptor's wrapper field. The engine
	// writes the entity onto exactly this key, so a wrong value here is the
	// same tsc failure it always was — just declared once per arm instead
	// of spelled out in three hand-rolled response builders.
	for _, want := range []string{
		`get: { rpc: "GetLLMKey", responseSchema: GetLLMKeyResponseSchema, entityField: "key" }`,
		`create: { rpc: "CreateLLMKey", responseSchema: CreateLLMKeyResponseSchema, entityField: "key"`,
		`update: { rpc: "UpdateLLMKey", responseSchema: CreateLLMKeyResponseSchema, entityField: "key"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected the descriptor to wrap the entity on the response-descriptor field. Missing:\n%q\nGot:\n%s", want, got)
		}
	}

	// And must NOT fall back to the naive camelCased entity name.
	if strings.Contains(got, `entityField: "lLMKey"`) {
		t.Errorf("descriptor must not use camelCase(EntityName) `lLMKey` wrapper. Got:\n%s", got)
	}
}

// TestMockTransport_NoEntities_EmitsScenarioCapableTransport is the
// regression test for the scenario-downgrade bug: forge used to emit a
// ~28-line do-nothing stub whenever the project had no entity-CRUD RPCs,
// which DROPPED the scenario-dispatch mechanism entirely. Scenarios do
// not require entity-CRUD (only the per-entity fixtures do), so the
// no-entity output must still be the rich, scenario-capable transport —
// scenario overlay + hybrid passthrough + Unimplemented fallback — just
// without the per-entity fixture switch.
func TestMockTransport_NoEntities_EmitsScenarioCapableTransport(t *testing.T) {
	got := renderMockTransport(t, nil)

	// Scenario dispatch must survive: the registry import, the resolution
	// call (which is what reads `?scenario=` and runs setup()), and the
	// entry point with its passthrough-carrying fallback parameter.
	for _, want := range []string{
		`import * as scenarios from "../mocks/scenarios/index_gen"`,
		`resolveActiveScenario(scenarios)`,
		`export function createMockTransport(fallback?: Transport): Transport`,
		`scenario: active,`,
		`fallback,`,
		`export const activeScenario = active`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected scenario-capable transport to contain %q. Got:\n%s", want, got)
		}
	}

	// It must NOT be the old do-nothing stub.
	for _, unwanted := range []string{
		"STUB_MESSAGE",
		"project has no entity-CRUD RPCs",
		"Code.Unavailable",
	} {
		if strings.Contains(got, unwanted) {
			t.Errorf("did not expect stub marker %q in scenario-capable transport. Got:\n%s", unwanted, got)
		}
	}

	// With no entities the per-entity fixture machinery is omitted
	// entirely: no descriptor array, no fixture imports, and no
	// MockEntityDescriptor type import — each would be an unused binding
	// under strict tsc / eslint.
	for _, unwanted := range []string{
		"const entities: readonly MockEntityDescriptor[]",
		"MockEntityDescriptor",
		`from "@/mocks/`,
		"entities,",
	} {
		if strings.Contains(got, unwanted) {
			t.Errorf("did not expect entity-fixture artifact %q in no-entity transport. Got:\n%s", unwanted, got)
		}
	}
}
