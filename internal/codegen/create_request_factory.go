package codegen

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/schemadef"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// Create-request factories: the rows the scaffolded CRUD lifecycle test
// creates, regenerated from the applied schema on every `forge generate`.
//
// handlers_crud_test.go is scaffold-once — the user's from line one, and never
// rewritten. It used to carry its fixtures inline: a literal INSERT block for
// the entity's foreign-key parents and literal create-request values, both
// derived from the schema AS IT STOOD AT BIRTH. The db skill then tells the
// author to harden that very schema right afterwards, and two of its own
// recommendations broke the frozen literals outright:
//
//   - a column made `GENERATED ALWAYS AS (…) STORED` — postgres refuses any
//     INSERT that names it, and the parent seed named it:
//
//     seed parent rows: pq: cannot insert a non-DEFAULT value into column "remaining_cents" (428C9)
//
//   - a one-way status CHECK (`status <> 'RESOLVED' OR resolved_at IS NOT
//     NULL`) — create #2 carried the second enum value with no stamp.
//
// Neither is a bug in either half; the defect is the shape. Forge KNEW the
// right answer on every later run (the regenerated factories_gen_test.go
// already tracked the schema), and the knowledge never reached a file forge
// does not write. So the data moved to the file forge does write: the owned
// test now calls New<CreateRequest>(t, db, variant), and this file renders it.
//
// WHAT A REQUEST CARRIES. The values are the fixture model's
// (crud_test_fixtures.go): FK fields reference parents a MINIMAL seed plan
// inserts first, plain scalars carry constraint-satisfying values, a
// discriminated union is satisfied by the minimal row's branch. Which fields
// are SET follows the minimal row too (seedplan.Config.Minimal):
//
//   - a plain scalar is always set. A proto3 scalar has no "unset" — leaving
//     it zero still writes zero — so a valid value is the only safe choice;
//   - an enum is set only when the minimal row writes the column. Otherwise
//     the create stores the column DEFAULT, which is a fresh row's lifecycle
//     state and exactly what a status guard's initial branch expects;
//   - a timestamp is set only when the schema requires one (NOT NULL with no
//     DEFAULT, an ordered NOT NULL pair, or a guard's consequent on the
//     branch taken), so a fresh row does not arrive already stamped;
//   - an explicit-presence (`optional`) field, a repeated field, and a field a
//     union requires absent are left unset.
//
// WHEN THE SCHEMA DEFEATS THE DERIVATION. Each request is checked against the
// live shadow schema before it is emitted — its parent INSERTs executed in a
// rolled-back transaction, its values evaluated against their columns'
// CHECKs. A failure does not fail the generate (the file is regenerated on
// every run, and refusing to generate over a test fixture would hold every
// other change hostage) and does not emit a request forge can prove wrong:
// the factory's body fails the calling test with postgres's own verdict, and
// the generate run prints the same message as a warning.

// createRequestSpec is one create RPC's baked factory.
type createRequestSpec struct {
	funcName  string // "NewCreateTicketRequest"
	inputType string // "CreateTicketRequest"
	entity    string // "Ticket"
	parentSQL string // FK-parent INSERTs, two rows per parent, each ON CONFLICT DO NOTHING
	fields    []CRUDTestFieldData
	// imports are the foreign Go packages (alias -> path) the request's
	// enum literals reference beyond `pb`.
	imports map[string]string
	// failure, when set, is why no request could be derived; the emitted
	// body fails the test with it instead of returning a request.
	failure string
	// ownerAware marks a request whose entity or FK parents declare
	// `forge:owner`: its factory takes FactoryOwner (see factoryOwnerPlan).
	ownerAware bool
	// ownerCells are the byte ranges of parentSQL that hold a planned owner
	// value: a parent's forge:owner cell, or a key cell that copies one.
	// FactoryOwner rewrites exactly these to the caller's owner, and no other
	// cell, whatever value it holds.
	ownerCells [][2]int
	// ownerField is the request field (Go name) carrying the entity's own
	// owner column, "" when the request has none (a forge:read-only owner,
	// which the scoped handler stamps instead).
	ownerField string
}

// createRequestFactoryName is the factory's Go name for a create RPC — the
// request type prefixed with New. The scaffold-once lifecycle test calls it by
// this name, so both sides derive it from the same CRUDMethod.
func createRequestFactoryName(cm CRUDMethod) string {
	return "New" + cm.Method.InputGoName()
}

