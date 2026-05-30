// Copyright (c) 2025 Reliant Labs
package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	forgecli "github.com/reliant-labs/forge/cli"
	skillscore "github.com/reliant-labs/reliant/internal/skills/core"
)

// forgeNamespace is the synthetic top-level skill path under which all forge
// skills are surfaced. A forge skill at path "db" becomes "forge/db" in
// reliant's catalog; "api/handlers" becomes "forge/api/handlers".
const forgeNamespace = "forge"

// forgeSyntheticScheme + forgePathSeparator together form a Definition.Path
// scheme for forge skills. We never read this path from disk; instead
// LoadFullDefinition recognizes the scheme and routes the body fetch back to
// forge's public API. The null-byte separator avoids collision with any real
// path component (which can contain "/", spaces, etc.).
const (
	forgeSyntheticScheme = "forge://"
	forgePathSeparator   = "\x00"
)

func forgeSyntheticPath(projectRoot, forgePath string) string {
	return forgeSyntheticScheme + projectRoot + forgePathSeparator + forgePath
}

// parseForgeSyntheticPath splits a forge synthetic path back into its
// (projectRoot, forgePath) components. Returns ok=false for any path that
// wasn't produced by [forgeSyntheticPath].
func parseForgeSyntheticPath(p string) (projectRoot, forgePath string, ok bool) {
	if !strings.HasPrefix(p, forgeSyntheticScheme) {
		return "", "", false
	}
	rest := strings.TrimPrefix(p, forgeSyntheticScheme)
	sep := strings.Index(rest, forgePathSeparator)
	if sep < 0 {
		return "", "", false
	}
	return rest[:sep], rest[sep+1:], true
}

// forgeSkillsForInput enumerates forge skills for the project root and each
// nested repo source declared in input. The same NormalizedKey-prefixing
// rule used elsewhere in discoverAll is applied here so forge skills from
// different repos don't shadow each other.
//
// Skills surfaced under a nested repo get NormalizedKey="<source>/forge/..."
// instead of "forge/...", consistent with how reliant disambiguates skills
// from multiple repos. The synthesized parent's NormalizedKey is also
// prefixed so each repo gets its own "forge" group.
func forgeSkillsForInput(input DiscoverInput) []Definition {
	roots := append([]string{""}, input.RepoSources...)
	seen := map[string]struct{}{}
	var out []Definition
	for _, src := range roots {
		src = strings.TrimSpace(src)
		canonical := src
		if canonical == "." {
			canonical = ""
		}
		if _, dup := seen[canonical]; dup {
			continue
		}
		seen[canonical] = struct{}{}

		baseDir := input.ProjectPath
		if canonical != "" {
			baseDir = filepath.Join(input.ProjectPath, canonical)
		}
		defs := discoverForgeSkills(baseDir, canonical, input.LoadFullDefinitions)
		for _, def := range defs {
			if canonical != "" && def.NormalizedKey != "" {
				def.NormalizedKey = canonical + "/" + def.NormalizedKey
			}
			out = append(out, def)
		}
	}
	return out
}

