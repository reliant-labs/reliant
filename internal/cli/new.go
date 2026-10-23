package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/cli/scaffold"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/nodeengine"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

func newNewCmd() *cobra.Command {
	var (
		projectPath     string
		nameFlag        string
		modulePath      string
		kindFlag        string
		serviceNames    []string
		frontendNames   []string
		frontendOutput  string
		frontendRoutes  []string
		goVersion       string
		inPlace         bool
		force           bool
		disableFeatures []string
		harness         string
		skipTools       bool
		bufPlugins      string
		binaryMode      string
		// frontendWorkspaces opts the project into the pnpm-workspaces
		// layout — packages/api + packages/hooks shared across all
		// frontends, frontends import via "@<scope>/api". Default off;
		// must be opted in explicitly. See SKILL.md/frontend-workspaces.
		frontendWorkspaces bool
		// linkForge opts the new project into compiling forge from the
		// checkout this binary was built from (a gitignored go.work `use`).
		// See forge_bridge.go for why it is never implicit.
		linkForge bool
		// forgeRoot names the checkout --link-forge bridges to, for a binary
		// that cannot find its own (see buildinfo.ResolveForgeRoot).
		forgeRoot string
		verbose   bool
		// verify keeps the compile check at the end of the bootstrap
		// generate. Off by default: see bootstrapPipelineFlags.
		verify bool
	)

	cmd := &cobra.Command{
		Use:   "new [project-name] --mod [module-path]",
		Short: "Create a new Forge project (service / CLI / library)",
		Long: `Create a new project with the Forge framework structure.

By default no service is scaffolded: the binary is a deployment unit
that mounts services — it is not a domain entity. Add your first
service after scaffolding with 'forge scaffold service <entity>' (name it
after a domain entity like item/order/user, not the binary), or opt
into an initial service at creation time with --service <entity>.

Pick a project kind with --kind:

  --kind service  (default) Connect-RPC service: handlers, middleware, deploy
                            manifests, observability wiring, frontend support.
  --kind cli                Cobra-based CLI binary: cmd/<name>/main.go +
                            cmd/<name>/version.go, no server scaffolding,
                            no proto/services, no deploy/.
  --kind library            Pure Go module: pkg/<name>/ skeleton, no cmd/,
                            no CI workflows by default.
  --kind site               A static site: adopt the frontend at the root of
                            the current directory (with --in-place). Writes
                            forge.yaml and deploy/kcl envs only — no Go.

Use --disable to turn features off at creation time. Each is pinned off in
forge.yaml's features: block, so a later change to the tree does not turn it
back on (orm has no key; --disable orm only leaves db/migrations out):
  forge project new my-project --mod ... --disable ci,deploy
  forge project new my-project --mod ... --disable orm --disable migrations

Valid feature names: orm, codegen, migrations, ci, build, deploy,
contracts, docs, frontend, observability, hot_reload.

Example:
  forge project new my-project --mod github.com/example/my-project
  forge project new my-project --mod github.com/example/my-project --service gateway
  forge project new my-project --mod github.com/example/my-project --frontend web
  forge project new mycli      --mod github.com/example/mycli --kind cli
  forge project new mylib      --mod github.com/example/mylib --kind library
  forge project new --in-place --mod github.com/example/my-project
  forge project new --in-place --name my-project --mod github.com/example/my-project

With --in-place and no name, the project is named after the DIRECTORY. That
name becomes cmd/<name>/, the binary, the image and the deploy manifests, so
in a worktree or a branch checkout — where the directory is named after the
branch rather than the product — pass --name.

--in-place never overwrites a file that already exists: forge keeps yours,
skips its own version, and lists what it kept (--force replaces them). An
existing .gitignore is merged — forge appends only the entries it lacks,
in a "# --- forge ---" block. A directory already inside a git repository
(its root or any subdirectory) is left alone: no git init, no commit. A
fresh directory outside any repository gets 'git init' + an initial commit.

--link-forge bridges the new project to the forge checkout THIS binary was
built from: a gitignored, machine-local go.work 'use', plus the npm twin for
@reliantlabs/forge-web-runtime (.forge-link/). The project then compiles the
library from that checkout instead of a published version — what a forge
contributor wants, and something nobody else should get by accident, so it
is never written unless asked for. An unreleased forge build cannot pin
itself in go.mod, so a service scaffold from one is refused without it. Once
bridged, generate, lint and doctor warn whenever this binary and the
checkout stop being the same source; drop the bridge with
'go work edit -dropuse=<checkout>'.`,
		Args: cobra.RangeArgs(0, 1),
		// project new owns git setup: initGitRepository activates hooks in a
		// repository it creates, and an existing one is left alone.
		Annotations: map[string]string{skipHookActivationAnnotation: ""},
		RunE: func(cmd *cobra.Command, args []string) error {
			projectName := nameFlag
			if len(args) > 0 {
				if nameFlag != "" && nameFlag != args[0] {
					return cliutil.UserErr("forge project new",
						fmt.Sprintf("project named twice: %q positionally and %q via --name", args[0], nameFlag),
						"",
						"pass the name once — as the positional arg, or as --name")
				}
				projectName = args[0]
			}
			frontendOutput = strings.ToLower(strings.TrimSpace(frontendOutput))
			frontendRoutes = scaffold.NormalizeRouteSlugs(frontendRoutes)
			if (cmd.Flags().Changed("output") || cmd.Flags().Changed("routes")) && len(frontendNames) == 0 {
				return fmt.Errorf("--output and --routes require --frontend")
			}
			if frontendOutput != "" && frontendOutput != "static" && frontendOutput != "standalone" && frontendOutput != "server" {
				return fmt.Errorf("invalid --output %q: use static, standalone, or server", frontendOutput)
			}
			explicitForgeRoot = forgeRoot
			defer func() { explicitForgeRoot = "" }()
			return runNew(cmd.Context(), projectName, projectPath, modulePath, kindFlag, serviceNames, frontendNames, goVersion, inPlace, force, disableFeatures, harness, skipTools, bufPlugins, binaryMode, frontendWorkspaces, linkForgeRequested(linkForge), verbose, verify, newFrontendOptions{output: frontendOutput, routes: frontendRoutes})
		},
	}

	cmd.Flags().StringVarP(&projectPath, "path", "p", ".", "Path where to create the project")
	cmd.Flags().StringVar(&modulePath, "mod", "", "Go module path (e.g., github.com/example/my-project). Defaults to example.com/<name>, so a first project needs no flag; set it to the path you will publish under")
	cmd.Flags().StringVar(&kindFlag, "kind", "", "Project kind: service (default), cli, library, site (adopt the static frontend at this directory's root, with --in-place)")
	cmd.Flags().StringSliceVar(&serviceNames, "service", nil, "Name(s) of initial Go services (repeatable or comma-separated). Name services after domain entities (item, order), not the binary. Omit to scaffold zero services and add them later via 'forge scaffold service <entity>'")
	cmd.Flags().StringSliceVar(&frontendNames, "frontend", nil, "Name(s) of Next.js frontends (can be repeated or comma-separated)")
	cmd.Flags().StringVar(&frontendOutput, "output", "", "Output for every --frontend: static (default), standalone, or server")
	cmd.Flags().StringSliceVar(&frontendRoutes, "routes", nil, "CRUD route allowlist for every --frontend; none generates no CRUD pages")
	cmd.Flags().StringVar(&goVersion, "go-version", "", "Go version to use in go.mod (e.g., 1.24); defaults to detected version")
	cmd.Flags().BoolVar(&inPlace, "in-place", false, "Create project in current directory instead of a new subdirectory")
	cmd.Flags().StringVar(&nameFlag, "name", "", "Project name. Same as the positional arg, and the way to name an --in-place project whose directory (a worktree, a branch checkout) is not the product name; defaults to the directory name")
	cmd.Flags().BoolVar(&force, "force", false, "With --in-place: scaffold over an existing forge.yaml, and REPLACE every pre-existing file the scaffold writes (README.md, go.mod, Taskfile.yml, …) with forge's version. Without it, existing files are kept and listed. .gitignore is always merged, never replaced")
	cmd.Flags().StringSliceVar(&disableFeatures, "disable", nil, "Features to leave out of the scaffold (comma-separated): "+strings.Join(disableableFeatures, ", ")+". Each is pinned off in forge.yaml's features: block (orm, which has no key, only steers this scaffold).")
	cmd.Flags().StringVar(&harness, "harness", "reliant", "AI harness conventions to scaffold for. Each writes a memory file; Forge never writes skill files for any harness. reliant (default): reliant.md — skills are read from the forge binary ('forge skill search|load') and discovered via forge.yaml. claude: CLAUDE.md. For non-reliant harnesses, add your own skill telling the agent to run 'forge skill search <query>'. cursor: .cursorrules. copilot: .github/copilot-instructions.md. codex: AGENTS.md. Recorded as 'harness:' in forge.yaml and honored by every later generate")
	cmd.Flags().BoolVar(&skipTools, "skip-tools", false, "Skip auto-installing protoc-gen-go / protoc-gen-connect-go (run 'forge tools install' later)")
	cmd.Flags().StringVar(&bufPlugins, "buf-plugins", "local", "Default proto plugin source: 'local' (resolved from PATH; no BSR auth needed) or 'remote' (BSR-hosted, requires login under load)")
	cmd.Flags().StringVar(&binaryMode, "binary", "per-service", "Binary packaging: 'per-service' (default — canonical cmd/server.go cobra root, one Application per service) or 'shared' (one Go binary, cobra subcommand per service, KCL MultiServiceApplication for deploy)")
	cmd.Flags().BoolVar(&frontendWorkspaces, "frontend-workspaces", false, "Opt into pnpm-workspaces layout: emit packages/api + packages/hooks + packages/ui-web shared across all frontends. Off by default; recommended once you have 2+ frontends (web + mobile).")
	cmd.Flags().BoolVar(&linkForge, "link-forge", false, "Bridge the project to the forge checkout this binary was built from (gitignored go.work 'use' + .forge-link/ for the web runtime). Off by default; required for a service scaffold from an unreleased forge build (also: "+linkForgeEnv+"=1)")
	cmd.Flags().StringVar(&forgeRoot, "forge-root", "", "The forge checkout --link-forge bridges to (also: "+buildinfo.ForgeRootEnv+"). Default: the checkout this binary was built from, else the go.mod replace it embeds forge through, else the checkout it sits or was started in")
	cmd.Flags().BoolVar(&verify, "verify", false, "Compile the scaffold before returning: the bootstrap generate then ends with its go build check and a render of every deploy env (adds ~1s warm, ~40s on a cold build cache, and saturates the CPU while it runs). Off by default: the scaffold is deterministic and forge's own e2e corpus compiles it, and your first go build or forge generate pays for the same compile")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Print the raw output behind a step forge summarizes in one line (e.g. git's explanation when the initial commit is skipped for want of a git identity)")

	return cmd
}

// defaultModulePath returns the Go module path a new project uses: --mod when
// given, else `example.com/<name>` — the shape the Go tutorial itself uses
// (`go mod init example.com/greetings`), a valid main-module path for an
// application nobody imports yet, and one forge.yaml's module_path check
// accepts. example.com is reserved for examples (RFC 2606), so the default
// reads as the placeholder it is.
//
// --mod used to be REQUIRED, which made the README's first command
// (`forge project new my-app`) fail before anything was written. A module
// path only matters once the code is published or imported from elsewhere,
// and the GitHub-derived defaults (CI image owner, CODEOWNERS) already fall
// back cleanly for a non-github path, so a working default beats a wall.
func defaultModulePath(flag, projectName string) (modulePath string, defaulted bool) {
	if m := strings.TrimSpace(flag); m != "" {
		return m, false
	}
	return "example.com/" + projectName, true
}

// validateNewArgs runs the pure validation/normalization logic for runNew —
// the part that doesn't touch the filesystem or run subprocesses. Returns the
// normalized kind, the normalized buf-plugins choice, the normalized binary
// mode, or an error.
//
// Extracted so tests can exercise the validation surface without invoking the
// full scaffold (which calls `go mod tidy`, `buf generate`, etc., and is slow
// or hangs in CI environments without network access).
func validateNewArgs(kindFlag, bufPlugins, binaryMode string, serviceNames, frontendNames []string) (kind, plugins, binary string, err error) {
	// Validate --kind. Empty string is treated as "service" for back-compat
	// (callers that don't pass the flag at all).
	kind = strings.ToLower(strings.TrimSpace(kindFlag))
	if kind == "" {
		kind = config.ProjectKindService
	}
	switch kind {
	case config.ProjectKindService, config.ProjectKindCLI, config.ProjectKindLibrary, projectKindSite:
		// ok
	default:
		return "", "", "", cliutil.UserErr("forge project new",
			fmt.Sprintf("invalid --kind %q: valid values are service, cli, library, site", kindFlag),
			"",
			"pass --kind=service for a Connect-RPC server, --kind=cli for a Cobra binary, --kind=library for a pure Go module, or --kind=site (with --in-place) to adopt a static frontend")
	}

	// Validate --buf-plugins. Default 'local' (no BSR auth required); the
	// 'remote' opt-in is preserved for users who genuinely want BSR-hosted
	// plugins (no install required, latest version always — but rate-limited
	// for anonymous users).
	plugins = strings.ToLower(strings.TrimSpace(bufPlugins))
	if plugins == "" {
		plugins = "local"
	}
	switch plugins {
	case "local", "remote":
		// ok
	default:
		return "", "", "", cliutil.UserErr("forge project new",
			fmt.Sprintf("invalid --buf-plugins %q: valid values are local, remote", bufPlugins),
			"",
			"pass --buf-plugins=local (default; uses protoc-gen-go on PATH) or --buf-plugins=remote (BSR-hosted, no install required)")
	}

	// Validate --binary. Empty string is treated as "per-service" for
	// back-compat (callers/tests that don't pass the flag at all). Only
	// meaningful for service projects.
	binary = strings.ToLower(strings.TrimSpace(binaryMode))
	if binary == "" {
		binary = config.ProjectBinaryPerService
	}
	switch binary {
	case config.ProjectBinaryPerService, config.ProjectBinaryShared:
		// ok
	default:
		return "", "", "", cliutil.UserErr("forge project new",
			fmt.Sprintf("invalid --binary %q: valid values are per-service, shared", binaryMode),
			"",
			"pass --binary=per-service (default; one cmd/server.go per service) or --binary=shared (one binary, cobra subcommand per service)")
	}

	// Reject incompatible flag combinations early so the user gets a
	// clean error before any directory is created.
	if kind != config.ProjectKindService {
		if len(serviceNames) > 0 {
			return "", "", "", cliutil.UserErr("forge project new",
				fmt.Sprintf("--service is only meaningful with --kind service (got --kind %s)", kind),
				"",
				"drop --service, or change to --kind service")
		}
		if len(frontendNames) > 0 {
			return "", "", "", cliutil.UserErr("forge project new",
				fmt.Sprintf("--frontend is only meaningful with --kind service (got --kind %s)", kind),
				"",
				"drop --frontend, or change to --kind service")
		}
		if binary == config.ProjectBinaryShared {
			return "", "", "", cliutil.UserErr("forge project new",
				fmt.Sprintf("--binary shared is only meaningful with --kind service (got --kind %s)", kind),
				"",
				"drop --binary=shared, or change to --kind service")
		}
	}
	return kind, plugins, binary, nil
}

type newFrontendOptions struct {
	output string
	routes []string
}

//nolint:revive,cyclop // Existing cobra flag fan-out; frontend settings travel together.
func runNew(ctx context.Context, projectName, projectPath, modulePath, kindFlag string, serviceNames []string, frontendNames []string, goVersion string, inPlace bool, force bool, disableFeatures []string, harness string, skipTools bool, bufPlugins, binaryMode string, frontendWorkspaces bool, linkForge bool, verbose, verify bool, frontend newFrontendOptions) error {
	kindNormalized, bufPluginsNormalized, binaryNormalized, err := validateNewArgs(kindFlag, bufPlugins, binaryMode, serviceNames, frontendNames)
	if err != nil {
		return err
	}
	if kindNormalized == projectKindSite {
		if !inPlace {
			return runNewSite("", projectName, false, force, harness)
		}
		targetPath, name, err := resolveNewTargetPath(projectName, projectPath, inPlace, force)
		if err != nil {
			return err
		}
		return runNewSite(targetPath, name, inPlace, force, harness)
	}
	targetPath, projectName, err := resolveNewTargetPath(projectName, projectPath, inPlace, force)
	if err != nil {
		return err
	}
	// A directory whose ROOT is a frontend app, and which is no Go module, is
	// a static site. The service scaffold would write a Go server beside it
	// and never declare the app; say so instead, unless --kind or --service
	// asked for a backend explicitly.
	if inPlace && strings.TrimSpace(kindFlag) == "" && len(serviceNames) == 0 && len(frontendNames) == 0 {
		if _, statErr := os.Stat(filepath.Join(targetPath, "go.mod")); os.IsNotExist(statErr) {
			if apps := detectExistingFrontendAppsIn(targetPath, []string{"."}); len(apps) > 0 {
				return cliutil.UserErr("forge project new --in-place",
					fmt.Sprintf("%s is a %s app at its root, with no Go module", targetPath, apps[0].framework()),
					"",
					"adopt it as a static site: forge project new --in-place --kind site --name <name>. "+
						"To scaffold a Go backend beside it instead, pass --kind service (or --service <entity>)")
			}
		}
	}
	// An in-place target may be a module that already requires forge. Its
	// pin is adopted, never moved — see new_inplace_forge_pin.go.
	var adopted *adoptedForge
	if inPlace {
		if a, ok := inspectAdoptedForge(targetPath, kindNormalized); ok {
			adopted = &a
		}
	}
	if err := checkScaffoldCanResolveForge(kindNormalized, linkForge, adopted); err != nil {
		return err
	}
	forgePin := ""
	if adopted != nil {
		forgePin = adopted.pin
		announceAdoptedForgePin(*adopted)
	}
	// An in-place target whose root module already owns gen/ keeps it.
	genInRootModule := inPlace && rootModuleOwnsGen(targetPath)
	if genInRootModule {
		fmt.Println("gen/ already holds Go packages of this module (no gen/go.mod): forge keeps it in the root module — no gen/go.mod, and go.work uses only the root.")
	}
	// An in-place target that already holds a project is adopted, not
	// created: its existing env reads make the typed-config guardrail start
	// advisory (generator.ProjectGenerator.Adopting).
	adopting := inPlace && dirHoldsProject(targetPath)
	if adopting {
		fmt.Println("Adopting an existing project: forge.yaml starts the typed-config guardrail advisory (config.enforce_typed_access: warn), so `forge lint` reports the env reads already here instead of failing on them. Set it to error once they all read the typed config.")
	}
	modulePath, defaulted := defaultModulePath(modulePath, projectName)
	if defaulted {
		fmt.Printf("Go module path: %s (no --mod given; pass --mod github.com/<you>/%s to set the path you will publish under)\n", modulePath, projectName)
	}

	if err := validateComponentNames(serviceNames, frontendNames, disableFeatures); err != nil {
		return err
	}

	// An in-place target may already hold a frontend app. Refuse to scaffold
	// a same-named second one beside it before anything is written; any
	// others are reported (with how to adopt them) at the end.
	var existingApps []existingFrontendApp
	if inPlace {
		existingApps = detectExistingFrontendApps(targetPath)
		if err := checkInPlaceFrontendCollisions(existingApps, frontendNames); err != nil {
			return err
		}
	}

	fmt.Printf("Creating new project '%s' at %s\n", projectName, targetPath)
	if len(serviceNames) > 0 {
		if len(serviceNames) == 1 {
			fmt.Printf("  Service: %s\n", serviceNames[0])
		} else {
			fmt.Printf("  Services: %s\n", strings.Join(serviceNames, ", "))
		}
	}
	if len(frontendNames) > 0 {
		fmt.Printf("  Frontend: %s\n", strings.Join(frontendNames, ", "))
	}

	scaffold, err := beginScaffold(targetPath, inPlace)
	if err != nil {
		return err
	}
	defer scaffold.undoUnlessFinished()

	merged, err := writeScaffoldFiles(scaffoldFilesInput{
		gen: newGeneratorConfig{
			projectName:        projectName,
			targetPath:         targetPath,
			modulePath:         modulePath,
			kind:               kindNormalized,
			binary:             binaryNormalized,
			serviceNames:       serviceNames,
			frontendNames:      frontendNames,
			frontendOutput:     frontend.output,
			frontendRoutes:     frontend.routes,
			goVersion:          goVersion,
			frontendWorkspaces: frontendWorkspaces,
			harness:            harness,
			disableFeatures:    disableFeatures,
			forgePin:           forgePin,
			genInRootModule:    genInRootModule,
			adopting:           adopting,
		},
		bufPlugins: bufPluginsNormalized,
		inPlace:    inPlace,
		force:      force,
	})
	if err != nil {
		return err
	}

	// Local-forge bridge, only with --link-forge: a gitignored go.work
	// linking the scaffold to the checkout this binary was built from. Done
	// BEFORE finalize so the bootstrap (forge generate) + go mod tidy below
	// run under the bridge, and the web-runtime twin follows it now — the
	// frontends were generated before the bridge existed, and npm must see
	// the twin on finalize's first install for it to take effect.
	writeDevForgeGoWork(targetPath, linkForge)
	if linkForge && len(frontendNames) > 0 {
		generator.EnsureDevWebRuntimeLink(targetPath)
	}

	existingRepo, genErr := finalizeNewProject(ctx, newFinalizeInput{
		targetPath:    targetPath,
		kind:          kindNormalized,
		binary:        binaryNormalized,
		bufPlugins:    bufPluginsNormalized,
		skipTools:     skipTools,
		frontendNames: frontendNames,
		markerPath:    scaffold.markerPath,
		verbose:       verbose,
		verify:        verify,
	})

	// Finished either way: a failed bootstrap generate keeps the files.
	scaffold.finish()
	if genErr == nil {
		fmt.Printf("\n✅ Project '%s' created successfully!\n", projectName)
		printNewNextSteps(projectName, inPlace, kindNormalized, serviceNames, len(frontendNames) > 0)
	}
	printNewProjectNotes(targetPath, existingRepo, merged, existingApps)
	if genErr != nil {
		return newBootstrapFailedErr(projectName, targetPath, inPlace, genErr)
	}
	return nil
}

// printNewProjectNotes prints what `project new` found and left alone: the
// in-place merge, frontend apps already there, an enclosing git repository.
// They hold whether or not the bootstrap generate succeeded.
func printNewProjectNotes(targetPath, existingRepo string, merged inPlaceMergeResult, existingApps []existingFrontendApp) {
	for _, line := range inPlaceMergeSummary(merged) {
		fmt.Println(line)
	}
	for _, line := range existingFrontendNote(existingApps) {
		fmt.Println(line)
	}
	if existingRepo != "" {
		where := "This directory is an existing git repository"
		if resolvePathForCompare(existingRepo) != resolvePathForCompare(targetPath) {
			where = "This directory is inside the existing git repository " + existingRepo
		}
		fmt.Printf("\n📁 %s — forge left the repository alone (no git init, no commit). "+
			"Review `git status` and commit the scaffold yourself.\n", where)
	}
}

// validateComponentNames refuses, before anything is written, a --service or
// --frontend name that cannot name the directories, packages and proto files
// scaffolded for it, and a component whose feature --disable leaves out
// (refuseDisabledComponents).
func validateComponentNames(serviceNames, frontendNames, disableFeatures []string) error {
	for _, svcName := range serviceNames {
		if err := validateServiceName(svcName); err != nil {
			return fmt.Errorf("invalid service name %q: %w", svcName, err)
		}
	}
	for _, feName := range frontendNames {
		if err := validateFrontendName(feName); err != nil {
			return fmt.Errorf("invalid frontend name %q: %w", feName, err)
		}
	}
	return refuseDisabledComponents(disableFeatures, serviceNames, frontendNames)
}

// refuseDisabledComponents refuses, before anything is written, a component
// asked for by name whose feature --disable leaves out. The scaffold used to
// honour the --disable and drop the component without a word: `--service
// orders --disable codegen` wrote no service at all (a service IS its proto
// and the code generated from it), and `--frontend web --disable frontend`
// left an empty frontends/web/.
func refuseDisabledComponents(disable, serviceNames, frontendNames []string) error {
	off := map[string]bool{}
	for _, name := range disable {
		off[strings.TrimSpace(strings.ToLower(name))] = true
	}
	if off[config.FeatureCodegen] && len(serviceNames) > 0 {
		return cliutil.UserErr(Name()+" project new --disable codegen",
			fmt.Sprintf("--service %s asks for a service, and --disable codegen leaves out what a service is made of: its proto and the code generated from it", strings.Join(serviceNames, ",")),
			"",
			"drop --service for a project without codegen (a frontend-only project, say), or drop codegen from --disable")
	}
	if off[config.FeatureFrontend] && len(frontendNames) > 0 {
		return cliutil.UserErr(Name()+" project new --disable frontend",
			fmt.Sprintf("--frontend %s asks for a frontend, and --disable frontend leaves frontends out of the scaffold", strings.Join(frontendNames, ",")),
			"",
			"drop one of the two flags")
	}
	return nil
}

// resolveNewTargetPath resolves the scaffold target directory and the effective
// project name from the CLI args, validating the name and refusing to scaffold
// over an existing project. In --in-place mode the project name defaults to the
// target directory's base name and an existing forge.yaml is only tolerated
// with --force. In fresh-directory mode a name is required and the target must
// not already exist. Returns (targetPath, resolvedName).
func resolveNewTargetPath(projectName, projectPath string, inPlace, force bool) (string, string, error) {
	if inPlace {
		// In-place mode: scaffold into the current (or --path) directory directly
		targetPath, err := filepath.Abs(projectPath)
		if err != nil {
			return "", "", fmt.Errorf("failed to resolve path: %w", err)
		}
		// Fall back to the directory name only when nothing named the project.
		//
		// The directory is a WEAK signal: in-place scaffolding is most often
		// run inside a worktree or a checkout whose name belongs to the
		// branch, the ticket, or whatever the clone was called — not to the
		// product. The name chosen here is not cosmetic either: it becomes
		// cmd/<name>/, the binary, the image, the KCL manifests and the
		// compose services, so a wrong one is spread across the tree before
		// the first build. Naming it explicitly (positional arg or --name) is
		// always better than inheriting the directory.
		if projectName == "" {
			projectName = filepath.Base(targetPath)
		}
		// Validate project name (hyphens allowed for directory/module paths)
		if err := validateProjectName(projectName); err != nil {
			return "", "", fmt.Errorf("invalid project name %q: %w", projectName, err)
		}
		// Check that we're not scaffolding over an existing project
		if _, err := os.Stat(filepath.Join(targetPath, defaultProjectConfigFile)); err == nil {
			if !force {
				return "", "", cliutil.UserErr("forge project new --in-place",
					fmt.Sprintf("%s already exists in %s; this directory already contains a Forge project", defaultProjectConfigFile, targetPath),
					"",
					"pass --force to overwrite, or scaffold into a fresh directory")
			}
			fmt.Printf("  --force: overwriting existing %s\n", defaultProjectConfigFile)
		}
		return targetPath, projectName, nil
	}

	if projectName == "" {
		return "", "", cliutil.UserErr("forge project new",
			"project name is required",
			"",
			"pass a project name as the first positional arg, or use --in-place to scaffold in the current directory")
	}

	targetPath := filepath.Join(projectPath, projectName)

	// Validate project name (hyphens allowed for directory/module paths)
	if err := validateProjectName(projectName); err != nil {
		return "", "", cliutil.WrapUserErr("forge project new",
			fmt.Sprintf("invalid project name %q", projectName),
			"",
			"use a name starting with a letter, containing only letters/digits/_/-",
			err)
	}

	// Check if directory already exists
	if _, err := os.Stat(targetPath); err == nil {
		return "", "", cliutil.UserErr("forge project new",
			fmt.Sprintf("directory %s already exists", targetPath),
			"",
			"pick a different project name, or use --in-place --force to overwrite")
	} else if !os.IsNotExist(err) {
		return "", "", cliutil.WrapUserErr("forge project new",
			fmt.Sprintf("failed to stat %s", targetPath),
			"",
			"check filesystem permissions on the parent directory",
			err)
	}
	return targetPath, projectName, nil
}

// scaffoldFilesInput carries the inputs writeScaffoldFiles needs.
type scaffoldFilesInput struct {
	// gen.targetPath is the project directory the files must end up in.
	gen        newGeneratorConfig
	bufPlugins string
	inPlace    bool
	force      bool
}

// writeScaffoldFiles renders every scaffold file — the project generator,
// services and frontends beyond the first, and the
// --buf-plugins=remote switch — and lands them in the target directory.
//
// A fresh project is rendered straight into its (new, empty) directory. An
// in-place one is rendered into a private staging directory and merged into
// the user's tree by mergeScaffoldInto, the one place that decides what an
// in-place scaffold may write: existing files are kept (or, with force,
// replaced) and .gitignore is merged. The returned result is what that merge
// did; it is zero for a fresh project.
func writeScaffoldFiles(in scaffoldFilesInput) (inPlaceMergeResult, error) {
	targetPath := in.gen.targetPath
	cfg := in.gen
	if in.inPlace {
		staging, err := os.MkdirTemp("", "forge-inplace-*")
		if err != nil {
			return inPlaceMergeResult{}, fmt.Errorf("failed to create scaffold staging directory: %w", err)
		}
		defer func() { _ = os.RemoveAll(staging) }()
		cfg.targetPath = staging
	}
	root := cfg.targetPath

	gen, err := configureProjectGenerator(cfg)
	if err != nil {
		return inPlaceMergeResult{}, err
	}
	if err := gen.Generate(); err != nil {
		return inPlaceMergeResult{}, fmt.Errorf("failed to generate project: %w", err)
	}

	// Services beyond the first get the handler/proto skeleton only — that
	// is the whole job in both binary modes, since forge derives the service
	// inventory from the proto descriptor.
	if err := generateAdditionalServices(root, cfg.modulePath, cfg.projectName, cfg.serviceNames); err != nil {
		return inPlaceMergeResult{}, err
	}
	if err := generateAdditionalFrontends(root, cfg.modulePath, cfg.projectName, cfg.frontendNames, gen.ServicePort, gen.FrontendPort, generator.FrontendGenOptions{Workspaces: cfg.frontendWorkspaces, Output: cfg.frontendOutput, Public: !fileExists(filepath.Join(root, "deploy", "kcl", "identity.k"))}, cfg.frontendRoutes); err != nil {
		return inPlaceMergeResult{}, err
	}

	// The --buf-plugins=remote opt-in must land before the bootstrap's
	// `buf generate` reads buf.gen.yaml. 'local' is what the template
	// already emits.
	if in.bufPlugins == "remote" {
		applyRemoteBufPlugins(root, cfg.frontendNames)
	}

	if !in.inPlace {
		return inPlaceMergeResult{}, nil
	}
	merged, err := mergeScaffoldInto(root, targetPath, in.force)
	if err != nil {
		return merged, fmt.Errorf("failed to write scaffold into %s: %w", targetPath, err)
	}
	// The dev web-runtime bridge symlink is relative to where it was laid,
	// so it is not merged — re-lay it in the real tree.
	if len(cfg.frontendNames) > 0 {
		generator.EnsureDevWebRuntimeLink(targetPath)
	}
	return merged, nil
}

// newGeneratorConfig carries the inputs configureProjectGenerator projects onto
// a generator.ProjectGenerator.
type newGeneratorConfig struct {
	projectName        string
	targetPath         string
	modulePath         string
	kind               string
	binary             string
	serviceNames       []string
	frontendNames      []string
	frontendOutput     string
	frontendRoutes     []string
	goVersion          string
	frontendWorkspaces bool
	harness            string
	disableFeatures    []string
	// forgePin is the forge require of the module an in-place scaffold
	// adopts (generator.ProjectGenerator.ForgePin); "" for a fresh project.
	forgePin string
	// genInRootModule reports an in-place target whose root module already
	// owns gen/ (rootModuleOwnsGen; generator.ProjectGenerator.GenInRootModule).
	genInRootModule bool
	// adopting reports an in-place target that already holds a project
	// (dirHoldsProject; generator.ProjectGenerator.Adopting).
	adopting bool
}

// configureProjectGenerator builds and configures the project generator from
// the CLI flags: the first service/frontend seed the primary names (additional
// services are passed through so binary=shared can emit one cobra subcommand
// per service at scaffold time), the harness is parsed, and kind-aware feature
// defaults are applied BEFORE --disable so an explicit --disable always wins.
func configureProjectGenerator(c newGeneratorConfig) (*generator.ProjectGenerator, error) {
	gen := generator.NewProjectGenerator(c.projectName, c.targetPath, c.modulePath)
	gen.Kind = c.kind
	gen.Binary = c.binary
	gen.AdditionalServices = nil
	if len(c.serviceNames) > 0 {
		gen.ServiceName = c.serviceNames[0]
		// Pass the rest so binary=shared can emit one cobra subcommand per
		// service at scaffold time. Per-service mode ignores this and
		// scaffolds the additional services post-scaffold via
		// GenerateServiceFiles.
		if len(c.serviceNames) > 1 {
			gen.AdditionalServices = append([]string(nil), c.serviceNames[1:]...)
		}
	}
	gen.GoVersionOverride = c.goVersion
	gen.ForgePin = c.forgePin
	gen.GenInRootModule = c.genInRootModule
	gen.Adopting = c.adopting
	if len(c.frontendNames) > 0 {
		gen.FrontendName = c.frontendNames[0]
	}
	gen.FrontendWorkspaces = c.frontendWorkspaces
	gen.FrontendOutput = c.frontendOutput
	gen.FrontendRoutes = c.frontendRoutes

	h, err := generator.ParseHarness(c.harness)
	if err != nil {
		return nil, err
	}
	gen.Harness = h

	// Kind-aware feature defaults BEFORE --disable. Service is the default and
	// leaves every feature enabled; CLI and library kinds turn off the
	// server-shaped features so the scaffolded forge.yaml accurately describes
	// the project shape. See ApplyKindFeatureDefaults for the per-kind matrix.
	gen.ApplyKindFeatureDefaults(c.kind)

	if err := applyDisableFlags(gen, c.disableFeatures); err != nil {
		return nil, err
	}
	return gen, nil
}

// generateAdditionalServices scaffolds every service beyond the first: the
// handler/proto skeleton, via GenerateServiceFiles. That is the whole job in
// both binary modes — forge derives the service inventory from the proto
// descriptor, so a scaffolded service needs no entry written anywhere.
//
// There is also no per-service port to assign: every service in a binary
// mounts onto the SAME Connect mux and the process listens once, on the port
// from AppConfig (env PORT, default 8080), which is a deploy fact set per
// environment in KCL.
func generateAdditionalServices(targetPath, modulePath, projectName string, serviceNames []string) error {
	if len(serviceNames) <= 1 {
		return nil
	}
	for _, svcName := range serviceNames[1:] {
		fmt.Printf("\n🔧 Adding additional service '%s'...\n", svcName)
		if err := generator.GenerateServiceFiles(targetPath, modulePath, svcName, projectName); err != nil {
			return fmt.Errorf("failed to generate service %s: %w", svcName, err)
		}
	}
	return nil
}

// generateAdditionalFrontends scaffolds every frontend beyond the first,
// appending each to forge.yaml.
//
// Port assignment mirrors the primary frontend's ephemeral model: when
// baseFrontendPort is 0 (the fresh-scaffold default — the primary is portless,
// FrontendConfig.Port omitempty, allocated at `forge env up`/`up` launch) EVERY
// additional frontend is also written portless (0), so N frontends all get a
// distinct free port assigned at launch by resolveEphemeralFrontendPorts and
// two dev stacks never fight. Only when the caller passes an explicit base
// (>0) do additionals get a distinct concrete port above it (base+i+1). This
// replaces the old unconditional base+i+1, which — once the primary went
// ephemeral (base 0) — emitted the nonsensical `port: 1`, `port: 2`, … into
// the scaffolded forge.yaml.
func generateAdditionalFrontends(targetPath, modulePath, projectName string, frontendNames []string, servicePort, baseFrontendPort int, opts generator.FrontendGenOptions, routes []string) error {
	for i, feName := range frontendNames[min(1, len(frontendNames)):] {
		fePort := 0
		if baseFrontendPort > 0 {
			fePort = baseFrontendPort + i + 1
		}
		if fePort > 0 {
			fmt.Printf("\n🔧 Adding additional frontend '%s' (port %d)...\n", feName, fePort)
		} else {
			fmt.Printf("\n🔧 Adding additional frontend '%s' (ephemeral port — allocated at launch)...\n", feName)
		}
		if err := generator.GenerateFrontendFilesWithOptions(targetPath, modulePath, projectName, feName, servicePort, "", opts); err != nil {
			return fmt.Errorf("failed to generate frontend %s: %w", feName, err)
		}
		scaffold.DeclareFrontendInKCL(targetPath, projectName, feName, "nextjs", fePort, routes)
	}
	return nil
}

// applyRemoteBufPlugins switches the scaffolded Go and per-frontend buf.gen.yaml
// files from local: plugins to BSR-hosted remote: plugins, the coherent
// --buf-plugins=remote opt-in. Failures are warned, not fatal.
func applyRemoteBufPlugins(targetPath string, frontendNames []string) {
	if err := rewriteBufGenYamlToRemote(targetPath); err != nil {
		fmt.Fprintf(os.Stderr, "\n⚠️  Failed to switch buf.gen.yaml to remote plugins: %v\n", err)
	} else {
		fmt.Println("\n🔧 Switched buf.gen.yaml to BSR-hosted (remote:) plugins per --buf-plugins=remote")
		fmt.Println("    Note: anonymous users may hit BSR rate limits; run 'buf registry login' if needed.")
	}
	// Also rewrite each frontend's buf.gen.yaml to use remote: bufbuild/es
	// rather than the local protoc-gen-es plugin. Mirrors the Go-side switch.
	for _, feName := range frontendNames {
		feBufGen := filepath.Join(targetPath, "frontends", feName, "buf.gen.yaml")
		if err := rewriteFrontendBufGenYamlToRemote(feBufGen, feName); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  Failed to switch frontends/%s/buf.gen.yaml to remote plugin: %v\n", feName, err)
		}
	}
}

