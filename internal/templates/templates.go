package templates

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"text/template"
	"unicode"

	"github.com/jinzhu/inflection"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/webruntimepeers"
)

//go:embed all:project all:deploy all:frontend all:ci all:test all:ingress service/*.tmpl all:internal-package webhook/*.tmpl worker/*.tmpl worker-cron/*.tmpl operator/*.tmpl crd/*.tmpl
var templateFS embed.FS

// FuncMap returns the shared template function map used across all templates.
func FuncMap() template.FuncMap {
	caser := cases.Title(language.English)
	return template.FuncMap{
		"lower": strings.ToLower,
		"humanize": func(s string) string {
			words := strings.Fields(naming.Humanize(s))
			for i, w := range words {
				words[i] = strings.ToUpper(w[:1]) + w[1:]
			}
			return strings.Join(words, " ")
		},
		"humanLower": naming.HumanizeLower,
		"upper":      strings.ToUpper,
		"title":      caser.String,
		"snakeCase":  hyphenToUnderscore,
		"camelCase":  toCamelCase,
		"pascalCase": toPascalCase,
		// goName spells a PROTO name (message, rpc, field, service) as the
		// Go identifier protoc-gen-go / protoc-gen-connect-go generate for
		// it — rpc GetOauth2token is the handler method GetOauth2Token.
		// Never pascalCase for these: that is forge's rule for forge's own
		// identifiers, and it disagrees with the generators at digits.
		"goName": naming.GoCamelCase,
		// tripsReviveVarNaming is true when revive's var-naming rule would
		// rewrite a connect-generated handler method (CreateApiKey→CreateAPIKey).
		// Scaffolded stubs must keep the Handler interface spelling, so templates
		// emit a targeted //nolint:revive instead of renaming.
		"tripsReviveVarNaming": naming.TripsReviveVarNaming,
		"kebabCase":            toKebabCase,
		"plural":               pluralize,
		"singular":             singularize,
		"formatComment":        formatComment,
		// typePinTarget maps a tsconfig `paths` KEY to the node_modules
		// directory its VALUE must point at. They differ only for packages
		// whose typings ship separately under @types/ — see
		// webruntimepeers.TypePinTarget.
		"typePinTarget": webruntimepeers.TypePinTarget,
		// typePinPath renders the single `paths` value for a pinned package,
		// aimed at whichever node_modules the project actually has. One
		// element, never a list: SWC panics `next build` on a multi-element
		// value for a non-wildcard key. See webruntimepeers.TypePinPath.
		//
		// It names the package DIRECTORY because a template renders before
		// any install exists to read a declaration entry from. The install
		// that follows every scaffold replaces it with the package's `.d.ts`
		// (generator.ReconcileFrontendTsconfigPeers) — a directory pin that
		// resolves splits the webpack bundle, so it must not outlive it.
		"typePinPath": func(name string, hoisted bool) string {
			return webruntimepeers.TypePinPath(name, hoisted, "")
		},
		"joinStrings":  strings.Join,
		"default":      getDefault,
		"add":          add,
		"last":         lastStringSlice,
		"tableFromFK":  tableFromFK,
		"columnFromFK": columnFromFK,
		// installForgeRun renders the one forge-install script every
		// scaffolded workflow shares — see installForgeScript.
		"installForgeRun": installForgeRun,
	}
}

// stripBuildIgnore removes a leading //go:build ignore directive and the
// following blank line from embedded template .go files. These directives
// prevent the Go toolchain from compiling the templates as part of forge
// itself, but must not appear in scaffolded output.
func stripBuildIgnore(data []byte) []byte {
	const header = "//go:build ignore\n"
	if s := string(data); strings.HasPrefix(s, header) {
		s = strings.TrimPrefix(s, header)
		s = strings.TrimLeft(s, "\n")
		return []byte(s)
	}
	return data
}

// TemplateCategory provides Get, Render, and List operations for a specific
// template directory within the embedded filesystem.
type TemplateCategory struct {
	basePath string
}

// Get returns the raw bytes of a template file. Any //go:build ignore
// directives are stripped from the output.
func (c TemplateCategory) Get(name string) ([]byte, error) {
	data, err := templateFS.ReadFile(path.Join(c.basePath, filepath.ToSlash(name)))
	if err != nil {
		return nil, err
	}
	return stripBuildIgnore(data), nil
}

// selfDefaulting is a payload that can fill its own optional fields before
// rendering. Declared HERE, at the consumer, and deliberately one method wide:
// Render's whole knowledge of the idea is "ask the payload to complete itself,"
// with no notion of what any payload contains.
//
// Any future payload opts in by growing the method; Render never changes.
type selfDefaulting interface {
	withDefaults() interface{}
}

