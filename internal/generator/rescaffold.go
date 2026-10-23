package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/templates"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Re-emitting the files `forge project new` writes.
//
// A scaffold-once file is the user's from birth, and deleting one is an act
// of ownership the birth ledger (.forge/scaffolded.json) makes stick across
// clones. Deliberate re-scaffolding is `forge project rescaffold <path>`,
// which drops the birth record and has forge write the file again.
//
// "Write it again" needs a renderer for every file forge scaffolds, and most
// of them had none after birth: the whole DX set (.pre-commit-config.yaml,
// .github/workflows/pre-commit.yml, the devcontainer, scripts/bootstrap.sh,
// SECURITY.md, the ADRs) is written by `forge project new` alone, so neither
// `forge generate` nor `forge project upgrade` could produce one. houndersclub
// copied them out of a throwaway scaffold.
//
// The answer is not a second registry of those files. ScaffoldProjectInto
// runs the SAME generator `forge project new` runs, configured from the
// project's forge.yaml, into a staging directory — so the set of re-emittable
// files is by construction every file the scaffold writes, including ones
// added after this comment, and their bytes are the scaffold's bytes.

// ScaffoldProjectInto renders the `forge project new` scaffold of the project
// described by cfg (rooted at projectDir) into dst, which must be empty.
//
// Nothing under projectDir is written. dst is a staging tree: the caller
// copies out the paths it wants and discards the rest.
//
// The shape comes from forge.yaml and the tree, never from flags:
//
//   - kind, binary mode, harness and features from cfg (the loader has
//     already derived what forge.yaml leaves unset);
//   - the first frontend from cfg.Frontends;
//   - the first server component discovered in the tree, which is what the
//     scaffold would have been given as --service. Its e2e harness is what
//     makes e2e.yml part of the project (see CIWorkflows).
//
// Render-time facts that depend on the TREE (the deploy envs the CI mapper
// reads, the go directive) come from projectDir via the projectDir-aware
// helpers, so a staged file matches this project rather than a fresh one.
func ScaffoldProjectInto(projectDir, dst string, cfg *config.ProjectConfig) error {
	if cfg == nil {
		return fmt.Errorf("scaffold project: no project config")
	}
	g := NewProjectGenerator(cfg.Name, dst, cfg.ModulePath)
	g.Kind = cfg.EffectiveKind()
	g.Binary = cfg.EffectiveBinary()
	g.Features = cfg.Features
	g.FrontendWorkspaces = cfg.IsFrontendWorkspacesEnabled()
	if v := goVersionFromGoMod(projectDir); v != "" {
		g.GoVersionOverride = v
	}
	if h, err := ParseHarness(cfg.Harness); err == nil {
		g.Harness = h
	}
	// A declared frontend, whether or not frontend codegen is on: the
	// scaffold writes the app for a named frontend (ProjectGenerator.Generate).
	if len(cfg.Frontends) > 0 {
		g.FrontendName = cfg.Frontends[0].Name
	}
	g.ServiceName = scaffoldServiceName(projectDir, cfg)
	// Facts about the tree (does it have migrations, which binaries does
	// cmd/ hold, which components exist) are read from the project, not
	// from the empty staging dir.
	g.factsDir = projectDir
	// The staging dir becomes its own project the moment the scaffold
	// writes forge.yaml, so every birth record the scaffold makes lands in
	// dst's throwaway ledger — never in projectDir's.
	return g.Generate()
}

