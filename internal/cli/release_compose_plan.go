package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/release"
)

func kclFrontendHasSource(e *KCLEntities, name string) bool {
	if e == nil {
		return false
	}
	for _, f := range e.Frontends {
		if f.Name == name {
			return f.Source != nil
		}
	}
	return false
}

// compositionSelection resolves runtime consumers independently of build units.
// Multiple consumers sharing an image repository NEVER expands this selection.
func compositionSelection(consumers []release.Consumer, selectors []string) ([]release.ConsumerKey, error) {
	byName := map[string][]release.ConsumerKey{}
	seen := map[release.ConsumerKey]bool{}
	for _, c := range consumers {
		if c.Key.Workload == "" || seen[c.Key] {
			return nil, fmt.Errorf("invalid or duplicate consumer %v", c.Key)
		}
		seen[c.Key] = true
		byName[c.Key.Workload] = append(byName[c.Key.Workload], c.Key)
	}
	if len(consumers) == 0 {
		return nil, fmt.Errorf("environment declares no release consumers")
	}
	selected := map[release.ConsumerKey]bool{}
	if len(selectors) == 0 {
		selectors = []string{"all"}
	}
	for _, s := range selectors {
		s = strings.TrimSpace(s)
		if s == "all" {
			if len(selectors) != 1 {
				return nil, fmt.Errorf("all cannot be combined with named selectors")
			}
			for k := range seen {
				selected[k] = true
			}
			continue
		}
		keys := byName[s]
		if len(keys) == 0 {
			return nil, fmt.Errorf("unknown release consumer %q", s)
		}
		if len(keys) != 1 {
			return nil, fmt.Errorf("ambiguous release consumer %q across clusters", s)
		}
		selected[keys[0]] = true
	}
	out := make([]release.ConsumerKey, 0, len(selected))
	for k := range selected {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cluster != out[j].Cluster {
			return out[i].Cluster < out[j].Cluster
		}
		return out[i].Workload < out[j].Workload
	})
	return out, nil
}

type compositionBuildPlan struct {
	Selected []release.ConsumerKey
	Build    buildPlanReport
	// Pending is deliberately not a final approval plan: pushed identities and
	// complete composed resource deltas are not yet known before building.
	Pending bool
}

// planCompositionBuild uses the normal target resolver and preflight. It does
// not read state, cut a release, generate, push, or claim final compatibility.
// Snapshot composition/closure validation remains the shared engine's job once
// candidate digests are known.
func planCompositionBuild(ctx context.Context, cfg *config.ProjectConfig, entities *KCLEntities, consumers []release.Consumer, selectors []string, base *release.SnapshotBase, opts buildOptions, in planInputs) (compositionBuildPlan, error) {
	selected, err := compositionSelection(consumers, selectors)
	if err != nil {
		return compositionBuildPlan{}, err
	}
	if len(selected) != len(consumers) && base == nil {
		return compositionBuildPlan{}, fmt.Errorf("partial release requires a captured base promotion; local build state is not a base")
	}
	if base != nil && (base.PromotionID == "" || !release.ValidDigest(base.SnapshotDigest) || !release.ValidDigest(base.BundleDigest)) {
		return compositionBuildPlan{}, fmt.Errorf("incomplete captured base promotion")
	}
	names := make([]string, 0, len(selected))
	for _, k := range selected {
		names = append(names, k.Workload)
	}
	opts.targets = names
	opts.buildTarget = "all"
	narrowed := narrowBuildEntities(cfg.Name, entities, opts)
	targets, err := resolveBuildTargetSet(cfg, narrowed, opts)
	if err != nil {
		return compositionBuildPlan{}, err
	}
	// The legacy completeness gate simulates a repository-keyed release and
	// therefore cannot judge composition. Full snapshot coverage is checked by
	// Compose; it must never be filled by harvesting historical state.
	in.cfg = cfg
	in.entities = narrowed
	in.targets = targets
	in.opts = opts
	in.opts.release = ""
	if entities != nil {
		if _, err := compositionBuildUnits(entities); err != nil {
			return compositionBuildPlan{}, err
		}
	}
	report := planBuild(ctx, in)
	return compositionBuildPlan{Selected: selected, Build: report, Pending: true}, nil
}

// compositionBuildUnits validates explicit recipe sharing against effective
// declarations, never repository equality. Unknown dynamic inputs do not earn
// reuse: this only deduplicates one execution within the current invocation.
func compositionBuildUnits(entities *KCLEntities) ([][]WorkloadEntity, error) {
	var units [][]WorkloadEntity
	indexes := map[string]int{}
	recipes := map[string]string{}
	goOutputs := map[string]string{}
	if entities == nil {
		return nil, fmt.Errorf("composition requires a rendered environment")
	}
	for _, w := range entities.Workloads {
		if w.Build.Type == "" {
			continue
		}
		if g := w.GoBuild(); g != nil {
			data, _ := json.Marshal(g)
			key := g.Cmd + "\x00" + g.OutputName
			if prior, ok := goOutputs[key]; ok && prior != string(data) {
				return nil, fmt.Errorf("go output %q has conflicting effective recipes", g.OutputName)
			}
			goOutputs[key] = string(data)
		}
		unit := w.Build.Unit
		if unit == "" {
			units = append(units, []WorkloadEntity{w})
			continue
		}
		recipe := w.Build
		recipe.Unit = ""
		// Recipe identity includes declared Go flags/toolchain fields rather
		// than cmd/output alone. Shell dynamic input receipts cannot authorize
		// automatic cross-invocation reuse even when declarations are equal.
		b, err := json.Marshal(struct {
			Build             BuildConfigEntity
			Image, BuildImage string
		}{recipe, w.Image, w.BuildImage})
		if err != nil {
			return nil, err
		}
		if i, ok := indexes[unit]; ok {
			if recipes[unit] != string(b) {
				return nil, fmt.Errorf("build unit %q has conflicting effective recipes", unit)
			}
			units[i] = append(units[i], w)
		} else {
			indexes[unit] = len(units)
			recipes[unit] = string(b)
			units = append(units, []WorkloadEntity{w})
		}
	}
	return units, nil
}
