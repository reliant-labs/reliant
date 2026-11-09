// Package cli — `forge project features` cobra command.
//
// `forge project features` prints the RESOLVED feature graph for the current
// project: every feature, whether it is on or off, WHY (derived from
// project shape vs explicitly set in forge.yaml), and its dependency
// edges (other features / shape preconditions it requires). It is the
// human/agent-facing window onto the dependency graph that
// internal/config/feature_graph.go validates at load time — when a
// config loads clean, this command shows the coherent set; the validator
// guarantees what it prints can never contradict itself.
//
// `forge project features` covers the whole menu, stable + experimental, with
// the dependency column — including the four opt-in experimental flags.

package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/config"
)

// featureDisplayOrder is the stable print order for `forge project features` —
// roughly the codegen → build → deploy → frontend flow a
// reader thinks in, rather than alphabetical. Any feature not listed
// here (defensive: a future Feature* constant the menu forgot) is
// appended alphabetically so it can never silently vanish from the
// output.
var featureDisplayOrder = []config.FeatureName{
	config.FeatureCodegen,
	config.FeatureORM,
	config.FeatureMigrations,
	config.FeatureContracts,
	config.FeatureFrontend,
	config.FeatureObservability,
	config.FeatureHotReload,
	config.FeatureCI,
	config.FeatureBuild,
	config.FeatureDeploy,
	config.FeatureIngress,
	config.FeatureOperators,
}

func newFeaturesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "features",
		Short: "Print the resolved feature graph (on/off, why, dependencies)",
		Long: `Print every forge feature for this project: whether it is enabled,
WHY (what in the repo made it on or off), and the features / shape
preconditions it depends on. Every feature is derived from what exists in
the repository, unless forge.yaml's features: block pins it on or off
(features: {observability: false}). The ORM has no key: //forge:no-orm in
internal/db switches it off.

The dependency graph is validated at config-load time — a feature
enabled with a dependency off is a load error. This command shows the
resolved, coherent set: codegen, orm, migrations, frontend, deploy,
ingress, and the rest, with their edges.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := loadProjectStore()
			if err != nil {
				return err
			}
			return printFeatureGraph(cmd, store.Config())
		},
	}
}

// printFeatureGraph renders the resolved feature table to the command's
// stdout. Layout is fixed-column so an agent can grep a feature line and
// read its state/origin/deps positionally.
func printFeatureGraph(cmd *cobra.Command, cfg *config.ProjectConfig) error {
	out := cmd.OutOrStdout()
	resolved := cfg.Features.EffectiveFeatures()

	// Build the print order: the curated order first, then any feature
	// present in `resolved` that the curated list missed, appended
	// alphabetically so the output is exhaustive by construction.
	seen := map[config.FeatureName]bool{}
	order := make([]config.FeatureName, 0, len(resolved))
	for _, n := range featureDisplayOrder {
		if _, ok := resolved[n]; ok {
			order = append(order, n)
			seen[n] = true
		}
	}
	var leftover []config.FeatureName
	for n := range resolved {
		if !seen[n] {
			leftover = append(leftover, n)
		}
	}
	sort.Strings(leftover)
	order = append(order, leftover...)

	_, _ = fmt.Fprintln(out, "Feature graph (resolved):")
	for _, name := range order {
		on := resolved[name]
		marker := "[ ]"
		if on {
			marker = "[x]"
		}
		line := fmt.Sprintf("  %s %-16s %s", marker, name, featureReason(cfg, name, on))
		if deps := config.FeatureDependencies(name); len(deps) > 0 {
			line += fmt.Sprintf("  →  requires: %s", strings.Join(deps, ", "))
		}
		_, _ = fmt.Fprintln(out, line)
	}
	return nil
}

// featureReason says what in the repo made the feature resolve the way it did,
// so a reader can change the repo rather than hunt for a setting that does not
// exist. The wording mirrors DeriveFeatureDefaults, which is the rule.
func featureReason(cfg *config.ProjectConfig, name config.FeatureName, on bool) string {
	kind := cfg.EffectiveKind()
	service := kind == config.ProjectKindService
	if cfg.Features.Pinned(name) {
		return "(pinned in forge.yaml features:)"
	}
	// A derived feature that a pinned requirement turned off says so, rather
	// than naming evidence that would have turned it on.
	if !on {
		for _, dep := range config.FeatureDependencies(name) {
			if cfg.Features.Pinned(dep) && !cfg.Features.EffectiveFeatures()[dep] {
				return fmt.Sprintf("(requires %s, pinned off in forge.yaml)", dep)
			}
		}
	}
	switch name {
	case config.FeatureCodegen:
		if on {
			return "(.proto files exist)"
		}
		return "(no .proto files)"
	case config.FeatureObservability, config.FeatureHotReload:
		if on {
			return "(server sources exist)"
		}
		return "(no pkg/app, internal/handlers or proto/services)"
	case config.FeatureDeploy:
		if on {
			return "(deploy/kcl exists)"
		}
		return "(no deploy/kcl)"
	case config.FeatureCI, config.FeatureBuild:
		return fmt.Sprintf("(kind: %s)", kind)
	case config.FeatureContracts:
		return "(always on)"
	case config.FeatureMigrations:
		if on {
			return "(db/migrations exists)"
		}
		return "(no db/migrations)"
	case config.FeatureORM:
		if on {
			return "(db/migrations exists)"
		}
		if service && cfg.Database.Driver != "" && cfg.Database.Driver != "none" {
			return "(//forge:no-orm in " + cfg.Database.ORM.PackageDir() + ")"
		}
		return "(no db/migrations)"
	case config.FeatureFrontend:
		if on {
			return "(a frontend exists)"
		}
		return "(no frontend found)"
	case config.FeatureIngress:
		if on {
			return "(deploy/kcl declares a forge.Gateway)"
		}
		return "(no forge.Gateway in deploy/kcl)"
	case config.FeatureOperators:
		if on {
			return "(internal/operators exists)"
		}
		return "(no internal/operators)"
	}
	return ""
}