// discoverForgeSkills enumerates skills from a sibling forge project rooted
// at baseDir. Returns nil silently (no diagnostics) when baseDir is not a
// forge project — the integration is best-effort.
//
// The returned slice always starts with a synthesized parent entry at
// SkillPath="forge" (so the skill appears as a top-level group in the
// announcement and the user can drill into its children), followed by one
// entry per forge skill at SkillPath="forge/<forge-path>". Bodies are left
// empty; [loadForgeSkillBody] hydrates them on demand via
// [LoadFullDefinition].
//
// source is propagated to each Definition.Source so the caller's
// NormalizedKey-prefixing logic can disambiguate forge skills across nested
// repos.
func discoverForgeSkills(baseDir, source string, loadFullDefinitions bool) []Definition {
	if _, err := os.Stat(filepath.Join(baseDir, "forge.yaml")); err != nil {
		return nil
	}

	skills, err := forgecli.ListSkills(baseDir)
	if err != nil || len(skills) == 0 {
		return nil
	}

	// Deterministic order for the synthesized parent's body.
	sort.Slice(skills, func(i, j int) bool { return skills[i].Path < skills[j].Path })

	defs := make([]Definition, 0, len(skills)+1)

	defs = append(defs, Definition{
		Name:          forgeNamespace,
		NormalizedKey: forgeNamespace,
		Description:   "Forge skills surfaced from this project's sibling forge module. Use the skill tool to load a specific sub-skill (e.g. forge/db, forge/proto, forge/api/handlers).",
		Body:          forgeParentBody(skills),
		Path:          forgeSyntheticPath(baseDir, ""),
		Scope:         skillscore.ScopeForge,
		Format:        skillscore.SkillFormatClaudeMarkdown,
		SkillDir:      forgeSyntheticPath(baseDir, ""),
		SkillPath:     forgeNamespace,
		HasChildren:   true,
		Source:        source,
	})

	for _, s := range skills {
		skillPath := forgeNamespace + "/" + s.Path
		defs = append(defs, Definition{
			Name:          skillscore.NormalizeSkillName(s.Name),
			NormalizedKey: skillPath,
			Description:   s.Description,
			Body:          "",
			Path:          forgeSyntheticPath(baseDir, s.Path),
			Scope:         skillscore.ScopeForge,
			Format:        skillscore.SkillFormatClaudeMarkdown,
			SkillDir:      forgeSyntheticPath(baseDir, s.Path),
			SkillPath:     skillPath,
			Source:        source,
		})
	}

	if loadFullDefinitions {
		for i := range defs {
			if defs[i].Body != "" {
				continue
			}
			loaded, err := loadForgeDefinition(defs[i])
			if err == nil {
				defs[i] = loaded
			}
		}
	}

	return defs
}

// forgeParentBody renders the synthetic body for the top-level "forge" skill.
// It exists so a model that lands on the parent (rather than a specific
// sub-skill) gets a clear navigational map of what's available.
func forgeParentBody(skills []forgecli.Skill) string {
	var b strings.Builder
	b.WriteString("# Forge skills\n\n")
	b.WriteString("These skills are surfaced from this project's sibling forge module. ")
	b.WriteString("Load a specific sub-skill via the skill tool to read its body. ")
	b.WriteString("All paths below are addressable as `forge/<path>`.\n\n")
	for _, s := range skills {
		desc := strings.TrimSpace(s.Description)
		if desc == "" {
			fmt.Fprintf(&b, "- **forge/%s**\n", s.Path)
		} else {
			fmt.Fprintf(&b, "- **forge/%s** — %s\n", s.Path, desc)
		}
	}
	return b.String()
}

// loadForgeDefinition hydrates a forge-scoped Definition's Body by calling
// forge's public LoadSkill. The synthetic parent (forgePath == "") returns
// its pre-rendered body; sub-skills hit forge for the SKILL.md content.
func loadForgeDefinition(def Definition) (Definition, error) {
	if def.Scope != skillscore.ScopeForge {
		return def, fmt.Errorf("not a forge skill: %s", def.Path)
	}
	if strings.TrimSpace(def.Body) != "" {
		return def, nil
	}
	baseDir, forgePath, ok := parseForgeSyntheticPath(def.Path)
	if !ok {
		return def, fmt.Errorf("invalid forge synthetic path: %s", def.Path)
	}
	if forgePath == "" {
		// Parent body should have been pre-rendered at discovery; nothing
		// useful to load from forge here. Fall through with empty body.
		return def, nil
	}
	body, err := forgecli.LoadSkill(baseDir, forgePath)
	if err != nil {
		return def, fmt.Errorf("load forge skill %q from %s: %w", forgePath, baseDir, err)
	}
	out := def
	out.Body = string(body)
	return out, nil
}
