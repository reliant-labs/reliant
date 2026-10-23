// File: internal/cli/scaffold/entity_from_proto_test.go
//
// Tests for the wire→schema birth affordance
// (`forge scaffold entity ... --from-proto`):
//
//   - single form (positional name, and the <svc>.<Message> dot form)
//     births the owned migration with the mapped columns;
//   - the printed next steps state the evolution contract;
//   - envelope refusals hold even for explicitly-listed messages;
//   - flag combos that belong to the field-list form are refused;
//   - the one-time guard: an already-applied table refuses;
//   - the batch form selects only full CRUD quintets with no applied
//     table (partial sets and applied tables are skipped with notes).

package scaffold

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// entityFromProtoDescriptor builds the fixture descriptor: TasksService
// with a full CRUD quintet for Invoice (entity message present, enum +
// optional + *_id fields), a full quintet for Order (whose table the
// tests pre-apply), a partial set for Draft (Create only), and an
// envelope-shaped InvoiceFilter message (page_token carrier).
func entityFromProtoDescriptor() codegen.ForgeDescriptor {
	const pkg = "services.tasks.v1"
	quintet := func(entity, plural string) []codegen.Method {
		return []codegen.Method{
			{Name: "Create" + entity, InputType: "Create" + entity + "Request", OutputType: "Create" + entity + "Response"},
			{Name: "Get" + entity, InputType: "Get" + entity + "Request", OutputType: "Get" + entity + "Response"},
			{Name: "Update" + entity, InputType: "Update" + entity + "Request", OutputType: "Update" + entity + "Response"},
			{Name: "Delete" + entity, InputType: "Delete" + entity + "Request", OutputType: "Delete" + entity + "Response"},
			{Name: "List" + plural, InputType: "List" + plural + "Request", OutputType: "List" + plural + "Response"},
		}
	}
	sd := codegen.ServiceDef{
		Name:      "TasksService",
		Package:   pkg,
		GoPackage: "example.com/testproj/gen/services/tasks/v1",
		PkgName:   "tasksv1",
		Methods: append(append(append(quintet("Invoice", "Invoices"), quintet("Order", "Orders")...),
			quintet("Customer", "Customers")...),
			codegen.Method{Name: "CreateDraft", InputType: "CreateDraftRequest", OutputType: "CreateDraftResponse"}),
		Schemas: map[string][]codegen.SchemaFieldDef{
			pkg + ".Invoice": {
				{Name: "id", Kind: "string"},
				{Name: "number", Kind: "string"},
				{Name: "note", Kind: "string", Optional: true},
				{Name: "customer_id", Kind: "string"}, // → REFERENCES customers (Customer is a real entity below)
				{Name: "status", Kind: "enum", TypeName: pkg + ".InvoiceStatus"},
				{Name: "created_at", Kind: "message", TypeName: "google.protobuf.Timestamp"},
			},
			pkg + ".Customer": {
				{Name: "id", Kind: "string"},
				{Name: "name", Kind: "string"},
			},
			pkg + ".Order": {
				{Name: "id", Kind: "string"},
				{Name: "sku", Kind: "string"},
			},
			pkg + ".Draft": {
				{Name: "id", Kind: "string"},
				{Name: "body", Kind: "string"},
			},
			pkg + ".CreateInvoiceRequest": {
				{Name: "number", Kind: "string"},
			},
			pkg + ".InvoiceFilter": {
				{Name: "page_token", Kind: "string"},
				{Name: "number", Kind: "string"},
			},
		},
		Enums: map[string][]string{
			pkg + ".InvoiceStatus": {"INVOICE_STATUS_UNSPECIFIED", "INVOICE_STATUS_PAID"},
		},
	}
	return codegen.ForgeDescriptor{Services: []codegen.ServiceDef{sd}}
}

