package deploytarget

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// FrontendConfigJSName is the filename of the runtime config document
// inside a deployed site. It MUST agree with
// codegen.FrontendConfigJSFile — the generator emits the <script src>
// that loads it, and this package writes the file that src resolves to.
// It is duplicated rather than imported because this package deliberately
// has no dependency on the codegen layer; the agreement is pinned by a
// test rather than by the type system.
const FrontendConfigJSName = "config.js"

// FirebaseProvider deploys a frontend's static build output to Firebase
// Hosting. It is the frontend analogue of ExternalProvider — but unlike
// External it is NOT a generic shell escape hatch: the contract is
// "build a static export, assemble it (plus any sibling static dirs)
// into one tree, and ship that tree to a Firebase Hosting site." Firebase
// Hosting is a common-enough target (per-env preview/staging/prod sites,
// SPA + sub-app co-hosting under a base_path) that owning the assembly +
// firebase.json generation in forge — rather than asking every project
// to hand-roll it in CI — is the coherent product move.
//
// The pipeline, per frontend:
//
//  1. Build + assemble — the SHARED static staging step (staticstage.go):
//     install, `npm run build` with the frontend's env injected, copy
//     public_dir under base_path plus every bundle dir, then write the
//     environment's runtime config document last. StaticSiteProvider runs
//     the identical step, which is why it lives there and not here.
//  2. Configure — write a firebase.json (hosting.public = staging,
//     hosting.site = Site, plus any Rewrites) and a .firebaserc mapping
//     the hosting Target to Site for the Project.
//  3. Deploy — run `firebase deploy --project <project> --only
//     hosting:<target> --non-interactive` from the staging parent.
//
// --dry-run prints the resolved plan (build command, assembled layout,
// and the exact firebase deploy command) and performs NO build, NO file
// assembly side effects beyond an in-memory plan, and NO firebase call.
type FirebaseProvider struct {
	// ProjectDir is the project root. Frontend paths and Bundle.Src
	// resolve against it. Empty means the current working directory.
	ProjectDir string

	// Runner is the os/exec indirection (npm / firebase). Nil falls
	// back to the package default. Tests inject a fake runner.
	Runner commandRunner

	// StagingRoot overrides where the assembled hosting tree is written.
	// Empty means a temp dir under os.TempDir(). Tests set it so they
	// can inspect the assembled layout.
	StagingRoot string

	// HTTPClient reads the live release (its asset manifest and the
	// assets retention carries forward). Nil means an *http.Client with
	// a two-minute timeout.
	HTTPClient HTTPDoer

	// SiteURL maps a Firebase Hosting site id to the origin its live
	// release is read from. Nil means https://<site>.web.app, the origin
	// every site answers on. Tests point it at an httptest server.
	SiteURL func(site string) string

	// Sleep waits between attempts to read the live release, returning
	// early with ctx's error. Nil means a real, context-aware sleep.
	Sleep func(ctx context.Context, d time.Duration) error
}

// HTTPDoer is the one method retention needs from an HTTP client.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// FirebaseFrontend is one frontend the Firebase provider should deploy.
// It carries the resolved build inputs plus the FirebaseHosting spec.
// The CLI builds this from the rendered KCL FrontendEntity; tests
// construct it directly.
type FirebaseFrontend struct {
	// Name is the forge frontend name (logging + target fallbacks).
	Name string

	// Path is the frontend source dir relative to the project root —
	// where `npm install` / `npm run build` run.
	Path string

	// DevRunner is "npm" (default) | "pnpm" | "yarn"; selects the
	// install command. The build command is always `<runner> run build`.
	DevRunner string

	// BuildEnv is the build-time env injected into the build process
	// (NEXT_PUBLIC_* / VITE_*). Layered on top of os.Environ().
	BuildEnv map[string]string

	// Project is the forge project name (recorded in the source map manifest).
	Project string

	// VersionEnvVar is the variable the bundle reads service.version from
	// (NEXT_PUBLIC_APP_VERSION / VITE_APP_VERSION); forge injects the build id
	// into it unless BuildEnv sets it. Empty injects nothing.
	VersionEnvVar string

	// Sourcemaps is "publish" (default), "public" or "off". See the
	// Sourcemaps* constants.
	Sourcemaps string

	// RuntimeConfigJS is this ENVIRONMENT's rendered runtime config
	// document — the `window.__FORGE_CONFIG__ = {...}` script the browser
	// loads before any bundle code runs, produced from the environment's
	// KCL (frontend_config_gen.k's runtime projection).
	//
	// It is written into the assembled tree AFTER the built bundle is
	// copied, which is what makes promotion real: the bundle is
	// environment-agnostic and this one file is the only part that
	// differs, so the same bytes ship to dev and prod carrying different
	// configuration. Empty means the frontend declares no typed config —
	// nothing is written and the assembled layout is unchanged.
	RuntimeConfigJS string

	// SourcemapPublisher files the source maps the build extracts (F1) so
	// errors can be symbolicated: pushed to the platform registry when the
	// env's observability is hosted, kept on disk otherwise. nil files nothing
	// (the maps are still extracted, and never served).
	SourcemapPublisher SourcemapPublisher

	// Spec is the FirebaseHosting deploy config.
	Spec FirebaseHostingSpec
}

