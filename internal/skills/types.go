package skills

import skillscore "github.com/reliant-labs/reliant/internal/skills/core"

// Scope defines where a skill was discovered.
type Scope = skillscore.Scope

const (
	ScopeProjectLocal = skillscore.ScopeProjectLocal
	ScopeProject      = skillscore.ScopeProject
	ScopeCodexAgents  = skillscore.ScopeCodexAgents
	ScopeCodexProject = skillscore.ScopeCodexProject
	ScopeClaude       = skillscore.ScopeClaude
	ScopeGlobal       = skillscore.ScopeGlobal
	ScopeCodexGlobal  = skillscore.ScopeCodexGlobal
	ScopeClaudeGlobal = skillscore.ScopeClaudeGlobal
	ScopeBuiltin      = skillscore.ScopeBuiltin
)

const (
	// DisabledDefinitionPathsSettingKey stores project-scoped disabled skill definition paths as JSON.
	DisabledDefinitionPathsSettingKey = skillscore.DisabledDefinitionPathsSettingKey
)

type SkillFormat = skillscore.SkillFormat

const (
	SkillFormatClaudeMarkdown = skillscore.SkillFormatClaudeMarkdown
)

type SupportingFile = skillscore.SupportingFile

type SupportingFilesLimits = skillscore.SupportingFilesLimits

type Skill struct {
	Name          string
	NormalizedKey string
	Description   string
	License       string
	Compatibility string
	Metadata      map[string]string
	AllowedTools  []string
	Body          string
	Path          string
	Scope         Scope
	Format        SkillFormat
	SkillDir      string
	Files         []SupportingFile
}

type Diagnostic = skillscore.Diagnostic

type Result struct {
	Skills       []Skill
	ByName       map[string]Skill
	Diagnostics  []Diagnostic
	ShadowedBy   map[string]string
	ShadowedFrom map[string]string
}

type NoticeLevel = skillscore.NoticeLevel

const (
	NoticeLevelInfo    = skillscore.NoticeLevelInfo
	NoticeLevelWarning = skillscore.NoticeLevelWarning
)

type Notice = skillscore.Notice

// CanonicalDefinitionPath normalizes a skill definition path for equality checks.
func CanonicalDefinitionPath(raw string) string {
	return skillscore.CanonicalDefinitionPath(raw)
}
