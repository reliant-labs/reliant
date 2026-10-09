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

// forgeSkillsForInput enumerates forge skills for the project: the
// directory-independent ones (forge-shipped and user-global, plus the single
// synthesized "forge" parent) once, and each repo source's project-local
// <repo>/.forge/skills on its own. Project-local skills from a nested repo get
// Source=<source> — the same identity rule discoverAll applies —
// so same-named local skills in different repos don't shadow each other.
func forgeSkillsForInput(input DiscoverInput) []Definition {
	// forgecli.ListSkills(baseDir) = forge-shipped (embedded in this binary,
	// identical for every baseDir) + user-global (~/.forge/skills, also
	// baseDir-independent) + project-local (<baseDir>/.forge/skills). Only the
	// last varies per directory; RenderSkill's output differs per baseDir solely
	// by the audience (forge.yaml present) and a version-skew advisory. So the
	// baseDir-independent skills are discovered ONCE, and only project-local
	// ones are discovered per source. Enumerating everything per repo repeated
	// ~25 byte-identical skills once per repo.
	var out []Definition

	// Shared pass. Use a forge project dir as baseDir when there is one so
	// emit:both bodies keep their @forge-only sections (the audience is derived
	// from forge.yaml at baseDir) and the forge namespace is surfaced.
	sharedDir := input.ProjectPath
	sharedForge := false
	candidates := append([]string{""}, input.RepoSources...)
	for _, src := range candidates {
		src = strings.TrimSpace(src)
		if src == "." {
			src = ""
		}
		dir := filepath.Join(input.ProjectPath, src)
		if _, err := os.Stat(filepath.Join(dir, "forge.yaml")); err == nil {
			sharedDir, sharedForge = dir, true
			break
		}
	}
	out = append(out, discoverForgeSkillsFiltered(sharedDir, "", input.LoadFullDefinitions, sharedForge, false,
		func(s forgecli.Skill) bool { return s.Scope != string(projectSkillScope) })...)

	seen := map[string]struct{}{}
	for _, src := range candidates {
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
		_, yamlErr := os.Stat(filepath.Join(baseDir, "forge.yaml"))
		defs := discoverForgeSkillsFiltered(baseDir, canonical, input.LoadFullDefinitions, yamlErr == nil, true,
			func(s forgecli.Skill) bool { return s.Scope == string(projectSkillScope) })
		for _, def := range defs {
			out = append(out, def)
		}
	}
	return out
}

// projectSkillScope is forge's scope value for skills read from
// <baseDir>/.forge/skills.
const projectSkillScope = "project"

// discoverForgeSkills enumerates skills surfaced from a sibling forge
// module rooted at baseDir. The result is partitioned by each skill's
// `Emit` value (set from frontmatter, defaulting to "forge" for legacy
// shipped skills):
//
//   - Emit "general" or "both": always returned, regardless of whether
//     baseDir contains forge.yaml. Surfaced at the bare skill path
//     (e.g. "testing-methodology", "code-review", "debug") so preset
//     references and the model's skill discovery resolve directly.
//     emit:both skill bodies are loaded through audience="general" when
//     no forge.yaml is present, which strips `@forge-only` sections.
//
//   - Emit "forge" (and the legacy empty default): only returned when
//     baseDir contains forge.yaml. Surfaced under the "forge/" namespace
//     prefix, alongside a synthesized "forge" parent skill that doubles
//     as a navigational map of the framework children.
//
// Returns nil when forge enumeration fails or returns zero skills —
// integration is best-effort and silent.
//
// source is propagated to each Definition.Source so the catalog
// key (Source, SkillPath) can disambiguate forge skills across
// nested repos.
func discoverForgeSkills(baseDir, source string, loadFullDefinitions bool) []Definition {
	_, err := os.Stat(filepath.Join(baseDir, "forge.yaml"))
	return discoverForgeSkillsFiltered(baseDir, source, loadFullDefinitions, err == nil, false, nil)
}

