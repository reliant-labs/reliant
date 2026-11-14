package audit

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/audittype"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// These tests pin the THIRD state this category was missing.
//
// `unscoped_auth` classifies an RPC by asking one question: does the
// handler body reach the auth seam? For a delegating CRUD handler that
// is a sound proxy for "is this scoped" — resolving the caller is the
// only step the scaffold left undone, and the audit knows the op seam
// well enough to hand back the wrapper that finishes the job.
//
// For a handler whose query is a hand-written SQL string it is not a
// proxy for anything. Resolving the caller and then never putting the
// owner column in the WHERE clause is exactly as easy as doing it right,
// and the difference lives inside a string constant the AST pass cannot
// see through. A measured run shipped precisely that: an aggregation RPC
// that called the seam, queried an owner-declared table, filtered on a
// vehicle id and nothing else, and was counted among `scoped_rpcs`. The
// audit printed the same confident ✓ it prints for a genuinely scoped
// delegation.
//
// The rule these tests pin:
//
//	raw SQL naming an owner-declared table, owner column absent  → ERROR
//	raw SQL naming an owner-declared table, owner column present → WARN,
//	                                        reported as unverifiable
//	raw SQL over a table nobody declared owned                   → silent
//	no owner declaration anywhere in the project                 → silent
//
// The last two are what keep this from being noise. A check that fired
// on every raw query would be switched off within a week, and the real
// leak would be invisible again — this time behind a suppression rather
// than behind a ✓.

// rawSQLBody renders a handler in the shape that leaked: it resolves the
// caller through the real seam, then runs a hand-written query. Whatever
// predicate the caller passes in is the entire difference between the
// safe version and the one that returns another owner's rows.
func rawSQLBody(method, query string) string {
	seam := codegen.CRUDAuthSeam()
	return "func (s *Service) " + method + "(ctx context.Context, req *connect.Request[pb." + method + "Request]) (*connect.Response[pb." + method + "Response], error) {\n" +
		"\tif _, err := " + seam + "(ctx); err != nil {\n\t\treturn nil, err\n\t}\n" +
		"\trows, err := s.db.Query(ctx, \"" + query + "\")\n" +
		"\tif err != nil {\n\t\treturn nil, err\n\t}\n" +
		"\t_ = rows\n\treturn nil, nil\n}\n"
}

// The query that actually shipped, reduced: a time-bucketed rollup over
// an owner-declared table, filtered by the row id the caller named and
// by nothing about the caller.
const leakingAggregateQuery = "SELECT vehicle_id, date_trunc('day', recorded_at) AS bucket, SUM(liters) " +
	"FROM vehicles WHERE vehicle_id = $1 AND recorded_at >= $2 GROUP BY 1, 2"

// The same rollup with the owner predicate present.
const ownerFilteredAggregateQuery = "SELECT vehicle_id, date_trunc('day', recorded_at) AS bucket, SUM(liters) " +
	"FROM vehicles WHERE fleet_id = $1 AND vehicle_id = $2 GROUP BY 1, 2"

func rawSQLFindings(t *testing.T, cat audittype.Category, key string) []rawSQLRPC {
	t.Helper()
	raw, ok := cat.Details[key]
	if !ok {
		return nil
	}
	found, ok := raw.([]rawSQLRPC)
	if !ok {
		t.Fatalf("%s is %T, not []rawSQLRPC — the derived set changed shape", key, raw)
	}
	return found
}

// TestUnscopedAuth_RawSQLOverOwnedTableWithoutOwnerColumnIsFlagged is the
// reproduction. This handler resolves the caller, so the old predicate
// moved it into `scoped_rpcs` and the category reported clean over an RPC
// that returned any owner's rows to any signed-in caller who could name a
// vehicle id.
func TestUnscopedAuth_RawSQLOverOwnedTableWithoutOwnerColumnIsFlagged(t *testing.T) {
	src := handlerHeader + rawSQLBody("GetFuelEfficiency", leakingAggregateQuery)
	dir := writeProject(t, "ShopService", map[string]bool{"GetFuelEfficiency": true}, src)
	writeOwnerMigration(t, dir, "vehicles", "fleet_id")

	cat := auditUnscopedAuth(nil, dir)

	if cat.Status != audittype.StatusError {
		t.Fatalf("status = %q, want error — the handler queries vehicles, whose fleet_id is declared %s, and its SQL never names that column anywhere\nsummary: %s",
			cat.Status, schemadef.ColumnMarkerOwner, cat.Summary)
	}
	flagged := rawSQLFindings(t, cat, "raw_sql_unscoped_rpcs")
	if len(flagged) != 1 || flagged[0].Method != "GetFuelEfficiency" {
		t.Fatalf("raw_sql_unscoped_rpcs = %+v, want exactly [GetFuelEfficiency]", flagged)
	}
	if len(flagged[0].Tables) != 1 || flagged[0].Tables[0] != "vehicles" {
		t.Errorf("finding = %+v, want it to name the owned table its SQL reads", flagged[0])
	}
	if len(flagged[0].OwnerColumns) != 1 || flagged[0].OwnerColumns[0] != "fleet_id" {
		t.Errorf("finding = %+v, want it to name the column the migration declared, so the reader can check the claim", flagged[0])
	}
	// The old behaviour is the bug: this RPC must no longer be counted
	// as verified simply because it reached the seam.
	if got := cat.Details["scoped_rpcs"]; got != 0 {
		t.Errorf("scoped_rpcs = %v, want 0 — resolving the caller proves nothing about a predicate forge cannot see", got)
	}
	if !strings.Contains(cat.Summary, "vehicles") {
		t.Errorf("summary = %q, want it to name the table, so the finding is falsifiable at the migration", cat.Summary)
	}
}

