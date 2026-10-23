package generator

import (
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/templates"
)

// SiteFrontend is the frontend a static site is made of: the app at the
// project root (`forge project new --in-place --kind site`).
type SiteFrontend struct {
	// Name is the forge.Frontend name every env declares it under.
	Name string
	// Type is the KCL forge.Frontend.type: "vite" or "nextjs".
	Type string
	// PublicDir is where its build lands, relative to the root ("dist" for
	// Vite, "out" for a Next.js static export).
	PublicDir string
}

// WriteSiteScaffold writes a static site's forge files into dir: forge.yaml,
// deploy/kcl/kcl.mod and the dev / staging / prod envs, each declaring the
// root frontend (`path = "."`) and binding it — to its dev server in dev, to
// hosted static hosting (forge.OnHosted) in staging and prod.
//
// That is the whole project. A site has no Go module, so there is no
// cmd/, go.mod, proto/, Dockerfile or server to scaffold; every forge
// command reads the absent go.mod (config.ProjectConfig.HasGoModule) and
// skips its Go steps. The frontend's own files are never touched: the app
// was there first and stays exactly as it was.
//
// dir is normally a staging directory the caller merges into the project
// (the in-place rule that an existing file is kept unless --force).
func WriteSiteScaffold(dir, projectName string, harness Harness, fe SiteFrontend) error {
	cfg := config.ProjectConfig{
		Name:         projectName,
		ForgeVersion: buildinfo.Version(),
		Harness:      string(harness.Normalized()),
	}
	data, err := yaml.Marshal(config.NormalizeForWrite(&cfg))
	if err != nil {
		return fmt.Errorf("marshal project config: %w", err)
	}
	header := []byte("# Forge project manifest — see https://github.com/reliant-labs/forge.\n" +
		"# A static site: the frontend at this directory's root and its deploy envs\n" +
		"# (deploy/kcl/<env>/main.k). There is no Go module, so there is no\n" +
		"# module_path (it is required again once the project has a go.mod).\n" +
		"# Everything else is derived from the tree.\n\n")
	if err := writeSiteFile(dir, "forge.yaml", append(header, data...)); err != nil {
		return err
	}

	// The machine-local state a site's forge commands write (the subset of
	// the service scaffold's .gitignore a site can produce). An in-place
	// adoption merges it into the app's own .gitignore.
	if err := writeSiteFile(dir, ".gitignore", []byte(siteGitignore)); err != nil {
		return err
	}

	kclMod, err := templates.DeployTemplates().Render("kcl/kcl.mod.tmpl", struct{ ProjectName string }{projectName})
	if err != nil {
		return fmt.Errorf("render kcl.mod template: %w", err)
	}
	if err := writeSiteFile(dir, filepath.Join("deploy", "kcl", "kcl.mod"), kclMod); err != nil {
		return err
	}

	envs := append([]string{codegen.DevEnvName}, hostedEnvNames()...)
	for _, env := range envs {
		content, err := templates.DeployTemplates().Render("kcl/frontend-only/main.k.tmpl", templates.EnvTemplateData{
			ProjectName:       projectName,
			EnvName:           env,
			HasFrontend:       true,
			DatabaseDisabled:  true,
			FrontendName:      fe.Name,
			FrontendIdent:     naming.KCLIdentifier(fe.Name),
			FrontendRoutes:    []string{config.RoutesNone},
			FrontendPath:      ".",
			FrontendType:      fe.Type,
			FrontendPublicDir: fe.PublicDir,
		})
		if err != nil {
			return fmt.Errorf("render %s env: %w", env, err)
		}
		if err := writeSiteFile(dir, filepath.Join("deploy", "kcl", env, "main.k"), content); err != nil {
			return err
		}
	}
	return nil
}

// siteGitignore keeps forge's per-developer state out of a site's commits
// while keeping the ledger and ownership records that are shared: the same
// `.forge/*` + re-include shape the service scaffold uses (a `.forge/`
// directory pattern would make the re-includes impossible).
const siteGitignore = `# forge: machine-local state (never committed)
.forge-kcl/
secrets/
.forge/*
!.forge/disowned.json
!.forge/hashes.json
!.forge/scaffolded.json
!.forge/releases/
!.forge/releases/*.json
!.forge/promotions/
!.forge/promotions/*.jsonl
`

func writeSiteFile(dir, rel string, content []byte) error {
	dest := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(dest, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	return nil
}
