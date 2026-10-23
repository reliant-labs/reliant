// File: internal/cli/scaffold/sweep_test.go
//
// The big-fixture test for `forge scaffold`: a service proto
// with 8 `// forge:entity`-marked entities (mixed shapes: enum,
// optional, FK-ish *_id, oneof-TODO, map, repeated), two custom RPCs
// (one unary with a pb-through unwired stub, one streaming), one
// envelope-shaped marked decoy, and one marked already-tabled entity.
//
//   - --dry-run prints the phase 1 plan and writes NOTHING;
//   - the real run births all 8 (quintet completion + owned migration
//     pair), refuses the decoy loudly, reports the tabled one inert, and
//     runs the (test-stubbed) generate pipeline once;
//   - a re-run is a clean no-op (all markers inert, descriptor fresh) that
//     modifies not a single byte.

package scaffold

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// scaffoldFixtureProto is the wire truth the fixture author "just
// edited": two custom RPCs plus the marked entity messages.
const scaffoldFixtureProto = `syntax = "proto3";

package services.tasks.v1;

import "forge/v1/forge.proto";
import "google/protobuf/timestamp.proto";

option go_package = "example.com/testproj/gen/services/tasks/v1;tasksv1";

service TasksService {
  rpc SubmitOrder(SubmitOrderRequest) returns (SubmitOrderResponse) {
    option (forge.v1.method) = {
      auth_required: true
    };
  }
  rpc TailEvents(TailEventsRequest) returns (stream TailEventsResponse);
}

message SubmitOrderRequest {
  string customer_id = 1;
  optional string note = 2;
  google.protobuf.Timestamp when = 3;
  repeated string tags = 4;
  Order order = 5;
  OrderStatus status = 6;
  map<string, int64> attrs = 7;
}

message SubmitOrderResponse {
  string id = 1;
  google.protobuf.Timestamp created_at = 2;
  repeated Order items = 3;
}

message TailEventsRequest { string cursor = 1; }
message TailEventsResponse { string event = 1; }

// forge:entity
message Order {
  string id = 1;
  string customer_id = 2;
  optional string note = 3;
  OrderStatus status = 4;
  repeated string tags = 5;
  google.protobuf.Timestamp placed_at = 6;
}

// forge:entity
message Customer {
  string id = 1;
  string name = 2;
  string email = 3;
}

// forge:entity
message Invoice {
  string id = 1;
  string number = 2;
  oneof payment {
    string card_token = 3;
    string iban = 4;
  }
}

// forge:entity
message Product {
  string id = 1;
  double price = 2;
  map<string, string> attrs = 3;
}

// forge:entity
message Warehouse {
  string id = 1;
  string region = 2;
  bool active = 3;
}

// forge:entity
message Shipment {
  string id = 1;
  string order_id = 2;
  double weight = 3;
}

// forge:entity
message Supplier {
  string id = 1;
  string country = 2;
  repeated string emails = 3;
}

//forge:entity
message AuditNote {
  string id = 1;
  string body = 2;
  string author_id = 3;
}

// The decoy: envelope-shaped (pagination field) — the marker must NOT
// override the guard.
// forge:entity
message OrderQuery {
  string page_token = 1;
  string number = 2;
}

// Already tabled (db/migrations/00001) — the marker is inert.
// forge:entity
message Legacy {
  string id = 1;
  string title = 2;
}

enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  ORDER_STATUS_OPEN = 1;
  ORDER_STATUS_CLOSED = 2;
}
`

// scaffoldFixtureEntities are the 8 birthable marked entities and their
// tables.
var scaffoldFixtureEntities = map[string]string{
	"Order":     "orders",
	"Customer":  "customers",
	"Invoice":   "invoices",
	"Product":   "products",
	"Warehouse": "warehouses",
	"Shipment":  "shipments",
	"Supplier":  "suppliers",
	"AuditNote": "audit_notes",
}

// setupProjectScaffoldFixture lays down the big fixture and returns its
// root. The handler package carries the two pb-through unwired stubs.
func setupProjectScaffoldFixture(t *testing.T) string {
	t.Helper()
	dir := setupVerticalProject(t) // forge.yaml + go.mod + handlers + descriptor (SubmitOrder/TailEvents)
	writeFixtureFile(t, dir, filepath.Join("proto", "services", "tasks", "v1", "tasks.proto"), scaffoldFixtureProto)
	writeFixtureFile(t, dir, filepath.Join("db", "migrations", "00001_create_legacies.up.sql"),
		"CREATE TABLE legacies (id TEXT PRIMARY KEY CHECK (id <> ''), title TEXT NOT NULL DEFAULT '');\n")
	return dir
}