// Render executes a template with the given data and returns the result.
//
// Deliberately generic: it renders eleven unrelated template categories and
// knows nothing about any one payload's shape. A type switch here on one
// specific struct would make the shared renderer a place every domain is
// tempted to special-case. Payload defaulting belongs to the payload — see
// FrontendTemplateData.withDefaults.
func (c TemplateCategory) Render(name string, data interface{}) ([]byte, error) {
	if d, ok := data.(selfDefaulting); ok {
		data = d.withDefaults()
	}
	return RenderFromFS(templateFS, c.basePath, name, data)
}

// List returns all template names in the category (recursive).
func (c TemplateCategory) List(subdir string) ([]string, error) {
	return listTemplates(path.Join(c.basePath, subdir), true)
}

// ListFlat returns only direct children (non-recursive).
func (c TemplateCategory) ListFlat(subdir string) ([]string, error) {
	return listTemplates(path.Join(c.basePath, subdir), false)
}

// Category accessors return a TemplateCategory rooted at the named
// embedded template directory.
//
// These were package-level vars in forge; they were converted to functions
// to satisfy `contracts.allow_exported_vars: false` while keeping the
// call-site shape close to the original (`ProjectTemplates()` instead of
// `ProjectTemplates`). TemplateCategory is a value type with no internal
// state, so reconstructing on each call is free.

// ProjectTemplates returns the category for project-scaffold templates.
func ProjectTemplates() TemplateCategory { return TemplateCategory{basePath: "project"} }

// FrontendTemplates returns the category for frontend-scaffold templates.
func FrontendTemplates() TemplateCategory { return TemplateCategory{basePath: "frontend"} }

// DeployTemplates returns the category for deploy-scaffold templates.
func DeployTemplates() TemplateCategory { return TemplateCategory{basePath: "deploy"} }

// IngressTemplates returns the embedded Gateway API install assets
// (the Envoy Gateway VERSION pins + the vendored `eg` GatewayClass)
// keyed under `internal/templates/ingress/<provider>/`. The Envoy
// Gateway controller is installed via `helm` from the pinned
// gateway-helm chart, and the Gateway API CRDs are fetched from the
// upstream GitHub release at `forge cluster up` time and cached under
// ~/.cache/forge/ — see internal/cli/dev_cluster_ingress.go.
func IngressTemplates() TemplateCategory { return TemplateCategory{basePath: "ingress"} }

// TestTemplates returns the category for test-scaffold templates.
func TestTemplates() TemplateCategory { return TemplateCategory{basePath: "test"} }

// InternalPkgTemplates returns the category for shared internal-package templates.
func InternalPkgTemplates() TemplateCategory {
	return TemplateCategory{basePath: "internal-package"}
}

// ServiceTemplates returns the category for service-scaffold templates.
func ServiceTemplates() TemplateCategory { return TemplateCategory{basePath: "service"} }

// WebhookTemplates returns the category for webhook-scaffold templates.
func WebhookTemplates() TemplateCategory { return TemplateCategory{basePath: "webhook"} }

// WorkerTemplates returns the category for worker-scaffold templates.
func WorkerTemplates() TemplateCategory { return TemplateCategory{basePath: "worker"} }

// WorkerCronTemplates returns the category for worker-cron-scaffold templates.
func WorkerCronTemplates() TemplateCategory { return TemplateCategory{basePath: "worker-cron"} }

// OperatorTemplates returns the category for operator-scaffold templates.
func OperatorTemplates() TemplateCategory { return TemplateCategory{basePath: "operator"} }

// CITemplates returns a TemplateCategory for a specific CI provider.
func CITemplates(provider string) TemplateCategory {
	return TemplateCategory{basePath: path.Join("ci", provider)}
}

// InternalPkgKindTemplates returns a TemplateCategory for a specific
// internal-package kind subdirectory.
func InternalPkgKindTemplates(kind string) TemplateCategory {
	return TemplateCategory{basePath: path.Join("internal-package", kind)}
}

// ListInternalPackageKindTemplates lists template files for a specific
// internal-package kind subdirectory (e.g. "client", "eventbus").
func ListInternalPackageKindTemplates(kind string) ([]string, error) {
	return InternalPkgKindTemplates(kind).ListFlat("")
}

// RenderInternalPackageKindTemplate renders a template from a kind subdirectory
// of the internal-package templates.
func RenderInternalPackageKindTemplate(kind, name string, data interface{}) ([]byte, error) {
	return InternalPkgKindTemplates(kind).Render(name, data)
}

