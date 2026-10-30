// Copyright (c) 2025 Reliant Labs
package catalog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	forgecli "github.com/reliant-labs/forge/cli"
	"github.com/reliant-labs/reliant/internal/forgepin"
	skillscore "github.com/reliant-labs/reliant/internal/skills/core"
)

// =============================================================================
// THE PINNED FORGE'S SKILLS
//
// forge's shipped skills live inside the forge binary, so the skills a forge
// serves describe THAT forge. Read from the forge compiled into reliant, a
// chat in a project pinned to v1.0.4 got the v0.1.47 guidance (package
// forgepin says why this matters everywhere).
//
// So inside a project that pins another forge, the shipped skills come from
// THAT forge: `forge skill list --json --with-bodies` (one call for the whole
// catalog), or — from a pin that predates the flag — `skill list --json` and
// `skill load <path>` per body. forgepin caches them per pinned forge.
//
// What stays on the embedded forge: user (~/.forge/skills) and project
// (.forge/skills) skills, which are the user's files rather than any forge's
// content, and every project with no pin.
//
// When the pinned forge cannot be obtained, substituting the embedded forge's
// framework skills is the bug. Instead the forge namespace is one entry that
// says what failed and how to fix it; the project-agnostic methodology skills
// (emit: general) keep coming from the embedded forge, so presets that
// preload them still start — a chat must never fail to start over this.
// =============================================================================

// pinnedForge runs the forge projects pin; the tests replace it.
var pinnedForge = forgepin.Default

// pinnedForgeCache holds a pinned forge's parsed catalog, per forge identity.
var pinnedForgeCache sync.Map // key -> *pinnedForgeEntry

type pinnedForgeEntry struct {
	mu     sync.Mutex
	list   []forgecli.Skill
	bodies map[string]string
}

// pinnedForgeFor reports the forge the project at baseDir pins when it is not
// the embedded one (ok), or why it could not be obtained (err).
func pinnedForgeFor(baseDir string) (forgecli.PinnedForge, bool, error) {
	return pinnedForge.Pinned(baseDir, "skill load")
}

func pinnedEntry(key string) *pinnedForgeEntry {
	e, _ := pinnedForgeCache.LoadOrStore(key, &pinnedForgeEntry{bodies: map[string]string{}})
	return e.(*pinnedForgeEntry)
}

// pinnedShippedSkills is the pinned forge's own (scope "forge") skills.
func pinnedShippedSkills(pf forgecli.PinnedForge, baseDir string) ([]forgecli.Skill, error) {
	e := pinnedEntry(pf.Key)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loadList(pf, baseDir)
}

