// derive.go — everything forge.yaml no longer says, derived from the repo.
//
// A scaffolded forge.yaml is minimal: name, module_path, forge_version and
// the few real settings. Everything else is DERIVED at load time from what
// exists in the tree:
//
//   - the project kind (service | cli | library) from the sources;
//   - every feature flag (DeriveFeatureDefaults) from the kind and from
//     marker files and directories; the optional `features:` block pins one
//     on or off whatever the tree says (FeaturesConfig);
//   - the database driver and migrations directory (DeriveDatabase) from
//     whether db/migrations exists;
//   - the pnpm-workspaces layout (DeriveFrontendWorkspaces) from
//     pnpm-workspace.yaml;
//   - the frontend inventory from frontends/ on disk, overlaid by the KCL
//     `forge.Frontend` declarations (frontend_inventory.go);
//   - absent section blocks (ci, lint, deploy, k8s, database.migration_safety)
//     are filled with the canonical scaffold defaults for the kind.
//
// Anything the user writes in a section that still exists is taken
// literally; derivation never overrides a present value. The write side is
// symmetric: NormalizeForWrite drops values byte-identical to what
// derivation would produce, so a load → mutate → write round-trip keeps
// forge.yaml minimal.
package config

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// NoORMDirective is the package-level marker that opts a project out of the
// generated ORM. It goes in the doc comment of the hand-written DB package
// (internal/db), beside the code it affects:
//
//	// Package db is the hand-owned repository layer.
//	//
//	//forge:no-orm: the repository is hand-written; entities are not projected
//	package db
//
// It mirrors `//forge:exclude-contract: <why>`, the other per-package opt-out
// already in use: the exemption sits in the package it exempts, with a
// reason, where a reader of that package sees it. A forge.yaml key was the
// wrong home because it described a property of internal/db from a file that
// never mentions internal/db. The reason is not required by the loader.
const NoORMDirective = "//forge:no-orm"

// hasNoORMMarker reports whether the ORM package directory (relDir, by
// default internal/db) declares `//forge:no-orm`. Only non-test,
// non-generated .go files directly in it are read,
// and only line comments — the same scope `//forge:exclude-contract` is
// honoured in.
func hasNoORMMarker(projectDir, relDir string) bool {
	if projectDir == "" {
		return false
	}
	dir := filepath.Join(projectDir, filepath.FromSlash(relDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") ||
			strings.HasSuffix(n, "_test.go") || strings.HasSuffix(n, "_gen.go") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		found := false
		for sc.Scan() {
			// Spaced (`// forge:no-orm`) or unspaced, like every other Go
			// marker: the spaced spelling used to be read as prose, so a
			// project that wrote it kept its ORM with no word said.
			text, isComment := strings.CutPrefix(strings.TrimSpace(sc.Text()), "//")
			if !isComment {
				continue
			}
			text = strings.TrimLeft(text, " \t")
			if rest, ok := strings.CutPrefix(text, strings.TrimPrefix(NoORMDirective, "//")); ok {
				if rest == "" || rest[0] == ':' || rest[0] == ' ' || rest[0] == '\t' {
					found = true
					break
				}
			}
		}
		_ = f.Close()
		if found {
			return true
		}
	}
	return false
}

// gatewayDeclRE matches a KCL `forge.Gateway { ... }` declaration.
var gatewayDeclRE = regexp.MustCompile(`\bforge\.Gateway\s*\{`)

// declaresGateway reports whether the project's KCL tree declares a Gateway —
// the object that makes ingress mean anything. It is a TEXT scan of
// deploy/kcl, deliberately not a render: a render costs seconds per env, and
// config load runs on every command. A `forge.Gateway {` literal is exactly
// the declaration the renderer would lower, so the two cannot disagree about
// whether one exists. The vendored copy (.forge-kcl) is not under deploy/kcl.
func declaresGateway(projectDir string) bool {
	if projectDir == "" {
		return false
	}
	root := filepath.Join(projectDir, "deploy", "kcl")
	found := false
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".k") {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr == nil && gatewayDeclRE.Match(body) {
			found = true
		}
		return nil
	})
	return found
}