// buildCreateRequestFields derives one create RPC's request fields and their
// two variants' values. See the file header for which fields are set.
func buildCreateRequestFields(svc ServiceDef, cm CRUDMethod, fix *crudTestFixtures) []CRUDTestFieldData {
	msgFields, ok := svc.Messages[cm.Method.InputType]
	if !ok {
		return nil
	}
	var out []CRUDTestFieldData
	for _, f := range msgFields {
		// Explicit-presence fields are pointers on the wire struct and, by
		// definition, omittable: unset writes NULL.
		if f.IsOptional {
			continue
		}
		// Repeated fields are slices on the wire struct and equally
		// omittable. A repeated ENUM's entity GoType drops the slice marker,
		// so the proto type is what identifies one.
		if strings.HasPrefix(f.ProtoType, "[]") {
			continue
		}
		// A discriminated-union CHECK requires this column to hold NO value
		// on the branch the request is written against. No literal means
		// absent; an unset field writes NULL.
		if fix.unionOmitsField(cm.Entity, f.Name) {
			continue
		}
		// A GENERATED column is the database's to compute. The create
		// cannot write it, so a value here would only read as if it did.
		if fix.columnIsGenerated(cm.Entity, f.Name) {
			continue
		}
		goType := ProtoTypeToGoType(f.ProtoType)
		for _, ef := range cm.Entity.Fields {
			if ef.Name == f.Name {
				goType = ef.GoType
				break
			}
		}
		kind := DetermineFieldKind(f.ProtoType, goType)
		if kind != FieldKindScalar && kind != FieldKindEnum && kind != FieldKindTimestamp {
			continue
		}

		writes, known := fix.minimalWrites(cm.Entity, f.Name)
		required := writes || fix.unionRequiresPresent(cm.Entity, f.Name)
		if (kind == FieldKindEnum || kind == FieldKindTimestamp) && known && !required {
			// The database fills it: an unset enum stores the column
			// DEFAULT, an unset timestamp writes NULL or its DEFAULT.
			continue
		}

		tv1, tv2, okFix := fix.fieldFixture(svc, cm.Entity, cm.Method.InputType, f.Name, goType, kind)
		if !okFix {
			if kind == FieldKindTimestamp {
				if !required {
					continue // no schema model: the historical unset
				}
				// Required, and nothing orders it: any instant will do.
				tv1, tv2 = timestampAfter(0), timestampAfter(1)
			} else {
				tv1, tv2 = testValueForType(goType), testValueForType2(goType)
				// The legacy literal is a fixture like any other, written to
				// the same column under the same constraints — and the one
				// the guard most needs to see, since reaching it means the
				// derivation found nothing to go on.
				fix.record(cm.Entity.TableName, f.Name, tv1, goType)
				fix.record(cm.Entity.TableName, f.Name, tv2, goType)
			}
		}
		out = append(out, CRUDTestFieldData{
			ProtoName:  messageFieldGoName(msgFields, f.Name),
			GoType:     goType,
			Kind:       kind,
			TestValue:  tv1,
			TestValue2: tv2,
		})
	}
	return out
}

// buildCreateRequestSpecs bakes one spec per create RPC of svc, verified
// against the fixture model's live shadow schema.
func buildCreateRequestSpecs(ctx context.Context, svc ServiceDef, methods []CRUDMethod, fix *crudTestFixtures) []createRequestSpec {
	var specs []createRequestSpec
	for _, cm := range methods {
		if cm.Operation != "create" {
			continue
		}
		spec := createRequestSpec{
			funcName:  createRequestFactoryName(cm),
			inputType: cm.Method.InputGoName(),
			entity:    cm.Entity.Name,
			fields:    buildCreateRequestFields(svc, cm, fix),
		}
		if esp := fix.plans[cm.Entity.TableName]; esp != nil {
			spec.parentSQL = esp.seedSQL
		}
		var ownerErr error
		spec.ownerAware, spec.ownerCells, spec.ownerField, ownerErr = fix.factoryOwnerPlan(svc, cm, spec.fields)
		spec.imports = fix.importsUsedBy(spec.fields)
		spec.failure = verifyCreateRequest(ctx, fix, cm.Entity.TableName, spec.parentSQL)
		if spec.failure == "" && ownerErr != nil {
			spec.failure = ownerErr.Error()
		}
		specs = append(specs, spec)
	}
	return specs
}

