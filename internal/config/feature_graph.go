// feature_graph.go — the explicit feature dependency graph.
//
// Features are derived from the repo (DeriveFeatureDefaults), and the edges
// between them used to live implicitly in the generate pipeline's gate
// functions (frontend codegen runs after proto codegen; the ORM step reads
// proto/services; ingress gates on deploy). That made it possible for a
// derivation rule to produce a set that LOADS clean but then either silently
// no-ops a feature or blows up mid-generate with a confusing downstream error.
//
// This file makes those edges EXPLICIT. Each feature declares the features and
// shape preconditions it requires; config load validates the derived feature
// set against the graph and rejects any enabled feature whose dependency is
// off — loudly, naming both sides and the fix. With nothing configurable it is
// the guard that keeps a future derivation rule honest.
//
// The edges are verified against real consumers in the generate
// pipeline (internal/cli/generate_pipeline.go) — see the comment on each
// featureDeps entry for the gate/step that justifies it. Adding a new
// feature edge here is the single place to encode "X needs Y"; the
// validator, derivation-consistency check, and `forge project features` output
// all read from this one registry.

package config

import (
	"fmt"
	"sort"
)

// featureRequirement is one edge in the dependency graph: a feature that
// must also be enabled (Feature) and/or a shape precondition that must
// hold (Shape) for the dependent feature to be coherent.
type featureRequirement struct {
	// Feature is the name of a required feature (another key in the
	// graph). Empty when this requirement is a pure shape precondition.
	Feature FeatureName
	// Shape, when non-nil, is a project-shape precondition that must hold
	// for the dependent feature to be coherent (e.g. "a database driver
	// is configured"). label names it for the error message.
	Shape func(*ProjectConfig) bool
	// label is the human-readable name of a Shape precondition, used in
	// the validation error when Shape returns false. Empty for Feature
	// edges (the feature name carries its own label).
	label string
	// fix is the imperative remediation appended to the validation error.
	// Names both sides of the edge so the user can fix it from either
	// direction.
	fix string
}

// featureDeps maps a feature to the requirements it imposes when ENABLED.
// A feature with no entry (or an empty slice) has no dependencies.
//
// Edges + their justifying consumer (internal/cli/generate_pipeline.go):
//
//   - frontend → codegen: the frontend hooks / CRUD-pages steps
//     (gateFrontendHasServices) consume the proto-derived service surface
//     that the codegen steps produce; frontend TS stubs run after the Go
//     stubs. Frontend codegen against codegen=off generates nothing
//     useful.
//   - orm → codegen + database-driver: stepInternalDBORM (gateORMHasServices)
//     is entity-driven off proto/services, which only exists when codegen
//     runs; the ORM projects a DB schema, so a driver must be configured.
//   - migrations → codegen + database-driver: entity-aware seeds /
//     migration generation derive from the proto/db + proto/services
//     entities (codegen) and target a concrete driver.
//   - deploy → build: the deploy pipeline renders KCL and applies images
//     that the build pipeline produces; deploying without build has
//     nothing to ship.
//   - ingress → deploy: gateIngressEnabled is literally
//     DeployEnabled() && IngressEnabled() — ingress is a deploy-time
//     Gateway API overlay.
//
// Operator components depend on features.operators — but that edge is
// component-shape → feature, not feature → feature, so it lives in the
// validator (validateFeatureGraph) rather than this map.
var featureDeps = map[FeatureName][]featureRequirement{
	FeatureFrontend: {
		{Feature: FeatureCodegen, fix: "codegen derives from .proto files existing; a frontend needs it"},
	},
	FeatureORM: {
		{Feature: FeatureCodegen, fix: "codegen derives from .proto files existing; the ORM needs it"},
		{Shape: hasDatabaseDriver, label: "a database driver", fix: "add db/migrations — the ORM projects the applied schema"},
	},
	FeatureMigrations: {
		{Feature: FeatureCodegen, fix: "codegen derives from .proto files existing; migrations need it"},
		{Shape: hasDatabaseDriver, label: "a database driver", fix: "add db/migrations"},
	},
	FeatureDeploy: {
		{Feature: FeatureBuild, fix: "build derives from the project not being a library; deploy needs it"},
	},
	FeatureIngress: {
		{Feature: FeatureDeploy, fix: "deploy derives from deploy/kcl existing; ingress is a deploy-time overlay"},
	},
}

// hasDatabaseDriver reports whether a concrete database driver is
// configured (after section defaulting). Mirrors the derive.go hasDB
// predicate's driver clause (postgres, not "" or "none").
func hasDatabaseDriver(c *ProjectConfig) bool {
	d := c.Database.Driver
	return d != "" && d != "none"
}

// validateFeatureGraph checks the RESOLVED feature set (derived defaults
// + explicit overrides) against featureDeps. A feature that resolves to
// enabled whose dependency is off (or whose shape precondition fails) is
// a load error naming both sides and the fix. All violations are batched
// into validationIssues so the caller surfaces the full list at once.
//
// Called by LoadProject AFTER ApplyDerivedDefaults, so the feature
// accessors resolve against the effective (defaulted) shape — derivation
// and explicit overrides are both folded in by the time we get here.
func validateFeatureGraph(cfg *ProjectConfig) []validationIssue {
	var out []validationIssue
	f := cfg.Features
	enabled := f.EffectiveFeatures()

	// Feature → (feature | shape) edges, in stable name order so the
	// batched error list is deterministic.
	names := make([]FeatureName, 0, len(featureDeps))
	for name := range featureDeps {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if !enabled[name] {
			continue
		}
		for _, req := range featureDeps[name] {
			if req.Feature != "" {
				if !enabled[req.Feature] {
					out = append(out, validationIssue{
						msg: fmt.Sprintf("feature %q requires %q, which is disabled", name, req.Feature),
						fix: req.fix,
					})
				}
				continue
			}
			if req.Shape != nil && !req.Shape(cfg) {
				out = append(out, validationIssue{
					msg: fmt.Sprintf("feature %q requires %s, which is not configured", name, req.label),
					fix: req.fix,
				})
			}
		}
	}

	return out
}

// FeatureDependencies returns the feature-graph edges for name as a flat
// list of human-readable dependency labels (other feature names and
// shape preconditions). Stable order. Used by `forge project features` to print
// each feature's deps. Returns an empty slice for a feature with no
// edges.
func FeatureDependencies(name FeatureName) []string {
	reqs, ok := featureDeps[name]
	if !ok {
		return []string{}
	}
	out := make([]string, 0, len(reqs))
	for _, req := range reqs {
		if req.Feature != "" {
			out = append(out, req.Feature)
			continue
		}
		if req.label != "" {
			out = append(out, req.label)
		}
	}
	return out
}