func TestProjectScaffold_DryRunPlansEverythingAndWritesNothing(t *testing.T) {
	dir := setupProjectScaffoldFixture(t)
	protoPath := filepath.Join(dir, "proto", "services", "tasks", "v1", "tasks.proto")
	protoBefore := readFileT(t, protoPath)

	runs := 0
	f := testFactory()
	f.Gen.RunPipeline = func(string) error { runs++; return nil }

	out, err := captureStdout(t, func() error {
		return runSweep(f, "", true)
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}

	// Phase 1 predictions: all 8 births, each with quintet injection.
	for msg, table := range scaffoldFixtureEntities {
		if !strings.Contains(out, "would birth services.tasks.v1."+msg+" → table \""+table+"\"") {
			t.Errorf("plan missing birth prediction for %s:\n%s", msg, out)
		}
	}
	if got := strings.Count(out, "would inject 5 rpc(s)"); got != 8 {
		t.Errorf("plan should predict full quintet injection for all 8 entities, got %d:\n%s", got, out)
	}
	// The decoy and the tabled entity.
	if !strings.Contains(out, "OrderQuery") || !strings.Contains(out, "does not override the envelope guard") {
		t.Errorf("plan should refuse the envelope decoy loudly:\n%s", out)
	}
	if !strings.Contains(out, `Legacy: table "legacies" exists — marker inert`) {
		t.Errorf("plan should report the tabled marker inert:\n%s", out)
	}
	// Phase 2 prediction.
	if !strings.Contains(out, "would run the generate pipeline") {
		t.Errorf("plan should predict the phase-2 generate:\n%s", out)
	}

	// Nothing written, nothing run.
	if runs != 0 {
		t.Errorf("dry run must not run the generate pipeline (ran %d times)", runs)
	}
	if got := readFileT(t, protoPath); got != protoBefore {
		t.Error("dry run modified the proto")
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "db", "migrations"))
	if len(entries) != 1 { // the fixture's one pre-existing up migration
		t.Errorf("dry run must write no migrations, dir has %d entries", len(entries))
	}
}