// setupEntityFromProtoProject lays down forge.yaml + the descriptor and
// chdirs into the project.
func setupEntityFromProtoProject(t *testing.T) string {
	t.Helper()
	dir := withTempProject(t, minimalServiceForgeYAML)
	desc, err := json.MarshalIndent(entityFromProtoDescriptor(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, dir, filepath.Join("gen", "forge_descriptor.json"), string(desc))
	return dir
}

// captureStdout runs fn with os.Stdout redirected and returns what it
// printed (the affordances promise specific next-step wording).
//
// The pipe is drained WHILE fn runs, not after. A pipe's buffer is finite
// and platform-specific (64 KiB on Linux, far less on Windows), so reading
// only once fn returns deadlocks as soon as fn prints more than the buffer
// holds: fn blocks in write, and nothing reads until fn returns. On Windows
// that hung TestProjectScaffold_BigFixtureBirthsEverythingThenNoOps until the
// 30-minute test timeout.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	type readResult struct {
		out []byte
		err error
	}
	drained := make(chan readResult, 1)
	go func() {
		out, rerr := io.ReadAll(r)
		drained <- readResult{out, rerr}
	}()
	os.Stdout = w
	runErr := fn()
	w.Close()
	os.Stdout = old
	res := <-drained
	if res.err != nil {
		t.Fatal(res.err)
	}
	return string(res.out), runErr
}

func TestAddEntityFromProto_SingleBirthsOwnedMigration(t *testing.T) {
	dir := setupEntityFromProtoProject(t)

	out, err := captureStdout(t, func() error {
		return runEntityFromProto([]string{"invoice"}, "tasks", entityOpts{})
	})
	if err != nil {
		t.Fatalf("runEntityFromProto: %v", err)
	}

	up := readFileT(t, birthMigrationPath(t, dir, "create_invoices"))
	for _, want := range []string{
		"-- Born from proto message services.tasks.v1.Invoice",
		"CREATE TABLE invoices (",
		"number TEXT NOT NULL DEFAULT ''",
		"note TEXT,",
		"customer_id TEXT NOT NULL,",
		// Born at the first REAL member, and the CHECK admits only real
		// members: the proto zero means "the caller did not say", which is a
		// fact about a request and never about a stored row. An unset field
		// arrives as the zero and stores this DEFAULT (codegen.assignToDB).
		"status TEXT NOT NULL DEFAULT 'INVOICE_STATUS_PAID' CHECK (status IN ('INVOICE_STATUS_PAID'))",
		"created_at TIMESTAMPTZ NOT NULL DEFAULT (now())",
		// customers is a known entity but has no table yet — this is the
		// first migration. The index does not depend on the referent and is
		// emitted now; the FOREIGN KEY is added by the customers birth,
		// which is the first migration that can apply it.
		"CREATE INDEX invoices_customer_id_idx ON invoices (customer_id);",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("up.sql missing %q:\n%s", want, up)
		}
	}
	// A constraint naming a table no earlier migration created would fail
	// the apply — forge must not write one, in either form.
	for _, bad := range []string{"ADD CONSTRAINT invoices_customer_id_fkey", "-- ALTER TABLE"} {
		if strings.Contains(up, bad) {
			t.Errorf("up.sql must not contain %q before customers exists:\n%s", bad, up)
		}
	}
	if downs, _ := filepath.Glob(filepath.Join(dir, "db", "migrations", "*.down.sql")); len(downs) != 0 {
		t.Errorf("birth must write no .down.sql (forge rolls forward only), found %v", downs)
	}

	// The evolution contract must be stated plainly in the next steps.
	for _, want := range []string{
		"it is yours",
		"forge never re-reads the proto",
		"new migration plus a proto edit",
		"never",
		"re-derives either truth",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("printed next steps missing %q:\n%s", want, out)
		}
	}
}

func TestAddEntityFromProto_DotFormDerivesName(t *testing.T) {
	dir := setupEntityFromProtoProject(t)
	if err := runEntityFromProto(nil, "tasks.Invoice", entityOpts{}); err != nil {
		t.Fatalf("runEntityFromProto: %v", err)
	}
	birthMigrationPath(t, dir, "create_invoices") // fails unless the dot form derived entity/table
}

func TestAddEntityFromProto_RefusesEnvelopeMessages(t *testing.T) {
	setupEntityFromProtoProject(t)

	// Request/Response suffix — refused even when explicitly listed.
	err := runEntityFromProto(nil, "tasks.CreateInvoiceRequest", entityOpts{})
	if err == nil || !strings.Contains(err.Error(), "request/response envelope") {
		t.Errorf("Request-suffixed message must refuse, got: %v", err)
	}
	// Pagination-field carrier — refused even when explicitly listed.
	err = runEntityFromProto(nil, "tasks.InvoiceFilter", entityOpts{})
	if err == nil || !strings.Contains(err.Error(), "page_token") {
		t.Errorf("page_token-carrying message must refuse, got: %v", err)
	}
}

