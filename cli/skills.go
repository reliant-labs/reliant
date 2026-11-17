// Copyright (c) 2025 Reliant Labs
package cli

import (
	internalcli "github.com/reliant-labs/forge/internal/cli"
)

// Skill is the public metadata view of a forge skill, suitable for
// out-of-process consumers (e.g. reliant surfacing forge skills natively).
type Skill struct {
	// Path is the skill identifier (e.g. "db", "frontend/state").
	Path string
	// Name is the human-readable name from the SKILL.md frontmatter.
	Name string
	// Description is the one-line summary from the SKILL.md frontmatter.
	Description string
	// Scope is where the skill was discovered: "forge", "project", or "user".
	Scope string
	// Emit declares which audience the skill targets — "forge" (framework
	// skills, default when frontmatter omits the field), "general"
	// (methodology that applies to any project), or "both" (mixed
	// content, with framework-specific portions inside @forge-only
	// blocks the renderer strips for general audiences). Consumers use
	// this to decide whether to surface a skill outside a forge project.
	Emit string
	// Relevance classifies when the skill is worth surfacing: "" (always —
	// the default) or "migration" (a one-time upgrade-transition playbook).
	// ListSkills excludes relevance=migration skills; ListSkillsWithOptions
	// can opt them back in. Consumers building their own retrieval layer
	// should treat migration skills as transition-scoped, not steady-state.
	Relevance string
	// Version is the release that introduced the breaking change this
	// migration skill covers, passed through verbatim from the `version:`
	// frontmatter for consumers that want to do their own version gating.
	// A project needs the migration when its pin is BELOW this version and
	// it still exhibits the old shape.
	//
	// Forge itself gates listings on the relevance class only (binary
	// versions are routinely dev builds / pseudo-versions, which are not
	// meaningfully comparable); the authoritative project-aware version +
	// detection gate is `forge project upgrade list`. Empty when undeclared
	// and for non-migration skills.
	Version string

	// SkillForgeVersion is the forge version whose embedded templates the
	// skill content comes from — i.e. the forge module linked into THIS
	// process, not the project's pin. Empty for project/user-scope skills.
	SkillForgeVersion string
	// ProjectForgeVersion is the forge_version pinned in the project's
	// forge.yaml ("" when projectRoot was empty, no pin is declared, or the
	// file could not be read).
	ProjectForgeVersion string
	// VersionSkew is true when the serving forge version and the project's
	// pin are both real release versions and differ. Harness consumers
	// should surface skewed skills with caution: the guidance may describe
	// a different forge version than the one that generated the project.
	VersionSkew bool
}

// ListSkills returns every available forge skill for projectRoot — merging
// forge-shipped, project (.forge/skills under projectRoot), and user-global
// (~/.forge/skills) sources. On path collision, precedence is user > project
// > forge.
//
// An empty projectRoot skips the project source. The result is sorted by
// Path. Returns an error only if the embedded forge-shipped skills cannot be
// enumerated; missing disk sources are silently skipped.
//
// One-time migration skills (Relevance == "migration") are excluded from
// this default listing — they document specific forge version transitions
// and are noise for any project not mid-transition. Use
// [ListSkillsWithOptions] to opt them in; they always remain loadable by
// exact path via [LoadSkill].
func ListSkills(projectRoot string) ([]Skill, error) {
	return ListSkillsWithOptions(projectRoot, ListSkillsOptions{})
}

// ListSkillsOptions tunes ListSkillsWithOptions. The zero value matches
// ListSkills' default behavior.
type ListSkillsOptions struct {
	// IncludeMigrationSkills opts relevance=migration skills back into
	// the listing (e.g. for an upgrade-assistant surface).
	IncludeMigrationSkills bool
}

