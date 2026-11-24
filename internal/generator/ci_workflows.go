package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/internal/toolpin"
)

// CIWorkflowFile is one scaffolded CI file: the template that renders it,
// where it lands, and the data it renders with.
type CIWorkflowFile struct {
	Template string
	Dest     string
	Data     any
}

// Render renders the file from its provider's templates
// (internal/templates/ci/<provider>/): the provider whose files include its
// Dest (CIProviderOfPath).
func (f CIWorkflowFile) Render() ([]byte, error) {
	return f.category().Render(f.Template, f.Data)
}

// SourcePath names the template file Render reads (templates.SourcePath).
func (f CIWorkflowFile) SourcePath() string { return f.category().SourcePath(f.Template) }

// category is the provider template category whose files include f.Dest.
func (f CIWorkflowFile) category() templates.TemplateCategory {
	provider := config.CIProviderGitHub
	if isReliantCIPath(f.Dest) {
		provider = config.CIProviderReliant
	}
	return templates.CITemplates(provider)
}

// CIWorkflows is THE mapping from a project to its scaffolded GitHub Actions
// files. `forge project new` (generateCIFiles) and `forge generate`
// (internal/cli's generateCIWorkflows) both call it; neither builds template
// data of its own.
//
// There used to be two mappers, one per command, and they disagreed on a
// project seconds old. houndersclub deleted ci.yml to pick up forge v0.1.18's
// template, re-ran `forge generate`, and lost the whole Docker Build job: the
// generate-side mapper never set HasDocker. It also dropped e2e.yml, the
// reconcile workflow, and — on a project without a service proto yet — every
// buf step, even though `forge generate` runs `buf generate` on that very
// project. TestCIWorkflows_NewAndGenerateRenderIdentically pins the two
// commands to identical bytes so a second mapper cannot grow back.
//
// root is the project directory, read for the facts that live on disk: the
// declared deploy envs (deploy/kcl/<env>/main.k) and the e2e harness (e2e/).
// cfg is the project config with derived defaults applied — what the loader
// returns, or what `forge project new` is about to write. frontends are the
// repo-relative frontend directories CI drives a Node toolchain for; the
// caller supplies them because only it knows the authoritative source (the
// KCL topology on a live project, the requested frontend at scaffold time).
func CIWorkflows(root string, cfg *config.ProjectConfig, frontends []templates.FrontendCIConfig) []CIWorkflowFile {
	return CIWorkflowsFor(root, cfg, CIInputs{Frontends: frontends})
}

// CIInputs are the facts only the caller can answer, because they come from a
// KCL render of a live project (a freshly scaffolded one has no topology yet).
type CIInputs struct {
	// Frontends are the repo-relative frontend directories CI drives a Node
	// toolchain for.
	Frontends []templates.FrontendCIConfig
	// HostedEnvs are the declared envs a control plane RUNS (forge.ControlPlane
	// plus a hosted workload or database). Non-empty ⇒ release.yml and the
	// vendored forge-deploy action are scaffolded, and deploy.yml stops
	// rebuilding per env for them (release.yml builds once and deploys that
	// one release through each of them).
	HostedEnvs []string
	// MixedEnvs is the subset of HostedEnvs that ALSO apply a part from CI —
	// a cluster or compose workload, a cluster database, infra. Their
	// release.yml stage always deploys client-side (a control plane never
	// converges a cluster half) and carries the kubeconfig step.
	MixedEnvs []string
	// PGOEnvs is the subset of HostedEnvs that build a Go binary `forge pgo
	// pull --env <env>` writes a profile for: the binaries pull itself
	// resolves from the env's render (internal/cli ResolvePGOBinaries), PGO
	// on, main package in this checkout. pgo-refresh.yml pulls the last of
	// them in promotion order, and a project with none — a hosted env that
	// only serves a static site — gets no pgo-refresh.yml.
	PGOEnvs []string
	// Buf, when set, is the buf the project pins, stated by a caller that
	// knows it without asking the module graph. Nil resolves it from root's
	// go.mod (toolpin.BufFor), which execs `go list -m`.
	Buf *toolpin.Buf
}