// factoryOwnerPlan derives what FactoryOwner rewrites in one create
// request's factory.
//
// A scoped Create stamps its row's owner from the CALLER, while the factory
// seeded that row's parents under owners the seed planner invented. With any
// same-owner guard in the schema — a composite (tenant_id, id) foreign key, a
// trigger — every scoped create in the lifecycle test then failed. So when
// the entity or any parent declares forge:owner, the factory takes the
// owner: every owner cell the parent rows were planned with is rewritten to
// it, and so is every foreign key that copies one (the planner gives a
// reference the very value it references), and the request's own owner field
// is set to it.
//
// The rewrite is cell-exact: cells are the byte ranges of the parent SQL in
// owner-linked columns (ownerLinkedColumns) whose value is a planned owner.
// Any other cell keeps its value even when it happens to equal an owner — a
// tenant's slug, say — so FactoryOwner never changes what a column the owner
// has nothing to do with was planned to hold.
//
// aware is false when no table the request touches declares an owner; the
// factory then keeps its plain signature, so FactoryOwner on it does not
// compile rather than doing nothing.
func (fx *crudTestFixtures) factoryOwnerPlan(svc ServiceDef, cm CRUDMethod, fields []CRUDTestFieldData) (aware bool, cells [][2]int, field string, err error) {
	if fx == nil {
		return false, nil, "", nil
	}
	if col := ownerColumnOf(fx.tables[cm.Entity.TableName]); col != "" {
		aware = true
		goName := messageFieldGoName(svc.Messages[cm.Method.InputType], col)
		for _, f := range fields {
			if f.ProtoName == goName && f.GoType == "string" {
				field = goName
			}
		}
	}
	esp := fx.plans[cm.Entity.TableName]
	if esp == nil || esp.plan == nil {
		return aware, nil, field, nil
	}
	// The entity's own planned row is included although it is never
	// inserted: its owner value is the one its owner FOREIGN KEY copies off
	// a parent (tenants.id under agent_runs.tenant_id), and that parent has
	// to move to the owner too — whether or not it declares forge:owner.
	planned := map[string]bool{}
	for _, table := range esp.plan.Tables() {
		col := ownerColumnOf(fx.tables[table])
		if col == "" {
			continue
		}
		aware = true
		for row := 0; row < 2; row++ {
			if raw, ok := esp.plan.NaturalValue(table, col, row); ok {
				planned[sqlStringLiteral(raw)] = true
			}
		}
	}
	if !aware || len(planned) == 0 || esp.seedSQL == "" {
		return aware, nil, field, nil
	}
	cells, err = ownerCellRanges(esp.seedSQL, fx.ownerLinkedColumns(esp.plan.Tables()), planned)
	if err != nil {
		return aware, nil, field, fmt.Errorf("forge could not place FactoryOwner in the parent seed SQL (a forge bug; please report it): %w", err)
	}
	return aware, cells, field, nil
}