// scaffoldServiceName is the service `forge project new --service` would have
// been given for this project: its first server component.
//
// The component inventory reads the proto descriptor, which only `forge
// generate` writes — a project that has not generated since its scaffold (or
// whose gen/ is not committed) has none. The service protos themselves are the
// fallback: the scaffold writes proto/services/<svc>/v1/<svc>.proto, so the
// directory name IS the service it was given.
func scaffoldServiceName(projectDir string, cfg *config.ProjectConfig) string {
	if servers := codegen.DiscoverProjectComponents(projectDir, cfg.Name).OfKind(config.ComponentKindServer); len(servers) > 0 {
		return servers[0].Name
	}
	entries, err := os.ReadDir(filepath.Join(projectDir, "proto", "services"))
	if err != nil {
		return ""
	}
	for _, e := range entries { // ReadDir sorts: the first is stable
		if !e.IsDir() {
			continue
		}
		protos, _ := filepath.Glob(filepath.Join(projectDir, "proto", "services", e.Name(), "v1", "*.proto"))
		if len(protos) > 0 {
			return e.Name()
		}
	}
	return ""
}

// StagedScaffoldBirthHash returns the birth hash the staging render recorded
// for relPath, or "" when it recorded none. A few scaffolds (the frontend nav
// and dashboard) keep refreshing until the user edits them, and that rests on
// the ledger knowing the bytes forge wrote; a re-emitted copy must carry the
// same record or it would read as edited from the moment it lands.
func StagedScaffoldBirthHash(stagingRoot, relPath string) string {
	if !checksums.ScaffoldRecorded(stagingRoot, relPath) || !checksums.ScaffoldIsPristine(stagingRoot, relPath) {
		return ""
	}
	content, err := os.ReadFile(filepath.Join(stagingRoot, filepath.FromSlash(relPath)))
	if err != nil {
		return ""
	}
	return checksums.Hash(content)
}

// RenderUpgradeManagedFile renders relPath the way `forge project upgrade`
// would write it into this project today, reporting ok=false when relPath is
// not an upgrade-managed file here. tier is the file's ownership tier: a
// Tier-1 file is regenerated by `forge generate`, not re-scaffolded.
func RenderUpgradeManagedFile(projectDir string, cfg *config.ProjectConfig, relPath string) (content []byte, tier int, ok bool, err error) {
	rel := filepath.Clean(filepath.FromSlash(relPath))
	for _, f := range filterManagedFiles(managedFilesForCfg(cfg), cfg) {
		if filepath.Clean(f.destPath) != rel {
			continue
		}
		content, err = renderManagedFile(f, buildTemplateData(cfg, projectDir))
		return content, f.tier, true, err
	}
	return nil, 0, false, nil
}

// RenderAdvisoryFile renders a scaffold-once advisory row (a frontend's shared
// mechanism module, a .github starter) — the bytes `forge project upgrade
// --force <path>` adopts — reporting ok=false when relPath is not one.
func RenderAdvisoryFile(projectDir string, cfg *config.ProjectConfig, relPath string) (content []byte, ok bool, err error) {
	rows, err := AdvisoryFilesFor(projectDir, cfg)
	if err != nil {
		return nil, false, err
	}
	rel := filepath.Clean(filepath.FromSlash(relPath))
	for _, r := range rows {
		if filepath.Clean(r.Path) == rel {
			content, err = r.Render()
			return content, true, err
		}
	}
	return nil, false, nil
}

// IsForgeGenerated reports whether content carries forge's generated-file
// banner — a Tier-1 file `forge generate` re-renders, as opposed to a
// scaffold the user owns.
func IsForgeGenerated(content []byte) bool {
	head := content
	if len(head) > 1024 {
		head = head[:1024]
	}
	return strings.Contains(string(head), "Code generated by forge")
}

// CIWorkflowFileFor renders one of the project's CI files through the single
// CI mapper, reporting ok=false when this project has no such file.
//
// This is the authority rescaffold consults for .github/workflows/* and
// .github/dependabot.yml: CIWorkflows decides which workflows a project HAS
// (e2e.yml only with an e2e suite
// feature), and a staging render of `forge project new` must never smuggle in
// one it would not emit.
func CIWorkflowFileFor(projectDir string, cfg *config.ProjectConfig, in CIInputs, relPath string) (content []byte, ok bool, err error) {
	for _, f := range CIWorkflowsFor(projectDir, cfg, in) {
		if f.Dest != filepath.ToSlash(relPath) {
			continue
		}
		content, err = templates.CITemplates("github").Render(f.Template, f.Data)
		if err != nil {
			return nil, false, fmt.Errorf("render %s: %w", f.Dest, err)
		}
		return content, true, nil
	}
	return nil, false, nil
}