// TestUnscopedAuth_RawSQLHintDoesNotRepeatAdviceAlreadyTaken keeps the
// refusal actionable. The category's standing hint is "resolve the
// caller and scope what the handler touches" — advice these handlers
// already followed, which is how they ended up in this state. Printing
// it here would send the reader to re-do the one step that is already
// done and leave the leak untouched.
func TestUnscopedAuth_RawSQLHintDoesNotRepeatAdviceAlreadyTaken(t *testing.T) {
	src := handlerHeader + rawSQLBody("GetFuelEfficiency", leakingAggregateQuery)
	dir := writeProject(t, "ShopService", map[string]bool{"GetFuelEfficiency": true}, src)
	writeOwnerMigration(t, dir, "vehicles", "fleet_id")

	cat := auditUnscopedAuth(nil, dir)

	hint, _ := cat.Details["hint"].(string)
	if strings.Contains(hint, "resolve the caller with") {
		t.Errorf("hint = %q, want the raw-SQL guidance — these handlers already resolve the caller, so repeating that instruction is advice that cannot fix the finding", hint)
	}
	if !strings.Contains(hint, "not SQL") {
		t.Errorf("hint = %q, want it to state plainly what forge did not check", hint)
	}
	if !strings.Contains(hint, AuthUnscopedOKDirective) {
		t.Errorf("hint = %q, want it to name the EXISTING acknowledgement directive rather than a second convention", hint)
	}
}

// TestUnscopedAuth_RawSQLMentioningOwnerColumnIsUnverifiableNotClean is
// the honesty half. Forge can see that the owner column appears in the
// query text; it cannot see that the value bound to it came from the
// caller's claims rather than from the request. Reporting that as ✓ is
// the same false confidence in a quieter form, so it is reported as its
// own state — and it does NOT fail the build, because there is no
// evidence of a defect, only an absence of evidence of correctness.
func TestUnscopedAuth_RawSQLMentioningOwnerColumnIsUnverifiableNotClean(t *testing.T) {
	src := handlerHeader + rawSQLBody("GetFuelEfficiency", ownerFilteredAggregateQuery)
	dir := writeProject(t, "ShopService", map[string]bool{"GetFuelEfficiency": true}, src)
	writeOwnerMigration(t, dir, "vehicles", "fleet_id")

	cat := auditUnscopedAuth(nil, dir)

	if cat.Status != audittype.StatusWarn {
		t.Fatalf("status = %q, want warn — the owner column is in the query, which is evidence but not proof; forge must say it did not verify rather than claim it did\nsummary: %s",
			cat.Status, cat.Summary)
	}
	if flagged := rawSQLFindings(t, cat, "raw_sql_unscoped_rpcs"); len(flagged) != 0 {
		t.Errorf("raw_sql_unscoped_rpcs = %+v, want empty — the predicate is present, so this is not the leak shape", flagged)
	}
	unverifiable := rawSQLFindings(t, cat, "raw_sql_unverifiable_rpcs")
	if len(unverifiable) != 1 || unverifiable[0].Method != "GetFuelEfficiency" {
		t.Fatalf("raw_sql_unverifiable_rpcs = %+v, want exactly [GetFuelEfficiency]", unverifiable)
	}
}