// loadList reads the pinned forge's catalog once. e.mu is held.
func (e *pinnedForgeEntry) loadList(pf forgecli.PinnedForge, baseDir string) ([]forgecli.Skill, error) {
	if e.list != nil {
		return e.list, nil
	}
	raw, err := pinnedForge.Output(pf, baseDir, "skills-list.json",
		[]string{"skill", "list", "--json", "--with-bodies"},
		[]string{"skill", "list", "--json"})
	if err != nil {
		return nil, err
	}
	var listed []struct {
		Path        string  `json:"path"`
		Name        string  `json:"name"`
		Description string  `json:"description"`
		Scope       string  `json:"scope"`
		Emit        string  `json:"emit"`
		Relevance   string  `json:"relevance"`
		Body        *string `json:"body"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return nil, fmt.Errorf("%s: `skill list --json`: %w", pf.Want, err)
	}
	skills := make([]forgecli.Skill, 0, len(listed))
	for _, s := range listed {
		if s.Scope != "forge" {
			continue // the user's files: read where they are, never cached
		}
		skills = append(skills, forgecli.Skill{Path: s.Path, Name: s.Name, Description: s.Description, Scope: s.Scope, Emit: s.Emit, Relevance: s.Relevance})
		if s.Body != nil {
			e.bodies[s.Path] = *s.Body
		}
	}
	e.list = skills
	return skills, nil
}

// pinnedSkillBody is `skill load <path>` as the pinned forge prints it.
func pinnedSkillBody(pf forgecli.PinnedForge, baseDir, path string) (string, error) {
	e := pinnedEntry(pf.Key)
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.loadList(pf, baseDir); err != nil {
		return "", err
	}
	if body, ok := e.bodies[path]; ok {
		return body, nil
	}
	raw, err := pinnedForge.Output(pf, baseDir, "skill-"+forgepin.Fingerprint([]byte(path))+".md",
		[]string{"skill", "load", path})
	if err != nil {
		return "", err
	}
	e.bodies[path] = string(raw)
	return string(raw), nil
}

// shippedSkillsFor returns the forge-shipped skills baseDir's agents should
// read, plus the user's own (user scope) from the embedded reader. In a
// project pinned to another forge the shipped ones are that forge's; when it
// cannot be obtained, unavailable carries the reason and only the embedded
// general-audience skills are returned.
//
// includeProject keeps the project's own (.forge/skills) skills in the result.
func shippedSkillsFor(baseDir string, hasForgeYAML, includeProject bool) (skills []forgecli.Skill, unavailable error) {
	embedded, err := forgecli.ListSkills(baseDir)
	if err != nil {
		return nil, nil
	}
	local := func(s forgecli.Skill) bool { return s.Scope != projectSkillScope || includeProject }
	if !hasForgeYAML {
		return keepSkills(embedded, local), nil
	}
	pf, ok, err := pinnedForgeFor(baseDir)
	if !ok && err == nil {
		return keepSkills(embedded, local), nil
	}
	if err == nil {
		var pinned []forgecli.Skill
		if pinned, err = pinnedShippedSkills(pf, baseDir); err == nil {
			// The user's own skills shadow a shipped one of the same path,
			// as they do in forge's own listing.
			shadow := map[string]bool{}
			var out []forgecli.Skill
			for _, s := range embedded {
				if s.Scope != "forge" {
					shadow[s.Path] = true
					if local(s) {
						out = append(out, s)
					}
				}
			}
			for _, s := range pinned {
				if !shadow[s.Path] {
					out = append(out, s)
				}
			}
			return out, nil
		}
	}
	var general []forgecli.Skill
	for _, s := range embedded {
		if local(s) && (s.Scope != "forge" || s.Emit == "general") {
			general = append(general, s)
		}
	}
	return general, err
}

func keepSkills(skills []forgecli.Skill, keep func(forgecli.Skill) bool) []forgecli.Skill {
	out := skills[:0:0]
	for _, s := range skills {
		if keep(s) {
			out = append(out, s)
		}
	}
	return out
}

// pinnedForgeUnavailable is the forge namespace when the project's forge
// cannot be obtained: what failed, and that nothing else will stand in for it.
func pinnedForgeUnavailable(baseDir, source string, cause error) Definition {
	body := "# Forge skills are unavailable\n\n" +
		"This project pins a forge that could not be obtained, so its skills cannot be shown — " +
		"and the forge built into reliant is a different version, whose guidance would be wrong here.\n\n" +
		"```\n" + strings.TrimSpace(cause.Error()) + "\n```\n\n" +
		"Fix what stopped it (offline? a pin nobody published?), or move the pin deliberately with `" +
		forgepin.CLIName() + " project upgrade`. Every `" + forgepin.CLIName() + "` command in this project " +
		"fails the same way until then.\n"
	return Definition{
		Name:        forgeNamespace,
		Description: forgePurpose + " Unavailable: this project's pinned forge could not be obtained.",
		Body:        body,
		Path:        forgeSyntheticPath(baseDir, ""),
		Scope:       skillscore.ScopeForge,
		Format:      skillscore.SkillFormatClaudeMarkdown,
		SkillDir:    forgeSyntheticPath(baseDir, ""),
		SkillPath:   forgeNamespace,
		Source:      source,
	}
}

// pinnedBodyFor is the body of the forge skill at forgePath in baseDir when the
// project pins another forge and the skill is that forge's (not one of the
// user's, which shadow it): handled reports whether it applies.
func pinnedBodyFor(baseDir, forgePath string) (body string, handled bool, err error) {
	if _, err := os.Stat(filepath.Join(baseDir, "forge.yaml")); err != nil {
		return "", false, nil
	}
	if embedded, err := forgecli.ListSkills(baseDir); err == nil {
		for _, s := range embedded {
			if s.Path == forgePath && s.Scope != "forge" {
				return "", false, nil // the user's file
			}
		}
	}
	pf, ok, err := pinnedForgeFor(baseDir)
	if err != nil {
		return "", true, err
	}
	if !ok {
		return "", false, nil
	}
	body, err = pinnedSkillBody(pf, baseDir, forgePath)
	return body, true, err
}
