package cli

import (
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// The CRUD projection COMPILES AGAINST the generated ORM. Every op
// handlers_crud_ops_gen.go.tmpl renders names db.<Entity>,
// db.Create<Entity>, db.Get<Entity>ByID, db.List<Entity> — symbols that
// exist only in the internal/db/<entity>_orm_gen.go that stepInternalDBORM
// emits. So the two steps have to agree about when they run, and pre-fix
// they did not: the ORM half was gated on features.orm and the projection
// half was not.
//
// What that cost, in a real project: control-plane sets the ORM being off
// because its internal/db is hand-written. Adding a `Domain` proto message
// and a `domains` table was enough — entity matching resolves a message to
// a table BY NAME — to make `forge generate` emit a
// handlers_crud_ops_gen.go full of db.Domain / db.DomainBinding references
// and then fail its own `go build` validate step:
//
//	internal/handlers/domain/handlers_crud_ops_gen.go:154:107: undefined: db.Domain
//	Error: step "go build (validate generated code)": ... does not compile
//
// The project could not generate at all until the table was renamed to
// custom_domains purely to dodge codegen. Nothing about the project asked
// for CRUD; a name collision was sufficient, because no gate asked whether
// the generated store the projection calls into would exist.

// crudStepGate returns the live Gate of the named pipeline step, read from
// the real generateSteps() table rather than from a copy — a test that
// re-declared the gate would keep passing if the table stopped using it.
func crudStepGate(t *testing.T, stepName string) func(*pipelineContext) bool {
	t.Helper()
	for _, s := range generateSteps() {
		if s.Name == stepName {
			if s.Gate == nil {
				t.Fatalf("step %q has a nil Gate", stepName)
			}
			return s.Gate
		}
	}
	t.Fatalf("no pipeline step named %q — the step table was renamed and this guard is no longer pinning anything", stepName)
	return nil
}

func crudProjectionCtx(orm *bool) *pipelineContext {
	features := config.FeaturesConfig{}
	if orm != nil {
		features = features.With(config.FeatureORM, *orm)
	}
	return &pipelineContext{
		Cfg:         &config.ProjectConfig{Features: features},
		HasServices: true,
	}
}

// TestCRUDProjectionGate_SkippedWhenORMDisabled is the regression guard.
// With the ORM being off the ORM emitter does not run, so the projection
// that consumes its output must not run either.
func TestCRUDProjectionGate_SkippedWhenORMDisabled(t *testing.T) {
	ormOff := false
	ctx := crudProjectionCtx(&ormOff)

	// Sanity: the fixture must be one the ORM step really declines, or a
	// passing projection assertion would prove nothing.
	if gateORMHasServices(ctx) {
		t.Fatal("fixture does not disable the ORM step — the ORM being off must gate stepInternalDBORM off")
	}

	if crudStepGate(t, "CRUD handlers")(ctx) {
		t.Error("the CRUD projection ran with the ORM being off. Its generated ops reference " +
			"db.<Entity> / db.Create<Entity> / db.Get<Entity>ByID, which stepInternalDBORM did not " +
			"emit, so `forge generate` writes a handlers_crud_ops_gen.go that cannot compile and " +
			"then fails its own validate step")
	}
}

// TestCRUDProjectionGate_StaleSweepSkippedWhenORMDisabled: the Tier-1
// stale sweep has to use the emitter's own gate. If it kept the looser
// codegen gate, a project with the ORM being off would have any existing
// handlers_crud_ops_gen.go treated as stale and deleted, because the step
// that would have rewritten it never ran — turning a bad emission into a
// silent removal of the user's wiring.
func TestCRUDProjectionGate_StaleSweepSkippedWhenORMDisabled(t *testing.T) {
	ormOff := false
	ctx := crudProjectionCtx(&ormOff)

	gate := tier1OwnerGate("internal/handlers/orders/handlers_crud_ops_gen.go")
	if gate == nil {
		t.Fatal("no Tier-1 owner gate resolves for internal/handlers/*/handlers_crud_ops_gen.go")
	}
	if gate(ctx) {
		t.Error("the stale sweep claims ownership of handlers_crud_ops_gen.go while the " +
			"emitting step is gated off — absence from WrittenThisRun is uninformative here, " +
			"so the sweep would delete a file nothing was going to rewrite")
	}
}

// TestCRUDProjectionGate_RunsOnANormalForgeProject is the positive case,
// and it is the half that makes the fix a narrowing rather than a removal.
// An ordinary project — ORM on, whether by default (nil) or explicitly —
// still gets its CRUD wiring.
func TestCRUDProjectionGate_RunsOnANormalForgeProject(t *testing.T) {
	gate := crudStepGate(t, "CRUD handlers")
	ormOn := true

	for _, tc := range []struct {
		name string
		orm  *bool
	}{
		{"ORM unset (zero value: on)", nil},
		{"ORM explicitly on", &ormOn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !gate(crudProjectionCtx(tc.orm)) {
				t.Error("a normal forge project lost its CRUD projection — the fix must narrow " +
					"the gate to ORM-less projects, not disable CRUD generation")
			}
		})
	}

	// The directory-scan fallback (no forge.yaml at all) keeps forge's
	// historical permissive default, same as every other feature gate.
	if !gate(&pipelineContext{Cfg: nil, HasServices: true}) {
		t.Error("a project with no forge.yaml lost its CRUD projection; nil-cfg means \"no opinion\", not \"off\"")
	}
}
