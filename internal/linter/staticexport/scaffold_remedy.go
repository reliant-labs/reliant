package staticexport

import "fmt"

// crudRoutesMigration is the migration skill that moves forge's scaffolded
// CRUD pages off dynamic `[id]` segments, written with the scaffold change
// that made them static (`/<slug>/view?id=…`, `/<slug>/edit?id=…`).
const crudRoutesMigration = "migrations/v0.1.44"

// scaffoldPageRemedy is the fix for a `[id]` page forge's scaffold-once CRUD
// templates wrote. Its wording follows that migration: a page nobody edited
// is replaced by re-scaffolding (forge generate then writes view/ and edit/,
// which were never scaffolded beside the old pages), an edited one is moved.
//
// slugDir is the entity's route directory, project-relative
// (frontends/web/src/app/books); kind is "detail" or "edit".
func scaffoldPageRemedy(slugDir, kind string) string {
	newRoute := "view"
	if kind == "edit" {
		newRoute = "edit"
	}
	return fmt.Sprintf("this %s page came from forge's scaffold-once CRUD template, which now scaffolds the static route %s/%s/page.tsx (the id in `?id=`) instead — follow `forge skill load %s`. "+
		"For pages you never edited: `rm -rf '%s/[id]' %s/page.tsx %s/new/page.tsx && forge project rescaffold %s/page.tsx %s/new/page.tsx`; "+
		"for edited ones, move them to view/ and edit/ and read the id with useEntityIdParam() as the migration shows",
		kind, slugDir, newRoute, crudRoutesMigration, slugDir, slugDir, slugDir, slugDir, slugDir)
}