// RenderInternalPackageTemplate renders a base internal-package template.
func RenderInternalPackageTemplate(name string, data interface{}) ([]byte, error) {
	return InternalPkgTemplates().Render(name, data)
}

// RenderFromFS renders a template from an arbitrary fs.FS. It reads the file at
// basePath/name, and if the name has a .tmpl suffix it parses and executes it
// with the shared FuncMap. Non-.tmpl files are returned as-is. Any //go:build
// ignore directives are stripped from the output.
//
// This is the canonical template-rendering function used by both the built-in
// template helpers and the pack system.
func RenderFromFS(fsys fs.FS, basePath, name string, data interface{}) ([]byte, error) {
	content, err := fs.ReadFile(fsys, path.Join(basePath, filepath.ToSlash(name)))
	if err != nil {
		return nil, fmt.Errorf("read template %s: %w", name, err)
	}

	if !strings.HasSuffix(name, ".tmpl") {
		return stripBuildIgnore(content), nil
	}

	content = stripBuildIgnore(content)

	tmpl, err := template.New(name).Funcs(FuncMap()).Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("parse template %s: %w", name, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute template %s: %w", name, err)
	}

	return stripBuildIgnore(buf.Bytes()), nil
}

// listTemplates walks the embedded template FS and returns template names under root.
// If recursive is true, it walks subdirectories. Otherwise, only lists direct children.
//
// root may be spelled with the host separator, as Get's name may: an embed.FS
// path is always slash-separated, so a filepath.Join-built root named no
// directory on Windows.
func listTemplates(root string, recursive bool) ([]string, error) {
	root = filepath.ToSlash(root)
	entries, err := templateFS.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read template dir %s: %w", root, err)
	}

	var files []string
	var walk func(dir string, entries []fs.DirEntry) error
	walk = func(dir string, entries []fs.DirEntry) error {
		for _, e := range entries {
			rel := path.Join(dir, e.Name())
			if e.IsDir() {
				if !recursive {
					continue
				}
				sub, err := templateFS.ReadDir(path.Join(root, rel))
				if err != nil {
					return err
				}
				if err := walk(rel, sub); err != nil {
					return err
				}
			} else {
				files = append(files, rel)
			}
		}
		return nil
	}

	if err := walk("", entries); err != nil {
		return nil, err
	}
	return files, nil
}

// CIWorkflowData holds data for the spec-driven CI workflow template.
type CIWorkflowData struct {
	ProjectName  string
	HasFrontends bool
	Frontends    []FrontendCIConfig // from project config
	HasServices  bool

	// Lint
	LintGolangci        bool
	LintBuf             bool
	LintBufBreaking     bool
	LintFrontend        bool
	LintFrontendStyles  bool
	LintMigrationSafety bool

	// Test
	TestRace     bool
	TestCoverage bool

	// Vuln scan
	VulnGo     bool // govulncheck
	VulnDocker bool // trivy
	VulnNPM    bool // npm audit

	// License compliance
	LicenseCheck bool // go-licenses

	// E2E
	E2EEnabled bool
	E2ERuntime string // "docker-compose" or "k3d"

	// Permissions
	PermContents string // default "read"

	// There is deliberately no extra-jobs field: nothing ever populated it
	// (forge.yaml's ci.extra_jobs was removed for exactly that reason), and
	// adding a job is one edit to the ci.yml the user already owns.

	// Deploy-related
	HasKCL    bool // validate KCL manifests
	HasDocker bool // emit docker-build job (project has a Dockerfile)
	// VerifyGenerated controls whether the verify-generated job is emitted.
	VerifyGenerated bool
	// RunsBufGenerate is true when `forge generate` runs `buf generate` on
	// this project. verify-generated reruns generate, so it installs buf
	// exactly then. The mapper sets it from the generate pipeline's own
	// gate condition, whatever the project kind.
	RunsBufGenerate bool

	// Environments (for KCL validation)
	Environments []string

	// Legacy fields used by other CI templates (build-images, deploy, dependabot)
	Module       string
	FrontendName string // first frontend name for dependabot

	// GitHubOwner is the inferred GitHub owner for default CODEOWNERS entries.
	// Empty when we couldn't confidently infer one (e.g. non-github module
	// path), in which case the CODEOWNERS template emits no file contents and
	// the generator drops the file. Populated by the generator from the
	// project's module path (e.g. "github.com/example/demo" -> "example").
	GitHubOwner string

	// There is deliberately no forge-version field. Every job that installs
	// forge reads the version from the project AT RUN TIME (go.mod, falling
	// back to forge.yaml's forge_version) — see the "install-forge" block in
	// ci.yml.tmpl. The workflow is scaffold-once, so a version stamped here
	// froze at scaffold time and drifted from go.mod on every forge bump.

	// GolangciLintVersion is the golangci-lint release the lint job pins.
	// Left empty by every caller and filled by withDefaults with
	// GolangciLintVersion, so a new construction site cannot ship `latest`.
	GolangciLintVersion string

	// BufVersion is the buf release the checks job installs. Filled by
	// withDefaults with BufVersion for the same reason.
	BufVersion string
}