// newFinalizeInput carries the inputs finalizeNewProject needs for the
// post-generation phase.
type newFinalizeInput struct {
	targetPath    string
	kind          string
	binary        string
	bufPlugins    string
	skipTools     bool
	frontendNames []string
	markerPath    string
	// verbose prints the raw output of a step forge otherwise summarizes
	// in one line (today: the initial commit skipped for want of a git
	// identity).
	verbose bool
	// verify keeps the bootstrap generate's compile check (--verify).
	verify bool
}

// finalizeNewProject runs the post-generation phase: install proto codegen
// plugins (local-plugin service kind only), install frontend deps BEFORE
// bootstrap (the scaffolded buf.gen.yaml's local: es plugin needs node_modules),
// bootstrap proto/Connect codegen (service kind only), re-record frozen-file
// checksums (bootstrap's goimports -w reformats pkg/* and would otherwise show
// as user-modified), init git, run go mod tidy, and remove the in-progress
// marker. Every step but one is best-effort — failures are warned, and the
// rest still run. The exception is the bootstrap generate: a failure there is
// returned as genErr after everything else has run, so `project new` exits
// non-zero over a project that does not generate (newBootstrapFailedErr),
// with the files it wrote kept.
//
// Git is only initialised for a target OUTSIDE any existing work tree. When
// the target is already inside one — an existing product repo converted in
// place, or a subdirectory of a monorepo — forge must not add a nested repo,
// reconfigure the user's repo, or commit (`git add .` would sweep in every
// untracked file they have). The work tree's top level is returned so the
// caller can say so; "" means forge initialised (or tried to) itself.
func finalizeNewProject(ctx context.Context, in newFinalizeInput) (existingRepo string, genErr error) {
	// Auto-install required proto plugins for the default local-plugin
	// workflow. Skipped for --skip-tools, remote plugins (no local binaries),
	// and non-service kinds.
	if !in.skipTools && in.bufPlugins == "local" && in.kind == config.ProjectKindService {
		fmt.Println("\n🔧 Ensuring proto codegen plugins are installed (use --skip-tools to skip)...")
		if err := runToolsInstall(ctx, "latest", false); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  Plugin install incomplete: %v\n", err)
			fmt.Fprintf(os.Stderr, "    Run '%s tools install' manually before '%s generate'.\n", Name(), Name())
		}
	}

	// Frontend dependencies must be installed BEFORE bootstrapGeneratedCode
	// runs, because the scaffolded buf.gen.yaml uses a `local:` plugin path
	// pointing at frontends/<name>/node_modules/.bin/protoc-gen-es.
	if len(in.frontendNames) > 0 {
		fmt.Println("🔧 Installing frontend dependencies (this generates package-lock.json)...")
		if err := runNpmInstall(ctx, in.targetPath, in.frontendNames); err != nil {
			fmt.Printf("Warning: npm install failed: %v\n", err)
			fmt.Println("    @bufbuild/protoc-gen-es will be missing — run 'npm install' in each frontends/<name>/ before 'forge generate'.")
			fmt.Println("    CI also requires package-lock.json to exist.")
		}
		// The install just decided the node_modules layout AND made each
		// pinned package's manifest readable, so this is the one moment the
		// tsconfig peer pins can be aimed at real declaration files. Until
		// then the template could only name package DIRECTORIES, which
		// Next's webpack resolver honours for app code, splitting every
		// pinned package with an exports/module mismatch into two module
		// instances (prerender: "No QueryClient set"). `forge scaffold
		// frontend` runs the same pass after its install.
		generator.ReconcileFrontendTsconfigPeers(in.targetPath)
	}

	// Service projects bootstrap proto/Connect codegen immediately so the
	// scaffold compiles. CLI/library kinds have no proto/services.
	//
	// A failure is RECOVERABLE BY RE-RUNNING: everything this pass owns —
	// gen/, internal/app, and cmd/<bin>/main.go — is either regenerated or
	// written write-if-absent, so a later `forge generate` fills in exactly
	// what is missing. That is why the scaffold does not pre-write a
	// placeholder composition root: an absent main.go gets the real one on
	// retry, a bare one would be permanent. It is also why the failure does
	// not stop the steps below (git init, go mod tidy): the project they
	// leave is the one the re-run completes. It is reported, not swallowed.
	if in.kind == config.ProjectKindService {
		fmt.Println("\n🔧 Bootstrapping generated proto code...")
		genErr = bootstrapGenerate(in.targetPath, in.verify)
	}

	// Re-record frozen-file checksums after bootstrap (goimports -w reformats
	// pkg/* godoc list markers, which would otherwise show as user-modified).
	postBootstrapBinary := config.ProjectBinaryPerService
	if in.binary != "" {
		postBootstrapBinary = in.binary
	}
	if err := generator.RecordFrozenChecksums(in.targetPath, postBootstrapBinary, in.kind); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to re-record frozen checksums: %v\n", err)
	}

	if top, inRepo := gitWorkTreeRoot(ctx, in.targetPath); inRepo {
		existingRepo = top
	} else {
		fmt.Println("\n🔧 Initializing git repository...")
		if err := initGitRepository(ctx, in.targetPath); err != nil {
			reportGitInitError(os.Stdout, err, in.verbose)
		}
	}

	fmt.Println("🔧 Running go mod tidy...")
	if err := runGoModTidy(ctx, in.targetPath); err != nil {
		fmt.Printf("Warning: go mod tidy failed: %v\n", err)
		fmt.Println("You can run 'go mod tidy' manually later")
	}

	// Scaffold finished — remove the in-progress marker so a later failure
	// (if any were ever added) wouldn't delete a completed project.
	if err := os.Remove(in.markerPath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "warning: failed to remove scaffold marker: %v\n", err)
	}
	return existingRepo, genErr
}