// CIWorkflowDataFor returns what the scaffolded ci.yml renders with: which lints,
// scanners and jobs this project has. `forge ci gate` plans its checks from
// the same struct, so a forge.yaml toggle means the same thing to the workflow
// and to the local gate.
//
// It reads the full set, ci.workflows notwithstanding: a project that writes
// its own ci.yml (`ci.workflows: {ci: false}`) still has a gate to plan.
func CIWorkflowDataFor(root string, cfg *config.ProjectConfig, in CIInputs) templates.CIWorkflowData {
	for _, f := range ciWorkflowsAll(root, cfg, in) {
		if f.Dest == ".github/workflows/ci.yml" {
			return f.Data.(templates.CIWorkflowData)
		}
	}
	return templates.CIWorkflowData{}
}

// CIWorkflowsFor is CIWorkflows with every caller-supplied input, minus the
// workflows forge.yaml's `ci.workflows` switches off. Every writer — `forge
// project new`, `forge generate`, rescaffold and the upgrade advisory — goes
// through it, so a switched-off workflow is one forge never offers anywhere.
//
// It answers for the project's `ci.provider`: GitHub Actions files for
// github, the Reliant workflows under .reliant/workflows/ for reliant
// (reliantCIWorkflows), and nothing for a provider forge has no templates
// for. A project that switches provider keeps the other provider's files —
// they are scaffold-once and the user's — but forge offers only the new ones.
func CIWorkflowsFor(root string, cfg *config.ProjectConfig, in CIInputs) []CIWorkflowFile {
	var all []CIWorkflowFile
	switch cfg.CI.EffectiveProvider() {
	case config.CIProviderGitHub:
		all = ciWorkflowsAll(root, cfg, in)
	case config.CIProviderReliant:
		all = reliantCIWorkflows(cfg)
	}
	out := all[:0:0]
	for _, f := range all {
		if cfg.CI.WorkflowEnabled(CIWorkflowName(f.Dest)) {
			out = append(out, f)
		}
	}
	return out
}

// CIWorkflowName is the `ci.workflows` switch that governs relPath: the
// workflow file's name without .yml, "release" for the vendored forge-deploy
// action release.yml calls, "dependabot" for .github/dependabot.yml, a
// Reliant workflow's name without .yaml (.reliant/workflows/ci.yaml is "ci"),
// and "" for a path the CI mapper does not own.
func CIWorkflowName(relPath string) string {
	rel := filepath.ToSlash(relPath)
	switch {
	case rel == ForgeDeployActionPath:
		return "release"
	case rel == ".github/dependabot.yml":
		return "dependabot"
	case strings.HasPrefix(rel, ".github/workflows/") && strings.HasSuffix(rel, ".yml"):
		return strings.TrimSuffix(strings.TrimPrefix(rel, ".github/workflows/"), ".yml")
	case strings.HasPrefix(rel, reliantWorkflowDir) && strings.HasSuffix(rel, ".yaml"):
		return strings.TrimSuffix(strings.TrimPrefix(rel, reliantWorkflowDir), ".yaml")
	}
	return ""
}

