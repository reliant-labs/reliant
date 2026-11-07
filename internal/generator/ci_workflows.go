package generator

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/internal/toolpin"
)

// CIWorkflowFile is one scaffolded GitHub Actions file: the template that
// renders it, where it lands, and the data it renders with.
type CIWorkflowFile struct {
	Template string
	Dest     string
	Data     any
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
}

// CIWorkflowDataFor returns what the scaffolded ci.yml renders with: which lints,
// scanners and jobs this project has. `forge ci gate` plans its checks from
// the same struct, so a forge.yaml toggle means the same thing to the workflow
// and to the local gate.
func CIWorkflowDataFor(root string, cfg *config.ProjectConfig, in CIInputs) templates.CIWorkflowData {
	for _, f := range CIWorkflowsFor(root, cfg, in) {
		if f.Dest == ".github/workflows/ci.yml" {
			return f.Data.(templates.CIWorkflowData)
		}
	}
	return templates.CIWorkflowData{}
}

// CIWorkflowsFor is CIWorkflows with every caller-supplied input.
func CIWorkflowsFor(root string, cfg *config.ProjectConfig, in CIInputs) []CIWorkflowFile {
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

	ci := templates.CIWorkflowData{
		ProjectName: cfg.Name,
		// The buf this project pins (go.mod's, else forge's): the one the
		// gate holds PATH to and `forge generate` runs.
		BufVersion:   toolpin.BufFor(root).Bare(),
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
		if len(hostedEnvs) > 0 {
			data := templates.ReleaseWorkflowData{
				ProjectName: cfg.Name,
				BuildEnv:    hostedEnvs[0].Name,
				Stages:      templates.ReleaseStages(hostedEnvs, mixed),
			}
			files = append(files,
				CIWorkflowFile{"release.yml.tmpl", ".github/workflows/release.yml", data},
				CIWorkflowFile{"forge-deploy-action.yml.tmpl", ForgeDeployActionPath, data},
			)
		}
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
		if _, err := os.Stat(filepath.Join(kclDir, e.Name(), "main.k")); err == nil {
			envs = append(envs, e.Name())
		}
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