// bootstrapGenerate is the generate run `project new` ends with. A variable
// so a test can make it fail without breaking a real project.
var bootstrapGenerate = bootstrapGeneratedCode

// newBootstrapFailedErr is what `project new` returns when the project it
// wrote failed its own first generate. That project is not finished — it may
// not even build — so the command must not exit 0 and print success: a
// script, a CI step or an agent reading the exit status would carry on over
// it, which is how a whole project shape (--disable codegen) stayed broken
// with nothing noticing. The files stay, because a re-run of generate
// completes them (finalizeNewProject).
func newBootstrapFailedErr(projectName, targetPath string, inPlace bool, err error) error {
	what := fmt.Sprintf("project %q was scaffolded at %s, but its initial code generation failed, so it is not complete", projectName, targetPath)
	// A run that reached the end with failed steps kept everything else it
	// generated; only one that aborted left the tree bare.
	var stepsFailed *factory.StepsFailedError
	if errors.As(err, &stepsFailed) {
		what += " (everything else was generated; the output of the failed step(s) is missing)"
	} else {
		what += " (gen/, internal/app/ and the cmd/<bin>/main.go composition root are missing, so it does not build)"
	}
	dir := ""
	if !inPlace {
		dir = "cd " + targetPath + " && "
	}
	return cliutil.WrapUserErr(Name()+" project new", what, "",
		fmt.Sprintf("the scaffolded files are kept: fix the failure above, then run `%s%s generate` to complete it", dir, Name()), err)
}