// withDefaults satisfies selfDefaulting. An explicit GolangciLintVersion
// still wins; only the zero value is filled.
func (d CIWorkflowData) withDefaults() interface{} {
	if d.GolangciLintVersion == "" {
		d.GolangciLintVersion = GolangciLintVersion
	}
	if d.BufVersion == "" {
		d.BufVersion = BufVersion
	}
	return d
}

// FrontendCIConfig is a minimal frontend descriptor for CI templates.
type FrontendCIConfig struct {
	Name string
	Path string
}

// DeployEnv represents a single deploy environment (e.g. staging, prod).
type DeployEnv struct {
	Name       string // "staging", "preprod", "prod"
	Auto       bool   // auto-deploy after image build
	Protection bool   // GitHub environment protection
	URL        string // environment URL
}

// DeployWorkflowData holds data for the deploy workflow template.
type DeployWorkflowData struct {
	ProjectName string
	// Environments are the envs the project DECLARES (deploy/kcl/<env>/main.k,
	// dev excluded), ordered along the promotion path. Never a hard-coded
	// list: a job for an env with no main.k fails on every run.
	Environments []DeployEnv
	HasFrontends bool
	// FrontendPath is the first frontend's directory: setup-node reads its
	// package.json `engines.node`, and `npm ci` there installs what
	// `forge build <env>` needs to build it.
	FrontendPath     string
	FrontendDeploy   string // "firebase", "vercel", "none"
	MigrationTest    bool   // test migrations before deploy
	Concurrency      bool   // per-env concurrency groups
	CancelInProgress bool
}

// BuildImagesWorkflowData holds data for the build-images workflow template.
type BuildImagesWorkflowData struct {
	ProjectName string
	// BuildEnv is the env the once-per-commit image is built for: the first
	// deploy env in promotion order, whose KCL-declared registry the image
	// is pushed to. The workflow names no registry — forge reads it from
	// deploy/kcl/<BuildEnv>/main.k.
	BuildEnv string
	// No frontend fields: frontend IMAGES are per-env (each renders that
	// env's config.js), so deploy.yml builds them, and `forge build <env>
	// --target <project>` builds the project image alone — no frontend's
	// `npm run build` — so this job needs no Node toolchain.
	VulnDocker bool // trivy scanning

	// Hosted says BuildEnv pushes to the PLATFORM registry, which changes
	// what the job has to supply: nothing. forge authenticates that registry
	// with the control-plane credential it already resolves (ADR-0003 F3), so
	// a hosted job carries FORGE_CONTROL_PLANE_TOKEN — which it needs anyway
	// — and no registry credential, no --username and no --password-stdin.
	//
	// It is a per-env fact and not a project-wide one: a project may build
	// its per-commit image for a cluster env and promote releases through
	// hosted ones, and only the env this job builds for decides.
	Hosted bool
}

// ReleaseWorkflowData holds data for release.yml and the vendored
// forge-deploy composite action. Emitted only
// for a project with at least one HOSTED env — one whose KCL declares
// forge.ControlPlane and a workload or database the platform runs.
type ReleaseWorkflowData struct {
	ProjectName string
	// BuildEnv is the hosted env the release's images are built and cut
	// against (the first in promotion order): its declared registry and
	// control plane are where the bytes and the release live.
	BuildEnv string
	// Stages are the hosted envs, in promotion order, one job each. The
	// first is promoted by version; every later one `--from` the stage
	// before it, so a release reaches prod exactly as it ran on the stage
	// before. Prev/Next are precomputed here rather than derived with index
	// arithmetic in YAML, so the chain is testable Go and the template stays
	// readable.
	Stages []ReleaseStage
}

// ReleaseStage is one hosted env's deploy job in release.yml.
type ReleaseStage struct {
	Env DeployEnv
	// Prev is the stage promoted from (empty for the first, which is
	// promoted by version). Its job's promotion id is --from-promotion.
	Prev string
	// Next is the stage after this one (empty for the last). This job
	// captures Next's current promotion BEFORE Next's approval wait, so
	// Next's deploy can assert it with --expect-current.
	Next string
	// Mixed is set for an env that also applies a cluster/compose part from
	// CI. Its stage always deploys client-side — a control plane converges
	// only what it hosts — and writes the kubeconfig that deploy needs.
	Mixed bool
}