func TestAddEntityFromProto_FlagAndArgRefusals(t *testing.T) {
	setupEntityFromProtoProject(t)

	// A `name:type` argument is someone reaching for the removed field-list
	// grammar; it must be refused, never mistaken for a message name.
	if err := runEntityFromProto([]string{"invoice", "url:string"}, "tasks", entityOpts{}); err == nil {
		t.Error("field:type args must refuse under --from-proto (the message IS the field list)")
	}
	if err := runEntityFromProto(nil, "tasks.Missing", entityOpts{}); err == nil || !strings.Contains(err.Error(), "not in the descriptor") {
		t.Errorf("unknown message must name the descriptor gap, got: %v", err)
	}
	if err := runEntityFromProto(nil, "nosuchsvc.Invoice", entityOpts{}); err == nil || !strings.Contains(err.Error(), "no service") {
		t.Errorf("unknown service must be named, got: %v", err)
	}
}

func TestAddEntityFromProto_AppliedTableRefuses(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	dir := setupEntityFromProtoProject(t)
	writeFixtureFile(t, dir, filepath.Join("db", "migrations", "00001_create_invoices.up.sql"),
		"CREATE TABLE invoices (id TEXT PRIMARY KEY);\n")

	err := runEntityFromProto([]string{"invoice"}, "tasks", entityOpts{})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("applied table must refuse the one-time birth, got: %v", err)
	}
}

// entityFromProtoRawProto is the raw proto companion to the descriptor
// fixture: the TasksService block (no CRUD rpcs yet), the Invoice entity
// message, and a `// forge:entity`-marked Gadget the DESCRIPTOR does not
// know (freshly authored — the raw scan is its only truth).
const entityFromProtoRawProto = `syntax = "proto3";

package services.tasks.v1;

import "forge/v1/forge.proto";

option go_package = "example.com/testproj/gen/services/tasks/v1;tasksv1";

service TasksService {
  rpc CreateDraft(CreateDraftRequest) returns (CreateDraftResponse);
}

message Invoice {
  string id = 1;
  string number = 2;
}

// forge:entity
message Gadget {
  string id = 1;
  string label = 2;
  bool armed = 3;
}

message CreateDraftRequest { string body = 1; }
message CreateDraftResponse { string id = 1; }
`

func TestAddEntityFromProto_ExplicitListCompletesQuintet(t *testing.T) {
	dir := setupEntityFromProtoProject(t)
	protoRel := filepath.Join("proto", "services", "tasks", "v1", "tasks.proto")
	writeFixtureFile(t, dir, protoRel, entityFromProtoRawProto)

	out, err := captureStdout(t, func() error {
		return runEntityFromProto([]string{"invoice"}, "tasks", entityOpts{})
	})
	if err != nil {
		t.Fatalf("runEntityFromProto: %v", err)
	}

	// Migration half (descriptor-driven, as before).
	birthMigrationPath(t, dir, "create_invoices")
	// Quintet half: the missing CRUD surface was injected into the raw proto.
	proto := readFileT(t, filepath.Join(dir, protoRel))
	for _, want := range []string{
		"rpc CreateInvoice(CreateInvoiceRequest) returns (CreateInvoiceResponse)",
		"rpc ListInvoices(ListInvoicesRequest) returns (ListInvoicesResponse)",
		"message CreateInvoiceRequest {",
		"message UpdateInvoiceRequest {",
		"  Invoice invoice = 1;",
		`import "forge/v1/forge.proto";`,
		`import "google/protobuf/field_mask.proto";`,
	} {
		if !strings.Contains(proto, want) {
			t.Errorf("proto missing %q after quintet completion:\n%s", want, proto)
		}
	}
	// The entity message itself is NEVER re-rendered.
	if got := strings.Count(proto, "message Invoice {"); got != 1 {
		t.Errorf("message Invoice declared %d times, want 1", got)
	}
	if !strings.Contains(out, "Completed CRUD quintet") {
		t.Errorf("completion not reported:\n%s", out)
	}

	// Re-running against the (now complete) quintet injects nothing more.
	// (The applied-table guard fires first — the migration already landed —
	// which is the one-time-birth contract.)
	if err := runEntityFromProto([]string{"invoice"}, "tasks", entityOpts{}); err == nil {
		t.Error("second explicit birth must refuse (one-time)")
	}
	if got := strings.Count(readFileT(t, filepath.Join(dir, protoRel)), "rpc CreateInvoice("); got != 1 {
		t.Errorf("rpc CreateInvoice declared %d times, want 1", got)
	}
}