// ciWorkflowsAll is every CI file this project's shape has, before
// ci.workflows.
func ciWorkflowsAll(root string, cfg *config.ProjectConfig, in CIInputs) []CIWorkflowFile {
	frontends := in.Frontends
	isService := cfg.IsServiceKind()
	hasFrontends := isService && len(frontends) > 0
	if !hasFrontends {
		frontends = nil
	}
	// Services — and so buf lint and proto-breaking — are what a
	// service-kind project with codegen on is made of. Deliberately not "a
	// proto declares a service": the default scaffold has none yet.
	hasServices := isService && cfg.Features.CodegenEnabled()
	// `forge generate` runs `buf generate` whenever codegen is on — the gate
	// of internal/cli's "buf generate (Go stubs)" step — whatever the kind:
	// codegen derives from .proto files existing. verify-generated reruns
	// generate, so it needs buf exactly then; a CLI with protos is not a
	// service but still runs buf. TestCIVerifyGeneratedInstallsBufExactlyWhenGenerateRunsIt
	// (internal/cli) holds this to the step's real gate.
	runsBufGenerate := cfg.Features.CodegenEnabled()

	lint := cfg.CI.Lint
	lintDefault := lint == (config.CILintConfig{})
	vuln := cfg.CI.VulnScan
	vulnDefault := vuln.UsesDefaultScanners()
	test := cfg.CI.Test
	testDefault := test == (config.CITestConfig{})

	kclEnvs := declaredKCLEnvs(root)
	deployEnvs := ciDeployEnvs(cfg, kclEnvs)
	// Hosted envs leave deploy.yml: release.yml builds ONCE and deploys
	// the same bytes through them, where deploy.yml would rebuild per env
	// (staging and prod would run different digests). Cluster and compose
	// envs keep today's job unchanged.
	//
	// A MIXED env (hosted AND cluster parts) leaves WHOLE, not split. Its
	// ledger is the control plane, so once promoted, `forge env deploy`
	// pins both halves to the bound release: a deploy.yml rebuild of the
	// cluster half would build, push, and then deploy the OLD release's
	// digests — a green run that shipped nothing. One env, one pipeline.
	hosted := map[string]bool{}
	for _, e := range in.HostedEnvs {
		hosted[e] = true
	}
	mixed := map[string]bool{}
	for _, e := range in.MixedEnvs {
		mixed[e] = true
	}
	var hostedEnvs, clusterEnvs []templates.DeployEnv
	for _, e := range deployEnvs {
		if hosted[e.Name] {
			hostedEnvs = append(hostedEnvs, e)
		} else {
			clusterEnvs = append(clusterEnvs, e)
		}
	}
	// The once-per-commit image is built for the first env deploy.yml
	// deploys — the one it auto-deploys — and pushed to the registry THAT
	// env's KCL declares. A project whose every env is hosted still gets
	// the per-commit build (its signing, provenance and scan run on every
	// push to main), against the first hosted env's registry. With no
	// deploy env there is no registry to push to, so no build-images
	// workflow is emitted.
	var buildEnv string
	switch {
	case len(clusterEnvs) > 0:
		buildEnv = clusterEnvs[0].Name
	case len(deployEnvs) > 0:
		buildEnv = deployEnvs[0].Name
	}
	// Whether THAT env pushes to the platform registry, which decides
	// whether the job supplies a registry credential at all (ADR-0003 F3).
	// Read off buildEnv and not off "the project has a hosted env": a
	// project may build its per-commit image for a cluster env and promote
	// releases through hosted ones, and only the env this job builds for
	// says where its bytes go.
	buildEnvHosted := buildEnv != "" && hosted[buildEnv]
	e2eRuntime := cfg.CI.E2E.Runtime
	if e2eRuntime == "" {
		e2eRuntime = "docker-compose"
	}
	// The e2e workflow runs `task test:e2e` over ./e2e/..., so it exists
	// only where there is a suite to run: a service project that either
	// opted in (ci.e2e.enabled) or carries the generated e2e/ harness. A
	// project with neither would get a job that fails on `lstat ./e2e/`.
	hasE2E := isService && (cfg.CI.E2E.Enabled || isDir(filepath.Join(root, "e2e")))

	var firstFrontendPath, firstFrontendName string
	var firstFrontend templates.FrontendCIConfig
	if hasFrontends {
		firstFrontend = frontends[0]
		firstFrontendPath, firstFrontendName = frontends[0].Path, frontends[0].Name
	}

	// The buf this project pins (go.mod's, else forge's): the one the gate
	// holds PATH to and `forge generate` runs.
	buf := in.Buf
	if buf == nil {
		b := toolpin.BufFor(root)
		buf = &b
	}

	ci := templates.CIWorkflowData{
		ProjectName:  cfg.Name,
		BufVersion:   buf.Bare(),
		HasFrontends: hasFrontends,
		Frontends:    frontends,
		HasServices:  hasServices,

		LintGolangci:        lintDefault || lint.Golangci,
		LintBuf:             hasServices && (lintDefault || lint.Buf),
		LintBufBreaking:     hasServices && (lintDefault || lint.BufBreaking),
		LintFrontend:        hasFrontends && (lintDefault || lint.Frontend),
		LintFrontendStyles:  hasFrontends && (lintDefault || lint.Frontend) && cfg.Lint.Frontend.CSSHealth,
		LintMigrationSafety: isService && (lintDefault || lint.MigrationSafety),

		TestRace:     testDefault || test.Race,
		TestCoverage: test.Coverage,

		VulnGo:     vulnDefault || vuln.Go,
		VulnDocker: isService && (vulnDefault || vuln.Docker),
		VulnNPM:    hasFrontends && (vulnDefault || vuln.NPM),

		LicenseCheck: true,

		E2EEnabled: isService && cfg.CI.E2E.Enabled,
		E2ERuntime: e2eRuntime,

		PermContents: cfg.CI.EffectivePermContents(),

		HasKCL:       isService && len(kclEnvs) > 0,
		Environments: kclEnvs,
		// Every service project ships a Dockerfile, so every one gets the
		// PR-time image build. This is the flag the generate-side mapper
		// never set.
		HasDocker: isService,
		// verify-generated applies to every kind: contract-driven mocks
		// (mock_gen.go) drift silently in CLI and library projects too.
		VerifyGenerated: true,
		RunsBufGenerate: runsBufGenerate,

		Module:       cfg.ModulePath,
		FrontendName: firstFrontendName,
		GitHubOwner:  githubOwnerFromModulePath(cfg.ModulePath),
	}

	files := []CIWorkflowFile{{"ci.yml.tmpl", ".github/workflows/ci.yml", ci}}
	if isService {
		if buildEnv != "" {
			files = append(files, CIWorkflowFile{"build-images.yml.tmpl", ".github/workflows/build-images.yml", templates.BuildImagesWorkflowData{
				ProjectName: cfg.Name,
				BuildEnv:    buildEnv,
				VulnDocker:  ci.VulnDocker,
				Hosted:      buildEnvHosted,
				// What the workflow may run is what the pinned forge has.
				ForgeVersion: cfg.ForgeVersion,
			}})
		}
		// deploy.yml exists while it has an env to deploy. A project whose
		// every env is hosted has none — release.yml is its whole deploy
		// path — and a deploy.yml with no env renders no trigger and no
		// job, which GitHub rejects as an invalid workflow on every push.
		// A project with NO deploy env at all keeps today's placeholder.
		if len(clusterEnvs) > 0 || len(hostedEnvs) == 0 {
			files = append(files,
				CIWorkflowFile{"deploy.yml.tmpl", ".github/workflows/deploy.yml", templates.DeployWorkflowData{
					ProjectName:      cfg.Name,
					Environments:     clusterEnvs,
					HasFrontends:     hasFrontends,
					FrontendPath:     firstFrontendPath,
					Frontend:         firstFrontend,
					FrontendDeploy:   cfg.Deploy.FrontendDeploy,
					MigrationTest:    cfg.Deploy.MigrationTest,
					Concurrency:      cfg.Deploy.IsConcurrencyEnabled(),
					CancelInProgress: cfg.Deploy.Concurrency.CancelInProgress,
				}},
			)
		}
		files = append(files, hostedReleaseFiles(cfg.Name, hostedEnvs, mixed)...)
		files = append(files, pgoRefreshFiles(root, cfg.Name, hostedEnvs, in)...)
		if hasE2E {
			files = append(files, CIWorkflowFile{"e2e.yml.tmpl", ".github/workflows/e2e.yml", templates.E2EWorkflowData{
				ProjectName:  cfg.Name,
				Runtime:      e2eRuntime,
				HasFrontends: hasFrontends,
				FrontendPath: firstFrontendPath,
				Frontend:     firstFrontend,
			}})
		}
	}
	if ci.LintBufBreaking {
		files = append(files, CIWorkflowFile{"proto-breaking.yml.tmpl", ".github/workflows/proto-breaking.yml", ci})
	}
	files = append(files, CIWorkflowFile{"dependabot.yml.tmpl", ".github/dependabot.yml", templates.DependabotData{
		FrontendName:    firstFrontendName,
		RunsProtocGenGo: hasServices,
	}})
	return files
}