// ReleaseStages chains hosted envs (already in promotion order) into stages.
// mixed names the envs that also apply a part from CI (see ReleaseStage.Mixed).
func ReleaseStages(envs []DeployEnv, mixed map[string]bool) []ReleaseStage {
	out := make([]ReleaseStage, len(envs))
	for i, e := range envs {
		out[i].Env = e
		out[i].Mixed = mixed[e.Name]
		if i > 0 {
			out[i].Prev = envs[i-1].Name
		}
		if i+1 < len(envs) {
			out[i].Next = envs[i+1].Name
		}
	}
	return out
}

// E2EWorkflowData holds data for the standalone E2E test workflow template.
type E2EWorkflowData struct {
	ProjectName  string
	Runtime      string // "docker-compose" (default) or "k3d"
	HasFrontends bool
	// FrontendPath points the setup-node `node-version-file` input at a
	// package.json whose `engines.node` is honored by setup-node@v4. Empty
	// when there are no frontends — the template then falls back to a
	// fixed node-version.
	FrontendPath string
}

// NavPageData describes a single page entry for the frontend navigation.
type NavPageData struct {
	Label      string // display name, e.g. "Tasks"
	LabelLower string // lowercase for descriptions, e.g. "tasks"
	Slug       string // URL path segment, e.g. "tasks"
	// LabelSingular is the singular display name ("Task") — used by
	// "Create X" buttons. Derived from the proto entity name, not a
	// strip-the-trailing-s guess ("Categories" → "Category", not
	// "Categorie").
	LabelSingular string
	// HasCreate reports whether the page generator emitted a create page
	// (<slug>/new) for this entity. The dashboard's QuickActions grid
	// gates its "Create X" buttons on this — advertising a create route
	// the page generator never wrote is a guaranteed 404.
	HasCreate bool
	// ListHook is the generated React Query list hook name
	// ("useListTasks"); the dashboard tile uses it to render a real
	// entity count instead of a placeholder.
	ListHook string
	// HooksModule is the import path of the generated hooks file, e.g.
	// "@/hooks/task-service-hooks".
	HooksModule string
	// ItemsField is the camelCase repeated field on the list response
	// holding the entities ("tasks").
	ItemsField string
	// HasTotalCount / TotalCountField expose the List response's server-side
	// total ("totalCount") when the RPC declares one. The dashboard tile
	// counts with THIS, never with the length of the page it fetched: a
	// tile reading `data.tasks.length` reports the page size once the table
	// outgrows one page ("25" forever), and it is forge's own generated
	// exemplar that teaches the habit to everything downstream.
	HasTotalCount   bool
	TotalCountField string
	// HasPageSize reports whether the List request carries page_size. The
	// tile asks for ONE row when it does — the count comes from
	// total_count, so pulling a full page of entities to display a single
	// number is waste on every dashboard load.
	HasPageSize bool
	// ComponentIdent is a valid TS identifier for per-entity generated
	// components ("Tasks" → TasksTile).
	ComponentIdent string
}

// DependabotData renders ci/github/dependabot.yml.tmpl.
type DependabotData struct {
	// FrontendName is the first frontend's name. Empty emits no npm entry.
	FrontendName string
	// RunsProtocGenGo is true when `forge generate` runs protoc-gen-go —
	// a service-kind project with codegen on. Only then does a
	// google.golang.org/protobuf bump change generated output, so only
	// then is it kept out of Dependabot.
	RunsProtocGenGo bool
}

// NavHookImport is one merged import statement the dashboard template
// emits for list hooks: all hook symbols whose generated hooks file is
// the same module. Pre-grouped in Go so two entities served by one
// service don't produce duplicate import statements (import/no-duplicates).
type NavHookImport struct {
	Module  string   // "@/hooks/task-service-hooks"
	Symbols []string // ["useListTasks", "useListProjects"] — sorted
}