// IsCIMapperPath reports whether relPath is a file the CI mapper owns — any
// GitHub Actions workflow template forge ships, plus dependabot.yml — whether
// or not THIS project has it.
func IsCIMapperPath(relPath string) bool {
	rel := filepath.ToSlash(relPath)
	if rel == ".github/dependabot.yml" || rel == ForgeDeployActionPath {
		return true
	}
	if !strings.HasPrefix(rel, ".github/workflows/") {
		return false
	}
	names, err := templates.CITemplates("github").ListFlat("")
	if err != nil {
		return false
	}
	want := strings.TrimPrefix(rel, ".github/workflows/") + ".tmpl"
	for _, n := range names {
		// The composite action's template lives beside the workflows but
		// is not one: it lands at ForgeDeployActionPath, never under
		// .github/workflows/.
		if filepath.Base(n) == "forge-deploy-action.yml.tmpl" {
			continue
		}
		if filepath.Base(n) == want {
			return true
		}
	}
	return false
}

// CIWorkflowAbsenceReason explains why a project has no file at a CI-mapper
// path, in the terms of the switch that would give it one.
func CIWorkflowAbsenceReason(relPath string) string {
	switch filepath.ToSlash(relPath) {
	case ".github/workflows/e2e.yml":
		return "it runs the project's e2e suite, and this project has none: add an e2e/ suite " +
			"(scaffolded with the first service) or set ci.e2e.enabled: true in forge.yaml"
	case ".github/workflows/proto-breaking.yml":
		return "it checks service protos for breaking changes, which only a service project with codegen on has"
	case ".github/workflows/release.yml", ForgeDeployActionPath:
		return "it builds once and deploys that release through HOSTED environments, and this project declares none: " +
			"an env needs forge.ControlPlane in its KCL and a workload or database the platform runs (forge.OnHosted)"
	case ".github/workflows/build-images.yml":
		return "it builds and deploys service images, which only a service-kind project has"
	case ".github/workflows/deploy.yml":
		return "it deploys service images to the envs that are not hosted, and only a service-kind project has one; " +
			"when every env is hosted (forge.ControlPlane plus forge.OnHosted), .github/workflows/release.yml is the whole deploy path"
	}
	return "this project's shape does not include it"
}

// WriteRescaffoldedFile writes a re-emitted scaffold-once file and records its
// birth, preserving mode (scripts/bootstrap.sh is executable). birthHash is
// non-empty only for the scaffolds that refresh until touched (see
// StagedScaffoldBirthHash).
func WriteRescaffoldedFile(root, relPath string, content []byte, mode os.FileMode, birthHash string) error {
	full := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	checksums.RecordPreWrite(root, relPath)
	if mode == 0 {
		mode = 0o644
	}
	if err := atomicfile.WriteFile(full, content, mode.Perm()); err != nil {
		return err
	}
	// WriteFile does not change the mode of an existing file, and a
	// re-emitted one never exists — but chmod keeps the promise explicit.
	if err := os.Chmod(full, mode.Perm()); err != nil {
		return err
	}
	switch {
	case IsForgeGenerated(content):
		// Not a scaffold: no birth record (see rescaffoldPaths).
	case birthHash != "" && birthHash == checksums.Hash(content):
		checksums.RecordScaffoldWithHash(root, relPath, content)
	default:
		checksums.RecordScaffold(root, relPath)
	}
	checksums.MarkWrittenThisRun(filepath.ToSlash(relPath))
	return nil
}