// TestUnscopedAuth_RawSQLAcknowledgementIsSilent reuses the escape hatch
// this category already has rather than inventing a second one. The
// reason stays mandatory for the same reason it always did.
func TestUnscopedAuth_RawSQLAcknowledgementIsSilent(t *testing.T) {
	src := handlerHeader +
		"// " + AuthUnscopedOKDirective + " operator rollup; the owner predicate is enforced by the RLS policy on vehicles.\n" +
		rawSQLBody("GetFuelEfficiency", leakingAggregateQuery)
	dir := writeProject(t, "ShopService", map[string]bool{"GetFuelEfficiency": true}, src)
	writeOwnerMigration(t, dir, "vehicles", "fleet_id")

	cat := auditUnscopedAuth(nil, dir)

	if cat.Status != audittype.StatusOK {
		t.Fatalf("status = %q, want ok — the RPC is acknowledged in code with a reason\nsummary: %s", cat.Status, cat.Summary)
	}
	if flagged := rawSQLFindings(t, cat, "raw_sql_unscoped_rpcs"); len(flagged) != 0 {
		t.Errorf("raw_sql_unscoped_rpcs = %+v, want empty", flagged)
	}
	if unverifiable := rawSQLFindings(t, cat, "raw_sql_unverifiable_rpcs"); len(unverifiable) != 0 {
		t.Errorf("raw_sql_unverifiable_rpcs = %+v, want empty", unverifiable)
	}
}

// TestUnscopedAuth_BareRawSQLAcknowledgementDoesNotSuppress carries the
// reason-required rule onto this path too.
func TestUnscopedAuth_BareRawSQLAcknowledgementDoesNotSuppress(t *testing.T) {
	src := handlerHeader +
		"// " + AuthUnscopedOKDirective + "\n" +
		rawSQLBody("GetFuelEfficiency", leakingAggregateQuery)
	dir := writeProject(t, "ShopService", map[string]bool{"GetFuelEfficiency": true}, src)
	writeOwnerMigration(t, dir, "vehicles", "fleet_id")

	if cat := auditUnscopedAuth(nil, dir); cat.Status != audittype.StatusError {
		t.Fatalf("status = %q, want error — a reasonless directive is not an acknowledgement\nsummary: %s", cat.Status, cat.Summary)
	}
}

// TestUnscopedAuth_RawSQLOverUndeclaredTableIsSilent is the noise
// control, and it is the assertion that decides whether this check
// survives contact with a real project. A reporting screen over a
// catalog, a lookup table, a metrics rollup — none of them cross a
// boundary anybody declared, and firing on them would train the reader
// to skip the whole category.
func TestUnscopedAuth_RawSQLOverUndeclaredTableIsSilent(t *testing.T) {
	src := handlerHeader + rawSQLBody("GetPartUsage",
		"SELECT part_id, SUM(quantity) FROM part_usages WHERE part_id = $1 GROUP BY 1")
	dir := writeProject(t, "ShopService", map[string]bool{"GetPartUsage": true}, src)
	writeOwnerMigration(t, dir, "vehicles", "fleet_id")

	cat := auditUnscopedAuth(nil, dir)

	if cat.Status != audittype.StatusOK {
		t.Fatalf("status = %q, want ok — part_usages declares no owner column, so this query crosses no declared boundary\nsummary: %s",
			cat.Status, cat.Summary)
	}
}

// TestUnscopedAuth_RawSQLUnarmedProjectIsSilent preserves the arming
// model exactly as the rest of the category has it: a project that has
// declared no ownership has told forge there is no boundary, and a fresh
// scaffold must not go red on day one.
func TestUnscopedAuth_RawSQLUnarmedProjectIsSilent(t *testing.T) {
	src := handlerHeader + rawSQLBody("GetFuelEfficiency", leakingAggregateQuery)
	dir := writeProject(t, "ShopService", map[string]bool{"GetFuelEfficiency": true}, src)

	cat := auditUnscopedAuth(nil, dir)

	if cat.Status != audittype.StatusOK {
		t.Fatalf("status = %q, want ok — nothing in this project declares %s, so there is no boundary to cross\nsummary: %s",
			cat.Status, schemadef.ColumnMarkerOwner, cat.Summary)
	}
}

// TestUnscopedAuth_ScopedDelegationStaysScoped is the regression guard
// on the state that was already correct. The raw-SQL rule must not
// reclassify a delegating handler that resolves the caller — that is the
// shape the gate's own remediation produces, and demoting it would make
// following forge's advice fail forge's own check.
func TestUnscopedAuth_ScopedDelegationStaysScoped(t *testing.T) {
	src := handlerHeader + scopedBody("GetVehicle")
	dir := writeProject(t, "ShopService", map[string]bool{"GetVehicle": true}, src)
	writeOwnerMigration(t, dir, "vehicles", "fleet_id")

	cat := auditUnscopedAuth(nil, dir)

	if cat.Status != audittype.StatusOK {
		t.Fatalf("status = %q, want ok — a delegating handler that resolves the caller holds no SQL string and is verified as before\nsummary: %s",
			cat.Status, cat.Summary)
	}
	if got := cat.Details["scoped_rpcs"]; got != 1 {
		t.Errorf("scoped_rpcs = %v, want 1", got)
	}
}

