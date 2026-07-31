// Copyright (c) 2025 Reliant Labs
package catalog

import "sort"

// SkillPathsForProject returns every path a skill in projectPath is
// addressable by — reliant's embedded builtins, forge's skills under the emit
// partition that decides whether each carries the synthetic "forge/" namespace
// or its bare path, and the project's own on-disk skills.
//
// This is the candidate set workflow validation judges a `skills:` block
// against (see internal/workflow/validation/skills.go). Two properties make it
// usable for that and not just for display:
//
//   - The user-home roots (~/.reliant, ~/.claude, ~/.codex) are excluded. A
//     shipped charter must validate the same way on every machine; folding in
//     whatever a developer happens to have installed would let a bad name pass
//     on the laptop that wrote it and fail in CI, or vice versa.
//
//   - It is the SAME enumeration the runtime catalog performs, so a name that
//     validates is a name the agent can actually load. A parallel
//     reimplementation would drift from the thing it claims to check — which is
//     precisely the failure this set exists to catch.
//
// Whether forge's framework skills appear under "forge/" depends, as at
// runtime, on projectPath containing forge.yaml.
func SkillPathsForProject(projectPath string) []string {
	snapshot := DiscoverAll(DiscoverInput{
		ProjectPath:        projectPath,
		ExcludeGlobalRoots: true,
	})

	seen := make(map[string]struct{}, len(snapshot.Definitions))
	paths := make([]string, 0, len(snapshot.Definitions))
	for _, def := range snapshot.Definitions {
		p := def.SkillPath
		if p == "" {
			p = def.NormalizedKey
		}
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// WorkflowSkillPaths returns the candidate set workflow validation judges a
// `skills:` block against: everything projectPath surfaces, UNION everything a
// forge project surfaces. An empty projectPath yields the forge view alone.
//
// The union is deliberate. Validation runs where the workflow file lives, not
// where it will execute, so it cannot know the target project — a charter
// written to build forge apps is routinely validated from a directory with no
// forge.yaml. Judging it against only the local catalog would report every
// "forge/..." reference as broken, and a check that cries wolf gets switched
// off.
//
// It is still tight enough to catch the defect it exists for. A name like
// "forge/service-layer" resolves in NO project context — forge surfaces that
// skill at its bare path, so nothing anywhere carries the prefixed spelling —
// and the union cannot rescue it. What the union permits is only the genuinely
// undecidable case: a name valid in some project other than this one.
func WorkflowSkillPaths(projectPath string) []string {
	seen := make(map[string]struct{})
	var paths []string

	add := func(candidates []string) {
		for _, p := range candidates {
			if p == "" {
				continue
			}
			if _, dup := seen[p]; dup {
				continue
			}
			seen[p] = struct{}{}
			paths = append(paths, p)
		}
	}

	if projectPath != "" {
		add(SkillPathsForProject(projectPath))
	} else {
		add(builtinSkillPathsOnly())
	}
	add(ForgeFrameworkSkillPaths())

	sort.Strings(paths)
	return paths
}

// builtinSkillPathsOnly returns just reliant's embedded builtin skill paths,
// with no disk access at all — the project-independent half of the catalog.
func builtinSkillPathsOnly() []string {
	defs := builtinSkills(false)
	paths := make([]string, 0, len(defs))
	for _, def := range defs {
		if def.SkillPath != "" {
			paths = append(paths, def.SkillPath)
		}
	}
	return paths
}