// hostedReleaseFiles are the files a project with hosted envs (in promotion
// order) gets beyond ci.yml: release.yml and the vendored forge-deploy action
// it calls. None without a hosted env.
func hostedReleaseFiles(project string, hostedEnvs []templates.DeployEnv, mixed map[string]bool) []CIWorkflowFile {
	if len(hostedEnvs) == 0 {
		return nil
	}
	data := templates.ReleaseWorkflowData{
		ProjectName: project,
		BuildEnv:    hostedEnvs[0].Name,
		Stages:      templates.ReleaseStages(hostedEnvs, mixed),
	}
	return []CIWorkflowFile{
		{"release.yml.tmpl", ".github/workflows/release.yml", data},
		{"forge-deploy-action.yml.tmpl", ForgeDeployActionPath, data},
	}
}

// PGORefreshPath is where the scaffolded PGO refresh workflow lands.
const PGORefreshPath = ".github/workflows/pgo-refresh.yml"

// pgoRefreshFiles is pgo-refresh.yml, for a project that can use it. The
// platform stores a hosted env's profiles, so a hosted project can keep its
// committed default.pgo current with the env real traffic reaches. The
// workflow runs `forge pgo pull --env <env>`, which writes a profile only for
// the Go binaries that env builds, so it is born only where one does
// (pgoRefreshEnv), and never where the project already keeps its profiles
// current another way (PGORefreshDeclaredElsewhere).
func pgoRefreshFiles(root, project string, hostedEnvs []templates.DeployEnv, in CIInputs) []CIWorkflowFile {
	env := pgoRefreshEnv(hostedEnvs, in)
	if env == "" || PGORefreshDeclaredElsewhere(root) != "" {
		return nil
	}
	return []CIWorkflowFile{{"pgo-refresh.yml.tmpl", PGORefreshPath, templates.PGORefreshWorkflowData{
		ProjectName: project,
		Env:         env,
	}}}
}

