package generator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/pkg/components"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/templates"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// recordFrontendScaffoldBirth records a frontend scaffold file in the birth
// ledger with the hash of the bytes that landed, so it is pristine until its
// author edits it. A forge-generated file is not a scaffold and gets no
// record (rescaffold.go applies the same rule).
//
// Every scaffold-once file of the tree needs it, not only the two the nav
// generator keeps current (internal/cli/generate_frontend_nav.go,
// emitScaffoldUntilTouched, gated on this hash). `forge lint`'s format lane
// skips a pristine scaffold (ownedFrontendSources): a file forge wrote and
// nobody touched is not the author's formatting to fix. Without a record it
// reads as edited from birth, and a fresh project's first lint under CI —
// report-only there — failed on 31 files its author never opened
// (TestE2EFreshScaffoldLintExitsZero).
func recordFrontendScaffoldBirth(root, path string, content []byte) {
	if IsForgeGenerated(content) {
		return
	}
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		checksums.RecordScaffoldWithHash(root, rel, content)
	}
}

// frontendProjectRoot is the forge project containing frontendDir: the
// nearest ancestor holding forge.yaml, or "" when there is none.
func frontendProjectRoot(frontendDir string) string {
	for dir := filepath.Dir(frontendDir); ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "forge.yaml")); err == nil {
			return dir
		}
		if parent := filepath.Dir(dir); parent == dir {
			return ""
		}
	}
}

// frontendTemplateDir returns the per-kind template subdirectory for the
// given kind. kind="mobile" uses react-native templates; kind="vite-spa"
// uses vite-spa templates; everything else (including "" and "web") uses
// nextjs.
//
// This names the PLATFORM-SPECIFIC directory only. The full tree a
// frontend is scaffolded from also includes the shared roots — see
// templates.FrontendTemplateRoots / templates.ListFrontendTree.
func frontendTemplateDir(kind string) string {
	switch kind {
	case "mobile":
		return "react-native"
	case "vite-spa":
		return "vite-spa"
	default:
		return "nextjs"
	}
}

// frontendKindHasMockSurface reports whether a frontend --kind gets the
// generated mock/scenario surface (see EmitFrontendMockSurface). Browser
// frontends do; React Native does not — its connect.ts never references
// "@/lib/mock-transport", and Metro would bundle the fixtures into the app.
func frontendKindHasMockSurface(kind string) bool {
	return frontendTemplateDir(kind) != "react-native"
}

// FrontendTypeHasMockSurface is frontendKindHasMockSurface keyed by the
// forge.yaml `frontends[].type` (nextjs / vite-spa / react-native) rather
// than by the `--kind` flag the scaffold takes. Used by the `forge generate`
// frontend-mocks step, which reads the persisted config. An unrecognised
// type gets nothing — the surface is rendered from templates that only the
// two browser kinds import.
func FrontendTypeHasMockSurface(feType string) bool {
	return strings.EqualFold(feType, "nextjs") || strings.EqualFold(feType, "vite-spa")
}