// printNewNextSteps prints the post-scaffold guidance block. The
// zero-service default is deliberate: a binary is a deployment unit that
// mounts services — it is NOT a domain entity, so `forge project new` never
// invents a `<project>Service` with CRUD RPCs nobody asked for. On a
// bare service-kind scaffold the documented first step is
// `forge scaffold service <entity>` with a real domain entity name.
//
// Every forge command printed here is one the user can PASTE, in order, with
// no substitution. That is the whole contract of this block, and it is
// enforced by TestNewNextStepsArePasteable.
//
// Step one is a proto edit, because the proto is the only place an entity is
// declared. That is an instruction, not a command, so the block does the two
// things that used to be missing when it was worded that way: it names the
// exact FILE (not a directory), and it shows the `// forge:entity` marker
// inline — the one piece of forge-specific syntax involved, which nothing
// else on screen teaches. `forge scaffold` then does the rest in one step,
// which is what makes naming the proto edit affordable.
func printNewNextSteps(projectName string, inPlace bool, kind string, serviceNames []string, hasFrontend bool) {
	for _, line := range newNextSteps(projectName, inPlace, kind, serviceNames, hasFrontend) {
		fmt.Println(line)
	}
}

// newNextSteps renders the block printNewNextSteps writes, as lines. Split
// out so the paste-ability contract is testable without capturing stdout.
func newNextSteps(projectName string, inPlace bool, kind string, serviceNames []string, hasFrontend bool) []string {
	n := Name()
	out := []string{"", "Next steps:"}
	if !inPlace {
		out = append(out, "  cd "+projectName)
	}
	switch {
	case kind == config.ProjectKindCLI:
		out = append(out,
			"  go build ./...        # the cobra skeleton compiles out of the box",
			"  see README.md for the CLI workflow")
	case kind == config.ProjectKindLibrary:
		out = append(out,
			"  go build ./...        # the pkg/ skeleton compiles out of the box",
			"  add exported types under pkg/ and tests alongside them")
	case len(serviceNames) == 0 && hasFrontend:
		// Frontend-first: a landing page, a marketing or docs site, or a SPA
		// over an API that lives elsewhere. Its first step is its frontend —
		// leading with `scaffold service item` is how a static site ends up
		// with a CRUD backend nobody asked for. The service step stays one
		// line away for the project that does grow an API.
		//
		// The dev loop is the first command: the scaffolded dev env needs no
		// cluster (its k3d cluster_target places only a Namespace and a
		// Gateway, which `env up` leaves alone until something runs there),
		// so it comes up on a fresh machine. The skill follows for shipping.
		out = append(out,
			"  build the site in frontends/ — it is starter code, yours to rewrite",
			fmt.Sprintf("  %s env up dev", n),
			"      ↳ the frontend's dev server (and its sign-in), on this machine",
			fmt.Sprintf("  %s skill load deploy/static-site", n),
			"      ↳ the dev loop, the static export, hosted static hosting, and deploying it",
			"",
			"  Need an API or a database too?",
			fmt.Sprintf("  %s scaffold service item", n),
			"      ↳ name it after a DOMAIN ENTITY (item, order, user) — not after the binary")
	case len(serviceNames) == 0:
		out = append(out,
			fmt.Sprintf("  %s scaffold service item", n),
			"      ↳ name it after a DOMAIN ENTITY (item, order, user) — not after the binary",
			fmt.Sprintf("  %s env up dev", n),
			"      ↳ boots the stack; /healthz serves even before any service exists")
	default:
		svc := serviceNames[0]
		protoPath := fmt.Sprintf("proto/services/%s/v1/%s.proto", naming.ServicePackage(svc), svc)
		scaffold := fmt.Sprintf("  %s scaffold", n)
		if len(serviceNames) > 1 {
			// More than one service — narrow the sweep to the one just named.
			scaffold += " --service " + svc
		}
		out = append(out,
			"  declare your first entity in "+protoPath+":",
			"",
			"      // forge:entity",
			"      message Item {",
			"        string name = 2;",
			"        int64 price_cents = 3;",
			"        bool active = 4;",
			"      }",
			"",
			"      ↳ the marker is the tablizing decision; custom RPCs go in the same file",
			scaffold,
			"      ↳ births every marked message — migration pair + CRUD quintet — then generates",
			fmt.Sprintf("  %s env up dev", n))
		if hasFrontend {
			// Auth is fail-closed, so every RPC 401s until an identity
			// provider is wired. `forge env up` does that wiring itself and
			// prints the credentials — but a reader who never gets there
			// reads a 401 as a broken scaffold. Say it here, where they
			// are already looking.
			out = append(out,
				"      ↳ applies migrations, seeds demo rows, brings up the dev IdP,",
				"        and prints your URLs + the sign-in you were scaffolded",
				"",
				"  The UI you will see is starter code, not a design — yours to rewrite.",
				"      ↳ why, and what finishing it takes, is in your project memory file")
		} else {
			out = append(out, "      ↳ applies migrations, seeds demo rows, prints your URLs")
		}
		out = append(out,
			"",
			fmt.Sprintf("  Field types and markers: %s project annotations --kind field", n))
	}
	return out
}