// ListSkillsWithOptions is [ListSkills] with explicit listing options.
// Additive API — ListSkills' signature is stable for existing consumers.
func ListSkillsWithOptions(projectRoot string, opts ListSkillsOptions) ([]Skill, error) {
	metas, err := internalcli.ListSkillsAtWithOptions(projectRoot, internalcli.SkillListOptions{
		IncludeMigrations: opts.IncludeMigrationSkills,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Skill, 0, len(metas))
	for _, m := range metas {
		out = append(out, Skill{
			Path:                m.Path,
			Name:                m.Name,
			Description:         m.Description,
			Scope:               string(m.Scope),
			Emit:                string(m.Emit),
			Relevance:           string(m.Relevance),
			Version:             m.Version,
			SkillForgeVersion:   m.SkillForgeVersion,
			ProjectForgeVersion: m.ProjectForgeVersion,
			VersionSkew:         m.VersionSkew,
		})
	}
	return out, nil
}

// LoadSkill returns the SKILL.md body for skillPath under projectRoot,
// honoring the same user > project > forge precedence as ListSkills.
// Returns an error if the skill is not found.
//
// Version-skew advisory: when the skill is forge-shipped and the forge
// version serving it differs from the project's pinned forge_version
// (see Skill.VersionSkew), a one-line "Note: this guidance is from
// forge <X>; this project pins forge <Y>..." advisory is inserted after
// the YAML frontmatter so downstream readers see the skew inline.
func LoadSkill(projectRoot, skillPath string) ([]byte, error) {
	body, _, err := internalcli.ResolveSkillContentAt(projectRoot, skillPath)
	return body, err
}

// LoadSkillForAudience is like [LoadSkill] but filters the body for the
// given audience. Pass "general" to strip `<!-- @forge-only:start/end -->`
// blocks (e.g. when surfacing a skill outside a forge project). Pass
// "forge" or "" to keep the full body. Reliant and other harness shims
// should use this when a project lacks forge.yaml so emit:both skills
// surface without forge-specific tooling instructions.
func LoadSkillForAudience(projectRoot, skillPath, audience string) ([]byte, error) {
	body, err := LoadSkill(projectRoot, skillPath)
	if err != nil {
		return nil, err
	}
	return internalcli.RenderSkillForAudience(body, internalcli.SkillAudience(audience)), nil
}

// RenderSkillOptions tunes [RenderSkill]. The zero value renders exactly what
// `forge skill load` prints from THIS process.
type RenderSkillOptions struct {
	// Audience filters the body the same way as [LoadSkillForAudience]:
	// "general" strips `@forge-only` blocks, "forge" or "" keeps the full
	// body (which is what `skill load` prints).
	Audience string
	// CLIName is the command a reader types to invoke forge — "forge" for
	// the standalone binary, "reliant forge" when forge is mounted inside
	// reliant. Every bare `forge ` command reference in the body is
	// rewritten to it. "" means the name this process reports for itself,
	// the same one [RenderProjectMemory] renders, so a harness that injects
	// both gets one consistent spelling.
	CLIName string
}

// RenderSkill returns skillPath's content exactly as `<cli> skill load
// <skillPath>` prints it: the same user > project > forge resolution and
// version-skew advisory as [LoadSkill], plus the command-name rewrite the CLI
// applies so every command in the skill is copy-pasteable under the mount
// that serves it. `skill load` itself calls the same renderer, so the two
// cannot drift.
//
// Harnesses that inject a skill into an agent's context should use this
// rather than [LoadSkill]: the raw body says `forge generate`, which under a
// `reliant forge` mount names a command the agent may not have — or a
// different forge build than the one that generated its project.
//
// A "forge/" prefix on skillPath is accepted, matching `skill load`.
func RenderSkill(projectRoot, skillPath string, opts RenderSkillOptions) ([]byte, error) {
	cliName := opts.CLIName
	if cliName == "" {
		cliName = internalcli.Name()
	}
	return internalcli.RenderSkillContentAt(projectRoot, skillPath, internalcli.SkillAudience(opts.Audience), cliName)
}
