package cli

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/staticexport"
	"github.com/reliant-labs/forge/internal/linter/suppress"
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
	// dryRun prints what the site would write, and writes nothing.
	dryRun bool
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
		fe.DevPort = viteDefaultDevPort
		if _, data, ok := readViteConfig(dir); ok {
			if m := viteOutDirPattern.FindSubmatch(data); m != nil {
				fe.PublicDir = string(m[1])
			}
			fe.DevPort = viteFixedDevPort(data)
		}
	}
	return fe, true
}

// viteConfigNames are the files Vite reads its config from, in its order.
var viteConfigNames = []string{"vite.config.ts", "vite.config.js", "vite.config.mjs", "vite.config.mts"}

// readViteConfig returns the first vite config in dir, by name and content.
func readViteConfig(dir string) (string, []byte, bool) {
	for _, name := range viteConfigNames {
		if data, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			return name, data, true
		}
	}
	return "", nil, false
}

// viteDefaultDevPort is the port `vite` serves on when its config names none.
const viteDefaultDevPort = 5173

var (
	// vitePortEnvPattern: a config that mentions PORT reads the port
	// `forge env up` passes (the site starter's vite.config does).
	vitePortEnvPattern = regexp.MustCompile(`\bPORT\b`)
	// viteServerBlockPattern opens the config's `server: {` block.
	viteServerBlockPattern = regexp.MustCompile(`\bserver\s*:\s*\{`)
	// viteLiteralPortPattern is a literal `port: <n>`.
	viteLiteralPortPattern = regexp.MustCompile(`\bport\s*:\s*(\d{2,5})\b`)
)

