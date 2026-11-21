package staticexport

import "fmt"

// staticRoutesSkill is the skill that documents forge's static CRUD routes
// (`/<slug>/view?id=…`, `/<slug>/edit?id=…`) and the entity-routes helpers
// a page reads its id with.
const staticRoutesSkill = "frontend/serving"

// scaffoldPageRemedy is the fix for a `[id]` page forge's scaffold-once CRUD
// templates wrote: a page nobody edited is replaced by re-scaffolding (forge
// generate then writes view/ and edit/, which were never scaffolded beside the
// old pages), an edited one is moved.
//
// slugDir is the entity's route directory, project-relative
// (frontends/web/src/app/books); kind is "detail" or "edit".
func scaffoldPageRemedy(slugDir, kind string) string {
	newRoute := "view"
	if kind == "edit" {
		newRoute = "edit"
	}
	return fmt.Sprintf("this %s page came from forge's scaffold-once CRUD template, which now scaffolds the static route %s/%s/page.tsx (the id in `?id=`) instead (`forge skill load %s` documents it). "+
		"For pages you never edited: `rm -rf '%s/[id]' %s/page.tsx %s/new/page.tsx && forge project rescaffold %s/page.tsx %s/new/page.tsx`; "+
		"for edited ones, move them to view/ and edit/, read the id with useEntityIdParam() from src/lib/entity-routes.ts inside a <Suspense> boundary, and build links with entityViewHref/entityEditHref",
		kind, slugDir, newRoute, staticRoutesSkill, slugDir, slugDir, slugDir, slugDir, slugDir)
}
