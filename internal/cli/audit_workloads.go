package cli

import (
	"context"
	"sort"

	"github.com/reliant-labs/forge/internal/cli/audittype"
	"github.com/reliant-labs/forge/internal/codegen"
)

// auditWorkloadInventory is the workload half of `forge project audit`'s
// shape inventory: every workload each env declares, whatever it runs.
//
// It renders EVERY env, because a workload's runtime is bound per env and
// only the render knows it. The reader is renderEnvsPure — the one generate
// uses for its every-env steps — rather than a RenderKCL loop: it evaluates
// the envs concurrently (bounded), and it reverts any tracked file a render
// writes, which an audit (a read) must never leave behind. A source scan
// alone would be cheaper but cannot say where a workload runs.
//
// An env that does not render is reported in Failed and costs nothing else:
// the shape category notes it as a warning, the audit's exit code is
// unchanged, and the other envs' workloads are still listed.
func auditWorkloadInventory(projectDir string) audittype.WorkloadInventory {
	envs, err := ListEnvs(projectDir)
	if err != nil {
		return audittype.WorkloadInventory{
			Workloads: projectWorkloadInventory(nil, codegen.DeclaredWorkloadSites(projectDir)),
			Failed:    map[string]string{"*": err.Error()},
		}
	}
	renders := renderEnvsPure(context.Background(), projectDir, envs)
	inv := audittype.WorkloadInventory{Workloads: projectWorkloadInventory(renders, codegen.DeclaredWorkloadSites(projectDir))}
	for _, r := range renders {
		if r.err != nil {
			if inv.Failed == nil {
				inv.Failed = map[string]string{}
			}
			inv.Failed[r.env] = r.err.Error()
		}
	}
	return inv
}

// projectWorkloadInventory is the pure core: the rendered envs' workloads,
// merged by name, located by their literal's site, plus every literal no env
// renders. Sorted by name.
func projectWorkloadInventory(renders []envRender, sites []codegen.WorkloadSite) []audittype.Workload {
	siteOf := map[string]codegen.WorkloadSite{}
	for _, s := range sites {
		if _, dup := siteOf[s.Name]; !dup {
			siteOf[s.Name] = s // the first literal, by file and line
		}
	}
	ordered := append([]envRender(nil), renders...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].env < ordered[j].env })

	byName := map[string]*audittype.Workload{}
	for _, r := range ordered {
		if r.err != nil || r.entities == nil {
			continue
		}
		for _, w := range r.entities.Workloads {
			row, seen := byName[w.Name]
			if !seen {
				row = &audittype.Workload{
					Name:     w.Name,
					Kind:     w.Kind,
					Build:    workloadBuildType(w.Build.Type),
					Image:    w.Image,
					Runtimes: map[string]string{},
				}
				if row.Kind == "" {
					row.Kind = string(w.Spec.EffectiveKind())
				}
				if row.Image == "" {
					row.Image = w.Spec.Image
				}
				if s, ok := siteOf[w.Name]; ok {
					row.File, row.Line = s.File, s.Line
				}
				byName[w.Name] = row
			}
			row.Runtimes[r.env] = w.Runtime.Type
		}
	}
	for _, s := range sites {
		if _, ok := byName[s.Name]; ok {
			continue
		}
		kind := s.Kind
		if kind == "" {
			kind = codegen.WorkloadKindService
		}
		byName[s.Name] = &audittype.Workload{
			Name:     s.Name,
			Kind:     kind,
			Build:    workloadBuildType(s.Build),
			Image:    s.Image,
			Runtimes: map[string]string{},
			File:     s.File,
			Line:     s.Line,
		}
	}

	out := make([]audittype.Workload, 0, len(byName))
	for _, w := range byName {
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// workloadBuildType is a build discriminator as the inventory reports it:
// "none" for a workload forge builds nothing for.
func workloadBuildType(t string) string {
	if t == "" {
		return "none"
	}
	return t
}