// hasOperatorPackage reports whether internal/operators holds an operator —
// the same presence test the generate pipeline's component discovery uses.
func hasOperatorPackage(projectDir string) bool {
	if projectDir == "" {
		return false
	}
	entries, err := os.ReadDir(filepath.Join(projectDir, "internal", "operators"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() != "testdata" {
			return true
		}
	}
	return false
}

// DeriveDatabase fills the database driver and migrations directory from the
// tree: a project with db/migrations uses postgres and that directory;
// anything else has no database. There is no setting to write —
// the directory IS the declaration, the same way the schema is (the
// migrations are the source of truth for it).
func DeriveDatabase(c *ProjectConfig) {
	if c.projectDir != "" && dirExists(filepath.Join(c.projectDir, DefaultMigrationsDir)) {
		c.Database.Driver = "postgres"
		c.Database.MigrationsDir = DefaultMigrationsDir
		return
	}
	c.Database.Driver = "none"
	c.Database.MigrationsDir = ""
}

// DeriveFrontendWorkspaces reads the pnpm-workspaces layout off the tree: a
// root pnpm-workspace.yaml means the project shares generated clients and
// hooks across frontends through packages/api and packages/hooks.
func DeriveFrontendWorkspaces(c *ProjectConfig) {
	if c.projectDir == "" {
		return
	}
	body, err := os.ReadFile(filepath.Join(c.projectDir, "pnpm-workspace.yaml"))
	// A machine-local dev bridge is an install overlay, not the opt-in
	// shared API/hooks layout. It must not change generation on this machine.
	c.workspaces = err == nil && !strings.HasPrefix(string(body), "# forge-dev-workspace-root:")
}

// DeriveFeatureDefaults computes the enabled/disabled state of every feature
// from what exists in the repo. Each feature is decided by ITS OWN evidence,
// not by the project kind: a deploy-only project (deploy/kcl, no protos, no
// server) is a service-kind project that must get deploy and nothing else.
//
//	codegen       ⇔ .proto files exist (proto/ or a buf.yaml module)
//	orm           ⇔ a database AND codegen AND no //forge:no-orm marker in internal/db
//	migrations    ⇔ a database AND codegen
//	ci            ⇔ kind != library
//	build         ⇔ kind != library
//	contracts     ⇔ a Go module (contract.go works for every kind that has Go)
//	frontend      ⇔ a frontend exists (frontends/ or KCL) AND codegen
//	observability ⇔ server sources exist (pkg/app, internal/handlers, proto/services)
//	hot_reload    ⇔ server sources exist
//	deploy        ⇔ deploy/kcl exists
//	ingress       ⇔ deploy AND deploy/kcl declares a forge.Gateway
//	operators     ⇔ internal/operators holds an operator
//
// "a database" means db/migrations exists (DeriveDatabase).
//
// With no on-disk project to read (a hand-built config in a test), there is
// no evidence to consult and the kind stands in: a service is assumed to have
// protos, a server and a deploy tree.
//
// The set is dependency-consistent by construction (see feature_graph.go):
// every dependent is gated on the EFFECTIVE value of what it requires, so a
// derived set can never trip the graph validator.
func DeriveFeatureDefaults(c *ProjectConfig) map[FeatureName]bool {
	isLibrary := c.IsLibraryKind()
	hasDB := c.Database.Driver != "" && c.Database.Driver != "none"
	codegen := c.hasProtos()
	server := c.hasServerSources()
	deploy := c.hasDeployTree()
	// A static site (`forge project new --kind site`) is no Go module, so it
	// has no contract.go to check. HasGoModule.
	goModule := c.HasGoModule()
	return map[FeatureName]bool{
		FeatureORM:           hasDB && codegen && !hasNoORMMarker(c.projectDir, c.Database.ORM.PackageDir()),
		FeatureCodegen:       codegen,
		FeatureMigrations:    hasDB && codegen,
		FeatureCI:            !isLibrary,
		FeatureBuild:         !isLibrary,
		FeatureContracts:     goModule,
		FeatureFrontend:      len(c.Frontends) > 0 && codegen,
		FeatureObservability: server,
		FeatureHotReload:     server,
		FeatureDeploy:        deploy,
		FeatureIngress:       deploy && declaresGateway(c.projectDir),
		FeatureOperators:     hasOperatorPackage(c.projectDir),
	}
}

// sectionDefaults returns the canonical scaffold defaults for every
// derivable section block, for the given project shape. This is the
// single source of truth shared by the load-time fill
// (ApplyDerivedDefaults) and the write-time normalizer
// (NormalizeForWrite) — and it is exactly what `forge project new` used to
// write into every forge.yaml.
type sectionDefaultsSet struct {
	Database DatabaseConfig
	CI       CIConfig
	Deploy   DeployConfig
	K8s      K8sConfig
	Lint     LintConfig
}

func sectionDefaults(c *ProjectConfig) sectionDefaultsSet {
	isService := c.IsServiceKind()
	hasProtos := c.hasProtos()
	hasFrontend := len(c.Frontends) > 0
	t := true
	d := sectionDefaultsSet{
		CI: CIConfig{
			Provider: "github",
			Lint: CILintConfig{
				Golangci:        true,
				Buf:             hasProtos,
				BufBreaking:     hasProtos,
				Frontend:        hasFrontend,
				MigrationSafety: isService,
			},
			Test: CITestConfig{Race: true, Coverage: false},
			VulnScan: CIVulnConfig{
				Go:     true,
				Docker: isService,
				NPM:    hasFrontend,
			},
		},
		Lint: LintConfig{
			Frontend: FrontendLintConfig{
				CSSHealth:      hasFrontend,
				NoImportant:    "warn",
				NoInlineStyles: "warn",
			},
		},
	}
	if isService {
		// Server-shaped sections only exist for service projects: a CLI
		// or library has no DB layer, nothing to deploy, no image.
		d.Database = DatabaseConfig{
			MigrationSafety: MigrationSafetyConfig{
				Enabled:           &t,
				UnsafeAddColumn:   "error",
				DestructiveChange: "error",
				VolatileDefault:   "warn",
			},
		}
		// Deploy carries only pipeline-control knobs now; its zero value is
		// the canonical default (the dead `provider` field was removed — the
		// CI provider derives to "github" via cfg.CI). No section carries a
		// registry: it is declared in the env's KCL.
		d.Deploy = DeployConfig{}
		d.K8s = K8sConfig{KCLDir: "deploy/kcl"}
	}
	return d
}

// ApplyDerivedDefaults resolves the shape-derived state of a freshly
// unmarshalled ProjectConfig:
//
//  1. every section block that is entirely absent (zero value) is filled
//     with the canonical scaffold default for the project kind;
//  2. the features block gets its derivation context attached so the
//     *Enabled() accessors can resolve absent flags from shape.
//
// Called by the loader (LoadProject) — code that hand-constructs a
// ProjectConfig in tests without calling this keeps the historical
// zero-value semantics.
//
// Prefer ApplyDerivedDefaultsFromNode when the yaml.Node is in hand: a
// partially-written section needs FIELD-level defaulting, and only the node
// knows which keys the user actually wrote. See derive_fill.go for why a
// zero-value test cannot substitute.
func ApplyDerivedDefaults(c *ProjectConfig) {
	ApplyDerivedDefaultsFromNode(c, nil)
}

// ApplyDerivedDefaultsFromNode is ApplyDerivedDefaults with the parsed
// forge.yaml mapping in hand, so an absent FIELD inside a present section
// still gets its derived default.
//
// A nil root reproduces the historical whole-section behaviour: a section
// that is entirely zero takes the default, and a section with ANY value is
// left exactly as it is. With no node there is no telling which of its keys
// the user wrote, and a non-zero section is either theirs or already
// defaulted — so re-applying to a config that already went through the
// loader is a no-op on every section. It used to overwrite every section
// wholesale: the generate pipeline's frontend-inventory step re-derives a
// LOADED config, and that wiped database.orm.package (the ORM then
// projected into internal/db and the sweep deleted internal/store), the
// ci.vuln_scan exemptions, and every other section the user had written.
func ApplyDerivedDefaultsFromNode(c *ProjectConfig, root *yaml.Node) {
	d := sectionDefaults(c)
	if root == nil {
		fillZeroSection(&c.Database, d.Database)
		fillZeroSection(&c.CI, d.CI)
		fillZeroSection(&c.Deploy, d.Deploy)
		fillZeroSection(&c.K8s, d.K8s)
		fillZeroSection(&c.Lint, d.Lint)
	} else {
		present := presentKeys(root)
		fillSectionDefaults(&c.Database, d.Database, "database", present)
		fillSectionDefaults(&c.CI, d.CI, "ci", present)
		fillSectionDefaults(&c.Deploy, d.Deploy, "deploy", present)
		fillSectionDefaults(&c.K8s, d.K8s, "k8s", present)
		fillSectionDefaults(&c.Lint, d.Lint, "lint", present)
	}
	// Tree-derived state runs AFTER the section fill, and in dependency
	// order: the database (feature derivation reads it), the workspaces
	// layout, then the features that read both.
	DeriveDatabase(c)
	DeriveFrontendWorkspaces(c)
	c.Features.derived = followPinnedRequirements(DeriveFeatureDefaults(c), c.Features)
}

// FollowingRequirements returns f with the feature graph applied to its
// current state exactly as the loader applies it to a derived one
// (followPinnedRequirements): every unpinned feature whose required feature
// is off is turned off too, transitively. `forge project new` calls it on the
// feature set it is about to write a tree for, so the scaffold and every
// later load of that tree agree — one rule, in one place. Before, the
// scaffold kept a feature on that the loader then derived off (`--disable
// codegen` still wrote db/migrations and a database-backed dev env), or
// carried a hand-copied cascade of the edges.
func (f FeaturesConfig) FollowingRequirements() FeaturesConfig {
	current := make(map[FeatureName]bool, len(allFeatureNames))
	for _, name := range allFeatureNames {
		current[name] = f.resolve(name)
	}
	f.derived = followPinnedRequirements(current, f)
	return f
}

// followPinnedRequirements applies forge.yaml's `features:` pins to the
// derived set and turns OFF every derived (unpinned) feature whose
// requirement a pin turned off: `deploy: false` takes a derived ingress with
// it, `codegen: false` the derived orm, migrations and frontend. Without
// this a pin would load as a feature-graph error against values nobody
// wrote. A PINNED dependent is left alone — pinning a feature on while its
// requirement is off is a contradiction the user wrote, and the graph
// validator reports it naming both sides.
func followPinnedRequirements(derived map[FeatureName]bool, f FeaturesConfig) map[FeatureName]bool {
	out := make(map[FeatureName]bool, len(derived))
	for name, on := range derived {
		out[name] = on
	}
	for _, name := range allFeatureNames {
		if on, ok := f.pinned(name); ok {
			out[name] = on
		}
	}
	// A chain (codegen → orm, deploy → ingress) settles in a few rounds;
	// the bound keeps a future cyclic edge from spinning.
	for round := 0; round <= len(featureDeps); round++ {
		changed := false
		for name, reqs := range featureDeps {
			if !out[name] || f.Pinned(name) {
				continue
			}
			for _, req := range reqs {
				if req.Feature != "" && !out[req.Feature] {
					out[name] = false
					changed = true
					break
				}
			}
		}
		if !changed {
			break
		}
	}
	return out
}

// NormalizeForWrite returns a copy of c with every derivable value that
// matches its shape-derived default removed, so marshalling produces the
// minimal forge.yaml. Explicit values that DIFFER from derivation are
// preserved — overrides survive round-trips; boilerplate does not.
//
// Dropping a value that equals its derived default is behavior-preserving
// by construction: the loader re-derives the identical value on the next
// read. The original c is not mutated.
func NormalizeForWrite(c *ProjectConfig) *ProjectConfig {
	out := *c
	d := sectionDefaults(c)
	// Field-level, mirroring the field-level fill: a section that matches
	// its default in every field collapses to the zero value and vanishes
	// from forge.yaml, while a section with one real override keeps that
	// override alone instead of dragging the whole default block along.
	stripSectionDefaults(&out.Database, d.Database)
	stripSectionDefaults(&out.CI, d.CI)
	stripSectionDefaults(&out.Deploy, d.Deploy)
	stripSectionDefaults(&out.K8s, d.K8s)
	stripSectionDefaults(&out.Lint, d.Lint)

	return &out
}
