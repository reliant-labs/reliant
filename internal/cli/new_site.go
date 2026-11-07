package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

// `forge project new --kind site` — a static site (a landing page, a
// marketing or docs site, a SPA) as a forge project.
//
// A site is its frontend and its deploy envs, and nothing else: forge.yaml,
// deploy/kcl/kcl.mod and the dev / staging / prod envs declaring the app at
// the project root (`path = "."`), bound to its dev server in dev and to
// hosted static hosting in staging and prod. No go.mod, cmd/, proto/,
// Dockerfile or server. There are two ways in, and they converge on that one
// shape:
//
//   - ADOPT (`--in-place` inside an existing Vite or Next.js app): the app
//     was there first and is never touched; only the forge files are added.
//   - GREENFIELD (`forge project new <name> --kind site`, or `--in-place` in
//     an empty directory): forge writes a starter app at the root first — a
//     Next.js static export (the default) or a Vite SPA, picked with
//     --frontend-kind — and then exactly what an adoption writes. So a new
//     site IS an adopted one, and every later command sees one shape.
//
// The service scaffold is the wrong shape for either. It writes a Go server
// beside an app that has no backend, and its frontend is wired to that
// backend (typed-config proto, Connect transport, hooks, mocks), so in a
// project with no Go module it drags proto/ and gen/ in after it.

// projectKindSite is the `--kind site` value. It is a SCAFFOLD choice only:
// like every kind, the project's kind is derived from its tree afterwards,
// and a site's tree (deploy/kcl, no go.mod) needs no kind of its own —
// config.ProjectConfig.HasGoModule is what every command consults.
const projectKindSite = "site"

// defaultSiteFrontendKind is the starter a greenfield site gets with no
// --frontend-kind: a Next.js static export, which writes one HTML file per
// route — what a landing page or marketing site wants from search engines
// and link previews. vite-spa is the one-index.html app shape.
const defaultSiteFrontendKind = "nextjs"

// viteOutDirPattern reads a literal `outDir: '<dir>'` from a vite config. A
// computed outDir is not followed; the KCL `public_dir` is the one place to
// correct it.
var viteOutDirPattern = regexp.MustCompile(`outDir\s*:\s*['"]([^'"]+)['"]`)

// greenfieldSiteHint is the command that makes a new site, quoted by every
// refusal that sends someone there.
const greenfieldSiteHint = "forge project new <name> --kind site [--frontend-kind nextjs|vite-spa]"

// newSiteInput carries what runNewSite needs from the `project new` flags.
type newSiteInput struct {
	// targetPath is the project directory (resolveNewTargetPath): a new one
	// for a greenfield site, the current one with --in-place.
	targetPath  string
	projectName string
	inPlace     bool
	force       bool
	harness     string
	// frontendName names the frontend in every env's KCL ("" = web).
	frontendName string
	// frontendKind picks a greenfield starter: nextjs or vite-spa ("" =
	// defaultSiteFrontendKind). Meaningless when adopting an existing app.
	frontendKind string
	verbose      bool
}

// siteFrontendAt reports the static frontend at the root of dir, and whether
// there is one.
func siteFrontendAt(dir string) (generator.SiteFrontend, bool) {
	apps := detectExistingFrontendAppsIn(dir, []string{"."})
	if len(apps) == 0 {
		return generator.SiteFrontend{}, false
	}
	fe := generator.SiteFrontend{Name: config.RootFrontendName}
	switch apps[0].typ {
	case "nextjs":
		fe.Type, fe.PublicDir = "nextjs", "out"
	default:
		fe.Type, fe.PublicDir = "vite", "dist"
		for _, name := range []string{"vite.config.ts", "vite.config.js", "vite.config.mjs", "vite.config.mts"} {
			if data, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
				if m := viteOutDirPattern.FindSubmatch(data); m != nil {
					fe.PublicDir = string(m[1])
				}
				break
			}
		}
	}
	return fe, true
}

// emptyForSite reports whether dir holds nothing a greenfield site could
// collide with: only hidden entries (.git, .gitignore, an editor's
// dotfiles), README* and LICENSE*. Anything else — a plain-HTML site, a
// package.json for a framework forge cannot adopt — is something the user
// should decide about, not something to scaffold over.
func emptyForSite(dir string) (bool, []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, nil
	}
	var blocking []string
	for _, e := range entries {
		name := e.Name()
		upper := strings.ToUpper(name)
		if strings.HasPrefix(name, ".") || strings.HasPrefix(upper, "README") || strings.HasPrefix(upper, "LICENSE") {
			continue
		}
		blocking = append(blocking, name)
	}
	return len(blocking) == 0, blocking
}