func TestProjectScaffold_BigFixtureBirthsEverythingThenNoOps(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	dir := setupProjectScaffoldFixture(t)
	protoPath := filepath.Join(dir, "proto", "services", "tasks", "v1", "tasks.proto")

	runs := 0
	f := testFactory()
	f.Gen.RunPipeline = scaffoldPipelineStub(t, &runs)

	// ── run 1: births + projection ────────────────────────────────────
	out, err := captureStdout(t, func() error {
		return runSweep(f, "", false)
	})
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}

	// All 8 entities birthed: migration pairs on disk, quintets injected.
	proto := readFileT(t, protoPath)
	migDir := filepath.Join(dir, "db", "migrations")
	for msg, table := range scaffoldFixtureEntities {
		found := false
		entries, _ := os.ReadDir(migDir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), "_create_"+table+".up.sql") {
				found = true
			}
		}
		if !found {
			t.Errorf("entity %s: no create_%s migration", msg, table)
		}
		for _, rpc := range []string{"Create" + msg, "Get" + msg, "Update" + msg, "Delete" + msg} {
			if !strings.Contains(proto, "rpc "+rpc+"(") {
				t.Errorf("quintet rpc %s not injected:\n%s", rpc, proto)
			}
		}
		if !strings.Contains(proto, "message Create"+msg+"Request {") {
			t.Errorf("envelope message Create%sRequest not injected", msg)
		}
	}

	// Mixed-shape spot checks on the rendered migrations.
	ordersUp := readMigrationT(t, migDir, "orders")
	for _, want := range []string{
		"status TEXT NOT NULL DEFAULT 'ORDER_STATUS_OPEN' CHECK (status IN (", // enum → TEXT+CHECK, born at the first real member
		"note TEXT,",                // optional → nullable
		"customer_id TEXT NOT NULL", // FK-ish
		"CREATE INDEX orders_customer_id_idx ON orders (customer_id);", // the index never depends on birth order
		"tags TEXT[] NOT NULL DEFAULT '{}'",                            // repeated scalar
		"placed_at TIMESTAMPTZ",                                        // Timestamp
	} {
		if !strings.Contains(ordersUp, want) {
			t.Errorf("orders migration missing %q:\n%s", want, ordersUp)
		}
	}
	// Every resolvable reference becomes a REAL constraint exactly once,
	// whichever of the two tables the sweep births first. Commented-out
	// foreign keys shipped a database with no referential integrity and a
	// seeder that could not see the graph.
	customersUp := readMigrationT(t, migDir, "customers")
	fk := "ALTER TABLE orders ADD CONSTRAINT orders_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers (id);"
	if strings.Count(ordersUp+customersUp, fk) != 1 {
		t.Errorf("orders.customer_id must be constrained exactly once across the two births:\n--- orders ---\n%s\n--- customers ---\n%s", ordersUp, customersUp)
	}
	if strings.Contains(ordersUp, "-- ALTER TABLE") || strings.Contains(customersUp, "-- ALTER TABLE") {
		t.Errorf("foreign keys must be applied, not commented out:\n--- orders ---\n%s\n--- customers ---\n%s", ordersUp, customersUp)
	}
	invoicesUp := readMigrationT(t, migDir, "invoices")
	if !strings.Contains(invoicesUp, "TODO") || !strings.Contains(invoicesUp, "oneof") {
		t.Errorf("invoice oneof members must be carried as TODO comments:\n%s", invoicesUp)
	}

	// Decoy refused, tabled inert — loudly, in the phase output.
	if !strings.Contains(out, "OrderQuery") || !strings.Contains(out, "does not override the envelope guard") {
		t.Errorf("decoy refusal missing:\n%s", out)
	}
	if !strings.Contains(out, `Legacy: table "legacies" exists — marker inert`) {
		t.Errorf("inert marker note missing:\n%s", out)
	}
	if strings.Contains(proto, "rpc CreateOrderQuery(") || strings.Contains(proto, "rpc CreateLegacy(") {
		t.Error("refused/inert messages must get NO quintet")
	}
	entries, _ := os.ReadDir(migDir)
	for _, e := range entries {
		if strings.Contains(e.Name(), "order_queries") || (strings.Contains(e.Name(), "legacies") && !strings.HasPrefix(e.Name(), "00001")) {
			t.Errorf("refused/inert messages must get NO migration: %s", e.Name())
		}
	}

	// Phase 2 ran exactly once, with legible causality.
	if runs != 1 {
		t.Errorf("generate pipeline runs = %d, want 1", runs)
	}
	if !strings.Contains(out, "running the generate pipeline (entity births changed the protos") {
		t.Errorf("phase 2 causality line missing:\n%s", out)
	}

	// Summary names the work.
	if !strings.Contains(out, "entities birthed:    8") {
		t.Errorf("summary should count 8 births:\n%s", out)
	}
	if !strings.Contains(out, "quintets completed:  8") {
		t.Errorf("summary should count 8 quintet completions:\n%s", out)
	}
	// Invoice's two oneof members carried as TODOs.
	if !strings.Contains(out, "TODO fields carried: 2") {
		t.Errorf("summary should carry the TODO count:\n%s", out)
	}

	// ── run 2: clean no-op ────────────────────────────────────────────
	snapshot := snapshotTree(t, dir)
	out2, err := captureStdout(t, func() error {
		return runSweep(f, "", false)
	})
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if runs != 1 {
		t.Errorf("re-run must not run generate again (runs = %d)", runs)
	}
	for msg := range scaffoldFixtureEntities {
		if !strings.Contains(out2, msg+": table") || !strings.Contains(out2, "marker inert") {
			t.Errorf("re-run should report %s inert:\n%s", msg, out2)
			break
		}
	}
	// The re-run's verdict must reflect what the sweep actually saw: nine
	// markers already born and ONE (the envelope decoy) refused. "nothing
	// was missing — clean no-op" printed here before, which is the sweep
	// reporting success over an entity the fixture asked for and did not
	// get. Inert and refused are counted separately for exactly this.
	if !strings.Contains(out2, "already born:        9") {
		t.Errorf("re-run summary should count 9 inert markers:\n%s", out2)
	}
	if !strings.Contains(out2, "refused:             1") {
		t.Errorf("re-run summary should count the refused envelope decoy separately:\n%s", out2)
	}
	if strings.Contains(out2, "clean no-op") {
		t.Errorf("a run with a refused marker must not report a clean no-op:\n%s", out2)
	}
	if !strings.Contains(out2, "nothing birthed this run: 9 already born, 1 refused, 0 failed") {
		t.Errorf("re-run verdict should state the split:\n%s", out2)
	}
	if diff := diffTree(t, dir, snapshot); diff != "" {
		t.Errorf("re-run modified the tree:\n%s", diff)
	}
}