// FrontendGenOptions carries optional project-level settings forwarded
// to the per-frontend file emitter. Kept distinct from the existing
// positional GenerateFrontendFiles params so that adding new optional
// settings (here: Workspaces) doesn't churn every call site.
type FrontendGenOptions struct {
	// Workspaces opts the frontend into the pnpm-workspace layout —
	// its package.json declares "workspace:*" deps on @<scope>/api /
	// @<scope>/hooks, and templates render imports of those packages
	// instead of relative @/gen / @/hooks paths.
	Workspaces bool
	// Output selects the Next.js build/runtime shape rendered into
	// `next.config.ts` and the Dockerfile. Valid values: "static",
	// "standalone", "server". See config.FrontendConfig.Output for the
	// per-mode semantics. Empty string means the scaffold default,
	// config.FrontendOutputScaffoldDefault ("static") — this renders a NEW
	// frontend, so "the default" is the current one. (Reading an existing
	// forge.yaml entry is different: there an empty field is the legacy
	// standalone shape; see config.FrontendConfig.EffectiveOutput.)
	//
	// Ignored for kind=mobile (react-native) and kind=vite-spa; those
	// trees have their own production shapes.
	Output string
	// BasePath is the URL prefix the frontend is mounted under (e.g.
	// "/admin"), mirroring config.FrontendConfig.BasePath. Rendered
	// into `next.config.ts` (basePath + assetPrefix defaults) and
	// `src/lib/basepath_gen.ts` (BASE_PATH / joinBasePath fallback).
	// Empty = served from the host root. Like Output, only the nextjs
	// template tree reads it.
	BasePath string
	// TypedConfig describes the config message bound to THIS frontend by
	// (forge.v1.frontend_config), when the project declares one. Zero value
	// means the project has not opted in, and every template renders its
	// previous env-var form unchanged.
	TypedConfig FrontendTypedConfig
	// Public scaffolds a frontend with NO sign-in gate: no route guard
	// wrapping the app, and no sign-in screens or the forms behind them
	// (isSignInScreen). It is what `--auth-mode none` renders (`forge
	// project new` and `forge scaffold frontend`), and what `forge scaffold
	// frontend` picks by default for a project whose dev environment
	// declares no identity provider — there, a guarded frontend can only
	// ever redirect every page to a sign-in form nothing can satisfy.
	//
	// Nor is the API client of the sign-in flow (src/lib/auth/native-login),
	// so the frontend never calls the API's /auth/* routes: the auth context
	// answers "signed out" without a request (the fixture user in mock mode),
	// and otel_gen.ts asks no session endpoint who the user is
	// (IdentifySession). The context and use-auth modules, the session menu
	// and the unmounted route-guard.tsx are still emitted: they compile, and
	// are what gating the frontend later re-attaches to.
	Public bool
}

// FrontendTypedConfig tells the frontend templates which typed config
// fields exist for the frontend being rendered.
//
// It carries per-FIELD presence rather than a bare "has config" flag
// because the generated TypeScript module's type has exactly the keys the
// proto declared. A template that reads cfg.MOCK_API when the proto never
// declared mock_api produces a frontend that does not type-check — so each
// read is gated on the field that backs it.
//
// There are no OIDC gates here because a browser frontend carries no OIDC
// config at all: sign-in is NATIVE, so the issuer, client id, redirect URI,
// scopes and resource indicator are backend configuration and never reach
// the bundle. See frontendConfigEnvVars in frontend_config_proto.go.
type FrontendTypedConfig struct {
	// Bound reports that a config message names this frontend.
	Bound bool

	HasMockAPI bool
}

// FrontendTypedConfigFrom builds the per-field presence set from the leaf
// fields of the frontend's config message, keyed on each field's env_var
// (the one spelling shared by the proto, the KCL projection, the runtime
// document and the generated module).
func FrontendTypedConfigFrom(envVars []string) FrontendTypedConfig {
	out := FrontendTypedConfig{Bound: true}
	for _, v := range envVars {
		switch v {
		case "MOCK_API":
			out.HasMockAPI = true
		}
	}
	return out
}

// isSignInScreen reports whether a frontend template-tree path is part of
// the sign-in flow a public frontend omits: the Next.js /auth route files,
// and the forms, hooks and tests only those screens (or the Vite tree's
// inline sign-in routes, gated on .Public there) use.
//
// So is native-login.ts, the client of the API's /auth/* routes: a public
// frontend's context (context.tsx's .Public branch) and session menu take
// their Identity type from use-auth and send no request. The rest of the
// session plumbing stays: src/lib/auth/{context,use-auth,session-provider,
// return-to}, which the shell and the session menu read, and the unused
// route-guard.tsx, the seam gating the frontend later re-attaches to.
func isSignInScreen(rel string) bool {
	rel = strings.TrimSuffix(filepath.ToSlash(rel), ".tmpl")
	// src/app/account/ is the signed-in person's account page, which mounts
	// the passkeys panel: with no sign-in there is no account to manage.
	if strings.HasPrefix(rel, "src/app/auth/") || strings.HasPrefix(rel, "src/app/account/") {
		return true
	}
	_, ok := signInOnlyFiles[rel]
	return ok
}