// ownerLinkedColumns returns, per table (keyed by its SQL spelling, as the
// seed statements name it), the columns whose cells carry an owner value:
// every forge:owner column, and every column joined to one by a foreign key,
// in either direction and transitively — tenants.id because
// strategies.tenant_id (an owner) references it, and any other key that
// references tenants.id because it copies the same value.
func (fx *crudTestFixtures) ownerLinkedColumns(tables []string) map[string]map[string]bool {
	inPlan := make(map[string]bool, len(tables))
	for _, t := range tables {
		inPlan[t] = true
	}
	linked := map[[2]string]bool{}
	for _, t := range tables {
		if col := ownerColumnOf(fx.tables[t]); col != "" {
			linked[[2]string{t, col}] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, t := range tables {
			for _, fk := range fx.foreignKeys(fx.tables[t]) {
				if !inPlan[fk.RefTable] {
					continue
				}
				from, to := [2]string{t, fk.Column}, [2]string{fk.RefTable, fk.RefColumn}
				if linked[from] != linked[to] {
					linked[from], linked[to] = true, true
					changed = true
				}
			}
		}
	}
	out := map[string]map[string]bool{}
	for tc := range linked {
		q := seedplan.QualifiedTable(fx.tables[tc[0]])
		if out[q] == nil {
			out[q] = map[string]bool{}
		}
		out[q][tc[1]] = true
	}
	return out
}

// ownerCellRanges lexes the parent seed SQL — the seed planner's INSERT
// statements, `INSERT INTO <table> (<cols>) VALUES (<cells>), … ON CONFLICT …;`
// — and returns the byte range of every cell that sits in one of linked's
// columns and holds one of the planned owner literals, in order. A cell cast
// to a type (`'…'::uuid`) matches on its quoted literal, which is the range
// returned. Statements without a column list (DEFAULT VALUES) hold no cell.
func ownerCellRanges(sql string, linked map[string]map[string]bool, planned map[string]bool) ([][2]int, error) {
	var cells [][2]int
	for pos := 0; pos < len(sql); {
		const insert = "INSERT INTO "
		if !strings.HasPrefix(sql[pos:], insert) {
			return nil, fmt.Errorf("offset %d: expected %q", pos, insert)
		}
		lineEnd := strings.IndexByte(sql[pos:], '\n')
		if lineEnd < 0 {
			lineEnd = len(sql) - pos
		}
		line := sql[pos : pos+lineEnd]
		if strings.HasSuffix(line, " DEFAULT VALUES;") {
			// Every cell of the row is the database's.
			pos = skipNewlines(sql, pos+lineEnd)
			continue
		}
		if !strings.HasSuffix(line, " VALUES") {
			return nil, fmt.Errorf("offset %d: unreadable INSERT header %q", pos, line)
		}
		head := line[len(insert) : len(line)-len(" VALUES")]
		open := strings.Index(head, " (")
		if open < 0 || !strings.HasSuffix(head, ")") {
			return nil, fmt.Errorf("offset %d: unreadable INSERT header %q", pos, head)
		}
		table := head[:open]
		var cols []string
		for _, c := range strings.Split(head[open+2:len(head)-1], ", ") {
			if len(c) < 2 || c[0] != '"' || c[len(c)-1] != '"' {
				return nil, fmt.Errorf("offset %d: unreadable column %q", pos, c)
			}
			cols = append(cols, strings.ReplaceAll(c[1:len(c)-1], `""`, `"`))
		}
		i := pos + lineEnd + 1
		for {
			for i < len(sql) && (sql[i] == ' ' || sql[i] == '\n') {
				i++
			}
			if i >= len(sql) || sql[i] != '(' {
				return nil, fmt.Errorf("offset %d: expected a row tuple", i)
			}
			i++
			for col := 0; ; col++ {
				start := i
				next, err := scanSQLCell(sql, i)
				if err != nil {
					return nil, err
				}
				if col >= len(cols) {
					return nil, fmt.Errorf("offset %d: row has more cells than %s names columns", start, table)
				}
				if linked[table][cols[col]] {
					cell := sql[start:next]
					for lit := range planned {
						if cell == lit || strings.HasPrefix(cell, lit+"::") {
							cells = append(cells, [2]int{start, start + len(lit)})
							break
						}
					}
				}
				i = next
				if sql[i] == ')' {
					if col != len(cols)-1 {
						return nil, fmt.Errorf("offset %d: row has fewer cells than %s names columns", i, table)
					}
					i++
					break
				}
				i += len(", ")
			}
			if i < len(sql) && sql[i] == ',' {
				i++
				continue
			}
			break
		}
		// The ON CONFLICT clause names columns only: no literal, no ';'.
		end := strings.IndexByte(sql[i:], ';')
		if end < 0 {
			return nil, fmt.Errorf("offset %d: statement has no terminating ';'", i)
		}
		pos = skipNewlines(sql, i+end+1)
	}
	return cells, nil
}

// scanSQLCell returns the offset just past the cell starting at i: the next
// ',' or ')' outside a quoted literal and outside brackets.
func scanSQLCell(sql string, i int) (int, error) {
	depth := 0
	for i < len(sql) {
		switch c := sql[i]; {
		case c == '\'':
			i++
			for i < len(sql) && (sql[i] != '\'' || (i+1 < len(sql) && sql[i+1] == '\'')) {
				if sql[i] == '\'' {
					i++
				}
				i++
			}
			if i >= len(sql) {
				return 0, fmt.Errorf("unterminated string literal")
			}
		case c == '(' || c == '[':
			depth++
		case (c == ')' || c == ']') && depth > 0:
			depth--
		case (c == ',' || c == ')') && depth == 0:
			return i, nil
		}
		i++
	}
	return 0, fmt.Errorf("unterminated row tuple")
}

func skipNewlines(s string, i int) int {
	for i < len(s) && s[i] == '\n' {
		i++
	}
	return i
}

// ownerColumnOf is the table's first `forge:owner` column, "" for none —
// the column codegen.OwnerColumn names for the entity.
func ownerColumnOf(t schemadef.Table) string {
	for _, c := range t.Columns {
		if declaresOwnerMarker(c.Comment) {
			return c.Name
		}
	}
	return ""
}

// sqlStringLiteral quotes s the way the seed planner renders a text cell.
func sqlStringLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// verifyCreateRequest checks a baked request against the live shadow schema:
// the parent INSERTs must execute, and every recorded value must satisfy its
// column's CHECKs. It returns "" when both hold or when there is no live
// schema to ask (a hand-built model in a unit test), else the failure.
func verifyCreateRequest(ctx context.Context, fix *crudTestFixtures, table, parentSQL string) string {
	if fix == nil || fix.shadow == nil {
		return ""
	}
	if err := execRolledBack(ctx, fix.shadow.DB(), sqlStep{query: parentSQL}); err != nil {
		return fmt.Sprintf("seeding the foreign-key parents failed: %v%s", err, fix.unplacedConstraintNote(table, err.Error()))
	}
	t, ok := fix.tables[table]
	if !ok {
		return ""
	}
	violations, _, err := verifyFixtures(ctx, fix.shadow.DB(), t, fix.emitted[table])
	if err != nil {
		return fmt.Sprintf("could not verify the request against the applied schema: %v", err)
	}
	if len(violations) > 0 {
		return (&FixtureConstraintError{Violations: violations}).Error()
	}
	return ""
}

// sqlStep is one statement execRolledBack runs, with its bind arguments.
type sqlStep struct {
	query string
	args  []any
}

// execRolledBack executes steps against db, in order, inside a transaction
// that is always rolled back, and returns the first error postgres raises. It
// is how a generator asks the authority whether SQL it is about to emit would
// run, without leaving a trace in the database it asked.
func execRolledBack(ctx context.Context, db *sql.DB, steps ...sqlStep) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, step := range steps {
		if strings.TrimSpace(step.query) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, step.query, step.args...); err != nil {
			return err
		}
	}
	return nil
}