func TestAddEntityFromProto_BatchUnionsMarkedMessages(t *testing.T) {
	dir := setupEntityFromProtoProject(t)
	protoRel := filepath.Join("proto", "services", "tasks", "v1", "tasks.proto")
	writeFixtureFile(t, dir, protoRel, entityFromProtoRawProto)

	out, err := captureStdout(t, func() error {
		return runEntityFromProto(nil, "tasks", entityOpts{})
	})
	if err != nil {
		t.Fatalf("batch runEntityFromProto: %v", err)
	}

	// The quintet sweep still births Invoice and Order (descriptor), AND
	// the marked Gadget (raw proto — not in the descriptor at all).
	migs := map[string]bool{}
	entries, _ := os.ReadDir(filepath.Join(dir, "db", "migrations"))
	for _, e := range entries {
		migs[e.Name()] = true
	}
	for _, table := range []string{"invoices", "orders", "gadgets"} {
		found := false
		for name := range migs {
			if strings.Contains(name, "_create_"+table+".up.sql") {
				found = true
			}
		}
		if !found {
			t.Errorf("batch should birth %s (have %v)", table, migs)
		}
	}
	// Gadget got its quintet injected (raw scan supplied the fields).
	proto := readFileT(t, filepath.Join(dir, protoRel))
	for _, want := range []string{
		"rpc CreateGadget(CreateGadgetRequest) returns (CreateGadgetResponse)",
		"message ListGadgetsRequest {",
		"  optional bool armed = ", // bool field → optional list filter
	} {
		if !strings.Contains(proto, want) {
			t.Errorf("proto missing %q after marked birth:\n%s", want, proto)
		}
	}
	if !strings.Contains(out, "Gadget") {
		t.Errorf("batch output should name the marked birth:\n%s", out)
	}
}

// entityFromProtoRetiredMarkerProto is the raw fixture carrying the retired
// `forge:server-set` spelling on a marked entity, PLUS the two shapes that
// must never trip the refusal: prose that merely mentions forge markers, and
// markers belonging to other passes (forge:secret in the descriptor path,
// forge:mutation in the frontend hook generator).
const entityFromProtoRetiredMarkerProto = `syntax = "proto3";

package services.tasks.v1;

option go_package = "example.com/testproj/gen/services/tasks/v1;tasksv1";

service TasksService {
  rpc CreateDraft(CreateDraftRequest) returns (CreateDraftResponse);
}

// forge:entity
// See the forge:read-only docs; the forge:server-set spelling was retired.
message Gadget {
  string id = 1;
  // forge:server-set
  string status = 2;
  int64 amount = 3;
}

message CreateDraftRequest { string body = 1; }
message CreateDraftResponse { string id = 1; }
`

// TestAddEntityFromProto_RetiredMarkerRefusesBirth is the end-to-end proof of
// the closed gap: before the retired-marker ledger this proto birthed
// happily, exit zero, with `status` client-writable on the born Create
// request — exactly what the author's `forge:server-set` comment says it is
// not. Birth must now refuse, name the replacement, and write nothing.
func TestAddEntityFromProto_RetiredMarkerRefusesBirth(t *testing.T) {
	dir := setupEntityFromProtoProject(t)
	protoRel := filepath.Join("proto", "services", "tasks", "v1", "tasks.proto")
	writeFixtureFile(t, dir, protoRel, entityFromProtoRetiredMarkerProto)

	_, err := captureStdout(t, func() error {
		return runEntityFromProto(nil, "tasks", entityOpts{})
	})
	if err == nil {
		t.Fatal("birth must REFUSE a message carrying a retired marker, got nil error")
	}
	msg := err.Error()
	// The message is a runbook: what is wrong, that it currently does
	// nothing, and the literal replacement spelling.
	for _, want := range []string{"forge:server-set", "forge:read-only", "Gadget"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal must mention %q, got:\n%s", want, msg)
		}
	}
	// Nothing written: the refusal costs no migration.
	entries, _ := os.ReadDir(filepath.Join(dir, "db", "migrations"))
	for _, e := range entries {
		if strings.Contains(e.Name(), "_create_gadgets") {
			t.Errorf("refused birth must write no migration, found %s", e.Name())
		}
	}
}