// FirebaseHostingSpec mirrors the kcl/schema.k FirebaseHosting schema
// (and the CLI-side FirebaseHostingDeploy entity). Kept in this package
// so the provider has no import on internal/cli.
type FirebaseHostingSpec struct {
	Project   string
	Site      string
	Target    string
	PublicDir string
	BasePath  string
	Bundle    []BundleDirSpec
	Rewrites  []map[string]any

	// SPAFallback is the entry document ("/index.html") served for a
	// client-side route — a missing path inside its directory whose last
	// segment is not a static file. Empty means no SPA fallback. Rendered
	// AFTER Rewrites, so an explicit rewrite always wins.
	SPAFallback string

	// AssetDir is the build's content-hashed asset directory relative to
	// the frontend's mount ("assets" for Vite, "_next/static" for a
	// Next.js export). Empty means the build declares none: no immutable
	// cache rule and no asset retention.
	AssetDir string

	// KeepAssetReleases is how many releases' hashed assets the live site
	// serves, the new one included. Zero means the default (10); the floor
	// is 2. See firebase_assets.go.
	KeepAssetReleases int

	// CacheControl is the frontend's declared Cache-Control rules
	// (first match wins). Empty means forge's default: the asset dir
	// immutable, everything else no-cache. See firebaseHeaders.
	CacheControl []CacheRuleSpec
}

// FirebaseCLI is the executable a Firebase Hosting deploy runs. Exported so a
// deploy's preflight can check it is installed before anything ships, rather
// than discovering its absence after the cluster half has already applied.
const FirebaseCLI = "firebase"

// Name returns the provider identifier.
func (FirebaseProvider) Name() string { return "firebase" }

func (p FirebaseProvider) runner() commandRunner {
	if p.Runner != nil {
		return p.Runner
	}
	return defaultRunner
}

// resolvedTarget returns the hosting selector for `--only hosting:<x>`.
// When an explicit Target alias is declared it's used (resolved via the
// generated .firebaserc target→site mapping); otherwise the bare site id
// is used — `firebase deploy --only hosting:<site>` accepts a site id
// directly, no target alias required.
func (s FirebaseHostingSpec) resolvedTarget() string {
	if s.Target != "" {
		return s.Target
	}
	return s.Site
}

// hasExplicitTarget reports whether the spec declares a real hosting
// target alias (distinct from defaulting to the site id). This is the
// switch that keeps `site` and `target` MUTUALLY EXCLUSIVE in the
// rendered firebase.json: the firebase CLI rejects a hosting config that
// carries BOTH on `deploy --only hosting:<x>`. With an explicit target we
// emit `target` (resolved via .firebaserc); without one we emit `site`
// (and deploy by site id directly).
func (s FirebaseHostingSpec) hasExplicitTarget() bool {
	return s.Target != ""
}

// mountRel is the frontend's mount relative to the site root: "" for the
// root, "admin" for base_path /admin.
func (s FirebaseHostingSpec) mountRel() string {
	if m := cleanDestRel(s.BasePath); m != "." {
		return filepath.ToSlash(m)
	}
	return ""
}