// renderCreateRequestFactory writes one create-request factory into b.
func renderCreateRequestFactory(b *strings.Builder, s createRequestSpec) {
	fmt.Fprintf(b, "\n// --- %s ---\n\n", s.inputType)
	constName := lowerFirst(strings.TrimPrefix(s.funcName, "New")) + "ParentSQL"
	ownersName := lowerFirst(strings.TrimPrefix(s.funcName, "New")) + "ParentOwners"
	if s.failure == "" && s.parentSQL != "" {
		fmt.Fprintf(b, "const %s = %s\n\n", constName, backquoteOrQuote(s.parentSQL))
		if s.ownerAware && len(s.ownerCells) > 0 {
			fmt.Fprintf(b, "// %s are the byte ranges of %s that hold a planned\n", ownersName, constName)
			b.WriteString("// owner value — a forge:owner cell or a key that copies one. FactoryOwner\n")
			b.WriteString("// rewrites these and no other cell.\n")
			fmt.Fprintf(b, "var %s = [][2]int{", ownersName)
			for i, c := range s.ownerCells {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(b, "{%d, %d}", c[0], c[1])
			}
			b.WriteString("}\n\n")
		}
	}
	fmt.Fprintf(b, "// %s returns a %s the applied schema accepts, after\n", s.funcName, s.inputType)
	b.WriteString("// seeding the foreign-key parent rows it references. Variants 0 and 1 are two\n")
	b.WriteString("// distinct rows — they differ on every field that admits a second value and\n")
	b.WriteString("// reference distinct parents; any other variant fails the test.\n")
	b.WriteString("//\n")
	b.WriteString("// Fields the database fills on its own are left unset — an enum the column\n")
	b.WriteString("// DEFAULT supplies, a nullable timestamp — so the created row starts at its\n")
	b.WriteString("// initial lifecycle state. Override what your test asserts on:\n")
	b.WriteString("//\n")
	fmt.Fprintf(b, "//\treq := %s(t, database, 0)\n", s.funcName)
	b.WriteString("//\treq.<Field> = …\n")
	b.WriteString("//\n")
	b.WriteString("// Regenerated from db/migrations on every `forge generate`, so it tracks\n")
	b.WriteString("// your schema; the scaffold-once handlers_crud_test.go calls it for exactly\n")
	b.WriteString("// that reason.\n")
	if s.ownerAware {
		b.WriteString("//\n")
		b.WriteString("// The table or its parents declare forge:owner. Pass FactoryOwner(owner) to\n")
		b.WriteString("// seed the parents under the owner your scoped handler will stamp on the\n")
		b.WriteString("// row, so a same-owner guard in the schema accepts the create:\n")
		b.WriteString("//\n")
		fmt.Fprintf(b, "//\treq := %s(t, database, 0, FactoryOwner(\"tenant-1\"))\n", s.funcName)
		fmt.Fprintf(b, "func %s(t testing.TB, database orm.Context, variant int, opts ...FactoryOption) *pb.%s {\n", s.funcName, s.inputType)
	} else {
		fmt.Fprintf(b, "func %s(t testing.TB, database orm.Context, variant int) *pb.%s {\n", s.funcName, s.inputType)
	}
	b.WriteString("\tt.Helper()\n")
	if s.failure != "" {
		// database and variant stay in the signature so the callers compile;
		// there is no request to build.
		fmt.Fprintf(b, "\tt.Fatalf(\"%s: forge could not derive a request the applied schema accepts (seen at `forge generate`):\\n%%s\", %s)\n",
			s.funcName, backquoteOrQuote(s.failure))
		b.WriteString("\treturn nil\n")
		b.WriteString("}\n")
		return
	}
	if s.ownerAware {
		renderOwnerAwareRequestBody(b, s, constName, ownersName)
		return
	}
	if s.parentSQL != "" {
		fmt.Fprintf(b, "\tseedFactoryParents(t, database, %s)\n", constName)
	}
	b.WriteString("\tswitch variant {\n")
	for v := 0; v < 2; v++ {
		fmt.Fprintf(b, "\tcase %d:\n", v)
		if len(s.fields) == 0 {
			fmt.Fprintf(b, "\t\treturn &pb.%s{}\n", s.inputType)
			continue
		}
		fmt.Fprintf(b, "\t\treturn &pb.%s{\n", s.inputType)
		for _, f := range s.fields {
			lit := f.TestValue
			if v == 1 {
				lit = f.TestValue2
			}
			fmt.Fprintf(b, "\t\t\t%s: %s,\n", f.ProtoName, lit)
		}
		b.WriteString("\t\t}\n")
	}
	b.WriteString("\t}\n")
	fmt.Fprintf(b, "\tt.Fatalf(\"%s: variant %%d: only variants 0 and 1 are distinct rows\", variant)\n", s.funcName)
	b.WriteString("\treturn nil\n")
	b.WriteString("}\n")
}