// pgoRefreshEnv is the env pgo-refresh.yml pulls: the last hosted env in
// promotion order that builds a Go binary to profile (CIInputs.PGOEnvs), or
// "" when none does. A hosted env with no Go binary (a static site) has no
// profile to pull, so `forge pgo pull --env <it>` would do nothing every week.
func pgoRefreshEnv(hostedEnvs []templates.DeployEnv, in CIInputs) string {
	builds := map[string]bool{}
	for _, e := range in.PGOEnvs {
		builds[e] = true
	}
	for i := len(hostedEnvs) - 1; i >= 0; i-- {
		if builds[hostedEnvs[i].Name] {
			return hostedEnvs[i].Name
		}
	}
	return ""
}

// pgoPullLine matches a `forge pgo pull` invocation (`reliant forge pgo pull`
// included) on a line that is not a YAML comment.
var pgoPullLine = regexp.MustCompile(`^\s*(?:[^#\s].*)?\bforge\s+pgo\s+pull\b`)

// PGORefreshDeclaredElsewhere names the project file that already keeps the
// committed default.pgo profiles current, or "" when there is none. forge
// scaffolds no pgo-refresh.yml beside one: two weekly jobs would open two
// pull requests for the same profile.
//
// It recognizes exactly two shapes, on purpose — a broader guess would turn
// off a workflow the project never replaced:
//
//   - a Reliant workflow named pgo-refresh (.reliant/workflows/pgo-refresh.yaml
//     or .yml), whatever it runs: the name is the declaration, so one that
//     calls a script (control-plane's scripts/pgo-refresh.sh) counts;
//   - a workflow file directly under .github/workflows/ or
//     .reliant/workflows/ (.yml or .yaml), other than pgo-refresh.yml itself,
//     with an uncommented line that runs `forge pgo pull`.
//
// Scripts a workflow calls are not read. Anything else declines the workflow
// with `ci.workflows: {pgo-refresh: false}`.
func PGORefreshDeclaredElsewhere(root string) string {
	for _, name := range []string{"pgo-refresh.yaml", "pgo-refresh.yml"} {
		rel := reliantWorkflowDir + name
		if fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err == nil && !fi.IsDir() {
			return rel
		}
	}
	for _, dir := range []string{".github/workflows/", reliantWorkflowDir} {
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil {
			continue
		}
		for _, e := range entries {
			rel := dir + e.Name()
			if e.IsDir() || rel == PGORefreshPath || !(strings.HasSuffix(rel, ".yml") || strings.HasSuffix(rel, ".yaml")) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(data), "\n") {
				if pgoPullLine.MatchString(line) {
					return rel
				}
			}
		}
	}
	return ""
}