// signInOnlyFiles are the files outside src/app/auth/ that nothing but the
// sign-in screens imports. TestFrontendScaffoldImportsResolve's public cases
// pin that the rest of a public tree never imports one, and
// TestPublicFrontendOmitsSignInFlow that each is a real file of the native
// tree and absent from the public one.
var signInOnlyFiles = map[string]struct{}{
	"src/components/accept-invite-form.tsx":    {},
	"src/components/account-settings.tsx":      {},
	"src/components/auth-ui.tsx":               {},
	"src/components/forgot-password-form.tsx":  {},
	"src/components/magic-link-form.tsx":       {},
	"src/components/passkeys-panel.tsx":        {},
	"src/components/password-requirements.tsx": {},
	"src/components/reset-password-form.tsx":   {},
	"src/components/sign-in-methods.tsx":       {},
	"src/components/social-callback.tsx":       {},
	"src/components/verify-email-form.tsx":     {},
	"src/lib/auth/native-login.ts":             {},
	"src/lib/auth/password.test.tsx":           {},
	"src/lib/auth/sign-in-methods.test.tsx":    {},
	"src/lib/auth/use-auth-options.ts":         {},
	"src/lib/auth/use-password-policy.ts":      {},
}

// GenerateFrontendFiles generates the frontend directory and files.
// kind selects the template set: "" or "web" for Next.js, "mobile" for React Native.
// Both the "new" project flow and the "scaffold frontend" flow delegate here so
// the output is always identical.
//
// This thin shim preserves the original signature for backward
// compatibility with the Service contract; new call sites should prefer
// GenerateFrontendFilesWithOptions when they have access to the
// project-level Frontend config.
func GenerateFrontendFiles(root, modulePath, projectName, frontendName string, apiPort int, kind string) error {
	return GenerateFrontendFilesWithOptions(root, modulePath, projectName, frontendName, apiPort, kind, FrontendGenOptions{})
}

// GenerateFrontendFilesWithOptions is GenerateFrontendFiles with an
// extra FrontendGenOptions param for project-level toggles (today only
// Workspaces). The default-zero opts struct produces output
// byte-identical to GenerateFrontendFiles so existing callers and
// snapshot tests are unaffected.
func GenerateFrontendFilesWithOptions(root, modulePath, projectName, frontendName string, apiPort int, kind string, opts FrontendGenOptions) error {
	return generateFrontendFiles(root, modulePath, projectName, frontendName, apiPort, kind, opts, webRuntimePublishedRange)
}