// renderOwnerAwareRequestBody writes the body of a factory that takes
// FactoryOwner: the parents seeded under the owner, the request built, and
// its own owner field set.
func renderOwnerAwareRequestBody(b *strings.Builder, s createRequestSpec, constName, ownersName string) {
	// A self-owned row with no parents (tenants, owned by its own id) seeds
	// nothing and carries no owner field: declaring owner there is a
	// "declared and not used" compile error in the generated test file.
	if s.parentSQL != "" || s.ownerField != "" {
		b.WriteString("\towner := factoryOwner(opts)\n")
	}
	if s.parentSQL != "" {
		owners := "nil"
		if len(s.ownerCells) > 0 {
			owners = ownersName
		}
		fmt.Fprintf(b, "\tseedFactoryParents(t, database, withFactoryOwner(%s, %s, owner))\n", constName, owners)
	}
	fmt.Fprintf(b, "\tvar req *pb.%s\n", s.inputType)
	b.WriteString("\tswitch variant {\n")
	for v := 0; v < 2; v++ {
		fmt.Fprintf(b, "\tcase %d:\n", v)
		fmt.Fprintf(b, "\t\treq = &pb.%s{\n", s.inputType)
		for _, f := range s.fields {
			lit := f.TestValue
			if v == 1 {
				lit = f.TestValue2
			}
			fmt.Fprintf(b, "\t\t\t%s: %s,\n", f.ProtoName, lit)
		}
		b.WriteString("\t\t}\n")
	}
	b.WriteString("\tdefault:\n")
	fmt.Fprintf(b, "\t\tt.Fatalf(\"%s: variant %%d: only variants 0 and 1 are distinct rows\", variant)\n", s.funcName)
	b.WriteString("\t\treturn nil\n")
	b.WriteString("\t}\n")
	if s.ownerField != "" {
		b.WriteString("\tif owner != \"\" {\n")
		fmt.Fprintf(b, "\t\treq.%s = owner\n", s.ownerField)
		b.WriteString("\t}\n")
	}
	b.WriteString("\treturn req\n")
	b.WriteString("}\n")
}