// validateSiteFlags checks the site-only flags before anything is written.
func validateSiteFlags(frontendName, frontendKind string) error {
	if frontendName != "" {
		if err := cmdutil.ValidateFrontendName(frontendName); err != nil {
			return cliutil.WrapUserErr("forge project new --kind site", fmt.Sprintf("invalid --frontend %q", frontendName), "",
				"use a name starting with a letter, containing letters/digits/_/-", err)
		}
	}
	if frontendKind != "" {
		if _, ok := generator.SiteStarterFrontend(frontendKind, ""); !ok {
			return cliutil.UserErr("forge project new --kind site",
				fmt.Sprintf("invalid --frontend-kind %q: valid values are %s", frontendKind, strings.Join(generator.SiteStarterKindNames(), ", ")),
				"",
				"pass --frontend-kind nextjs (a static export, one HTML file per route — the default) or --frontend-kind vite-spa (a single-page app)")
		}
	}
	return nil
}

func runNewSite(ctx context.Context, in newSiteInput) error {
	if err := validateSiteFlags(in.frontendName, in.frontendKind); err != nil {
		return err
	}
	if in.inPlace {
		if _, err := os.Stat(filepath.Join(in.targetPath, "go.mod")); err == nil {
			return cliutil.UserErr("forge project new --kind site",
				fmt.Sprintf("%s is a Go module (go.mod); a site has no Go code", in.targetPath),
				"",
				"for an app with a Go backend use --kind service and declare the frontend in deploy/kcl/<env>/main.k")
		}
		if fe, ok := siteFrontendAt(in.targetPath); ok {
			if in.frontendKind != "" {
				return cliutil.UserErr("forge project new --kind site",
					fmt.Sprintf("--frontend-kind picks the starter for a NEW site, and %s already has a %s app at its root", in.targetPath, fe.Type),
					"",
					"drop --frontend-kind to adopt the app that is there")
			}
			if in.frontendName != "" {
				fe.Name = in.frontendName
			}
			return adoptSite(in, fe)
		}
		if empty, blocking := emptyForSite(in.targetPath); !empty {
			return cliutil.UserErr("forge project new --kind site",
				fmt.Sprintf("%s has no frontend at its root (a package.json that depends on vite or next), and is not empty (%s)",
					in.targetPath, strings.Join(blocking, ", ")),
				"",
				"run it inside an existing Vite or Next.js app to adopt it, or in an empty directory for a new site: "+greenfieldSiteHint)
		}
	}
	return greenfieldSite(ctx, in)
}