// generateFrontendFiles renders against runtimeFloor, the web-runtime floor
// the floor-gated choices (viteDevLogFromRuntime) read: production passes
// webRuntimePublishedRange, and a test passes another floor to exercise
// those choices offline (as writeSiteStarter takes its range).
func generateFrontendFiles(root, modulePath, projectName, frontendName string, apiPort int, kind string, opts FrontendGenOptions, runtimeFloor string) error {
	frontendDir := filepath.Join(root, "frontends", frontendName)
	if err := os.MkdirAll(frontendDir, 0755); err != nil {
		return fmt.Errorf("create frontend directory: %w", err)
	}

	tmplDir := frontendTemplateDir(kind)

	// The tree is composed from frontend/shared[-web]/ plus the per-kind
	// directory (see templates.FrontendTemplateRoots): generic mechanism
	// modules live in exactly one place and render into every kind that
	// claims them.
	frontendFiles, err := templates.ListFrontendTree(tmplDir)
	if err != nil {
		return fmt.Errorf("list frontend templates: %w", err)
	}

	layout := NewFrontendWorkspaceLayout(projectName)
	// Default the Next.js output shape to the scaffold default (a static
	// export) when unset. The generated CRUD pages are static routes —
	// `/<slug>/view?id=…`, `/<slug>/edit?id=…`, the id read client-side —
	// so `output: "export"` builds on a project with entities, and the
	// result ships to every static runtime, the hosted one included.
	// We canonicalise here rather than in every template so callers can
	// pass "" for "use the scaffold default" without having to know what
	// it is.
	output := strings.ToLower(strings.TrimSpace(opts.Output))
	if output == "" {
		output = config.FrontendOutputScaffoldDefault
	}
	webTree := tmplDir == "nextjs" || tmplDir == "vite-spa"
	data := templates.FrontendTemplateData{
		Public:       opts.Public,
		FrontendName: frontendName,
		ProjectName:  projectName,
		Platform:     tmplDir,
		APIURL:       fmt.Sprintf("http://localhost:%d", apiPort),
		Module:       modulePath,
		Workspaces:   opts.Workspaces,
		Output:       output,
		BasePath:     opts.BasePath,
		// Where this function writes the frontend, relative to the project
		// root — the path the Dockerfile builds at.
		RepoPath: "frontends/" + frontendName,

		// Typed frontend config, when the project declares a config message
		// bound to THIS frontend. Gated per-field because a template cannot
		// read a key the generated module's type does not have.
		HasTypedConfig: opts.TypedConfig.Bound,
		HasMockAPI:     opts.TypedConfig.HasMockAPI,

		// The web trees ship native-login.ts and api-base.ts beside the
		// otel_gen.ts that reads these, so the scaffold renders what the
		// first `forge generate` re-derives from disk — except a public
		// tree, which ships no native-login.ts and has no session to ask
		// about.
		IdentifySession: webTree && !opts.Public,

		DevLogFromRuntime: tmplDir == "vite-spa" && viteDevLogFromRuntime(runtimeFloor),
	}
	if opts.Workspaces {
		data.APIPackage = layout.APIPackage
		data.HooksPackage = layout.HooksPackage
		data.UIWebPackage = layout.UIWebPackage
		// UINativePackage only surfaces in mobile (RN) templates —
		// the nextjs and vite-spa templates don't reference it (the
		// `{{.UINativePackage}}` tag never appears under those template
		// trees). Populate unconditionally for workspaces=on so the
		// RN package.json can refer to it; Next.js renders ignore it.
		data.UINativePackage = layout.UINativePackage
	}

	for _, file := range frontendFiles {
		// A public frontend has nothing to sign in to, so the sign-in
		// screens and the forms behind them are not emitted at all (the Vite
		// tree carries its sign-in routes inline in routes.tsx, gated on
		// .Public there).
		if opts.Public && isSignInScreen(file.Rel) {
			continue
		}
		// In the workspaces layout the frontend is a MEMBER of the root
		// pnpm workspace, and pnpm takes its settings from the root's
		// pnpm-workspace.yaml (writePnpmWorkspaceYaml carries them there). A
		// member's own file would make it a second workspace root, splitting
		// it from packages/* and its `workspace:*` dependencies.
		if opts.Workspaces && file.Rel == "pnpm-workspace.yaml.tmpl" {
			continue
		}
		// The runtime ships the dev-log receiver once the floor admits it.
		if data.DevLogFromRuntime && file.Rel == inlinedDevLogPlugin {
			continue
		}
		// nav.tsx and dashboard.tsx ARE emitted here, empty of routes, even
		// though the entity set that seeds them does not exist yet.
		//
		// Withholding them until the first entity was the obvious move and is
		// wrong: layout.tsx imports { Nav } and page.tsx imports the
		// dashboard, so `forge scaffold frontend` — which has no generate
		// pass behind it — would hand back a tree that does not typecheck.
		// TestFrontendScaffoldImportsResolve pins that invariant.
		//
		// The nav generator keeps them current instead, refreshing while the
		// user has not touched them (emitScaffoldUntilTouched), so the routes
		// arrive with the first entity and ownership transfers on the first
		// edit. See internal/cli/generate_frontend_nav.go.

		content, err := templates.FrontendTemplates().Render(file.Path, data)
		if err != nil {
			return fmt.Errorf("render frontend template %s: %w", file.Path, err)
		}

		destFile := strings.TrimSuffix(file.Rel, ".tmpl")

		destPath := filepath.Join(frontendDir, destFile)
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return err
		}
		if err := atomicfile.WriteFile(destPath, content, 0644); err != nil {
			return fmt.Errorf("write frontend file %s: %w", destFile, err)
		}
		// Recorded here, where the file is first written, because the hash
		// has to match the bytes that actually landed on disk.
		recordFrontendScaffoldBirth(root, destPath, content)
	}

	// Emit a nested go.mod so that `go test ./...` from the project root
	// skips this subtree. Frontends contain no first-party Go code, but
	// npm dependencies (e.g. flatted) occasionally ship .go files under
	// node_modules, which Go's package discovery would otherwise pick up.
	// A nested module is the idiomatic Go boundary marker.
	//
	// The `go` directive is read from the project's top-level go.mod so the
	// nested module stays in lockstep with the project's declared Go version
	// (no literal `go 1.25` to drift). Falls back to the generator's default
	// when the project go.mod is missing or unparseable (e.g. during the
	// first-ever scaffold before the project go.mod is written).
	goVersion := goVersionFromGoMod(root)
	if goVersion == "" {
		goVersion = defaultGoVersion
	}
	goModPath := filepath.Join(frontendDir, "go.mod")
	goModContent := fmt.Sprintf("// Nested module boundary so `go test ./...` from the project root\n"+
		"// skips node_modules and other frontend assets. This frontend has no\n"+
		"// first-party Go code; the module exists solely as a boundary marker.\n"+
		"module %s/frontends/%s\n\ngo %s\n", modulePath, frontendName, goVersion)
	if err := atomicfile.WriteFile(goModPath, []byte(goModContent), 0644); err != nil {
		return fmt.Errorf("write frontend go.mod: %w", err)
	}

	// Install core web UI components for browser-targeted frontends (Next.js
	// and Vite SPA). React Native uses platform-specific primitives and
	// should not receive web components.
	//
	// In workspaces mode the components live ONCE under packages/ui-web/
	// (emitted separately by WriteUIWebPackageFiles); frontends import them
	// via the `@<scope>/ui-web` workspace dep + a tsconfig path mapping
	// that redirects `@/components/*` → `packages/ui-web/src/components/*`.
	// Skipping the per-frontend copy here is what makes the multi-frontend
	// case stop diverging.
	if (tmplDir == "nextjs" || tmplDir == "vite-spa") && !opts.Workspaces {
		if err := installCoreComponents(root, frontendDir, opts.Public); err != nil {
			return fmt.Errorf("install core components: %w", err)
		}
	}

	// Emit the mock/scenario surface the scaffolded src/lib/connect.ts
	// references — through a literal import() under both Vite and Next.js,
	// which the bundler resolves at build time into a lazy chunk. Without it the
	// tree the scaffold hands back does not typecheck and does not build,
	// and `forge scaffold frontend` has no generate pass behind it to
	// fill the gap. Entity fixtures arrive on the next `forge generate`;
	// the no-entity render is complete and self-contained on its own — see
	// EmitFrontendMockSurface.
	//
	// The ownership state is loaded here rather than threaded in: this is
	// the scaffold path, where the frontend directory did not exist a
	// moment ago, so there is nothing disowned under it — but the writer
	// still records what it stamps, and reading the project's real state
	// keeps a re-scaffold over a disowned path honest.
	if frontendKindHasMockSurface(kind) {
		cs, err := LoadChecksums(root)
		if err != nil {
			return fmt.Errorf("load ownership state: %w", err)
		}
		feRel := filepath.Join("frontends", frontendName)
		if _, err := EmitFrontendMockSurface(root, feRel, nil, nil, nil, cs); err != nil {
			return fmt.Errorf("emit frontend mock surface: %w", err)
		}
		// No fixture-freshness guard here: the scaffold writes no entity
		// fixtures, and the guard is their validity tag. The `forge
		// generate` that writes the first fixtures writes it too — see
		// EmitFixtureFreshnessSurface.
		if err := SaveChecksums(root, cs); err != nil {
			return fmt.Errorf("save ownership state: %w", err)
		}
	}

	// Declare @reliantlabs/forge-web-runtime in the frontend's own (tracked)
	// package.json at its published range, then — on a dev build only — lay
	// down the gitignored workspace root that redirects that requirement to
	// the local forge checkout. Both must happen before anything installs:
	// the scaffold flow runs `npm install` straight after generating, and the
	// bridge only takes effect if npm sees it on that first install.
	EnsureWebRuntimeDependency(root, filepath.Join("frontends", frontendName), frontendName)
	EnsureTelemetryDependencies(root, filepath.Join("frontends", frontendName))
	EnsureDevWebRuntimeLink(root)

	return nil
}

