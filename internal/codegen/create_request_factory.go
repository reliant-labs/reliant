package codegen

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
		spec.imports = fix.importsUsedBy(spec.fields)
		spec.failure = verifyCreateRequest(ctx, fix, cm.Entity.TableName, spec.parentSQL)
		specs = append(specs, spec)
	}
	return specs
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
	if s.failure == "" && s.parentSQL != "" {
		fmt.Fprintf(b, "const %s = %s\n\n", constName, backquoteOrQuote(s.parentSQL))
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
	fmt.Fprintf(b, "func %s(t testing.TB, database orm.Context, variant int) *pb.%s {\n", s.funcName, s.inputType)
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