// assetRel is the build's hashed asset directory relative to the site root
// ("assets", "admin/assets"), or "" when the build declares none.
func (s FirebaseHostingSpec) assetRel() string {
	dir := strings.Trim(path.Clean("/"+strings.TrimSpace(s.AssetDir)), "/")
	if s.AssetDir == "" || dir == "" {
		return ""
	}
	return path.Join(s.mountRel(), dir)
}

// Deploy ships every frontend in the group to its Firebase Hosting
// site. It reads the frontends off group.Frontends and the dry-run knob
// off group.DryRun so the Firebase provider satisfies the same Provider
// interface as k8s-cluster / external / compose and dispatches through
// the registry — no bespoke hand-dispatch in forge env deploy.
func (p FirebaseProvider) Deploy(ctx context.Context, group ServiceGroup) error {
	return p.deployFrontends(ctx, group.Frontends, group.DryRun)
}

// deployFrontends builds, assembles, configures, and ships each frontend
// to its Firebase Hosting site. dryRun prints the plan and skips every
// side effect.
func (p FirebaseProvider) deployFrontends(ctx context.Context, fes []FirebaseFrontend, dryRun bool) error {
	for i, fe := range fes {
		if err := p.deployOne(ctx, fe, dryRun); err != nil {
			if i > 0 {
				return afterShipping(err)
			}
			return err
		}
	}
	return nil
}

// firebasePlan is the resolved, side-effect-free description of one
// frontend's Firebase deploy: the shared build-and-assemble StagePlan
// plus the Firebase-specific configure and deploy steps.
type firebasePlan struct {
	Stage         StagePlan
	Retention     assetRetentionPlan
	FirebaseJSON  string   // marshaled firebase.json contents
	FirebaseRC    string   // marshaled .firebaserc contents
	DeployCmd     []string // argv for the firebase deploy invocation
	DeployWorkdir string   // dir the firebase command runs from (StagingDir's parent)
}

// stageInput projects a FirebaseFrontend onto the target-neutral
// StageInput the shared build-and-assemble step consumes.
func (p FirebaseProvider) stageInput(fe FirebaseFrontend) StageInput {
	staging := p.StagingRoot
	if staging == "" {
		staging = filepath.Join(os.TempDir(), "forge-firebase-"+fe.Name)
	}
	return StageInput{
		Name:            fe.Name,
		Path:            fe.Path,
		DevRunner:       fe.DevRunner,
		BuildEnv:        fe.BuildEnv,
		PublicDir:       fe.Spec.PublicDir,
		BasePath:        fe.Spec.BasePath,
		Bundle:          fe.Spec.Bundle,
		RuntimeConfigJS: fe.RuntimeConfigJS,
		ProjectDir:      p.ProjectDir,
		StagingRoot:     staging,
		Project:         fe.Project,
		VersionEnvVar:   fe.VersionEnvVar,
		Sourcemaps:      fe.Sourcemaps,
	}
}

// buildPlan resolves a frontend into its firebasePlan. Pure aside from
// path resolution (filepath.Abs) — no build, no copy, no firebase call.
func (p FirebaseProvider) buildPlan(fe FirebaseFrontend) (firebasePlan, error) {
	stage, err := buildStagePlan(p.stageInput(fe))
	if err != nil {
		return firebasePlan{}, fmt.Errorf("firebase %s: %w", fe.Name, err)
	}

	fbJSON, err := renderFirebaseJSON(stage.StagingDir, fe.Spec)
	if err != nil {
		return firebasePlan{}, fmt.Errorf("firebase %s: render firebase.json: %w", fe.Name, err)
	}
	fbRC, err := renderFirebaseRC(fe.Spec)
	if err != nil {
		return firebasePlan{}, fmt.Errorf("firebase %s: render .firebaserc: %w", fe.Name, err)
	}

	return firebasePlan{
		Stage: stage,
		Retention: assetRetentionPlan{
			AssetRel:    fe.Spec.assetRel(),
			ManifestRel: path.Join(fe.Spec.mountRel(), AssetManifestName),
			SiteURL:     p.siteURL(fe.Spec.Site),
			Keep:        effectiveKeepAssetReleases(fe.Spec.KeepAssetReleases),
		},
		FirebaseJSON: fbJSON,
		FirebaseRC:   fbRC,
		DeployCmd: []string{
			FirebaseCLI, "deploy",
			"--project", fe.Spec.Project,
			"--only", "hosting:" + fe.Spec.resolvedTarget(),
			"--non-interactive",
		},
		DeployWorkdir: filepath.Dir(stage.StagingDir),
	}, nil
}