// coreComponents lists the components automatically installed during scaffold.
//
// The list is deliberately split: the "primitives" group is the layered
// base library that the owned frontend components shipped in the base
// scaffold (`src/components/nav.tsx`, `src/components/session_nav.tsx`, …)
// import
// from instead of inlining their own button/input/etc. markup. The
// trailing "domain" group is higher-level building blocks the scaffold
// ships unconditionally because every forge frontend tends to reach for
// them.
var coreComponents = []string{
	// Primitives — base building blocks for every generated frontend.
	// "link" first: every navigating component (page_header,
	// row_actions_menu, and button for its `href` form) imports "./link". The library copy is a plain
	// anchor; installCoreComponents/EnsureCoreComponents overwrite it
	// with a framework-aware version (see linkComponentForDir).
	"link",
	"button",
	// The classes button imports ("./button_classes").
	"button_classes",
	// The context input, textarea, label, select and form import ("./form_field_context").
	"form_field_context",
	"input",
	// The generated create/edit forms import "@/components/ui/textarea" for
	// a multi-line or bytes field; a page importing a component the
	// scaffold never installed fails `next build` on a file forge wrote.
	"textarea",
	"label",
	"form",
	"card",
	"avatar",
	"tabs",
	"table",
	"select",
	"chip",
	"row_actions_menu",
	// The user menu (src/components/session_nav.tsx) and the theme toggle
	// beside it; dropdown_menu imports "./popover".
	"popover",
	"dropdown_menu",
	"tooltip",
	"progress_bar",
	"status",
	"tag",
	"mono",

	// Domain components — higher-level shapes the scaffold ships by default.
	"sidebar_layout",
	"page_header",
	"badge",
	"modal",
	// The dialog lifecycle hook modal and sidebar_layout import ("./modal_dialog").
	"modal_dialog",
	"skeleton_loader",
	"pagination",
	"search_input",
	"alert_banner",
	"toast_notification",
	"key_value_list",
	"login_form",

	// Shapes every CRUD frontend reaches for, added because a measured build
	// hand-wrote them WHILE the library copies sat one `component_library get`
	// away. Three separate sub-agents fetched `empty_state` and all three
	// wrote their own; a fourth fetched `stat_grid` and wrote a StatCard.
	//
	// The old reason was cost, not ignorance: the library shipped hardcoded
	// palette classes, so installing one meant re-theming every className.
	// That is fixed at the source, and shipping these in the scaffold closes
	// the rest of the gap — an installed file is edited, a fetched snippet is
	// reimplemented.
	//
	// Anything domain-SHAPED (kanban, funnel, gantt) stays in the library on
	// purpose: a scaffolded file nobody deletes is worse than an uninstalled
	// one, because it lives in the tree, in review, and in every grep.
	"empty_state",
	"stat_grid",
	"metric_card",
	"filter_bar",
	"confirmation_dialog",
	"data_table",
}