// PGORefreshNotice is the line `forge generate` prints when this project
// would be given pgo-refresh.yml but already refreshes its profiles another
// way (PGORefreshDeclaredElsewhere), naming the file and the forge.yaml key
// that says so. "" when there is nothing to say: the workflow is declined,
// on disk, deleted by the user (the birth ledger records it), or the project
// has no Go binary for it to profile anyway.
func PGORefreshNotice(root string, cfg *config.ProjectConfig, in CIInputs) string {
	if cfg == nil || !cfg.IsServiceKind() || cfg.CI.EffectiveProvider() != config.CIProviderGitHub || !cfg.CI.WorkflowEnabled("pgo-refresh") {
		return ""
	}
	if len(in.PGOEnvs) == 0 || onDisk(root, filepath.FromSlash(PGORefreshPath)) || !checksums.ScaffoldMayBeBorn(root, PGORefreshPath) {
		return ""
	}
	by := PGORefreshDeclaredElsewhere(root)
	if by == "" {
		return ""
	}
	return fmt.Sprintf("  ⏭️  %s not scaffolded: %s already refreshes default.pgo. Say so in forge.yaml to silence this: ci.workflows: {pgo-refresh: false}",
		PGORefreshPath, by)
}

// ForeignCIWorkflow reports whether relPath, a CI file forge would scaffold,
// already exists as the project's OWN file: on disk, never born by forge (no
// birth record in .forge/scaffolded/), and carrying neither of forge's
// banners (a scaffold's "yours: scaffolded once", a generated file's "Code
// generated by forge"). A repository adopting forge usually has a deploy.yml
// or ci.yml of its own under the very name forge uses.
//
// Forge never overwrites one — CI files are scaffold-once — but it must not
// adopt one silently either: the project should say in forge.yaml that forge
// has no such workflow here (`ci.workflows: {<name>: false}`).
func ForeignCIWorkflow(root, relPath string) bool {
	if checksums.ScaffoldRecorded(root, relPath) {
		return false // forge's scaffold, however edited since
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relPath)))
	if err != nil {
		return false
	}
	if len(data) > 2048 {
		data = data[:2048]
	}
	head := string(data)
	return !strings.Contains(head, "yours: scaffolded once") && !strings.Contains(head, "Code generated by forge")
}

