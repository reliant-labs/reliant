package cli

import (
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// planORMEntitiesStep is the pipeline step that plans the ORM's entity set.
const planORMEntitiesStep = "plan ORM entities"

// ormEntityPlan is one computation of the ORM's entity set.
type ormEntityPlan struct {
	dir      string // the ORM package directory it was planned for
	entities []codegen.EntityDef
	err      error
}

// stepPlanORMEntities computes the ORM's entity set right after the
// descriptor is extracted, and refuses a schema whose relations cannot all
// project: two tables (or a table and a view) whose entities share one Go
// name.
//
// That refusal used to come from the ORM step, two dozen steps in. It was a
// soft failure, so the run went on through the frontend TypeScript stubs (a
// pnpm install on a fresh checkout), the pages, the mocks, three go mod
// tidies, every infra emitter and the validation build before exiting
// non-zero: on a loaded machine control-plane waited about 25 minutes to
// learn that audit_log and audit_logs both project to AuditLog (forge bug
// 22dc63db). Nothing those steps do can change the answer, which is known
// the moment the migrations are applied, so it is decided here. The shadow
// schema this applies is the one every later reader in the run reuses, so
// planning early costs no second apply.
//
// Every other failure to compute the entity set stays the ORM step's to
// report, exactly as before: this step only moves the one error that is
// certain and costs the most to discover late.
func stepPlanORMEntities(ctx *pipelineContext) error {
	dir := config.ORMPackage(ctx.Cfg).PackageDir()
	entities, err := codegen.ParseORMEntities(ctx.ProjectDir, dir)
	ctx.ormPlan = &ormEntityPlan{dir: dir, entities: entities, err: err}
	var collision *codegen.EntityCollisionError
	if errors.As(err, &collision) {
		return fmt.Errorf("the applied schema cannot project to forge's ORM (refused before generating anything): %w", err)
	}
	return nil
}

// ormEntities returns the ORM's entity set for the package in dir: the plan
// when stepPlanORMEntities made one for it, else a fresh computation (a step
// preset or a test that runs the ORM step alone).
func (ctx *pipelineContext) ormEntities(dir string) ([]codegen.EntityDef, error) {
	if p := ctx.ormPlan; p != nil && p.dir == dir {
		return p.entities, p.err
	}
	return codegen.ParseORMEntities(ctx.ProjectDir, dir)
}