// signInComponents are the core components only a frontend people sign in
// to has a use for: login_form renders a password field. A public frontend
// (FrontendGenOptions.Public) is not handed one — it would be dead code, and
// a credential field in an unclaimed deploy's bundle is what the hosted
// content scan refuses.
var signInComponents = map[string]bool{"login_form": true}

// frontendSignsIn reports whether a forge-scaffolded frontend signs in: it
// ships the sign-in flow's API client, src/lib/auth/native-login.ts, which a
// public frontend is scaffolded without (signInOnlyFiles). The same file
// decides otel_gen.ts's IdentifySession on every `forge generate`.
func frontendSignsIn(frontendDir string) bool {
	_, err := os.Stat(filepath.Join(frontendDir, "src", "lib", "auth", "native-login.ts"))
	return err == nil
}

// installCoreComponents writes core UI components from the component library
// into the frontend's src/components/ui/ directory. A public frontend gets
// every one but signInComponents.
func installCoreComponents(root, frontendDir string, public bool) error {
	lib := components.NewLibrary()
	componentsDir := filepath.Join(frontendDir, "src", "components", "ui")
	if err := os.MkdirAll(componentsDir, 0755); err != nil {
		return fmt.Errorf("create components dir: %w", err)
	}

	var refused []error
	for _, name := range coreComponents {
		if public && signInComponents[name] {
			continue
		}
		content := componentContentForDir(frontendDir, name)
		if content == "" {
			c, err := lib.Get(name)
			if err != nil {
				return fmt.Errorf("get component %s: %w", name, err)
			}
			content = c
		}
		if existing := CaseCollision(componentsDir, name+".tsx"); existing != "" {
			refused = append(refused, CaseCollisionError(componentsDir, name+".tsx", existing))
			continue
		}
		dest := filepath.Join(componentsDir, name+".tsx")
		if err := atomicfile.WriteFile(dest, []byte(content), 0644); err != nil {
			return fmt.Errorf("write component %s: %w", name, err)
		}
		recordFrontendScaffoldBirth(root, dest, []byte(content))
	}
	return errors.Join(refused...)
}

