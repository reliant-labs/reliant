package projectstore

import "github.com/reliant-labs/forge/internal/config"

// Store is the forge.yaml-backed project store. It wraps a
// *config.ProjectConfig that has already been through the loader
// (LoadStrict → ApplyDerivedDefaults → path/kind normalization) and
// projects its read surface into the view types below.
//
// This is the ONLY type that holds a *config.ProjectConfig. Phase 2's
// source swap replaces (or supplements) this implementation; consumers
// depend on narrow interfaces they declare themselves (see the package
// doc), so the swap stays invisible to them.
//
// Accept interfaces, return structs: New returns a concrete *Store rather
// than a wide interface. Each consumer that takes the store as a
// dependency declares the one- or two-method interface it actually uses
// (e.g. `featureReader { Features() FeatureSet }`), so there is no
// speculative all-methods abstraction for callers to over-depend on.
type Store struct {
	cfg *config.ProjectConfig
}

// New wraps an already-loaded, already-derived project config in a *Store.
// The caller owns loading + normalization (the cli/generator loaders); New
// takes the resulting config and exposes it through the view accessors. The
// pointer is retained (not copied) so mutation methods and Config() observe
// the same underlying config the loader produced.
func New(cfg *config.ProjectConfig) *Store {
	return &Store{cfg: cfg}
}

// Meta returns project-level metadata (name, module, kind, versions).
func (s *Store) Meta() ProjectMeta {
	return ProjectMeta{
		Name:         s.cfg.Name,
		ModulePath:   s.cfg.ModulePath,
		Kind:         s.cfg.Kind,
		Binary:       s.cfg.Binary,
		ForgeVersion: s.cfg.ForgeVersion,
	}
}

// Features returns the resolved (derived + explicit) feature set.
func (s *Store) Features() FeatureSet { return s.cfg.Features }

// Frontends returns the declared frontend configs.
func (s *Store) Frontends() []config.FrontendConfig { return s.cfg.Frontends }

// Database returns the `database:` section.
func (s *Store) Database() config.DatabaseConfig { return s.cfg.Database }

// CI returns the `ci:` section.
func (s *Store) CI() config.CIConfig { return s.cfg.CI }

// K8s returns the `k8s:` section.
func (s *Store) K8s() config.K8sConfig { return s.cfg.K8s }

// Lint returns the `lint:` section.
func (s *Store) Lint() config.LintConfig { return s.cfg.Lint }

// Contracts returns the `contracts:` section.
func (s *Store) Contracts() config.ContractsConfig { return s.cfg.Contracts }

// Config returns the underlying project config — the write/marshal +
// whole-config escape hatch, and the one seam Phase 2 must reconcile.
func (s *Store) Config() *config.ProjectConfig { return s.cfg }