// FrontendTemplateData holds data for frontend template rendering.
type FrontendTemplateData struct {
	FrontendName string
	ProjectName  string
	// Platform names the frontend template tree being rendered —
	// "nextjs", "vite-spa" or "react-native". It exists so a template
	// under frontend/shared* (rendered into every tree that claims it)
	// can gate the one or two lines that genuinely differ per platform
	// instead of being forked into N near-identical copies. Today the
	// only gate is the Next.js App Router's `"use client"` prologue.
	//
	// Set by the frontend scaffolder from the resolved tree name, so it
	// cannot drift from the roots being composed. Empty when a caller
	// renders a single per-kind template directly (nav, dashboard,
	// apiurl_gen, …) — those live under one tree and never gate on it.
	Platform string
	// APIURL and APIPackage are exposed to text/template files under
	// internal/templates/frontend/, so their Go field names are the
	// template variable names: a customized .tmpl referencing {{.ApiUrl}}
	// must be updated to {{.APIURL}}. forge carries no backwards
	// compatibility — the rename is the record.
	//
	// (ApiPort was removed when the hand-editable .env.local scaffolds were
	// ripped out — the only consumers were those templates'
	// `http://localhost:{{.ApiPort}}` lines; the regenerated apiurl_gen.ts
	// bakes the full APIURL instead.)
	APIURL string
	Module string
	Pages  []NavPageData
	// HasTypedConfig reports whether this frontend has a config message
	// bound to it by (forge.v1.frontend_config). When true, templates read
	// their settings through the generated src/lib/config_gen.ts — values
	// delivered at RUNTIME by the KCL-rendered config.js — instead of
	// build-time-inlined NEXT_PUBLIC_* / VITE_* env vars.
	//
	// It gates rather than replaces because frontend config is OPT-IN: a
	// project that declares no frontend-bound message keeps the previous
	// env-var behavior byte-for-byte, so adopting this is a proto edit and
	// nothing else.
	HasTypedConfig bool
	// Has<Field> reports which well-known fields that message actually
	// declares. A template cannot reference cfg.MOCK_API when the proto
	// never declared it — the generated module's type would not have the
	// key and `tsc` would fail — so each read is gated on its own field
	// being present.
	//
	// There is no OIDC gate here, and that is deliberate: browser sign-in
	// is NATIVE (credentials POSTed to this app's own API, HttpOnly cookie
	// back, server runs the OIDC flow), so no issuer, client id, redirect
	// URI, scope list or resource indicator is ever delivered to a bundle.
	// Handing the browser those values is what would let a browser-side
	// PKCE flow — and a script-readable token — come back.
	HasMockAPI bool
	// NavHookImports is the per-module aggregation of the list hooks the
	// dashboard tiles consume. Derived from Pages by the nav generator.
	NavHookImports []NavHookImport
	// Workspaces reports whether the project opted into the pnpm-
	// workspaces layout. When true, frontend templates emit imports
	// of APIPackage / HooksPackage instead of relative @/gen and
	// @/hooks paths, and the per-frontend package.json declares
	// "workspace:*" deps on those packages.
	//
	// When false (the default), templates produce the historic
	// single-frontend output — byte-identical to projects scaffolded
	// before workspaces landed. Snapshot tests rely on this.
	Workspaces bool
	// WebRuntimeTypePins are the packages a frontend's tsconfig must pin
	// to its OWN copy, so tsc sees one identity per type.
	//
	// A plain []string, not a type from another package: this struct is a
	// TEMPLATE PAYLOAD, so its fields are the values the templates render
	// and nothing more. Whoever computes the list owns its meaning; this
	// package only writes it out.
	//
	// Left nil by every caller today, and filled by withDefaults. It stays
	// a field rather than a method so a caller CAN pin a different set —
	// which is what makes the default a default and not a wall.
	WebRuntimeTypePins []string
	// WebRuntimePinsHoisted selects WHICH node_modules the pins above point
	// at: the project root's (true) or the frontend's own (false, the
	// default).
	//
	// It exists because a `paths` value for a non-wildcard key must hold
	// EXACTLY ONE element — SWC panics `next build` otherwise — so the
	// emitter cannot list both layouts and let the toolchain choose. See
	// webruntimepeers.TypePinPath.
	//
	// True when an npm workspace hoists the frontend's dependencies to the
	// project root, which is what forge's own dev bridge creates.
	WebRuntimePinsHoisted bool
	// APIPackage is the npm package name for the shared Connect TS
	// clients workspace, e.g. "@myapp/api". Empty when Workspaces is
	// false.
	APIPackage string
	// HooksPackage is the npm package name for the shared React Query
	// hooks workspace, e.g. "@myapp/hooks". Empty when Workspaces is
	// false.
	HooksPackage string
	// UIWebPackage is the npm package name for the shared web UI
	// component workspace, e.g. "@myapp/ui-web". Empty when Workspaces
	// is false. Frontend package.json templates declare a
	// "workspace:*" dep on this, and tsconfig templates redirect
	// `@/components/*` paths into the package via path mapping.
	UIWebPackage string
	// UINativePackage is the npm package name for the shared React
	// Native primitives workspace, e.g. "@myapp/ui-native". Empty
	// when Workspaces is false OR the project has no RN frontend.
	// React Native templates reference this in their package.json
	// workspace deps and example screens.
	UINativePackage string
	// Output selects the Next.js build/runtime shape. Mirrors
	// config.FrontendConfig.Output. Only the nextjs templates read it
	// (`next.config.ts.tmpl`); other template trees ignore it.
	//
	// Valid values rendered by templates:
	//   - "static":     emit `output: "export"` gated on
	//     NODE_ENV=production (the scaffold default; callers resolve it
	//     before rendering). Dev server stays unchanged. The generated
	//     CRUD pages are static routes, so the export builds.
	//   - "standalone": emit `output: "standalone"` unconditionally —
	//     the Node server the standalone Dockerfile runs.
	//   - "server":     omit `output` entirely (full Next.js dev+prod).
	//
	// Empty string renders the standalone arm, which is what an empty
	// forge.yaml `output:` has always meant
	// (config.FrontendConfig.EffectiveOutput). A NEW frontend never
	// reaches the template empty: GenerateFrontendFilesWithOptions
	// resolves "" to config.FrontendOutputScaffoldDefault first.
	Output string
	// BasePath mirrors config.FrontendConfig.BasePath — the URL prefix
	// the frontend is mounted under (e.g. "/admin"), or "" for root.
	// Read by the nextjs templates only:
	//
	//   - next.config.ts.tmpl renders it as the build-time default for
	//     `basePath` + `assetPrefix` (overridable via the single
	//     canonical env var NEXT_PUBLIC_BASE_PATH).
	//   - src/lib/basepath_gen.ts.tmpl bakes it as the fallback for
	//     BASE_PATH / joinBasePath().
	//   - Dockerfile mounts a static export under it, as the StaticSite
	//     deploy does.
	//
	// Already validated by config.LoadStrict (leading "/", no trailing
	// "/", [A-Za-z0-9._-] segments) — templates splice it verbatim into
	// TypeScript string literals.
	BasePath string
	// RepoPath is the frontend's directory relative to the project root
	// ("frontends/web"), slash-separated. The nextjs Dockerfile builds at
	// /src/<RepoPath> so every relative path the committed config carries —
	// tsconfig's `../../node_modules/…` pins above all — means inside the
	// image what it means in the repository. Empty falls back to
	// frontends/<FrontendName>, where the scaffolder writes every frontend.
	RepoPath string
	// Public renders the frontend WITHOUT a sign-in gate (auth_mode: none):
	// providers.tsx / routes.tsx mount no RouteGuard and the Vite tree
	// declares no /auth/sign-in route. See generator.FrontendGenOptions.Public.
	Public bool
}