// adoptSite adds the forge files to the app already at in.targetPath.
func adoptSite(in newSiteInput, fe generator.SiteFrontend) error {
	h, err := generator.ParseHarness(in.harness)
	if err != nil {
		return err
	}
	staging, err := os.MkdirTemp("", "forge-site-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging) //nolint:errcheck // best-effort cleanup of a temp dir
	if err := generator.WriteSiteScaffold(staging, in.projectName, h, fe); err != nil {
		return err
	}
	merged, err := mergeScaffoldInto(staging, in.targetPath, in.force)
	if err != nil {
		return err
	}

	fmt.Printf("✅ Static site '%s' adopted at %s\n", in.projectName, in.targetPath)
	fmt.Printf("   frontend %q: the %s app at the root (path \".\", builds into %s/)\n", fe.Name, fe.Type, fe.PublicDir)
	fmt.Println("   wrote forge.yaml, deploy/kcl/kcl.mod, deploy/kcl/{dev,staging,prod}/main.k — no Go, no server")
	for _, line := range inPlaceMergeSummary(merged) {
		fmt.Println(line)
	}
	printSiteNextSteps("")
	return nil
}

// greenfieldSite writes a starter app at in.targetPath's root and adopts it.
func greenfieldSite(ctx context.Context, in newSiteInput) error {
	h, err := generator.ParseHarness(in.harness)
	if err != nil {
		return err
	}
	kind := in.frontendKind
	if kind == "" {
		kind = defaultSiteFrontendKind
	}
	name := in.frontendName
	if name == "" {
		name = config.RootFrontendName
	}
	fe, _ := generator.SiteStarterFrontend(kind, name)

	staging, err := os.MkdirTemp("", "forge-site-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging) //nolint:errcheck // best-effort cleanup of a temp dir
	if err := generator.WriteSiteStarter(staging, in.projectName, fe); err != nil {
		return err
	}
	if err := generator.WriteSiteScaffold(staging, in.projectName, h, fe); err != nil {
		return err
	}

	scaffold, err := beginScaffold(in.targetPath, in.inPlace)
	if err != nil {
		return err
	}
	defer scaffold.undoUnlessFinished()
	merged, err := mergeScaffoldInto(staging, in.targetPath, in.force)
	if err != nil {
		return err
	}

	fmt.Printf("✅ Static site '%s' created at %s\n", in.projectName, in.targetPath)
	fmt.Printf("   frontend %q: a %s starter at the root (path \".\", builds into %s/)\n", fe.Name, siteStarterLabel(kind), fe.PublicDir)
	fmt.Println("   wrote the app, forge.yaml, deploy/kcl/kcl.mod, deploy/kcl/{dev,staging,prod}/main.k — no Go, no server")
	for _, line := range inPlaceMergeSummary(merged) {
		fmt.Println(line)
	}

	// npm install, so `forge env up dev` and `npm run build` work as
	// handed over and package-lock.json is in the first commit. Best-effort,
	// like every frontend install `project new` runs; FORGE_SKIP_NPM_INSTALL
	// is the same testing seam `forge scaffold frontend` honours.
	if os.Getenv("FORGE_SKIP_NPM_INSTALL") == "" {
		fmt.Println("🔧 Installing the site's dependencies (this generates package-lock.json)...")
		if err := npmInstallIn(ctx, in.targetPath, "the site"); err != nil {
			fmt.Printf("Warning: %v\n    Run 'npm install' in %s before 'forge env up dev'.\n", err, in.targetPath)
		}
	}

	if _, inRepo := gitWorkTreeRoot(ctx, in.targetPath); !inRepo {
		fmt.Println("🔧 Initializing git repository...")
		if err := initGitRepositoryWith(ctx, in.targetPath, false); err != nil {
			reportGitInitError(os.Stdout, err, in.verbose)
		}
	}

	if err := os.Remove(scaffold.markerPath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "warning: failed to remove scaffold marker: %v\n", err)
	}
	scaffold.finish()

	cd := ""
	if !in.inPlace {
		cd = filepath.Base(in.targetPath)
	}
	printSiteNextSteps(cd)
	return nil
}

func siteStarterLabel(kind string) string {
	if kind == "vite-spa" {
		return "Vite SPA"
	}
	return "Next.js static export"
}

// printSiteNextSteps prints siteNextSteps.
func printSiteNextSteps(cd string) {
	fmt.Println()
	for _, line := range siteNextSteps(cd) {
		fmt.Println(line)
	}
}

// siteNextSteps is what to do with a site `project new` just made or
// adopted: the skill that covers the whole flow, run it, ship it. cd, when
// set, is the directory to change into first. Every command in it resolves
// in the command tree (TestSiteNextStepsArePasteable).
func siteNextSteps(cd string) []string {
	cli := Name()
	lines := []string{"Next steps:"}
	if cd != "" {
		lines = append(lines, "  cd "+cd)
	}
	steps := [][2]string{
		{cli + " skill load deploy/static-site", "the whole flow: envs, releases, hosting, domains"},
		{cli + " env up dev", "the dev server (browser console mirrored to .forge/logs/dev/)"},
		{cli + " lint --static-export", "what a static export cannot serve, with file:line"},
		{cli + " env build prod --release v0.1.0", "build once, push, record the release"},
		{cli + " env deploy prod v0.1.0", "publish to hosted static hosting (one env's site per org is free)"},
		{cli + " domain add <host> --env prod", "your own hostname"},
	}
	for _, s := range steps {
		lines = append(lines, "  "+s[0], "      ↳ "+s[1])
	}
	return append(lines,
		"  build-time config (VITE_* / NEXT_PUBLIC_*): `env_vars` on the frontend in each env's main.k",
		fmt.Sprintf("  the starter page is yours to replace: get a brief, then `%s skill load frontend/design`", cli))
}