// rewriteBufGenYamlToRemote switches the scaffolded buf.gen.yaml from
// `local:` plugins (the default) to BSR-hosted `remote:` plugins. Used
// by `forge project new --buf-plugins=remote` for users who explicitly want the
// no-install-required experience and accept BSR rate-limits / auth.
//
// Idempotent: a buf.gen.yaml that already declares the remote plugins is
// rewritten to itself.
func rewriteBufGenYamlToRemote(projectDir string) error {
	path := filepath.Join(projectDir, "buf.gen.yaml")
	if _, err := os.Stat(path); err != nil {
		// No buf.gen.yaml in this project (e.g. library kind). Nothing to do.
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat buf.gen.yaml: %w", err)
	}
	remote := `version: v2
# Switched to BSR-hosted plugins via 'forge project new --buf-plugins=remote'.
# No local protoc-gen-go install required, but anonymous users may hit
# BSR rate limits during heavy generate cycles — 'buf registry login'
# raises the cap. To switch back, replace 'remote: <bsr-path>' with
# 'local: <binary-name>'.
` + bufGenGoInputs + `plugins:
  - remote: buf.build/protocolbuffers/go
    out: gen
    opt:
      - paths=source_relative
  - remote: buf.build/connectrpc/go
    out: gen
    opt:
      - paths=source_relative
`
	return atomicfile.WriteFile(path, []byte(remote), 0o644)
}