// TestSweepSummaryVerdictMatchesItsEvidence pins the summary's last line —
// the one a human scrolls to and an agent greps — against every shape the
// tally can take. "✅ nothing was missing — clean no-op" used to print for
// any run that birthed nothing and hit no hard failure, which covered both
// "the project has no markers at all" and "every marker the project wrote
// was refused".
func TestSweepSummaryVerdictMatchesItsEvidence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		summary sweepSummary
		want    string
		notWant string
	}{
		{
			name:    "no markers anywhere",
			summary: sweepSummary{},
			want:    "nothing was missing — clean no-op",
		},
		{
			name:    "steady state: every marker already born",
			summary: sweepSummary{Inert: []string{"A", "B", "C"}},
			want:    "nothing to birth — 3 marked entities already born",
			notWant: "clean no-op",
		},
		{
			name:    "every marker refused",
			summary: sweepSummary{Refused: []string{"A", "B"}},
			want:    "nothing birthed this run: 0 already born, 2 refused, 0 failed",
			notWant: "clean no-op",
		},
		{
			name:    "mixed: some born already, one refused",
			summary: sweepSummary{Inert: []string{"A"}, Refused: []string{"B"}},
			want:    "nothing birthed this run: 1 already born, 1 refused, 0 failed",
			notWant: "clean no-op",
		},
		{
			name:    "a birth happened",
			summary: sweepSummary{EntitiesBirthed: []string{"pkg.A → a"}},
			want:    "Next: fill in the pb-through handler stubs",
			notWant: "clean no-op",
		},
		{
			// The inner loop is the cached, -short Taskfile tier — not a raw
			// whole-module `go test` that bypasses the Taskfile.
			name:    "a birth points at the inner-loop test tier",
			summary: sweepSummary{EntitiesBirthed: []string{"pkg.A → a"}},
			want:    "`task test:short -- ./internal/handlers/...`",
			notWant: "`go test ./...`",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.summary
			out, err := captureStdout(t, func() error {
				printProjectScaffoldSummary(&s, false)
				return nil
			})
			if err != nil {
				t.Fatalf("capture: %v", err)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("verdict missing %q:\n%s", tc.want, out)
			}
			if tc.notWant != "" && strings.Contains(out, tc.notWant) {
				t.Errorf("verdict wrongly claims %q:\n%s", tc.notWant, out)
			}
		})
	}
}

// scaffoldPipelineStub stands in for `forge generate`: it refreshes the
// descriptor with every rpc the raw proto declares (what the real
// descriptor compile would produce), so the staleness discriminator
// reads fresh on the re-run.
func scaffoldPipelineStub(t *testing.T, runs *int) func(string) error {
	t.Helper()
	return func(root string) error {
		*runs++
		scan, err := codegen.ScanRawProtoDir(filepath.Join(root, "proto", "services", "tasks"))
		if err != nil {
			return err
		}
		desc := verticalDescriptor()
		known := map[string]bool{}
		for _, m := range desc.Services[0].Methods {
			known[m.Name] = true
		}
		for _, r := range scan.RPCs {
			if known[r.Name] {
				continue
			}
			desc.Services[0].Methods = append(desc.Services[0].Methods, codegen.Method{
				Name:            r.Name,
				InputType:       r.Name + "Request",
				OutputType:      r.Name + "Response",
				InputTypeFQ:     "services.tasks.v1." + r.Name + "Request",
				OutputTypeFQ:    "services.tasks.v1." + r.Name + "Response",
				ServerStreaming: r.Streaming,
			})
		}
		raw, merr := json.MarshalIndent(desc, "", "  ")
		if merr != nil {
			return merr
		}
		return os.WriteFile(filepath.Join(root, "gen", "forge_descriptor.json"), raw, 0o644)
	}
}

// readMigrationT reads the create-table up migration for table.
func readMigrationT(t *testing.T, migDir, table string) string {
	t.Helper()
	entries, err := os.ReadDir(migDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "_create_"+table+".up.sql") {
			return readFileT(t, filepath.Join(migDir, e.Name()))
		}
	}
	t.Fatalf("no create_%s migration in %s", table, migDir)
	return ""
}

// snapshotTree captures every file's content under dir (relative path →
// content) for the byte-identical no-op assertion.
func snapshotTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		out[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// diffTree reports files added, removed, or changed since the snapshot.
func diffTree(t *testing.T, dir string, before map[string]string) string {
	t.Helper()
	after := snapshotTree(t, dir)
	var diffs []string
	for rel := range before {
		if _, ok := after[rel]; !ok {
			diffs = append(diffs, "removed: "+rel)
		}
	}
	for rel, content := range after {
		prev, ok := before[rel]
		switch {
		case !ok:
			diffs = append(diffs, "added: "+rel)
		case prev != content:
			diffs = append(diffs, "changed: "+rel)
		}
	}
	sort.Strings(diffs)
	return strings.Join(diffs, "\n")
}
