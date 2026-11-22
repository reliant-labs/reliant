// Package projectstore is the single read surface for a forge project's
// PROJECT-GLOBAL state. Every consumer outside the config loader reads
// project metadata and feature flags through a [Store] rather than touching
// *config.ProjectConfig directly.
//
// It deliberately does NOT answer "what does this project contain". Servers,
// workers, crons, operators and binaries are declared by the code that
// implements them, and the only way to learn about them is to read that code
// (codegen.DiscoverProjectComponents). A store method returning them would
// be a cache of the tree that is stale the moment an add verb runs.
//
// Why the indirection: the project + feature state is the part of forge.yaml
// that a future revision wants to relocate out of the hand-edited file.
// Routing every consumer through this one type localizes that swap to one
// implementation; nothing else in the tree assumes the state lives in a
// *config.ProjectConfig.
//
// Interfaces at the consumer, not here. [New] returns a concrete *[Store]
// (accept interfaces, return structs). Callers that take the store as a
// dependency declare the narrow interface they actually use next to
// themselves — e.g. `type featureReader interface { Features() FeatureSet }`
// for the feature-gate helpers, `type metaReader interface { Meta()
// ProjectMeta }` for the namespace resolvers. A wide interface declared here
// that each caller used a slice of was interface bloat: 16 of its 29 methods
// had zero callers. The store therefore exposes only the accessors that are
// actually read; add a method when a consumer needs it, not before.
//
//forge:exclude-contract: the project-state store: a concrete *Store over a *config.ProjectConfig read from local files, with one implementation and no Service/Deps/New shape
package projectstore

import "github.com/reliant-labs/forge/internal/config"

// ProjectMeta is the project-level metadata view: identity, kind, binary
// mode, and the pinned versions. The Effective*/Is* accessors mirror the
// config helpers so consumers get the derived forms without re-deriving.
type ProjectMeta struct {
	Name         string
	ModulePath   string
	Kind         string // raw kind: "" | service | cli | library
	Binary       string // raw binary mode: "" | per-service | shared
	ForgeVersion string
}

// EffectiveKind returns the project kind, defaulting to "service".
func (m ProjectMeta) EffectiveKind() string { return config.EffectiveProjectKind(m.Kind) }

// EffectiveBinary returns the binary mode, defaulting to "per-service".
func (m ProjectMeta) EffectiveBinary() string { return config.EffectiveProjectBinary(m.Binary) }

// IsBinaryShared reports whether the project uses the shared-binary mode.
func (m ProjectMeta) IsBinaryShared() bool {
	return m.EffectiveBinary() == config.ProjectBinaryShared
}

// EffectiveForgeVersion returns the pinned forge version, defaulting to
// "0.0.0" for projects predating the field.
func (m ProjectMeta) EffectiveForgeVersion() string {
	if v := trim(m.ForgeVersion); v == "" {
		return "0.0.0"
	}
	return m.ForgeVersion
}

// IsCLIKind reports whether the project is a CLI binary.
func (m ProjectMeta) IsCLIKind() bool { return m.EffectiveKind() == config.ProjectKindCLI }

// IsLibraryKind reports whether the project is a pure Go library.
func (m ProjectMeta) IsLibraryKind() bool { return m.EffectiveKind() == config.ProjectKindLibrary }

// IsServiceKind reports whether the project is a Connect-RPC service.
func (m ProjectMeta) IsServiceKind() bool { return m.EffectiveKind() == config.ProjectKindService }

// FeatureSet is the resolved feature state of a project — the same
// derived+explicit resolution config.FeaturesConfig performs. It is a thin
// alias today (yamlStore returns the config block directly) so every
// existing *Enabled() accessor keeps working; the type exists so the
// interface advertises features as a first-class surface for Phase 2.
type FeatureSet = config.FeaturesConfig