// rewriteFrontendBufGenYamlToRemote switches a frontend's buf.gen.yaml from
// the default local: TS plugin to the BSR-hosted remote: bufbuild/es. Mirrors
// rewriteBufGenYamlToRemote — used by `forge project new --buf-plugins=remote` so
// users who explicitly want the no-install BSR experience get it on both
// the Go and TS sides. Idempotent and a no-op when the file is missing.
func rewriteFrontendBufGenYamlToRemote(path, feName string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", path, err)
	}
	remote := fmt.Sprintf(`version: v2
# Switched to BSR-hosted plugin via 'forge project new --buf-plugins=remote'.
# No npm install of @bufbuild/protoc-gen-es required, but anonymous users
# may hit BSR rate limits — 'buf registry login' raises the cap. To switch
# back, replace 'remote: buf.build/bufbuild/es' with
#   'local: ./frontends/%s/node_modules/.bin/protoc-gen-es'
plugins:
  - remote: buf.build/bufbuild/es
    out: frontends/%s/src/gen
    include_imports: true
    opt:
      - target=ts
      - import_extension=.js
`, feName, feName)
	return atomicfile.WriteFile(path, []byte(remote), 0o644)
}

// forgeModulePathForBridge is forge's module path — the single module that
// carries both the CLI and the pkg/* runtime libraries a scaffold imports.
// The dev bridge resolves it from the local checkout's root.
const forgeModulePathForBridge = "github.com/reliant-labs/forge"

// writeDevForgeGoWork bridges a freshly-scaffolded project to the LOCAL forge
// checkout this binary was built from (buildinfo.DevForgeRoot, injected by the
// `make dev` / `task install:dev` ldflag, else recovered from the binary's
// compiled paths) — and only when link is set, i.e. the user passed
// --link-forge.
//
// Why a bridge exists at all: a fresh scaffold pins a PUBLISHED forge, but a
// forge built from source can generate code that targets unpublished forge/pkg
// APIs, so that pin won't build. A gitignored go.work that `use`s the checkout
// is the fix (gen-go.mod.tmpl's own comment references it).
//
// Why it is never implicit: it used to be written for every dev build. A host
// binary that embeds forge through a workspace (reliant) is a dev build too,
// so every project it scaffolded got bridged to whatever checkout that host
// compiled from — on a shared machine, the main checkout everyone pulls into.
// The library under those projects then moved with every merge while the
// binary generating their code did not, and nothing said so. The bridge is now
// a decision the user makes, and forgecompat.InspectBridge reports whenever it
// drifts.
//
// It augments the starter go.work the generator emitted for every kind that
// imports forge (use ., plus gen when gen/ is its own module) with `use <root>`. go.work / go.work.sum are already in the scaffold's
// .gitignore, so the machine-local path never gets committed.
func writeDevForgeGoWork(targetPath string, link bool) {
	if !link {
		return
	}
	root, _ := devForgeBridgeRoot()
	if root == "" {
		// checkScaffoldCanResolveForge refuses --link-forge without a root
		// before anything is written; this only guards a direct caller.
		fmt.Fprintf(os.Stderr, "warning: --link-forge: this forge cannot locate the checkout it was built from; no bridge written\n")
		return
	}

	workPath := filepath.Join(targetPath, "go.work")
	data, err := os.ReadFile(workPath)
	if err != nil {
		// No go.work: a library, which imports no forge package, so there
		// is nothing to bridge. Every kind that imports forge — a service
		// with or without codegen, a CLI — is scaffolded with one.
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "warning: could not read go.work for dev forge bridge: %v\n", err)
		}
		return
	}
	wf, err := modfile.ParseWork(workPath, data, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not parse go.work for dev forge bridge: %v\n", err)
		return
	}

	// forge is ONE module: pkg/* are packages inside it, not a submodule, so
	// the workspace `use`s the forge repo ROOT. (It used to `use <root>/pkg`
	// and deliberately skip the main module to keep forge's dependency tree
	// out of the project's build; with a single module that choice no longer
	// exists, and the graph weight is the accepted cost of generator and
	// runtime never being able to disagree about a version.)
	//
	// AddUse tags the entry with the module path so a re-run is idempotent.
	if err := wf.AddUse(root, forgeModulePathForBridge); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not add dev forge use directive: %v\n", err)
		return
	}
	wf.Cleanup()
	if err := atomicfile.WriteFile(workPath, modfile.Format(wf.Syntax), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write dev forge go.work: %v\n", err)
		return
	}
	fmt.Printf("🔗 --link-forge: wrote go.work bridging this project to %s (gitignored, machine-local; undo: go work edit -dropuse=%s)\n", root, root)
}

// explicitForgeRoot is --forge-root for the `project new` run in progress.
var explicitForgeRoot string

