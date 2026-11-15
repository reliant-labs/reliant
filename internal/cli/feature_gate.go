// Package cli — feature-gating helpers shared across cobra commands.
//
// Features (see config.FeaturesConfig) gate major subsystems — deploy, build,
// frontend, ci, observability, ... — and are DERIVED from what exists in the
// repo, unless forge.yaml's `features:` block pins one. Two modes are
// supported:
//
//   - requireFeature is the strict gate: a direct cobra subcommand
//     (e.g. `forge env deploy`, `forge build`) returns
//     config.DisabledFeatureError when the relevant feature is off.
//     The error format is centralised so sub-agents and humans
//     grepping for the "feature 'X' is disabled" string find one
//     authoritative spelling.
//
//   - skipFeature is the orchestrator gate: when `forge env up` is driving
//     several phases, a disabled phase logs a one-line skip and the
//     orchestrator continues with whatever remaining phases are
//     enabled. Returns false when the feature is off so the caller can
//     branch around the phase without surfacing an error to the user.
//
// Both helpers tolerate `cfg == nil` (project missing or unreadable)
// by treating it as "feature enabled" — the canonical "no forge.yaml,
// no opinion" behaviour every existing direct-invoke gate already
// uses. The `loadAndCheckFeature` helper is the one-liner most call
// sites need: load the project config, return the canonical disabled
// error if the feature is off, otherwise return the loaded config.

package cli

import (
	"fmt"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/projectstore"
)

// machineInvokedAnnotation marks a command that forge runs as a subprocess of
// its own pipeline rather than one a person types. Such a command must not
// emit interactive output: nobody is reading it, and the parent invocation has
// already said whatever there was to say. `protoc-gen-forge` is spawned by buf
// once per proto file, and each spawn is a fresh process.
const machineInvokedAnnotation = "forge.machine-invoked"

// featureCheck is the per-feature predicate signature. Each Feature*
// constant in package config has a paired FeaturesConfig.<Name>Enabled
// method; this type lets callers pass the method by reference without
// importing the FeaturesConfig type into the call site.
type featureCheck func(config.FeaturesConfig) bool

// featureChecks maps every config.Feature* constant to its
// FeaturesConfig accessor. Used by requireFeature so call sites pass
// just the feature name and the helper knows which accessor to invoke
// — keeps the name-to-accessor mapping in one place (mismatch is a
// compile-time error rather than a runtime mis-spelling).
var featureChecks = map[string]featureCheck{
	config.FeatureORM:           func(f config.FeaturesConfig) bool { return f.ORMEnabled() },
	config.FeatureCodegen:       func(f config.FeaturesConfig) bool { return f.CodegenEnabled() },
	config.FeatureMigrations:    func(f config.FeaturesConfig) bool { return f.MigrationsEnabled() },
	config.FeatureCI:            func(f config.FeaturesConfig) bool { return f.CIEnabled() },
	config.FeatureBuild:         func(f config.FeaturesConfig) bool { return f.BuildEnabled() },
	config.FeatureContracts:     func(f config.FeaturesConfig) bool { return f.ContractsEnabled() },
	config.FeatureFrontend:      func(f config.FeaturesConfig) bool { return f.FrontendEnabled() },
	config.FeatureObservability: func(f config.FeaturesConfig) bool { return f.ObservabilityEnabled() },
	config.FeatureHotReload:     func(f config.FeaturesConfig) bool { return f.HotReloadEnabled() },
	config.FeatureDeploy:        func(f config.FeaturesConfig) bool { return f.DeployEnabled() },
	config.FeatureIngress:       func(f config.FeaturesConfig) bool { return f.IngressEnabled() },
	config.FeatureOperators:     func(f config.FeaturesConfig) bool { return f.OperatorsEnabled() },
}

// featureReader is the narrow slice of the project store the feature-gate
// helpers depend on — declared here, at the consumer, rather than importing
// the store's whole method set. Anything that resolves the feature set
// (the concrete *projectstore.Store, or a test double) satisfies it.
type featureReader interface {
	Features() projectstore.FeatureSet
}

// isFeatureEnabled reports whether a named feature is enabled in cfg.
// A nil cfg (project missing) is treated as "enabled" so callers that
// don't bother loading config get the historical permissive default.
// An unknown feature name returns true with no error — keeps adding a
// new gate site backwards-compatible across forge versions that
// haven't yet registered the constant in featureChecks.
func isFeatureEnabled(store featureReader, name string) bool {
	if store == nil {
		return true
	}
	check, ok := featureChecks[name]
	if !ok {
		return true
	}
	return check(store.Features())
}

// requireFeature is the strict gate for direct cobra subcommands. It
// loads the project config and returns config.DisabledFeatureError
// when the named feature is off. Returns the loaded config on the
// happy path so the caller can hold on to it without a second read.
//
// Use from the top of a cobra RunE when the subcommand has no useful
// fallback (e.g. `forge env deploy` against a project with
// features.deploy: false). Don't use from orchestrators — see
// skipFeature for the orchestrator shape.
func requireFeature(name string) (*projectstore.Store, error) {
	store, err := loadProjectStore()
	if err != nil {
		return nil, err
	}
	if !isFeatureEnabled(store, name) {
		return nil, config.DisabledFeatureError(name)
	}
	return store, nil
}

// skipFeature is the orchestrator gate. Returns true when the
// orchestrator SHOULD skip the phase, false when the phase should
// run. When skipping, emits a one-line log so the user can see WHY
// the phase was elided.
//
// Used by `forge env up` to elide build/deploy/frontend phases against
// projects that have those features turned off. Unlike requireFeature
// this never errors — the orchestrator wants to finish whatever
// remaining phases are enabled.
func skipFeature(store featureReader, name, phase string) bool {
	if isFeatureEnabled(store, name) {
		return false
	}
	fmt.Printf("[%s] feature '%s' is off for this project (derived from the repo; see `forge project features`) — skipping\n", phase, name)
	return true
}
