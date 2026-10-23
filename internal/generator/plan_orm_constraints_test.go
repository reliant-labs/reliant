package generator

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// Constraint-name constants exist for the same reason column constants do:
// so a service that branches on a schema identifier does not spell it by
// hand. orm.ConstraintName(err) returns "jobs_estimate_id_key" and the
// service must compare it against something; without a generated constant
// the only option is a literal, and renaming the constraint in a migration
// then leaves the build green while the branch silently stops matching.
//
// These tests pin the SHAPE. The value's agreement with what postgres
// actually reports is pinned separately, against a real server, in
// plan_orm_constraints_pg_test.go — a constant that does not match the
// runtime value is worse than no constant at all.

func constraintTestEntity(cs []config.PlanEntityConstraint) config.PlanEntity {
	return config.PlanEntity{
		Name:      "Job",
		TableName: "jobs",
		Fields: []config.PlanEntityField{
			{Name: "id", Type: "string", PrimaryKey: true},
			{Name: "estimate_id", Type: "string", NotNull: true},
			{Name: "status", Type: "string", NotNull: true},
			{Name: "total_cents", Type: "int64", NotNull: true},
		},
		Constraints: cs,
	}
}

func TestRenderORMEntity_EmitsUniqueAndCheckConstraintConstants(t *testing.T) {
	code := string(renderORMEntity("db", constraintTestEntity([]config.PlanEntityConstraint{
		{Name: "jobs_estimate_id_key", Kind: config.ConstraintKindUnique},
		{Name: "jobs_total_cents_check", Kind: config.ConstraintKindCheck},
	}), false))

	// EstimateID: the identifier uses the same Go casing as the sibling
	// JobFieldEstimateID and the struct's own EstimateID field. One column
	// must not be spelled two ways in one file.
	for _, want := range []string{
		`JobConstraintEstimateIDKey = "jobs_estimate_id_key"`,
		`JobConstraintTotalCentsCheck = "jobs_total_cents_check"`,
	} {
		if !strings.Contains(code, want) {
			t.Errorf("generated ORM missing constraint constant:\n  want line containing: %s\ngot:\n%s", want, code)
		}
	}
}

// A foreign key is a named constraint a service routinely branches on:
// 23503 means the parent row is gone, which is a caller-visible NotFound,
// not a server fault.
func TestRenderORMEntity_EmitsForeignKeyConstraintConstant(t *testing.T) {
	code := string(renderORMEntity("db", constraintTestEntity([]config.PlanEntityConstraint{
		{Name: "jobs_estimate_id_fkey", Kind: config.ConstraintKindForeignKey},
	}), false))

	if want := `JobConstraintEstimateIDFkey = "jobs_estimate_id_fkey"`; !strings.Contains(code, want) {
		t.Errorf("generated ORM missing FK constraint constant %q; got:\n%s", want, code)
	}
}

// A constraint whose name does NOT begin with the table name keeps its full
// name in the identifier — stripping is a readability affordance for the
// prefix postgres itself adds, not a rule about what the constant means.
func TestRenderORMEntity_UnprefixedConstraintNameKeepsFullName(t *testing.T) {
	code := string(renderORMEntity("db", constraintTestEntity([]config.PlanEntityConstraint{
		{Name: "total_is_subtotal_plus_tax", Kind: config.ConstraintKindCheck},
	}), false))

	if want := `JobConstraintTotalIsSubtotalPlusTax = "total_is_subtotal_plus_tax"`; !strings.Contains(code, want) {
		t.Errorf("generated ORM missing unprefixed constraint constant %q; got:\n%s", want, code)
	}
}

// Stripping the table prefix can make two distinct constraint names collide
// on one Go identifier. Emitting the same const twice does not compile, and
// picking a winner would silently publish one constraint's name under the
// other's spelling — so the colliding pair falls back to the FULL name,
// which postgres guarantees is distinct within a table.
func TestRenderORMEntity_PrefixStripCollisionFallsBackToFullName(t *testing.T) {
	code := string(renderORMEntity("db", constraintTestEntity([]config.PlanEntityConstraint{
		{Name: "jobs_status_key", Kind: config.ConstraintKindUnique},
		{Name: "status_key", Kind: config.ConstraintKindUnique},
	}), false))

	for _, want := range []string{
		`JobConstraintJobsStatusKey = "jobs_status_key"`,
		`JobConstraintStatusKey = "status_key"`,
	} {
		if !strings.Contains(code, want) {
			t.Errorf("collision fallback missing %q; got:\n%s", want, code)
		}
	}
}

// An entity with no constraints must emit no constraint block at all — an
// empty `const ()` is not valid-looking generated code and an empty exported
// name would trip revive's exported rule.
func TestRenderORMEntity_NoConstraintsEmitsNoBlock(t *testing.T) {
	code := string(renderORMEntity("db", constraintTestEntity(nil), false))
	if strings.Contains(code, "JobConstraint") {
		t.Errorf("entity with no constraints emitted a constraint constant; got:\n%s", code)
	}
}

// A rename in a migration must move the constant's VALUE, because that is
// the whole point: the compiler cannot see a renamed string literal, but it
// can see a renamed constant.
func TestRenderORMEntity_ConstraintRenameMovesTheConstant(t *testing.T) {
	before := string(renderORMEntity("db", constraintTestEntity([]config.PlanEntityConstraint{
		{Name: "jobs_estimate_id_key", Kind: config.ConstraintKindUnique},
	}), false))
	after := string(renderORMEntity("db", constraintTestEntity([]config.PlanEntityConstraint{
		{Name: "one_job_per_estimate", Kind: config.ConstraintKindUnique},
	}), false))

	if !strings.Contains(before, `JobConstraintEstimateIDKey = "jobs_estimate_id_key"`) {
		t.Fatalf("pre-rename constant missing; got:\n%s", before)
	}
	if strings.Contains(after, "jobs_estimate_id_key") {
		t.Error("renamed constraint still emits the OLD name — the constant would keep matching a constraint that no longer exists")
	}
	if want := `JobConstraintOneJobPerEstimate = "one_job_per_estimate"`; !strings.Contains(after, want) {
		t.Errorf("post-rename constant missing %q; got:\n%s", want, after)
	}
}
