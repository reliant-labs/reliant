// Copyright (c) 2025 Reliant Labs
//
// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
package core

import "strings"

// ResolveSkillPathIndex resolves a caller-supplied skill path against the set
// of canonical skill paths, returning the index of the match or -1.
//
// This is THE addressing rule for skills. Every surface that answers "does this
// skill path resolve?" must go through it — the skill tool at runtime, and the
// workflow validator that checks a charter's `skills:` block before the run
// starts. A validator that disagreed with the runtime resolver would be worse
// than none: it would pass names the agent cannot load, or fail names it can.
//
// Order: exact, then case-insensitive, then a unique component-aligned suffix.
//
// The suffix step exists because reliant surfaces forge's skills under a
// synthetic "forge/" namespace (and, in multi-repo projects, a further
// "<repo>/" prefix) while forge's own CLI uses the bare path — `forge skill
// list` prints "frontend/design", `forge skill load frontend/design` works,
// and forge's skill bodies cross-reference each other unprefixed ("see the
// frontend/state skill"). Every one of those references is a path the tool
// must accept, or following a skill's own advice 404s.
//
// Matching is component-aligned: the candidate must have a "/" immediately
// before the requested path, so "design" never matches "frontend/design".
// An exact match always wins over a suffix match, so a project skill named
// "deploy" is never shadowed by "forge/deploy". Two suffix candidates resolve
// to nothing, leaving the caller to render its "did you mean" list rather than
// silently picking one.
//
// Note the asymmetry this deliberately does NOT paper over: the rule accepts a
// SHORTER spelling of a longer path, never a longer spelling of a shorter one.
// A forge skill that reliant surfaces at its bare path (emit "general"/"both",
// e.g. "service-layer") is NOT addressable as "forge/service-layer" — there is
// no such path to be a suffix of. That is the whole point of the check in
// internal/workflow/validation/skills.go.
func ResolveSkillPathIndex(paths []string, query string) int {
	for i, p := range paths {
		if p == query {
			return i
		}
	}

	lower := strings.ToLower(strings.TrimSpace(query))
	if lower == "" {
		return -1
	}
	for i, p := range paths {
		if strings.ToLower(p) == lower {
			return i
		}
	}

	suffix := "/" + lower
	match := -1
	for i, p := range paths {
		if !strings.HasSuffix(strings.ToLower(p), suffix) {
			continue
		}
		if match >= 0 {
			return -1
		}
		match = i
	}
	return match
}

// ResolveSkillPath reports the canonical path a caller-supplied path resolves
// to. ok is false when nothing resolves, or when the path is ambiguous.
func ResolveSkillPath(paths []string, query string) (canonical string, ok bool) {
	i := ResolveSkillPathIndex(paths, query)
	if i < 0 {
		return "", false
	}
	return paths[i], true
}