func (p FirebaseProvider) deployOne(ctx context.Context, fe FirebaseFrontend, dryRun bool) error {
	plan, err := p.buildPlan(fe)
	if err != nil {
		return shippedNothing(err)
	}

	if dryRun {
		printFirebasePlan(os.Stdout, plan)
		return nil
	}

	runner := p.runner()
	// Everything before `firebase deploy` is local: the live site is
	// untouched until it runs (frontend_not_shipped.go).
	if err := p.prepare(ctx, runner, fe, plan); err != nil {
		return shippedNothing(err)
	}

	// Deploy phase — firebase deploy from the workdir so it picks up the
	// generated firebase.json + .firebaserc.
	fmt.Printf("  [firebase] %s: deploying to project=%s site=%s target=%s...\n",
		plan.Stage.Name, fe.Spec.Project, fe.Spec.Site, fe.Spec.resolvedTarget())
	if err := runInDir(ctx, runner, plan.DeployWorkdir, nil, plan.DeployCmd); err != nil {
		return fmt.Errorf("firebase %s: deploy: %w", plan.Stage.Name, err)
	}
	fmt.Printf("  [firebase] %s: deployed.\n", plan.Stage.Name)
	return nil
}

// prepare is everything a Firebase deploy does before it writes the site:
// build and assemble the tree, file its source maps, carry the live release's
// assets into it, and write firebase.json + .firebaserc beside it.
func (p FirebaseProvider) prepare(ctx context.Context, runner commandRunner, fe FirebaseFrontend, plan firebasePlan) error {
	fmt.Printf("  [firebase] %s: building (%s) in %s...\n",
		plan.Stage.Name, strings.Join(plan.Stage.BuildCmd, " "), plan.Stage.FrontendDir)

	// Build + assemble — the shared static staging step.
	res, err := runStagePlan(ctx, runner, plan.Stage)
	if err != nil {
		return err
	}
	reportStageResult("firebase", plan.Stage.Name, res)

	// File the source maps BEFORE anything goes live: a build whose maps could
	// not be filed would reach production unsymbolicatable, so a failure here
	// stops the deploy with the previous release still serving.
	if err := FileStageSourcemaps(ctx, fe.SourcemapPublisher, "firebase", plan.Stage.Project, plan.Stage.Name, res); err != nil {
		return err
	}

	// Retain — put the previous releases' hashed assets back into the
	// tree, so a tab opened before this deploy can still load its chunks.
	if err := p.retainAssets(ctx, plan.Stage.Name, plan.Stage.StagingDir, plan.Retention); err != nil {
		return err
	}
	// Retention adds files after the stage's own guard ran.
	if err := guardNoSourceMaps(plan.Stage.StagingDir, plan.Stage.Sourcemaps); err != nil {
		return fmt.Errorf("firebase %s: %w", plan.Stage.Name, err)
	}

	// Configure phase — firebase.json + .firebaserc next to the staging
	// tree (in its parent, which is the firebase deploy workdir).
	if err := atomicfile.WriteFile(filepath.Join(plan.DeployWorkdir, "firebase.json"), []byte(plan.FirebaseJSON), 0o644); err != nil {
		return fmt.Errorf("firebase %s: write firebase.json: %w", plan.Stage.Name, err)
	}
	if err := atomicfile.WriteFile(filepath.Join(plan.DeployWorkdir, ".firebaserc"), []byte(plan.FirebaseRC), 0o644); err != nil {
		return fmt.Errorf("firebase %s: write .firebaserc: %w", plan.Stage.Name, err)
	}
	return nil
}

// BuildOnlyFrontend is a frontend that forge must BUILD (env-injected)
// but NOT deploy — a `deploy = None` frontend. Its build output (e.g. a
// Next.js static export under PublicDir) becomes available on disk so a
// sibling FirebaseHosting / StaticSite frontend can assemble it into its
// bundle. Mirrors the build inputs of FirebaseFrontend minus any deploy
// spec.
type BuildOnlyFrontend struct {
	// Name is the forge frontend name (logging).
	Name string

	// Path is the frontend source dir relative to the project root —
	// where install / `npm run build` run.
	Path string

	// DevRunner is "npm" (default) | "pnpm" | "yarn"; selects the
	// install command.
	DevRunner string

	// BuildEnv is the build-time env injected into the build process
	// (NEXT_PUBLIC_* / VITE_*). Layered over NODE_ENV=production.
	BuildEnv map[string]string

	// PublicDir is the build-output dir the build emits (relative to
	// Path), e.g. "out" for a Next.js static export. Used for dry-run
	// reporting of the emitted directory.
	PublicDir string
}

