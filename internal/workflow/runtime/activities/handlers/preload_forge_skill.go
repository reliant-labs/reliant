// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"strings"

	cfgpkg "github.com/reliant-labs/reliant/internal/config"
	skillscore "github.com/reliant-labs/reliant/internal/skills/core"
)

// forgeStartHereSkill is the catalog path of forge's own start-here skill: the
// body `reliant forge skill load forge` prints. Not "forge" — in reliant's
// catalog that path is the synthesized map of forge's sub-skills.
const forgeStartHereSkill = "forge/forge"

// withForgeStartHere returns the skills an agent node preloads: its own
// request, plus forge's start-here skill when the project root is a forge
// project. added names what the harness appended, for the log line.
//
// WHY. The memory of every forge project says to start with `reliant forge
// skill load forge`, while the presets preloaded only general-agent — so
// every agent spent turns loading it by hand, or skipped it and wrote code
// against conventions it had never read (dogfood run roofers-2026-10-05,
// finding #11).
//
// WHO. Only a node that already preloads skills. That is an agent doing
// project work — every builtin agent preset declares skills — while a
// call_llm node with none (a title, a classifier, a filter) gets nothing it
// did not ask for.
//
// WHEN. The entry is in the catalog exactly when the daemon found forge.yaml
// at the project ROOT: forge-scoped framework skills are surfaced only there
// (forgeAddressablePath), and a nested repo's copies carry that repo as their
// Source. So its presence is the condition, and there is no second forge.yaml
// check to disagree with it. A greenfield chat gets it from the first turn
// after `reliant forge project new --in-place` puts forge.yaml at the root,
// which is why the greenfield guidance asks for --in-place.
//
// SOURCE. Only the forge-scoped entry counts. Its body was rendered on the
// daemon from the forge linked into the reliant binary, through
// forgecli.RenderSkill — the renderer `reliant forge skill load` itself
// prints through. A copy rendered to disk (.claude/skills/...) is a different
// scope; if one shadows the forge entry, nothing is preloaded rather than the
// possibly-stale render.
//
// CACHE. Appended after the node's own skills, so the seed's order is fixed.
// The seed is rebuilt each turn byte-identically at the same offset (see
// insertSeededMessagesAfterFirstUserTurn) and never persisted, so exactly one
// copy sits in the cached prompt prefix for the whole session. It changes at
// most once — the turn a greenfield project becomes a forge project — and that
// turn already misses the cache, because forge's framework memory arrives at
// the head of the prompt at the same moment.
func withForgeStartHere(requested []string, catalog []cfgpkg.StoredSkill) (skills []string, added []string) {
	if len(requested) == 0 || !hasForgeStartHere(catalog) {
		return requested, nil
	}
	for _, p := range requested {
		if strings.EqualFold(strings.TrimSpace(p), forgeStartHereSkill) {
			return requested, nil
		}
	}
	skills = make([]string, 0, len(requested)+1)
	skills = append(skills, requested...)
	return append(skills, forgeStartHereSkill), []string{forgeStartHereSkill}
}

// hasForgeStartHere reports whether the catalog carries forge's start-here
// skill as the forge binary rendered it for the project root.
func hasForgeStartHere(catalog []cfgpkg.StoredSkill) bool {
	for _, s := range catalog {
		if s.SkillPath == forgeStartHereSkill &&
			s.Scope == string(skillscore.ScopeForge) &&
			s.Source == "" &&
			strings.TrimSpace(s.Body) != "" {
			return true
		}
	}
	return false
}