// ForeignCIWorkflowNotice is the warning for a ForeignCIWorkflow.
func ForeignCIWorkflowNotice(relPath string) string {
	return fmt.Sprintf("⚠️  %s is your own (it carries no forge banner) — forge leaves it alone. "+
		"Say so in forge.yaml to silence this: ci.workflows: {%s: false}", relPath, CIWorkflowName(relPath))
}

// ForgeDeployActionPath is where the vendored composite action lands
// (hosted-deploy-primitives §3.6, owner ruling Q5: vendored per repo, not
// published, so it versions with the project's forge pin).
const ForgeDeployActionPath = ".github/actions/forge-deploy/action.yml"

// declaredKCLEnvs lists every env with a deploy/kcl/<env>/main.k, sorted.
func declaredKCLEnvs(root string) []string {
	kclDir := filepath.Join(root, "deploy", "kcl")
	entries, err := os.ReadDir(kclDir)
	if err != nil {
		return nil
	}
	var envs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(kclDir, e.Name(), "main.k")); err != nil {
			continue
		}
		// A preview instance (`forge env preview create`) is a transient
		// copy of a template env, created and deleted by its own pipeline;
		// a workflow that deployed it on every push would keep it alive.
		if _, err := os.Stat(filepath.Join(kclDir, e.Name(), kclrender.PreviewMarkerFile)); err == nil {
			continue
		}
		envs = append(envs, e.Name())
	}
	sort.Strings(envs)
	return envs
}

// ciDeployEnvs is the environment list the deploy and reconcile workflows
// share. ONE list rather than two: the workflows must agree about which
// environments exist.
//
// forge.yaml's deploy.environments wins when present. Otherwise the list is
// every declared KCL env except dev (which runs locally via `forge env up`),
// never a hard-coded one: a job for an env with no main.k fails on every run
// (houndersclub had dev + prod and was scaffolded a deploy-staging job).
//
// Position carries meaning — envs[0] auto-deploys after every green image
// build on main, envs[len-1] is the protected env a `v*` tag ships to — so
// the discovered list is ordered along the promotion path, never lexically.
func ciDeployEnvs(cfg *config.ProjectConfig, kclEnvs []string) []templates.DeployEnv {
	var envs []templates.DeployEnv
	for _, e := range cfg.Deploy.Environments {
		envs = append(envs, templates.DeployEnv{Name: e.Name, Auto: e.Auto, Protection: e.Protection, URL: e.URL})
	}
	if len(envs) > 0 {
		return envs
	}
	for _, name := range kclEnvs {
		if name != "dev" {
			envs = append(envs, templates.DeployEnv{Name: name})
		}
	}
	sortByPromotionOrder(envs)
	// A lone env is the one users reach — protected, and never
	// auto-deployed: auto-promoting it would ship every merge to main
	// straight to production.
	if len(envs) > 0 {
		envs[len(envs)-1].Protection = true
	}
	if len(envs) > 1 {
		envs[0].Auto = true
	}
	return envs
}

// promotionRank orders an environment along the promotion path. Unknown
// names sort between staging and prod: a bespoke env is a mid-pipeline
// stage, and prod must stay last so the release tag and the protection gate
// land on it.
func promotionRank(name string) int {
	switch strings.ToLower(name) {
	case "staging", "stage", "stg":
		return 10
	case "preprod", "pre-prod", "preproduction", "uat", "qa", "canary":
		return 20
	case "prod", "production":
		return 30
	default:
		return 15
	}
}

// sortByPromotionOrder orders envs in place along the promotion path,
// breaking rank ties by name so the output is deterministic.
func sortByPromotionOrder(envs []templates.DeployEnv) {
	sort.SliceStable(envs, func(i, j int) bool {
		ri, rj := promotionRank(envs[i].Name), promotionRank(envs[j].Name)
		if ri != rj {
			return ri < rj
		}
		return envs[i].Name < envs[j].Name
	})
}
