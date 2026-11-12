package config

import "fmt"

// ormPinIssues rejects `features: {orm: true}` in a project whose ORM package
// declares `//forge:no-orm`: the two switches contradict each other, and
// honouring either one silently would undo the other's decision. `orm: false`
// beside the marker says the same thing twice, which is fine.
//
// features.orm was refused outright until the ORM became pinnable like every
// other feature: refusing it protected a stale `orm: false` from going unread,
// and a key that is read needs no such protection.
func ormPinIssues(cfg *ProjectConfig) []validationIssue {
	on, pinned := cfg.Features.pinned(FeatureORM)
	if !pinned || !on || !hasNoORMMarker(cfg.projectDir, cfg.Database.ORM.PackageDir()) {
		return nil
	}
	return []validationIssue{{
		msg: fmt.Sprintf("features.orm is pinned on, and %s declares //forge:no-orm", cfg.Database.ORM.PackageDir()),
		fix: "keep one decision: delete `orm: true` to keep the hand-written data layer, or delete the " +
			"//forge:no-orm marker to have forge project the ORM.",
	}}
}