// EnsureCoreComponents installs any missing core components into an existing
// frontend directory's src/components/ui/. See EnsureCoreComponentsIn.
func EnsureCoreComponents(frontendDir string) error {
	return EnsureCoreComponentsIn(frontendDir, "")
}

// EnsureCoreComponentsIn installs any missing core components into
// componentsRel (relative to frontendDir; "" = src/components/ui). Safe to
// call repeatedly.
//
// Each component is scaffold-once by the birth ledger, not by presence: one
// the user deleted stays deleted, the way every other "yours" file does.
// Outside a forge project there is no ledger, and presence decides. A
// component whose name collides case-insensitively with a file already in
// the directory is refused (CaseCollision) while the rest still install;
// the refusals come back together as one error.
func EnsureCoreComponentsIn(frontendDir, componentsRel string) error {
	lib := components.NewLibrary()
	componentsDir := componentsDirFor(frontendDir, componentsRel)
	if err := os.MkdirAll(componentsDir, 0755); err != nil {
		return fmt.Errorf("create components dir: %w", err)
	}
	root := frontendProjectRoot(frontendDir)
	signsIn := frontendSignsIn(frontendDir)

	var refused []error
	for _, name := range coreComponents {
		// A public frontend was scaffolded without the sign-in components;
		// a later generate must not install them after all.
		if !signsIn && signInComponents[name] {
			continue
		}
		dest := filepath.Join(componentsDir, name+".tsx")
		// Before the presence check: on a case-folding filesystem (macOS,
		// Windows) stat finds the app's Button.tsx under the name button.tsx,
		// and the app's component would pass for forge's installed one.
		if existing := CaseCollision(componentsDir, name+".tsx"); existing != "" {
			refused = append(refused, CaseCollisionError(componentsDir, name+".tsx", existing))
			continue
		}
		if !componentNeedsBirth(root, dest) {
			continue
		}
		content := componentContentForDir(frontendDir, name)
		if content == "" {
			c, err := lib.Get(name)
			if err != nil {
				return fmt.Errorf("get component %s: %w", name, err)
			}
			content = c
		}
		checksums.RecordPreWriteAbs(dest) // journaled: a failed run removes it
		if err := atomicfile.WriteFile(dest, []byte(content), 0644); err != nil {
			return fmt.Errorf("write component %s: %w", name, err)
		}
		if root != "" {
			recordFrontendScaffoldBirth(root, dest, []byte(content))
		}
	}
	return errors.Join(refused...)
}

// componentNeedsBirth reports whether a core component at dest is still to
// be scaffolded: by the ledger inside a project, by absence outside one.
//
// A present component with no birth record is BACKFILLED, as
// checksums.ScaffoldOnceDecision does for every other scaffold-once file: a
// frontend scaffolded before the ledger existed has its components on disk
// and no records, and without one the user's first deletion of a component
// reads as "never scaffolded", so the next generate wrote it again with a
// fresh record (and `forge generate --check` reported that rebirth as drift).
// Fresh frontends record every component at birth, so the backfill only
// brings an older frontend's ledger to the same state, once. Adopted
// frontends never reach here (ensureFrontendComponents skips them), so a
// present file under a core component's name is forge's starter copy.
func componentNeedsBirth(root, dest string) bool {
	rel := ""
	if root != "" {
		if r, err := filepath.Rel(root, dest); err == nil && !strings.HasPrefix(r, "..") {
			rel = filepath.ToSlash(r)
		}
	}
	if _, err := os.Stat(dest); err == nil {
		if rel != "" {
			checksums.RecordScaffold(root, rel) // no-op when already recorded
		}
		return false // present: the user's bytes
	} else if !os.IsNotExist(err) {
		return false // unreadable: not a birth forge can vouch for
	}
	if rel != "" {
		return !checksums.ScaffoldRecorded(root, rel)
	}
	return true
}