// createSpecsOwnerAware reports whether any request factory in a group takes
// FactoryOwner, so the file declares the option and its helpers.
func createSpecsOwnerAware(specs []createRequestSpec) bool {
	for _, s := range specs {
		if s.failure == "" && s.ownerAware {
			return true
		}
	}
	return false
}

// factoryOwnerHelperSource declares FactoryOwner and the helpers the
// owner-aware request factories call. Emitted only into a file that has one.
const factoryOwnerHelperSource = `// FactoryOption adjusts what a New<CreateRequest> factory seeds.
type FactoryOption func(*factoryOptions)

type factoryOptions struct{ owner string }

// FactoryOwner seeds the request's foreign-key parents under owner — every
// forge:owner column of every parent row, and every reference to one — and
// sets the request's own owner field when it has one. Pass the owner your
// scoped handler stamps on the created row (for the scaffolded wrapper,
// whatever it maps crudTestCtx()'s claims to), so a schema that holds a
// child to its parent's owner accepts the create.
func FactoryOwner(owner string) FactoryOption {
	return func(o *factoryOptions) { o.owner = owner }
}

func factoryOwner(opts []FactoryOption) string {
	var o factoryOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o.owner
}

// withFactoryOwner rewrites the owner cells of sql — the byte ranges cells
// names, in order — to owner. "" leaves sql as planned.
func withFactoryOwner(sql string, cells [][2]int, owner string) string {
` + rewriteOwnerLiteralsBody + `}
`

// rewriteOwnerLiteralsBody is the body of the emitted withFactoryOwner, and
// of rewriteOwnerLiterals, the generator-side copy forge's own tests execute
// against postgres (TestCreateRequestFactory_SeedsParentsUnderTheCallersOwner_PG
// pins the two equal).
const rewriteOwnerLiteralsBody = `	if owner == "" || len(cells) == 0 {
		return sql
	}
	lit := "'" + strings.ReplaceAll(owner, "'", "''") + "'"
	var b strings.Builder
	last := 0
	for _, c := range cells {
		b.WriteString(sql[last:c[0]])
		b.WriteString(lit)
		last = c[1]
	}
	b.WriteString(sql[last:])
	return b.String()
`

// rewriteOwnerLiterals is withFactoryOwner, for forge's own tests.
func rewriteOwnerLiterals(sql string, cells [][2]int, owner string) string {
	if owner == "" || len(cells) == 0 {
		return sql
	}
	lit := "'" + strings.ReplaceAll(owner, "'", "''") + "'"
	var b strings.Builder
	last := 0
	for _, c := range cells {
		b.WriteString(sql[last:c[0]])
		b.WriteString(lit)
		last = c[1]
	}
	b.WriteString(sql[last:])
	return b.String()
}

// createSpecsNeed reports which optional imports a set of create-request
// factories' literals reference.
func createSpecsNeed(specs []createRequestSpec) (timestamppb, timePkg bool) {
	for _, s := range specs {
		if s.failure != "" {
			continue
		}
		for _, f := range s.fields {
			for _, lit := range []string{f.TestValue, f.TestValue2} {
				if strings.Contains(lit, "timestamppb.") {
					timestamppb = true
				}
				if strings.Contains(strings.ReplaceAll(lit, "timestamppb.", ""), "time.") {
					timePkg = true
				}
			}
		}
	}
	return timestamppb, timePkg
}

// importsUsedBy returns the foreign enum packages (alias -> path) that the
// given fields' literals reference.
func (fx *crudTestFixtures) importsUsedBy(fields []CRUDTestFieldData) map[string]string {
	var used map[string]string
	for alias, path := range fx.enumImports {
		for _, f := range fields {
			if strings.Contains(f.TestValue, alias+".") || strings.Contains(f.TestValue2, alias+".") {
				if used == nil {
					used = map[string]string{}
				}
				used[alias] = path
			}
		}
	}
	return used
}
