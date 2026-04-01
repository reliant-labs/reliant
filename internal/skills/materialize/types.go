package materialize

import (
	skillcatalog "github.com/reliant-labs/reliant/internal/skills/catalog"
	skillscore "github.com/reliant-labs/reliant/internal/skills/core"
)

// ActiveSkill is the runtime/materialized form used after selection.
type ActiveSkill struct {
	Definition      skillcatalog.Definition
	Body            string
	SupportingFiles []skillscore.SupportingFile
	Trusted         bool
}
