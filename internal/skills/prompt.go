package skills

import (
	skillcatalog "github.com/reliant-labs/reliant/internal/skills/catalog"
	skillmaterialize "github.com/reliant-labs/reliant/internal/skills/materialize"
	skillprompt "github.com/reliant-labs/reliant/internal/skills/prompt"
)

type AvailableSkillsRenderLimits = skillprompt.AvailableSkillsRenderLimits

func skillSliceToDefinitions(skills []Skill) []skillcatalog.Definition {
	definitions := make([]skillcatalog.Definition, 0, len(skills))
	for _, skill := range skills {
		definitions = append(definitions, skillToDefinition(skill))
	}
	return definitions
}

func skillToDefinition(skill Skill) skillcatalog.Definition {
	definition := skillcatalog.Definition{
		Name:          skill.Name,
		NormalizedKey: skill.NormalizedKey,
		Description:   skill.Description,
		License:       skill.License,
		Compatibility: skill.Compatibility,
		Body:          skill.Body,
		Path:          skill.Path,
		Scope:         skill.Scope,
		Format:        skill.Format,
		SkillDir:      skill.SkillDir,
	}
	if skill.Metadata != nil {
		definition.Metadata = make(map[string]string, len(skill.Metadata))
		for key, value := range skill.Metadata {
			definition.Metadata[key] = value
		}
	}
	if skill.AllowedTools != nil {
		definition.AllowedTools = append([]string(nil), skill.AllowedTools...)
	}
	return definition
}

func skillToActiveSkill(skill Skill) skillmaterialize.ActiveSkill {
	active := skillmaterialize.ActiveSkill{
		Definition: skillToDefinition(skill),
		Body:       skill.Body,
		Trusted:    skill.Scope.IsTrustedForAutoActivation(),
	}
	if skill.Files != nil {
		active.SupportingFiles = append([]SupportingFile(nil), skill.Files...)
	}
	return active
}

func definitionToSkill(definition skillcatalog.Definition) Skill {
	skill := Skill{
		Name:          definition.Name,
		NormalizedKey: definition.NormalizedKey,
		Description:   definition.Description,
		License:       definition.License,
		Compatibility: definition.Compatibility,
		Body:          definition.Body,
		Path:          definition.Path,
		Scope:         definition.Scope,
		Format:        definition.Format,
		SkillDir:      definition.SkillDir,
	}
	if definition.Metadata != nil {
		skill.Metadata = make(map[string]string, len(definition.Metadata))
		for key, value := range definition.Metadata {
			skill.Metadata[key] = value
		}
	}
	if definition.AllowedTools != nil {
		skill.AllowedTools = append([]string(nil), definition.AllowedTools...)
	}
	return skill
}

func activeSkillToSkill(active skillmaterialize.ActiveSkill) Skill {
	skill := definitionToSkill(active.Definition)
	skill.Body = active.Body
	if active.SupportingFiles != nil {
		skill.Files = append([]SupportingFile(nil), active.SupportingFiles...)
	}
	return skill
}
