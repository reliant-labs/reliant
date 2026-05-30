package core

import (
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Scope defines where a skill was discovered.
type Scope string

const (
	ScopeProjectLocal Scope = "project_local"
	ScopeProject      Scope = "project"
	ScopeCodexAgents  Scope = "codex_agents_project"
	ScopeCodexProject Scope = "codex_project"
	ScopeClaude       Scope = "claude_project"
	ScopeGlobal       Scope = "global"
	ScopeCodexGlobal  Scope = "codex_global"
	ScopeClaudeGlobal Scope = "claude_global"
	ScopeBuiltin      Scope = "builtin"
	// ScopeForge marks skills surfaced from a sibling forge module (forge-shipped,
	// forge project, or forge user skills) when the project root contains
	// forge.yaml. The skills are loaded in-memory via forge's public API — none
	// of them live on the reliant filesystem.
	ScopeForge Scope = "forge"
)

const (
	// DisabledDefinitionPathsSettingKey stores project-scoped disabled skill definition paths as JSON.
	DisabledDefinitionPathsSettingKey = "skills.disabled_definition_paths"
)

func (s Scope) Priority() int {
	switch s {
	case ScopeProjectLocal:
		return 1
	case ScopeProject:
		return 2
	case ScopeGlobal:
		return 3
	case ScopeBuiltin:
		return 4
	case ScopeCodexAgents:
		return 5
	case ScopeCodexProject:
		return 6
	case ScopeClaude:
		return 7
	case ScopeCodexGlobal:
		return 8
	case ScopeClaudeGlobal:
		return 9
	case ScopeForge:
		return 10
	default:
		return 100
	}
}

func (s Scope) IsTrustedForAutoActivation() bool {
	switch s {
	case ScopeProjectLocal, ScopeProject, ScopeGlobal, ScopeBuiltin, ScopeForge:
		return true
	default:
		return false
	}
}

type SkillFormat string

const (
	SkillFormatClaudeMarkdown SkillFormat = "claude_markdown"
)

type SupportingFile struct {
	RelativePath string
	Content      string
	Truncated    bool
}

type SupportingFilesLimits struct {
	MaxFiles int
	MaxBytes int
}

func NormalizeSupportingFilesLimits(limits SupportingFilesLimits) SupportingFilesLimits {
	if limits.MaxFiles <= 0 {
		limits.MaxFiles = 8
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = 4096
	}
	return limits
}

type Diagnostic struct {
	Path    string
	Scope   Scope
	Message string
}

type NoticeLevel string

const (
	NoticeLevelInfo    NoticeLevel = "info"
	NoticeLevelWarning NoticeLevel = "warning"
)

type Notice struct {
	Level   NoticeLevel
	Message string
}

func NormalizeSkillName(name string) string {
	normalized := norm.NFKC.String(name)
	return strings.TrimSpace(strings.ToLower(normalized))
}

// CanonicalDefinitionPath normalizes a skill definition path for equality checks.
func CanonicalDefinitionPath(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	clean := filepath.Clean(filepath.FromSlash(trimmed))
	if runtime.GOOS == "windows" {
		clean = strings.ToLower(clean)
	}
	return clean
}