// buildOnlyPlan is the resolved, side-effect-free description of one
// build-only frontend's build. Computed first so --dry-run can print it
// without shelling out, and the real build executes against the same plan.
type buildOnlyPlan struct {
	Name        string
	FrontendDir string // absolute frontend source dir
	InstallCmd  []string
	BuildCmd    []string
	BuildEnv    map[string]string
	EmittedDir  string // absolute build-output dir the build is expected to emit
}

// buildOnlyPlanFor resolves a BuildOnlyFrontend into its buildOnlyPlan.
// Pure aside from path resolution (filepath.Abs).
func (p FirebaseProvider) buildOnlyPlanFor(fe BuildOnlyFrontend) (buildOnlyPlan, error) {
	projDir := p.ProjectDir
	if projDir == "" {
		projDir = "."
	}
	projDir, err := filepath.Abs(projDir)
	if err != nil {
		return buildOnlyPlan{}, fmt.Errorf("build-only %s: resolve project dir: %w", fe.Name, err)
	}
	frontendDir := fe.Path
	if !filepath.IsAbs(frontendDir) {
		frontendDir = filepath.Join(projDir, fe.Path)
	}
	emitted := fe.PublicDir
	if emitted != "" && !filepath.IsAbs(emitted) {
		emitted = filepath.Join(frontendDir, fe.PublicDir)
	}
	return buildOnlyPlan{
		Name:        fe.Name,
		FrontendDir: frontendDir,
		InstallCmd:  frontendInstallCmd(fe.DevRunner),
		BuildCmd:    []string{"npm", "run", "build"},
		BuildEnv:    buildTimeEnv(fe.BuildEnv),
		EmittedDir:  emitted,
	}, nil
}

// BuildOnly builds each build-only frontend (install + `npm run build`
// with its env_vars injected) so its output exists on disk before any
// deploying frontend assembles a bundle that references it. dryRun
// prints the build plan and performs no side effects, mirroring the
// Firebase deploy dry-run.
func (p FirebaseProvider) BuildOnly(ctx context.Context, fes []BuildOnlyFrontend, dryRun bool) error {
	for _, fe := range fes {
		plan, err := p.buildOnlyPlanFor(fe)
		if err != nil {
			return err
		}
		if dryRun {
			printBuildOnlyPlan(os.Stdout, plan)
			continue
		}
		fmt.Printf("  [build-only] %s: building (%s) in %s...\n", plan.Name, strings.Join(plan.BuildCmd, " "), plan.FrontendDir)
		if err := runFrontendBuild(ctx, p.runner(), plan.Name, plan.FrontendDir, plan.InstallCmd, plan.BuildCmd, fe.BuildEnv); err != nil {
			return err
		}
		fmt.Printf("  [build-only] %s: built", plan.Name)
		if plan.EmittedDir != "" {
			fmt.Printf(" -> %s", plan.EmittedDir)
		}
		fmt.Println(".")
	}
	return nil
}

// printBuildOnlyPlan renders the dry-run plan for one build-only
// frontend: the install + build commands, the injected build env, and
// the emitted output dir. Mirrors printFirebasePlan's "[DRY-RUN] would
// exec" style.
func printBuildOnlyPlan(w io.Writer, plan buildOnlyPlan) {
	_, _ = fmt.Fprintf(w, "  [DRY-RUN] build-only plan for frontend %q:\n", plan.Name)
	_, _ = fmt.Fprintf(w, "    build dir:    %s\n", plan.FrontendDir)
	_, _ = fmt.Fprintf(w, "    [DRY-RUN] would exec: %s\n", strings.Join(plan.InstallCmd, " "))
	if len(plan.BuildEnv) > 0 {
		_, _ = fmt.Fprintf(w, "    build env:    %s\n", formatBuildEnv(plan.BuildEnv))
	}
	_, _ = fmt.Fprintf(w, "    [DRY-RUN] would exec: %s (NODE_ENV=production)\n", strings.Join(plan.BuildCmd, " "))
	if plan.EmittedDir != "" {
		_, _ = fmt.Fprintf(w, "    emits dir:    %s\n", plan.EmittedDir)
	}
}

