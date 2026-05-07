package catalog

import skillscore "github.com/reliant-labs/reliant/internal/skills/core"

// Definition is the metadata-level representation of a discovered skill.
type Definition struct {
	Name          string
	NormalizedKey string
	Description   string
	License       string
	Compatibility string
	Metadata      map[string]string
	AllowedTools  []string
	Body          string
	Path          string
	Scope         skillscore.Scope
	Format        skillscore.SkillFormat
	SkillDir      string
	SkillPath     string // Hierarchical path relative to discovery root, e.g. "go/error-handling"
	HasChildren   bool   // True if this skill has sub-skills in subdirectories
	// Source is the repo relative path the skill was discovered in. "" means
	// project root scope; non-empty means a nested repo (e.g. "api"). Skills
	// with non-empty Source get their NormalizedKey prefixed with Source/ to
	// avoid collisions across repos.
	Source string

	// Claude-compatible fields
	ArgumentHint           string
	DisableModelInvocation bool
	UserInvocable          *bool // nil = default true
	Paths                  string
}

// Snapshot is the catalog/discovery output before activation/materialization.
type Snapshot struct {
	Definitions  []Definition
	ByName       map[string]Definition
	Diagnostics  []skillscore.Diagnostic
	ShadowedBy   map[string]string
	ShadowedFrom map[string]string
}