// withDefaults fills the optional fields a caller may leave zero, and
// satisfies selfDefaulting so Render applies it without knowing this type.
//
// WebRuntimeTypePins is defaulted rather than required because there are five
// construction sites, and one that forgot the field would emit a tsconfig with
// NO peer pins — which typechecks green on a registry install and fails only
// under a dev forge build. That is the silent drift the derived list exists to
// end, so "forgot it" must not be expressible. An explicit value still wins.
func (d FrontendTemplateData) withDefaults() interface{} {
	if d.WebRuntimeTypePins == nil {
		d.WebRuntimeTypePins = webruntimepeers.TypePins()
	}
	// The same "forgot it" argument, for the Dockerfile: a payload without
	// a build path would render `WORKDIR /src/` and quietly bring back the
	// /app-shaped build the field exists to end.
	if d.RepoPath == "" && d.FrontendName != "" {
		d.RepoPath = "frontends/" + d.FrontendName
	}
	return d
}

// WebhookTemplateData holds data for webhook template rendering.
type WebhookTemplateData struct {
	Name           string // webhook name (e.g. "stripe", "github")
	ServiceName    string // target service display name (may contain hyphens)
	ServicePackage string // Go-package-safe form of ServiceName (snake_case)
	Module         string // Go module path
	// ConfigImport is the import path of the config the service's Deps
	// takes (`Config *config.Config`), "" when it takes none; the test
	// passes a Config only then. ConfigImportAlias is "config" when the
	// path's last segment is not, so the test can spell config.Config.
	ConfigImport      string
	ConfigImportAlias string
}

// WebhookRoutesTemplateData holds data for the webhook_routes_gen.go template.
type WebhookRoutesTemplateData struct {
	Package  string                  // Go package name (e.g. "billing")
	Webhooks []WebhookRouteEntryData // all webhooks for this service
}

