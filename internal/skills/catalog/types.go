package catalog

import skillscore "github.com/reliant-labs/reliant/internal/skills/core"

// Definition is the metadata-level representation of a discovered skill.
type Definition struct {
	Name          string
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
	// project root scope; non-empty means a nested repo (e.g. "api"). Together
	// with SkillPath it forms the skill's identity (see Key).
	Source string

	// Claude-compatible fields
	ArgumentHint           string
	DisableModelInvocation bool
	UserInvocable          *bool // nil = default true
	Paths                  string
}

// SkillKey is a skill's catalog identity: which source it came from and its
// path within that source. It is a struct, not a joined string, so a repo named
// like a namespace ("forge") can never collide with a skill whose path happens
// to start with that namespace ("forge/proto").
type SkillKey struct {
	Source string
	Path   string
}

// String renders the key for humans only; never parse or compare it.
func (k SkillKey) String() string {
	src := k.Source
	if src == "" {
		src = "(root)"
	}
	return src + ":" + k.Path
}

// Key returns the definition's catalog identity.
func (d Definition) Key() SkillKey {
	p := d.SkillPath
	if p == "" {
		p = d.Name
	}
	return SkillKey{Source: d.Source, Path: p}
}

// ShadowedSkill is one skill delivered by two producers under the same key,
// where only one survives into the catalog.
//
// This is a fact the runtime can observe and used to keep to itself. The
// motivating case: `forge generate` renders its own skills into .claude/skills,
// which reliant discovers at ScopeClaude (priority 7) while the forge-embedded
// originals are ScopeForge (priority 10) — so the generated render wins, and
// the copy that wins carries a banner telling the reader that
// `forge skill load` prints the authoritative version. A preloaded skill that
// says the preload may be stale, with nothing anywhere reporting that two
// copies exist, is a question a reader cannot answer.
//
// The scope ORDER is not the bug: a hand-authored project skill outranking a
// framework default is correct. Silence about the duplicate is the bug.
type ShadowedSkill struct {
	// Key is the identity both copies claimed.
	Key SkillKey
	// Winner / Loser are the definition paths, with their scopes.
	WinnerPath  string
	WinnerScope skillscore.Scope
	LoserPath   string
	LoserScope  skillscore.Scope
	// BytesDiffer is true when the two copies are not the same content. False
	// when they match, and when bodies were not loaded (a metadata-only
	// discovery cannot tell, and must not claim they differ).
	BytesDiffer bool
}

// Snapshot is the catalog/discovery output before activation/materialization.
type Snapshot struct {
	Definitions []Definition
	ByName      map[SkillKey]Definition
	Diagnostics []skillscore.Diagnostic
	// Shadowed lists every key that two producers both claimed. Replaces a pair
	// of path→path maps that recorded the same thing and were read by nothing:
	// discovery knew about every collision and no surface ever mentioned one.
	Shadowed []ShadowedSkill
}