// componentContentForDir returns framework-specific override content for a
// component, or "" to use the library copy verbatim. Today only the "link"
// primitive is framework-specific: internal navigation must go through the
// host framework's router (next/link handles basePath + client transitions;
// tanstack-router's history does the SPA equivalent). The library's plain-
// anchor fallback would force a full page load AND 404 under a Next.js
// basePath deployment.
func componentContentForDir(frontendDir, name string) string {
	if name != "link" {
		return ""
	}
	switch detectFrontendKind(frontendDir) {
	case "nextjs":
		return nextLinkComponent
	case "vite-spa":
		return viteLinkComponent
	default:
		return ""
	}
}

// detectFrontendKind sniffs the frontend framework from config files the
// scaffold always lays down before components are installed. Returns
// "nextjs", "vite-spa", or "" when neither marker exists.
func detectFrontendKind(frontendDir string) string {
	for _, marker := range []string{"next.config.ts", "next.config.js", "next.config.mjs"} {
		if _, err := os.Stat(filepath.Join(frontendDir, marker)); err == nil {
			return "nextjs"
		}
	}
	if _, err := os.Stat(filepath.Join(frontendDir, "vite.config.ts")); err == nil {
		return "vite-spa"
	}
	return ""
}

// nextLinkComponent routes internal hrefs through next/link (client-side
// navigation + automatic basePath prefixing) and keeps plain anchors for
// external URLs. Generated pages and library components (page_header,
// row_actions_menu) import this instead of rendering raw <a href> — raw
// anchors break client routing and 404 under `--base-path` deployments.
const nextLinkComponent = `import NextLink from "next/link";
import React from "react";

/**
 * Link — the navigation primitive other library components route through
 * (PageHeader actions/breadcrumbs, RowActionsMenu href items, ...).
 *
 * Internal hrefs render next/link: client-side transitions, prefetching,
 * and automatic basePath prefixing. External URLs (http(s)://, mailto:,
 * tel:) render a plain <a> — next/link must never handle those.
 */

const EXTERNAL_HREF = /^(?:[a-z][a-z0-9+.-]*:)?\/\//i;

/** True for absolute/external URLs that must bypass client routing. */
function isExternalHref(href: string): boolean {
  return EXTERNAL_HREF.test(href) || href.startsWith("mailto:") || href.startsWith("tel:");
}

export type LinkProps = React.AnchorHTMLAttributes<HTMLAnchorElement> & {
  href: string;
};

export default function Link({ href, children, ...rest }: LinkProps) {
  if (isExternalHref(href)) {
    return (
      <a href={href} {...rest}>
        {children}
      </a>
    );
  }
  return (
    <NextLink href={href} {...rest}>
      {children}
    </NextLink>
  );
}
`

// viteLinkComponent is the tanstack-router flavor: internal hrefs push
// through the router's history (SPA navigation, no full reload) while
// modified-click / new-tab semantics and external URLs keep native anchor
// behavior.
const viteLinkComponent = `import { useRouter } from "@tanstack/react-router";
import React from "react";

/**
 * Link — the navigation primitive other library components route through
 * (PageHeader actions/breadcrumbs, RowActionsMenu href items, ...).
 *
 * Internal hrefs navigate via tanstack-router's history (client-side, no
 * full reload). External URLs (http(s)://, mailto:, tel:) and modified
 * clicks (cmd/ctrl/shift, middle-click, target="_blank") keep native
 * anchor behavior.
 */

const EXTERNAL_HREF = /^(?:[a-z][a-z0-9+.-]*:)?\/\//i;

/** True for absolute/external URLs that must bypass client routing. */
function isExternalHref(href: string): boolean {
  return EXTERNAL_HREF.test(href) || href.startsWith("mailto:") || href.startsWith("tel:");
}

export type LinkProps = React.AnchorHTMLAttributes<HTMLAnchorElement> & {
  href: string;
};

export default function Link({ href, children, onClick, target, ...rest }: LinkProps) {
  const router = useRouter();

  if (isExternalHref(href) || target === "_blank") {
    return (
      <a href={href} target={target} {...rest}>
        {children}
      </a>
    );
  }

  return (
    <a
      href={href}
      onClick={(e) => {
        onClick?.(e);
        if (
          e.defaultPrevented ||
          e.metaKey ||
          e.ctrlKey ||
          e.shiftKey ||
          e.altKey ||
          e.button !== 0
        ) {
          return;
        }
        e.preventDefault();
        router.history.push(href);
      }}
      {...rest}
    >
      {children}
    </a>
  );
}
`