// TestUnscopedAuth_RawSQLInPackageConstIsFound pins the indirection this
// shape actually uses. Nobody inlines a forty-line rollup at the call
// site; it is a package-level const, and a check that only looked at
// literals inside the handler body would report the leaking project
// clean — the same silent pass in a new place.
func TestUnscopedAuth_RawSQLInPackageConstIsFound(t *testing.T) {
	seam := codegen.CRUDAuthSeam()
	src := handlerHeader +
		"const fuelRollup = \"" + leakingAggregateQuery + "\"\n\n" +
		"func (s *Service) GetFuelEfficiency(ctx context.Context, req *connect.Request[pb.GetFuelEfficiencyRequest]) (*connect.Response[pb.GetFuelEfficiencyResponse], error) {\n" +
		"\tif _, err := " + seam + "(ctx); err != nil {\n\t\treturn nil, err\n\t}\n" +
		"\trows, err := s.db.Query(ctx, fuelRollup)\n" +
		"\tif err != nil {\n\t\treturn nil, err\n\t}\n" +
		"\t_ = rows\n\treturn nil, nil\n}\n"
	dir := writeProject(t, "ShopService", map[string]bool{"GetFuelEfficiency": true}, src)
	writeOwnerMigration(t, dir, "vehicles", "fleet_id")

	if cat := auditUnscopedAuth(nil, dir); cat.Status != audittype.StatusError {
		t.Fatalf("status = %q, want error — the query is a package const, which is where a real rollup lives\nsummary: %s",
			cat.Status, cat.Summary)
	}
}

// TestUnscopedAuth_RawSQLInPackageHelperIsFound is the same point one hop
// out. This category already follows package-local calls to find the auth
// seam; it has to follow them to find the query too, or factoring the SQL
// into a helper — the normal thing to do — silences the check.
func TestUnscopedAuth_RawSQLInPackageHelperIsFound(t *testing.T) {
	seam := codegen.CRUDAuthSeam()
	src := handlerHeader +
		"func (s *Service) GetFuelEfficiency(ctx context.Context, req *connect.Request[pb.GetFuelEfficiencyRequest]) (*connect.Response[pb.GetFuelEfficiencyResponse], error) {\n" +
		"\tif _, err := " + seam + "(ctx); err != nil {\n\t\treturn nil, err\n\t}\n" +
		"\treturn nil, s.runRollup(ctx)\n}\n\n" +
		"func (s *Service) runRollup(ctx context.Context) error {\n" +
		"\t_, err := s.db.Query(ctx, \"" + leakingAggregateQuery + "\")\n\treturn err\n}\n"
	dir := writeProject(t, "ShopService", map[string]bool{"GetFuelEfficiency": true}, src)
	writeOwnerMigration(t, dir, "vehicles", "fleet_id")

	if cat := auditUnscopedAuth(nil, dir); cat.Status != audittype.StatusError {
		t.Fatalf("status = %q, want error — the SQL is one package-local hop away, the same distance this category already follows to find the seam\nsummary: %s",
			cat.Status, cat.Summary)
	}
}

// TestUnscopedAuth_ProseMentioningATableIsNotAQuery keeps the SQL
// detector from firing on ordinary strings. A log line or an error
// message naming an owned table is not a query, and treating it as one
// would put this check straight into the ignored pile.
func TestUnscopedAuth_ProseMentioningATableIsNotAQuery(t *testing.T) {
	seam := codegen.CRUDAuthSeam()
	src := handlerHeader +
		"func (s *Service) GetVehicleSummary(ctx context.Context, req *connect.Request[pb.GetVehicleSummaryRequest]) (*connect.Response[pb.GetVehicleSummaryResponse], error) {\n" +
		"\tif _, err := " + seam + "(ctx); err != nil {\n\t\treturn nil, err\n\t}\n" +
		"\ts.log.Info(\"summarising vehicles for the caller\")\n" +
		"\treturn nil, nil\n}\n"
	dir := writeProject(t, "ShopService", map[string]bool{"GetVehicleSummary": true}, src)
	writeOwnerMigration(t, dir, "vehicles", "fleet_id")

	cat := auditUnscopedAuth(nil, dir)

	if cat.Status != audittype.StatusOK {
		t.Fatalf("status = %q, want ok — a log message naming a table is not a query over it\nsummary: %s", cat.Status, cat.Summary)
	}
}