// devForgeBridgeRoot is the local forge checkout --link-forge bridges a
// scaffold to, or "" when none can be found. A path stated by --forge-root or
// $FORGE_ROOT that is not a forge checkout is an error. How the rest is
// discovered, and why not only from the build: buildinfo.ResolveForgeRoot.
func devForgeBridgeRoot() (string, error) {
	r, _, err := buildinfo.ResolveForgeRoot(explicitForgeRoot)
	return r.Dir, err
}

// checkScaffoldCanResolveForge refuses, BEFORE anything is written, a
// --link-forge this binary cannot honour, and a service scaffold that could
// neither pin forge in go.mod nor be bridged (scaffoldForgeResolution). For an
// in-place scaffold adopting a go.mod that already requires forge (adopted
// non-nil), the require exists, so the question becomes whether this binary
// can generate into the forge it resolves (adoptedForgeResolution).
func checkScaffoldCanResolveForge(kind string, link bool, adopted *adoptedForge) error {
	root, rootErr := devForgeBridgeRoot()
	if rootErr != nil {
		return cliutil.UserErr(Name()+" project new --forge-root", rootErr.Error(), "",
			"name a forge checkout (a directory with forge's go.mod and pkg/), or drop the flag")
	}
	if link && root == "" {
		return cliutil.UserErr(Name()+" project new --link-forge",
			fmt.Sprintf("this forge (%s) cannot locate a forge checkout to bridge to: it carries no build-time path "+
				"(a -trimpath build, or a binary copied off the machine that built it), embeds no go.mod replace "+
				"that resolves, and neither it nor this directory is inside a checkout. Nothing was written",
				buildinfo.Version()),
			"",
			"name the checkout: --forge-root <path-to-forge-checkout> (or "+buildinfo.ForgeRootEnv+"=<path>), "+
				"or rebuild forge from its checkout with `task install:dev` / `make dev`")
	}
	if adopted != nil {
		return adoptedForgeResolution(kind, *adopted, link, buildinfo.InstallableVersion(), buildinfo.Version(),
			root, forgeInvocationArgs())
	}
	bridgeRoot := ""
	if link {
		bridgeRoot = root
	}
	return scaffoldForgeResolution(kind, buildinfo.InstallableVersion(), bridgeRoot, root)
}

// scaffoldForgeResolution is the pure decision behind
// checkScaffoldCanResolveForge.
//
// A service scaffold imports github.com/reliant-labs/forge/pkg/*, so its
// go.mod must get forge from SOMEWHERE: a `require` at a version a proxy can
// serve (pinnedVersion), or a go.work `use` of a local checkout (bridgeRoot).
// With neither, the scaffold used to proceed anyway and `go mod tidy` filled
// the gap itself — resolving github.com/reliant-labs/forge/pkg/forgepb to the
// longest module path that provides it, the RETIRED module
// github.com/reliant-labs/forge/pkg v0.1.15. The project then failed its very
// first `forge generate` with "requires the retired module", for a require
// forge had written. A plain `task install` (-trimpath, so no discoverable
// source) produced exactly that.
//
// Refusing up front is the honest outcome: nothing is written, and the error
// names the ways to get forge resolved. CLI and library kinds import no forge
// package, so they are unaffected.
//
// linkableRoot is the checkout --link-forge WOULD bridge to ("" when this
// binary cannot locate one); it decides which fix the refusal offers.
func scaffoldForgeResolution(kind, pinnedVersion, bridgeRoot, linkableRoot string) error {
	if kind != config.ProjectKindService || pinnedVersion != "" || bridgeRoot != "" {
		return nil
	}
	const retired = "Scaffolding anyway would let `go mod tidy` pick the retired " +
		"github.com/reliant-labs/forge/pkg module and the project would fail its first generate. Nothing was written"
	if linkableRoot != "" {
		return cliutil.UserErr(Name()+" project new (forge version)",
			fmt.Sprintf("this forge (%s) is an unreleased build no module proxy can serve, so go.mod cannot "+
				"require it, and the project was not bridged to its source. %s", buildinfo.Version(), retired),
			"",
			fmt.Sprintf("re-run with --link-forge to bridge the project to the checkout this forge was built from "+
				"(%s; a gitignored go.work — generate, lint and doctor then warn when the two drift apart), "+
				"or install a published forge (`go install github.com/reliant-labs/forge/cmd/forge@<version>`)", linkableRoot))
	}
	return cliutil.UserErr(Name()+" project new (forge version)",
		fmt.Sprintf("this forge (%s) cannot tell a new project where to get forge: it is not a version a "+
			"module proxy can serve, so go.mod cannot require it, and it cannot find the forge source it "+
			"was built from, so go.work cannot bridge to it. %s", buildinfo.Version(), retired),
		"",
		"use a forge that can resolve itself: install a published one "+
			"(`go install github.com/reliant-labs/forge/cmd/forge@<version>`), or, from a forge checkout, "+
			"`task install:dev` / `make dev` (which records the checkout) and scaffold with --link-forge")
}

// initGitRepository initializes a git repository and makes initial commit
func initGitRepository(ctx context.Context, path string) error {
	cmd := exec.CommandContext(ctx, "git", "init")
	cmd.Dir = path
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init failed: %s", string(output))
	}

	// Activate forge's committed git hooks (.githooks/pre-push runs
	// `forge lint` + `forge project audit`; pre-commit stays fast). core.hooksPath
	// is set RELATIVE so it resolves against each worktree's own checkout —
	// one setting in the shared .git/config makes the hooks fire in every
	// linked worktree. Set here, before the initial commit, so the repo
	// ships activated; fresh clones self-heal via ensureGitHooksActivated.
	cmd = exec.CommandContext(ctx, "git", "config", "--local", "core.hooksPath", ".githooks")
	cmd.Dir = path
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git config core.hooksPath failed: %s", string(output))
	}

	cmd = exec.CommandContext(ctx, "git", "add", ".")
	cmd.Dir = path
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %s", string(output))
	}

	// --no-verify: the initial commit is forge-authored, known-good
	// scaffolding — skip the pre-commit framework chain (gofmt/gitleaks/…)
	// on it. The heavy gate (forge lint + audit) is on pre-push, which this
	// commit doesn't trigger anyway; the hooks govern the user's work from
	// here on.
	cmd = exec.CommandContext(ctx, "git", "commit", "--no-verify", "-m", initialCommitMessage)
	cmd.Dir = path
	if output, err := cmd.CombinedOutput(); err != nil {
		if gitIdentityMissing(ctx, path) {
			return &gitIdentityError{output: string(output)}
		}
		return fmt.Errorf("git commit failed: %s", string(output))
	}

	return nil
}

const initialCommitMessage = "Initial commit from forge"

// gitIdentityError reports that the initial commit was not made because git
// has no author or committer identity: no user.name / user.email, and none
// it could guess from the machine. Everything before the commit succeeded —
// the repository exists, hooks are activated and every file is staged — so
// this is a skipped step the user finishes with one command, not a failed
// init. output is git's own explanation, shown only under --verbose.
type gitIdentityError struct {
	output string
}

func (e *gitIdentityError) Error() string {
	return "git commit failed: no git identity (user.name / user.email)"
}

// gitIdentityMissing reports whether git in dir has no usable author or
// committer identity. Asked after a failed commit, so the failure is
// attributed to identity only when git itself says that is the problem — not
// by matching commit's output, which is localized.
func gitIdentityMissing(ctx context.Context, dir string) bool {
	for _, ident := range []string{"GIT_AUTHOR_IDENT", "GIT_COMMITTER_IDENT"} {
		cmd := exec.CommandContext(ctx, "git", "var", ident)
		cmd.Dir = dir
		var exitErr *exec.ExitError
		if err := cmd.Run(); errors.As(err, &exitErr) {
			return true
		}
	}
	return false
}

// reportGitInitError prints why initGitRepository fell short. A missing git
// identity is the common case on a fresh machine and is one line: git's
// "Please tell me who you are" text names config commands but not the commit
// forge skipped, so it is shown only under --verbose. Anything else is
// unexpected, and git's output is the diagnosis.
func reportGitInitError(w io.Writer, err error, verbose bool) {
	var idErr *gitIdentityError
	if !errors.As(err, &idErr) {
		fmt.Fprintf(w, "Warning: failed to initialize git repository: %v\n", err)
		return
	}
	// --no-verify, as forge's own commit used: the commit-msg hook runs
	// commitlint when pre-commit is installed, and this message is not a
	// Conventional Commit.
	fmt.Fprintf(w, "⚠️  git repository initialized and staged, but the initial commit was skipped: "+
		"no git identity (set user.name / user.email, then 'git commit --no-verify -m %q').\n", initialCommitMessage)
	if verbose {
		fmt.Fprintf(w, "    git commit said:\n%s\n", indentLines(strings.TrimSpace(idErr.output), "      "))
	}
}

// npmInstallAttempts is how many times a frontend's `npm install` is tried
// before giving up. The registry is a network dependency on the critical path
// of the very first command a new user runs, and a single transient 429 or
// reset connection there does not leave a clean failure: the scaffold
// continues (its caller only warns), and the missing node_modules surfaces
// much later as some other tool reporting a package it cannot find — for tsc,
// an npx fallback that names a 2016 package nobody asked for. One retry turns
// most of that class into a slower success.
const npmInstallAttempts = 2

