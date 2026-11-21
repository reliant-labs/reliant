// Package config defines the canonical forge.yaml types shared across forge.
//
// The data types (ProjectConfig, ComponentConfig, etc.) are proto-like data
// carriers — they ship with accessor methods (Effective*, Is*) directly on
// the struct values. Those methods are part of the data, not behavior to
// mock. The Service interface below is the package's narrow behavior surface
// (currently kind classification); future YAML load/save behavior moves
// behind it as it lands (Load lives today in internal/cli and
// internal/generator and is ported in those phases).
package config

// Service is the behavioral surface of the config package.
//
// Today it wraps EffectiveProjectKind so the require-contract analyzer is
// satisfied without forcing the data-type accessor methods onto an interface
// they have no business being on. Future stateful behavior (file loading,
// validation, defaulting against forge.yaml on disk) will land here as
// internal/cli and internal/generator are ported and their config-touching
// helpers consolidate into this package.
type Service interface {
	// EffectiveKind normalizes a raw kind string to one of the canonical
	// ProjectKind* constants, defaulting to "service" for empty/unknown input.
	EffectiveKind(kind string) string
}

// Deps is the dependency set for the config Service. Empty today; expanded
// when file-loading behavior moves into this package.
type Deps struct{}

// New constructs a config.Service.
func New(_ Deps) Service {
	return &svc{}
}

type svc struct{}

// EffectiveKind delegates to the package-level EffectiveProjectKind helper.
func (s *svc) EffectiveKind(kind string) string {
	return EffectiveProjectKind(kind)
}