// TestAddEntityFromProto_OtherPassMarkersAndProseDoNotRefuse is the
// false-positive guard that matters most: a spurious birth refusal breaks
// every project that builds today. forge:secret (descriptor path),
// forge:mutation (frontend hooks) and prose containing `forge:` must all
// birth exactly as before.
func TestAddEntityFromProto_OtherPassMarkersAndProseDoNotRefuse(t *testing.T) {
	dir := setupEntityFromProtoProject(t)
	protoRel := filepath.Join("proto", "services", "tasks", "v1", "tasks.proto")
	writeFixtureFile(t, dir, protoRel, `syntax = "proto3";

package services.tasks.v1;

option go_package = "example.com/testproj/gen/services/tasks/v1;tasksv1";

service TasksService {
  rpc CreateDraft(CreateDraftRequest) returns (CreateDraftResponse);
}

// forge:entity
// Fields below follow the forge: marker conventions; see the forge:read-only
// docs for the write-surface rules.
message Gadget {
  string id = 1;
  // forge:secret
  string token = 2;
  // forge:read-only
  string status = 3;
  bool armed = 4; // forge:mutation applies to rpcs, not fields
}

message CreateDraftRequest { string body = 1; }
message CreateDraftResponse { string id = 1; }
`)

	if _, err := captureStdout(t, func() error {
		return runEntityFromProto(nil, "tasks", entityOpts{})
	}); err != nil {
		t.Fatalf("prose and other-pass markers must NOT refuse birth: %v", err)
	}
	found := false
	entries, _ := os.ReadDir(filepath.Join(dir, "db", "migrations"))
	for _, e := range entries {
		if strings.Contains(e.Name(), "_create_gadgets.up.sql") {
			found = true
		}
	}
	if !found {
		t.Error("Gadget should have birthed normally")
	}
}

func TestAddEntityFromProto_BatchDryRunWritesNothing(t *testing.T) {
	dir := setupEntityFromProtoProject(t)
	protoRel := filepath.Join("proto", "services", "tasks", "v1", "tasks.proto")
	writeFixtureFile(t, dir, protoRel, entityFromProtoRawProto)
	protoBefore := readFileT(t, filepath.Join(dir, protoRel))

	out, err := captureStdout(t, func() error {
		return runEntityFromProto(nil, "tasks", entityOpts{DryRun: true})
	})
	if err != nil {
		t.Fatalf("dry-run batch: %v", err)
	}
	for _, want := range []string{
		"would birth services.tasks.v1.Invoice",
		"would birth services.tasks.v1.Gadget",
		"would inject 5 rpc(s)",
		"(dry run — nothing written)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run plan missing %q:\n%s", want, out)
		}
	}
	if got := readFileT(t, filepath.Join(dir, protoRel)); got != protoBefore {
		t.Error("dry run modified the proto")
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "db", "migrations")); len(entries) != 0 {
		t.Errorf("dry run wrote migrations: %v", entries)
	}
}

func TestAddEntityFromProto_BatchSelectsQuintetsWithoutTables(t *testing.T) {
	dir := setupEntityFromProtoProject(t)
	// Order's table is already applied → the sweep must skip it.
	writeFixtureFile(t, dir, filepath.Join("db", "migrations", "00001_create_orders.up.sql"),
		"CREATE TABLE orders (id TEXT PRIMARY KEY);\n")

	out, err := captureStdout(t, func() error {
		return runEntityFromProto(nil, "tasks", entityOpts{})
	})
	if err != nil {
		t.Fatalf("batch runEntityFromProto: %v", err)
	}

	// Invoice (full quintet, no table) is birthed (its migration number
	// floats with however many siblings the sweep births ahead of it).
	entries, _ := os.ReadDir(filepath.Join(dir, "db", "migrations"))
	birthedInvoices := false
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "_create_invoices.up.sql") {
			birthedInvoices = true
		}
	}
	if !birthedInvoices {
		t.Errorf("batch should birth invoices (have %v)", entries)
	}
	// Order (table applied) and Draft (partial set) are not.
	for _, e := range entries {
		if strings.Contains(e.Name(), "drafts") {
			t.Errorf("partial CRUD set must not be swept: %s", e.Name())
		}
		if strings.Contains(e.Name(), "create_orders") && !strings.HasPrefix(e.Name(), "00001") {
			t.Errorf("applied table must not be re-birthed: %s", e.Name())
		}
	}
	// Both skips are reported, never silent.
	if !strings.Contains(out, "Draft") || !strings.Contains(out, "not a full CRUD quintet") {
		t.Errorf("batch output should note the partial-quintet skip:\n%s", out)
	}
	if !strings.Contains(out, "already applied") {
		t.Errorf("batch output should note the applied-table skip:\n%s", out)
	}
}