// discoverForgeSkillsFiltered is discoverForgeSkills with the forge-project
// decision made by the caller and an optional filter over forgecli's listing.
// keep == nil keeps everything.
func discoverForgeSkillsFiltered(baseDir, source string, loadFullDefinitions, hasForgeYAML, projectOnly bool, keep func(forgecli.Skill) bool) []Definition {
	skills, err := forgecli.ListSkills(baseDir)
	if err != nil {
		return nil
	}
	if keep != nil {
		kept := skills[:0:0]
		for _, s := range skills {
			if keep(s) {
				kept = append(kept, s)
			}
		}
		skills = kept
	}
	if len(skills) == 0 {
		return nil
	}

	// Deterministic order so the synthesized parent's body and the
	// returned slice both stay stable across runs.
	sort.Slice(skills, func(i, j int) bool { return skills[i].Path < skills[j].Path })

	defs := make([]Definition, 0, len(skills)+1)

	// Forge framework parent — only in forge projects. Its body lists
	// only the framework children that will actually appear under it.
	if hasForgeYAML && !projectOnly {
		defs = append(defs, Definition{
			Name:        forgeNamespace,
			Description: "Forge skills surfaced from this project's sibling forge module. Use the skill tool to load a specific sub-skill (e.g. forge/db, forge/proto, forge/api/handlers).",
			Body:        forgeParentBody(filterForgeAudience(skills)),
			Path:        forgeSyntheticPath(baseDir, ""),
			Scope:       skillscore.ScopeForge,
			Format:      skillscore.SkillFormatClaudeMarkdown,
			SkillDir:    forgeSyntheticPath(baseDir, ""),
			SkillPath:   forgeNamespace,
			HasChildren: true,
			Source:      source,
		})
	}

	for _, s := range skills {
		skillPath, ok := forgeAddressablePath(s, hasForgeYAML)
		if !ok {
			continue
		}

		defs = append(defs, Definition{
			Name:        skillscore.NormalizeSkillName(s.Name),
			Description: s.Description,
			Body:        "",
			Path:        forgeSyntheticPath(baseDir, s.Path),
			Scope:       skillscore.ScopeForge,
			Format:      skillscore.SkillFormatClaudeMarkdown,
			SkillDir:    forgeSyntheticPath(baseDir, s.Path),
			SkillPath:   skillPath,
			Source:      source,
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

// forgeAddressablePath is THE emit partition: the single decision about which
// path a forge skill is addressable by once reliant surfaces it.
//
//   - emit "general"/"both": the BARE path. These are methodology skills that
//     apply to any project, so they are not namespaced. Consequently they are
//     NOT addressable as "forge/<path>" — the synthetic namespace has no entry
//     for them, and the resolver's suffix rule only accepts shorter spellings
//     of longer paths, never the reverse.
//   - emit "forge": "forge/<path>", and only inside a forge project.
//
// ok is false when the skill is not surfaced at all.
//
// Both the runtime catalog and ForgeFrameworkSkillPaths (which backs workflow
// validation) go through here. A second implementation of this rule would be
// free to disagree with the first, and a validator that disagrees with the
// resolver is the bug it is meant to prevent.
func forgeAddressablePath(s forgecli.Skill, hasForgeYAML bool) (string, bool) {
	emit := s.Emit
	if emit == "" {
		emit = "forge"
	}
	switch emit {
	case "general", "both":
		return s.Path, true
	case "forge":
		if !hasForgeYAML {
			return "", false
		}
		return forgeNamespace + "/" + s.Path, true
	default:
		// Unknown emit value — be conservative and drop. A future emit
		// category should land here as a deliberate switch case rather
		// than silently surfacing somewhere wrong.
		return "", false
	}
}

// ForgeFrameworkSkillPaths returns every path a forge skill is addressable by
// INSIDE a forge project, from forge's embedded catalog alone.
//
// Workflow validation needs this view because a builtin charter like
// forge-one-shot targets forge projects by construction: its "forge/..."
// references resolve there and nowhere else. Validating such a charter against
// a non-forge working directory would report every one of them as broken.
func ForgeFrameworkSkillPaths() []string {
	skills, err := forgecli.ListSkills("")
	if err != nil {
		return nil
	}
	paths := make([]string, 0, len(skills))
	for _, s := range skills {
		if p, ok := forgeAddressablePath(s, true); ok {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths
}

// filterForgeAudience returns only the skills that should appear in the
// forge-audience view — emit "forge", "both", or the legacy empty
// default (treated as forge). Used to render the synthetic forge
// parent's body so it doesn't list general-only skills that surface at
// their bare path instead.
func filterForgeAudience(skills []forgecli.Skill) []forgecli.Skill {
	out := make([]forgecli.Skill, 0, len(skills))
	for _, s := range skills {
		emit := s.Emit
		if emit == "" {
			emit = "forge"
		}
		if emit == "forge" || emit == "both" {
			out = append(out, s)
		}
	}
	return out
}

// forgeParentBody renders the synthetic body for the top-level "forge" skill.
// It exists so a model that lands on the parent (rather than a specific
// sub-skill) gets a clear navigational map of what's available.
//
// Every entry is printed at the path it is ACTUALLY addressable by, via the
// same forgeAddressablePath the catalog uses. It previously printed a blanket
// "forge/<path>" for everything, which advertised emit:both skills — db,
// service-layer, testing — under a namespace they are not surfaced in. This
// map is the thing an agent reads to learn what to load, so a wrong path here
// does not merely mislead: it is copied into charters and into skill loads
// that then silently resolve to nothing.
func forgeParentBody(skills []forgecli.Skill) string {
	var b strings.Builder
	b.WriteString("# Forge skills\n\n")
	b.WriteString("These skills are surfaced from this project's sibling forge module. ")
	b.WriteString("Load a specific sub-skill via the skill tool to read its body, ")
	b.WriteString("using exactly the path shown below — framework skills carry the ")
	b.WriteString("`forge/` namespace, general methodology skills do not.\n\n")
	for _, s := range skills {
		path, ok := forgeAddressablePath(s, true)
		if !ok {
			continue
		}
		desc := strings.TrimSpace(s.Description)
		if desc == "" {
			fmt.Fprintf(&b, "- **%s**\n", path)
		} else {
			fmt.Fprintf(&b, "- **%s** — %s\n", path, desc)
		}
	}
	return b.String()
}

// loadForgeDefinition hydrates a forge-scoped Definition's Body by
// calling forge's public RenderSkill — the renderer behind `reliant forge
// skill load` itself, so the body an agent reads through the skill tool (or
// gets preloaded) is byte-for-byte what that command prints. That includes
// the command-name rewrite: forge's raw SKILL.md says `forge generate`, the
// CLI and the project memory say `reliant forge generate`, and an agent
// handed the raw form runs whatever `forge` is on PATH — a different build
// than the one linked into this binary, or nothing at all. CLIName is left
// to forge's default, the same name RenderProjectMemory renders, so memory
// and skills always agree.
//
// The audience is derived from whether the project has forge.yaml:
// present → "forge" (full body including any `@forge-only` sections),
// absent → "general" (those sections are stripped by the forge renderer).
// The synthetic parent (forgePath == "") returns its pre-rendered body
// unchanged.
//
// We re-check forge.yaml at load time rather than encoding the audience
// in the synthetic path so the body reflects current reality if the
// project's forge.yaml is added or removed between discovery and load.
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
	audience := "general"
	if _, err := os.Stat(filepath.Join(baseDir, "forge.yaml")); err == nil {
		audience = "forge"
	}
	body, err := forgecli.RenderSkill(baseDir, forgePath, forgecli.RenderSkillOptions{Audience: audience})
	if err != nil {
		return def, fmt.Errorf("load forge skill %q from %s: %w", forgePath, baseDir, err)
	}
	out := def
	out.Body = string(body)
	return out, nil
}