// runNpmInstall runs `npm install` in each frontend directory so that a
// package-lock.json exists before first commit. CI relies on `npm ci` which
// requires the lockfile.
func runNpmInstall(ctx context.Context, root string, frontends []string) error {
	if _, err := exec.LookPath("npm"); err != nil {
		return fmt.Errorf("npm not found on PATH: %w", err)
	}
	for _, name := range frontends {
		feDir := filepath.Join(root, "frontends", name)
		if _, err := os.Stat(filepath.Join(feDir, "package.json")); err != nil {
			continue
		}
		// A Node below the frontend's floor is not flaky: no retry passes,
		// and npm's own failure ("reading 'edgesOut'") never names Node.
		if err := nodeengine.Check(feDir); err != nil {
			return fmt.Errorf("npm install (%s) not run: %w", name, err)
		}
		var lastErr error
		for attempt := 1; attempt <= npmInstallAttempts; attempt++ {
			cmd := exec.CommandContext(ctx, "npm", "install", "--no-audit", "--no-fund")
			cmd.Dir = feDir
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			lastErr = cmd.Run()
			if lastErr == nil {
				break
			}
			// A cancelled context is the user or a timeout, not a flaky
			// registry — retrying only makes them wait for the same answer.
			if ctx.Err() != nil {
				return fmt.Errorf("npm install (%s) failed: %w", name, lastErr)
			}
			if attempt < npmInstallAttempts {
				fmt.Printf("  ⚠️  npm install in frontends/%s/ failed (attempt %d/%d): %v — retrying\n",
					name, attempt, npmInstallAttempts, lastErr)
			}
		}
		if lastErr != nil {
			return fmt.Errorf("npm install (%s) failed after %d attempts: %w", name, npmInstallAttempts, lastErr)
		}
	}
	return nil
}

// runGoModTidy runs go mod tidy in the project root and gen/ directories when safe.
func runGoModTidy(ctx context.Context, path string) error {
	// A dev-forge go.work bridge deliberately overrides a published require
	// (forge) with an unpublished local checkout. Tidy each module against
	// its OWN requirements (GOWORK=off) — the same bridged tidy the generate
	// pipeline runs, and exactly the graph `forge env up`'s `go mod tidy
	// -diff` preflight checks. This used to run `go work sync`, which never
	// writes the go.sum `/go.mod` hashes a GOWORK=off tidy wants, so a
	// freshly scaffolded project failed that preflight before its first
	// generate.
	bridged := devWorkspaceBridgesExternalModule(path)

	shouldTidyRoot, err := shouldRunRootGoModTidy(path)
	if err != nil {
		return err
	}

	switch {
	case !shouldTidyRoot:
		fmt.Printf("ℹ️  Skipping root go mod tidy until generated proto code exists. Run '%s generate' first.\n", Name())
	case bridged:
		if err := tidyBridgedModule(path, "root tidy"); err != nil {
			return err
		}
	default:
		cmd := exec.CommandContext(ctx, "go", "mod", "tidy")
		cmd.Dir = path
		cmd.Env = goexec.Env()
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("go mod tidy (root) failed: %w", err)
		}
	}

	genDir := filepath.Join(path, "gen")
	if _, err := os.Stat(filepath.Join(genDir, "go.mod")); err != nil {
		return nil
	}
	// A fresh scaffold's gen/ holds no Go files until the first generate, and
	// tidy on a module with no packages deletes every require the template
	// wrote — including forge, which is what then lets the next tidy resolve
	// forge/pkg/* to the retired forge/pkg module.
	if !dirHasGoFiles(genDir) {
		return nil
	}
	if bridged {
		return tidyBridgedModule(genDir, "gen/ tidy")
	}
	cmd := exec.CommandContext(ctx, "go", "mod", "tidy")
	cmd.Dir = genDir
	cmd.Env = goexec.Env()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go mod tidy (gen) failed: %w", err)
	}
	return nil
}

// bootstrapPipelineFlags is the generate run `project new` ends with.
//
// It skips the validation tail (the `go build` compile check and the deploy
// KCL render) unless the caller asked for it with --verify. That tail is the
// one step of the scaffold that saturates the CPU: on a cold build cache it
// was 40s of the pipeline's 63s and ran every package of the new tree through
// the compiler. It also proves nothing the scaffold does not already
// guarantee: the output is deterministic and forge's e2e corpus compiles it on
// every change. The first `go build` or `forge generate` the user runs pays for
// the same compile, with the build cache warm for it.
//
// Nothing here takes a heavy slot. `project new` is an install step the
// user waits on, not a build lane, and the CPU-bound step that justified a
// slot is the one this skips.
func bootstrapPipelineFlags(verify bool) pipelineFlags {
	if verify {
		return pipelineFlags{}
	}
	return pipelineFlags{
		SkipValidate:     true,
		SkipValidateNote: "⏩ compile check skipped (pass --verify to run it here; your first `go build` or `forge generate` runs it anyway)",
	}
}

func bootstrapGeneratedCode(path string, verify bool) error {
	return withGenerateLock(path, func() error {
		return runGeneratePipelineFlags(path, bootstrapPipelineFlags(verify))
	})
}

func shouldRunRootGoModTidy(path string) (bool, error) {
	moduleName, err := readModuleName(path)
	if err != nil {
		return false, err
	}

	serviceRoot := filepath.Join(path, "internal", "handlers")
	if _, err := os.Stat(serviceRoot); os.IsNotExist(err) {
		return true, nil
	} else if err != nil {
		return false, fmt.Errorf("inspect handlers directory: %w", err)
	}

	missingGeneratedImports := false
	err = filepath.Walk(serviceRoot, func(currentPath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || filepath.Ext(currentPath) != ".go" {
			return nil
		}

		contents, err := os.ReadFile(currentPath)
		if err != nil {
			return err
		}

		for _, line := range strings.Split(string(contents), "\n") {
			importPath, ok := extractQuotedImportPath(line)
			if !ok || !strings.HasPrefix(importPath, moduleName+"/gen/") {
				continue
			}

			relativeImportPath := strings.TrimPrefix(importPath, moduleName+"/")
			generatedPath := filepath.Join(path, filepath.FromSlash(relativeImportPath))
			if _, err := os.Stat(generatedPath); os.IsNotExist(err) {
				missingGeneratedImports = true
				return filepath.SkipAll
			} else if err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return false, fmt.Errorf("inspect generated imports before go mod tidy: %w", err)
	}

	return !missingGeneratedImports, nil
}

func extractQuotedImportPath(line string) (string, bool) {
	firstQuote := strings.Index(line, "\"")
	if firstQuote == -1 {
		return "", false
	}

	remaining := line[firstQuote+1:]
	secondQuote := strings.Index(remaining, "\"")
	if secondQuote == -1 {
		return "", false
	}

	return remaining[:secondQuote], true
}

func readModuleName(path string) (string, error) {
	contents, err := os.ReadFile(filepath.Join(path, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}

	for _, line := range strings.Split(string(contents), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "module ")), nil
		}
	}

	return "", fmt.Errorf("module directive not found in go.mod")
}

// applyDisableFlags turns the named features off, for this scaffold and
// after it. Each one is pinned off in the written forge.yaml's `features:`
// block, so a later change to the tree that would derive it back on (the
// first internal/handlers/ turning observability on) does not.
//
// The ORM has no key — its switch is `//forge:no-orm` in internal/db — so
// `--disable orm` only steers this scaffold: it writes no db/migrations, and
// a project without them derives orm off.
func applyDisableFlags(gen *generator.ProjectGenerator, disable []string) error {
	for _, name := range disable {
		key := strings.TrimSpace(strings.ToLower(name))
		switch key {
		case "hot-reload", "hotreload":
			key = config.FeatureHotReload
		case "ingress", "operators":
			return cliutil.UserErr("forge project new --disable",
				fmt.Sprintf("feature %q cannot be --disable'd: it derives from what exists (a forge.Gateway in deploy/kcl, internal/operators/), and a new project has neither", name),
				"",
				"nothing to disable — add the gateway or operator later and the feature turns on")
		}
		if !slices.Contains(disableableFeatures, key) {
			return cliutil.UserErr("forge project new --disable",
				fmt.Sprintf("unknown feature %q; valid features: %s", name, strings.Join(disableableFeatures, ", ")),
				"",
				"pick a feature from the list above (comma-separated, repeatable); names are case-insensitive")
		}
		gen.Features = gen.Features.With(key, false)
		gen.Features, _ = gen.Features.Pin(key, false)
	}
	return nil
}

// disableableFeatures is every feature `project new --disable` accepts, in
// the order its help lists them. Each is a project shape forge owes a working
// scaffold — generate, build and lint green from the first commit — and
// TestE2EProjectNewDisableShapes scaffolds every one of them.
var disableableFeatures = []config.FeatureName{
	config.FeatureORM, config.FeatureCodegen, config.FeatureMigrations, config.FeatureCI,
	config.FeatureBuild, config.FeatureDeploy, config.FeatureContracts, config.FeatureFrontend,
	config.FeatureObservability, config.FeatureHotReload,
}

// rootModuleOwnsGen reports whether the module at dir already owns gen/: dir
// has a go.mod, gen/ holds Go files, and gen/ is not a module of its own (no
// gen/go.mod). An in-place scaffold keeps such a gen/ where it is (see
// generator.ProjectGenerator.GenInRootModule). A fresh scaffold's gen/ holds
// no Go files, so it still becomes forge's separate gen module.
func rootModuleOwnsGen(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "gen", "go.mod")); err == nil {
		return false
	}
	return dirHasGoFiles(filepath.Join(dir, "gen"))
}

// dirHasGoFiles reports whether any .go file lives at or below dir.
func dirHasGoFiles(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return filepath.SkipDir
		}
		if !d.IsDir() && filepath.Ext(p) == ".go" {
			found = true
		}
		return nil
	})
	return found
}