// WebhookRouteEntryData holds per-webhook data for route generation.
type WebhookRouteEntryData struct {
	Name       string // kebab-case name for the URL path (e.g. "stripe")
	PascalName string // PascalCase name for the handler method (e.g. "Stripe")
}

// TemplateEngine handles code generation from service/middleware templates.
// NOTE: TemplateEngine pre-parses templates for reuse via a singleton (see generator/project.go),
// while the TemplateCategory.Render method parses on each call. Both share FuncMap().
// Consider consolidating if this becomes a maintenance burden.
type TemplateEngine struct {
	templates map[string]*template.Template
	funcMap   template.FuncMap
}

// NewTemplateEngine creates a new template engine with all service and
// middleware templates pre-loaded.
func NewTemplateEngine() (*TemplateEngine, error) {
	engine := &TemplateEngine{
		templates: make(map[string]*template.Template),
		funcMap:   FuncMap(),
	}

	if err := engine.loadTemplates(); err != nil {
		return nil, err
	}

	return engine, nil
}

// loadTemplates loads all embedded service and middleware templates.
func (e *TemplateEngine) loadTemplates() error {
	templateFiles := []string{
		"service/service.go.tmpl",
		"service/handlers.go.tmpl",
		"service/unit_test.go.tmpl",
		"worker/worker.go.tmpl",
		"worker/worker_test.go.tmpl",
		"worker-cron/worker.go.tmpl",
		"worker-cron/worker_test.go.tmpl",
		"operator/types.go.tmpl",
		"operator/controller.go.tmpl",
		"operator/controller_test.go.tmpl",
		"crd/groupversion.go.tmpl",
		"crd/types.go.tmpl",
		"crd/controller.go.tmpl",
		"crd/controller_test.go.tmpl",
		"crd/crd.k.tmpl",
		"function/function.go.tmpl",
		"function/function_test.go.tmpl",
	}

	for _, file := range templateFiles {
		content, err := templateFS.ReadFile(file)
		if err != nil {
			// Template doesn't exist yet, skip
			continue
		}
		content = stripBuildIgnore(content)

		tmpl, err := template.New(file).Funcs(e.funcMap).Parse(string(content))
		if err != nil {
			return fmt.Errorf("failed to parse template %s: %w", file, err)
		}

		e.templates[file] = tmpl
	}

	return nil
}

// RenderTemplate renders a template with the given data.
func (e *TemplateEngine) RenderTemplate(name string, data interface{}) (string, error) {
	tmpl, ok := e.templates[name]
	if !ok {
		return "", fmt.Errorf("template %s not found", name)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template %s: %w", name, err)
	}

	return string(stripBuildIgnore(buf.Bytes())), nil
}

// Case conversion functions

// hyphenToUnderscore replaces hyphens with underscores.
// Used in templates for proto package names where hyphens aren't valid.
func hyphenToUnderscore(s string) string {
	return strings.ReplaceAll(s, "-", "_")
}

func toCamelCase(s string) string {
	if s == "" {
		return s
	}
	// Handle PascalCase input (no underscores): just lowercase the first letter.
	if !strings.Contains(s, "_") {
		runes := []rune(s)
		runes[0] = unicode.ToLower(runes[0])
		return string(runes)
	}
	// Handle snake_case input: capitalize each part except the first.
	caser := cases.Title(language.English)
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		parts[i] = caser.String(parts[i])
	}
	return strings.Join(parts, "")
}

func toPascalCase(s string) string {
	return naming.ToPascalCase(s)
}

func toKebabCase(s string) string {
	return strings.ReplaceAll(s, "_", "-")
}

func pluralize(s string) string {
	return naming.Pluralize(s)
}

func singularize(s string) string {
	if len(s) == 0 {
		return s
	}
	return inflection.Singular(s)
}

func formatComment(s string) string {
	if s == "" {
		return ""
	}
	return "// " + s
}

func getDefault(defaultValue interface{}, actualValue interface{}) interface{} {
	if actualValue == nil {
		return defaultValue
	}
	v := reflect.ValueOf(actualValue)
	if v.IsZero() {
		return defaultValue
	}
	return actualValue
}

func add(a, b int) int {
	return a + b
}

func lastStringSlice(i int, slice interface{}) bool {
	switch s := slice.(type) {
	case []string:
		return i == len(s)-1
	default:
		return false
	}
}

func tableFromFK(fk string) string {
	parts := strings.Split(fk, ".")
	if len(parts) > 0 {
		return parts[0]
	}
	return fk
}

func columnFromFK(fk string) string {
	parts := strings.Split(fk, ".")
	if len(parts) > 1 {
		return parts[1]
	}
	return "id"
}