// viteFixedDevPort is the port an adopted Vite app's dev server listens on
// when its config does not read PORT: a stock `vite` ignores the PORT
// `forge env up` sets, so declaring the resolved port would print a URL
// nothing serves. It is the literal `server.port`, else Vite's default.
// Zero means the config reads PORT, so the resolved per-checkout port works.
// A computed port is not followed; the dev env's `port` line is the one
// place to correct it.
func viteFixedDevPort(config []byte) int {
	if vitePortEnvPattern.Match(config) {
		return 0
	}
	loc := viteServerBlockPattern.FindIndex(config)
	if loc == nil {
		return viteDefaultDevPort
	}
	// The server block's body: up to its matching brace.
	depth, end := 1, len(config)
	for i := loc[1]; i < len(config); i++ {
		switch config[i] {
		case '{':
			depth++
		case '}':
			depth--
		}
		if depth == 0 {
			end = i
			break
		}
	}
	if m := viteLiteralPortPattern.FindSubmatch(config[loc[1]:end]); m != nil {
		var p int
		if _, err := fmt.Sscanf(string(m[1]), "%d", &p); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return viteDefaultDevPort
}

// agentInstructionFiles are the files a coding agent's harness writes into a
// directory before any code exists (Claude Code's /init, Codex, Gemini CLI,
// Reliant). A folder holding only these is still empty: an agent told to
// start a site there should not be refused by its own memory file, and the
// merge keeps the user's copy rather than overwriting it.
var agentInstructionFiles = map[string]bool{"AGENTS.MD": true, "CLAUDE.MD": true, "GEMINI.MD": true, "RELIANT.MD": true}

// emptyForSite reports whether dir holds nothing a greenfield site could
// collide with: only hidden entries (.git, .gitignore, an editor's
// dotfiles), README*, LICENSE* and an agent's instruction file
// (agentInstructionFiles). Anything else — a plain-HTML site, a
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
		if strings.HasPrefix(name, ".") || strings.HasPrefix(upper, "README") || strings.HasPrefix(upper, "LICENSE") || agentInstructionFiles[upper] {
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
	if in.dryRun {
		fmt.Printf("Dry run: adopting %s as static site %q would write the files below. Nothing was written.\n\n", in.targetPath, in.projectName)
		fmt.Printf("frontend %q: the %s app at the root (path \".\", builds into %s/)\n", fe.Name, fe.Type, fe.PublicDir)
		printAdoptedSiteNotes(in.targetPath, fe)
		if err := previewScaffoldMerge(os.Stdout, staging, in.targetPath, in.force, nil); err != nil {
			return err
		}
		printSiteDryRunNextSteps()
		return nil
	}
	merged, err := mergeScaffoldInto(staging, in.targetPath, in.force)
	if err != nil {
		return err
	}

	fmt.Printf("✅ Static site '%s' adopted at %s\n", in.projectName, in.targetPath)
	fmt.Printf("   frontend %q: the %s app at the root (path \".\", builds into %s/)\n", fe.Name, fe.Type, fe.PublicDir)
	fmt.Println("   wrote forge.yaml, deploy/kcl/kcl.mod (+ its empty kcl.mod.lock), deploy/kcl/{dev,staging,prod}/main.k, a gitleaks .pre-commit-config.yaml — no Go, no server")
	fmt.Println("   Existing app: session recording remains off. To opt in, initialize browser telemetry and enable _session_replay in each env (forge skill load frontend-runtime/telemetry).")
	for _, line := range inPlaceMergeSummary(merged) {
		fmt.Println(line)
	}
	printAdoptedSiteNotes(in.targetPath, fe)
	printSiteNextSteps("", fe.Type)
	return nil
}

// printAdoptedSiteNotes prints what an adopted app still needs before it
// runs and ships as declared. adoptSite never edits the app's files, so
// each is the edit to make, not one made: the dev port of a Vite app that
// ignores PORT, the browser-console mirror, and every static-export finding
// (`forge lint --static-export`) that would fail the first hosted build.
func printAdoptedSiteNotes(dir string, fe generator.SiteFrontend) {
	cli := Name()
	var notes []string
	if fe.DevPort > 0 {
		cfg := "vite.config"
		if name, _, ok := readViteConfig(dir); ok {
			cfg = name
		}
		notes = append(notes, fmt.Sprintf("dev port: %s does not read PORT, so deploy/kcl/dev/main.k declares the port Vite listens on, %d (its server.port, or Vite's default). "+
			"To let `%s env up dev` pick a free port per checkout, read process.env.PORT in %s (the comment above that `port` line shows how)", cfg, fe.DevPort, cli, cfg))
	}
	devlog := "browser console in .forge/logs/dev/: call `installDevLogging({ dev: true })` from `@reliantlabs/forge-web-runtime/devlog` in the browser (that subpath needs no other package)"
	if fe.Type == "vite" {
		devlog += ", and add the `forgeDevLog()` plugin from `@reliantlabs/forge-web-runtime/vite` to vite.config, its dev-server receiver"
	} else {
		devlog += "; its dev-server receiver is a route (`" + cli + " skill load debug`)"
	}
	notes = append(notes, devlog)
	findings := adoptedSiteExportFindings(dir, fe)
	fmt.Println()
	fmt.Println("Before it runs and ships as declared (none of your files were changed):")
	for _, n := range notes {
		fmt.Println("  - " + n)
	}
	if len(findings) == 0 {
		return
	}
	fmt.Printf("  - static export: hosted static hosting publishes only a static export, and `%s lint --static-export` reports %d finding(s) here:\n", cli, len(findings))
	for _, f := range findings {
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		fmt.Printf("      %s %s [%s] %s\n", f.Severity, loc, f.Rule, f.Message)
		if f.Remediation != "" {
			fmt.Printf("        fix: %s\n", f.Remediation)
		}
	}
}

// adoptedSiteExportFindings runs the static-export lane over an adopted
// Next.js app as its staging and prod envs bind it (forge.OnHosted), so the
// adoption names what `next build` would refuse before anyone builds. A
// Vite app's build is static by construction, and its outDir was just read
// into public_dir, so there is nothing to judge.
func adoptedSiteExportFindings(dir string, fe generator.SiteFrontend) []finding.Finding {
	if fe.Type != "nextjs" {
		return nil
	}
	var bindings []staticexport.Binding
	for _, env := range []string{"staging", "prod"} {
		bindings = append(bindings, staticexport.Binding{Env: env, Runtime: "hosted", PublicDir: fe.PublicDir})
	}
	all, err := staticexport.Check(staticexport.Frontend{Name: fe.Name, Dir: dir, RelDir: ".", Bindings: bindings})
	if err != nil {
		return nil
	}
	sup := suppress.ApplyFiles(dir, all, os.ReadFile)
	return append(sup.Kept, sup.Violations...)
}

// printSiteDryRunNextSteps closes a --dry-run.
func printSiteDryRunNextSteps() {
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  the same command without --dry-run")
	fmt.Println("      ↳ write these files")
	fmt.Printf("  %s skill load deploy/static-site\n", Name())
	fmt.Println("      ↳ the whole flow: envs, releases, hosting, domains")
}

// previewScaffoldMerge prints what mergeScaffoldInto(staging, target,
// overwrite) would do, and writes nothing: each file it would write with its
// content, each existing file it would keep or (overwrite) replace, and the
// lines it would append to an existing .gitignore. Files in pathsOnly are
// listed by path alone (a greenfield starter's app files).
func previewScaffoldMerge(w io.Writer, staging, target string, overwrite bool, pathsOnly map[string]bool) error {
	var listed []string
	err := filepath.WalkDir(staging, func(src string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(staging, src)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		content, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		existing, readErr := os.ReadFile(filepath.Join(target, rel))
		exists := readErr == nil
		if !exists {
			if info, statErr := os.Stat(filepath.Join(target, rel)); statErr == nil && info.IsDir() {
				fmt.Fprintf(w, "\n----- %s (a directory there: kept, nothing written) -----\n", relSlash)
				return nil
			}
		}
		switch {
		case relSlash == ".gitignore":
			forgeBody, _ := dropTrackedIgnores(string(content), gitTrackedIgnoreMatcher(target))
			if !exists {
				fmt.Fprintf(w, "\n----- .gitignore -----\n%s", forgeBody)
				return nil
			}
			merged, added := mergeGitignore(string(existing), forgeBody)
			rest, _ := openForgeDirIgnore(stripForgeGitignoreBlock(string(existing)))
			if added == 0 || !strings.HasPrefix(merged, rest) {
				fmt.Fprintf(w, "\n----- .gitignore (yours already has every forge entry: unchanged) -----\n")
				return nil
			}
			fmt.Fprintf(w, "\n----- .gitignore (yours, with %d forge entr%s appended) -----\n%s", added, plural(added, "y", "ies"), strings.TrimLeft(strings.TrimPrefix(merged, rest), "\n"))
		case exists && !overwrite:
			fmt.Fprintf(w, "\n----- %s (exists: kept, forge's version is not written; --force replaces it) -----\n", relSlash)
		case pathsOnly[relSlash] && !exists:
			listed = append(listed, relSlash)
		case exists:
			fmt.Fprintf(w, "\n----- %s (exists: REPLACED, --force) -----\n%s", relSlash, content)
		default:
			fmt.Fprintf(w, "\n----- %s -----\n%s", relSlash, content)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(listed) > 0 {
		fmt.Fprintf(w, "\n----- the starter app (%d files) -----\n", len(listed))
		for _, p := range listed {
			fmt.Fprintln(w, "  "+p)
		}
	}
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
	starterFiles := map[string]bool{}
	if in.dryRun {
		if err := filepath.WalkDir(staging, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, err := filepath.Rel(staging, p)
			starterFiles[filepath.ToSlash(rel)] = true
			return err
		}); err != nil {
			return err
		}
	}
	if err := generator.WriteSiteScaffold(staging, in.projectName, h, fe); err != nil {
		return err
	}
	if in.dryRun {
		fmt.Printf("Dry run: a new static site %q at %s would get the files below. Nothing was written.\n\n", in.projectName, in.targetPath)
		fmt.Printf("frontend %q: a %s starter at the root (path \".\", builds into %s/)\n", fe.Name, siteStarterLabel(kind), fe.PublicDir)
		fmt.Println("then: npm install (writes package-lock.json), and git init when it is not in a repository")
		if err := previewScaffoldMerge(os.Stdout, staging, in.targetPath, in.force, starterFiles); err != nil {
			return err
		}
		printSiteDryRunNextSteps()
		return nil
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
	steps := cliutil.NewStepRunner(Name()+" project new", in.verbose)
	defer steps.Close() //nolint:errcheck // the log is best-effort
	if err := steps.MoveLog(filepath.Join(in.targetPath, newProjectLog)); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not keep the step log in the project: %v\n", err)
	}
	if os.Getenv("FORGE_SKIP_NPM_INSTALL") == "" {
		err := steps.Run(cliutil.Step{
			Title:   "site dependencies",
			Header:  "🔧 Installing the site's dependencies (this generates package-lock.json)...",
			Summary: installSummary,
			Run:     func() error { return npmInstallIn(ctx, in.targetPath, "the site") },
		})
		if err != nil {
			fmt.Printf("Warning: %v\n    Run 'npm install' in %s before 'forge env up dev'.\n", err, in.targetPath)
		}
	}

	if _, inRepo := gitWorkTreeRoot(ctx, in.targetPath); !inRepo {
		initGitStep(ctx, steps, in.targetPath, false, in.verbose)
	}
	printStepLog(steps)

	if err := os.Remove(scaffold.markerPath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "warning: failed to remove scaffold marker: %v\n", err)
	}
	scaffold.finish()

	cd := ""
	if !in.inPlace {
		cd = filepath.Base(in.targetPath)
	}
	printSiteNextSteps(cd, fe.Type)
	return nil
}

func siteStarterLabel(kind string) string {
	if kind == "vite-spa" {
		return "Vite SPA"
	}
	return "Next.js static export"
}

// printSiteNextSteps prints siteNextSteps.
func printSiteNextSteps(cd, feType string) {
	fmt.Println()
	for _, line := range siteNextSteps(cd, feType) {
		fmt.Println(line)
	}
}

// siteNextSteps is what to do with a site `project new` just made or
// adopted: the skill that covers the whole flow, run it, ship it. cd, when
// set, is the directory to change into first. feType is the frontend's KCL
// type: `lint --static-export` checks only a Next.js export, so a Vite site's
// export check is its own `npm run build`. Every command in it resolves in the
// command tree (TestSiteNextStepsArePasteable).
func siteNextSteps(cd, feType string) []string {
	cli := Name()
	lines := []string{"Next steps:"}
	if cd != "" {
		lines = append(lines, "  cd "+cd)
	}
	exportCheck := [2]string{cli + " lint --static-export", "what a static export cannot serve, with file:line"}
	if feType == "vite" {
		exportCheck = [2]string{"npm run build", "the export's only check: a Vite build is already static (lint --static-export checks Next.js)"}
	}
	steps := [][2]string{
		{cli + " skill load deploy/static-site", "the whole flow: envs, releases, hosting, domains"},
		{cli + " env up dev", "the dev server (browser console mirrored to .forge/logs/dev/)"},
		exportCheck,
		{cli + " env build prod --release v0.1.0", "build once, push, record the release"},
		{cli + " env deploy prod v0.1.0", "publish to hosted static hosting (one env's site per person is free)"},
		{cli + " domain add <host> --env prod", "your own hostname"},
	}
	for _, s := range steps {
		lines = append(lines, "  "+s[0], "      ↳ "+s[1])
	}
	return append(lines,
		"  build-time config (VITE_* / NEXT_PUBLIC_*): `env_vars` on the frontend in each env's main.k",
		fmt.Sprintf("  the starter page is yours to replace: get a brief, then `%s skill load frontend/design`", cli))
}