// renderFirebaseJSON builds the firebase.json contents. `hosting.public`
// is the staging dir (relative to the deploy workdir, which is its
// parent — so just the basename). `hosting.site` pins the target site;
// declared rewrites pass through verbatim, followed by the SPA fallback
// when one is declared; `headers` carries the Cache-Control policy (see
// firebase_serving.go). ignore mirrors the firebase defaults so the
// generated config files don't get uploaded.
func renderFirebaseJSON(stagingDir string, spec FirebaseHostingSpec) (string, error) {
	hosting := map[string]any{
		"public": filepath.Base(stagingDir),
		"ignore": []string{"firebase.json", "**/.*", "**/node_modules/**"},
	}
	// site and target are MUTUALLY EXCLUSIVE in a firebase.json hosting
	// config — the firebase CLI errors out ("Cannot have both site and
	// target ...") on `deploy --only hosting:<x>` when both are present.
	// Emit `target` only when an explicit alias is declared (resolved via
	// the .firebaserc target→site map); otherwise emit the bare `site`.
	if spec.hasExplicitTarget() {
		hosting["target"] = spec.Target
	} else {
		hosting["site"] = spec.Site
	}
	rewrites := append([]map[string]any{}, spec.Rewrites...)
	if spec.SPAFallback != "" {
		rewrites = append(rewrites, spaFallbackRewrite(spec.SPAFallback))
	}
	if len(rewrites) > 0 {
		hosting["rewrites"] = rewrites
	}
	hosting["headers"] = firebaseHeaders(spec.CacheControl, spec.assetRel())
	doc := map[string]any{"hosting": hosting}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

// renderFirebaseRC builds the .firebaserc contents: the default project
// plus a hosting target → site mapping so `--only hosting:<target>`
// resolves. Mirrors what `firebase target:apply hosting <target> <site>`
// would write.
func renderFirebaseRC(spec FirebaseHostingSpec) (string, error) {
	doc := map[string]any{
		"projects": map[string]any{"default": spec.Project},
	}
	// The target→site mapping is only meaningful when firebase.json
	// references a target alias. Without an explicit Target, firebase.json
	// carries `site` directly and the deploy selects `--only
	// hosting:<site>` by site id, so no target alias is configured (an
	// orphan mapping whose alias nothing references is dead config).
	if spec.hasExplicitTarget() {
		doc["targets"] = map[string]any{
			spec.Project: map[string]any{
				"hosting": map[string]any{
					spec.Target: []string{spec.Site},
				},
			},
		}
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

// printFirebasePlan renders the dry-run plan for one frontend. Output is
// stable + greppable: the build command, the assembled layout (one line
// per copy, with the destination mount), and the exact firebase deploy
// command. Mirrors the External provider's "[DRY-RUN] would exec" style.
func printFirebasePlan(w io.Writer, plan firebasePlan) {
	_, _ = fmt.Fprintf(w, "  [DRY-RUN] firebase deploy plan for frontend %q:\n", plan.Stage.Name)
	printStagePlanBuild(w, plan.Stage)
	_, _ = fmt.Fprintf(w, "    firebase.json (hosting.public=%s):\n", filepath.Base(plan.Stage.StagingDir))
	for _, line := range strings.Split(strings.TrimRight(plan.FirebaseJSON, "\n"), "\n") {
		_, _ = fmt.Fprintf(w, "      %s\n", line)
	}
	if r := plan.Retention; r.enabled() {
		_, _ = fmt.Fprintf(w, "    asset retention: keep /%s/** of the last %d release(s), read from %s\n",
			r.AssetRel, r.Keep, r.manifestURL())
	} else {
		_, _ = fmt.Fprintf(w, "    asset retention: off (the frontend declares no asset_dir)\n")
	}
	_, _ = fmt.Fprintf(w, "    [DRY-RUN] would exec (cwd %s): %s\n",
		plan.DeployWorkdir, strings.Join(plan.DeployCmd, " "))
}
