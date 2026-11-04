package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

var (
	version   string
	buildDate string // Set via ldflags at build time
	gitCommit string // Set via ldflags at build time
)

// Execute is the entrypoint used by main() to dispatch the assembled
// root cobra command. Wraps NewRootCmd().Execute().
//
// pgtest.Shutdown releases this process's share of the cross-process embedded
// postgres pool (a no-op unless a command — `forge generate` and the scaffold
// verbs are the ones that reach it — actually booted or reused one). Deferred
// HERE, before main()'s os.Exit, so a failed
// command still releases its share and the last process out tears the shared
// server down cleanly instead of leaking its data dir and SysV IPC.
//
// Before the tree runs, a project that pins another forge gets its command
// handed to that forge (HandOffToPinned, forge_pin_delegate.go).
func Execute() error {
	defer pgtest.Shutdown()
	if handed, err := HandOffToPinned(context.Background(), os.Args[1:], os.Stderr); err != nil || handed {
		return err
	}
	// The descriptor is never committed: whichever command first reads it
	// in a checkout extracts it from proto/ (codegen.LoadDescriptor).
	codegen.RegisterDescriptorBuilder(buildDescriptorOnDemand)
	return NewRootCmd().Execute()
}

// projectRoot forwards to cmdutil.ProjectRoot — the shared project-root
// resolver. The `scaffold` group lives in internal/cli/scaffold and calls cmdutil
// directly; this unexported forwarder keeps the remaining internal/cli call
// sites (ci.go, delete.go, disown.go, package.go) unchanged.
func projectRoot() (string, error) { return cmdutil.ProjectRoot() }

// validateProjectName / validateServiceName / validateFrontendName forward
// to the shared validators in cmdutil (now the canonical home, shared with
// the internal/cli/scaffold group). new.go calls these.
func validateProjectName(name string) error  { return cmdutil.ValidateProjectName(name) }
func validateServiceName(name string) error  { return cmdutil.ValidateServiceName(name) }
func validateFrontendName(name string) error { return cmdutil.ValidateFrontendName(name) }

// Name returns the command name users should type to invoke Forge.
// When the binary is "forge" (standalone install), it returns "forge".
// When embedded in another binary (e.g. "reliant"), it returns "reliant forge".
// Forwards to cmdutil.Name so the dir-nested command groups share one
// implementation without importing internal/cli.
func Name() string {
	return cmdutil.Name()
}

// forgeExecCommand returns exec-ready command tokens using the resolved
// executable path. Use this when spawning forge as a subprocess (the
// protoc-gen-forge buf plugin, `forge ci` re-entry).
//
// The subcommand route comes from cmdutil.CmdRouteTokens when the CLI
// recorded it (the normal case — the root PersistentPreRun records how
// forge is mounted before any RunE fires): a standalone forge binary
// self-invokes as [exe] no matter what the file is named, an embedded
// `reliant forge` mount as [exe, "forge"]. The basename heuristic
// remains only as a fallback for callers outside cobra execution —
// guessing from the basename was the trap that made a temp-built
// standalone binary self-invoke `<exe> forge …`, hit cobra's "unknown
// command", and break descriptor generation.
func forgeExecCommand() ([]string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable path: %w", err)
	}
	if tokens, ok := cmdutil.CmdRouteTokens(); ok {
		return append([]string{exePath}, tokens...), nil
	}
	if filepath.Base(exePath) == "forge" {
		return []string{exePath}, nil
	}
	return []string{exePath, "forge"}, nil
}

// SetVersion stamps the version/date/commit metadata used by the
// `version` subcommand and the rendered Cobra Version string. Called
// once from main() with ldflags-injected values.
func SetVersion(v, date, commit string) {
	version = v
	buildDate = date
	gitCommit = commit
}

// NewRootCmd builds and returns the fully assembled root command.
func NewRootCmd() *cobra.Command {
	var projectDir string

	var rootCmd *cobra.Command
	rootCmd = &cobra.Command{
		Use:   "forge",
		Short: "Build, run and ship static sites, SPAs, web/mobile apps and APIs, LLM-first",
		Long: `Reliant Forge (github.com/reliant-labs/forge) — not Atlassian Forge, Foundry's
forge (Solidity) or Laravel Forge.

Forge takes a project from an empty directory to production, purpose-built
for LLM-driven development: environments, build-once releases, hosting,
observability and linters from day 0.

A landing page, docs site, SPA or PWA with no backend is a complete forge
project: one static frontend on hosted static hosting, no server to run.
Adopt an existing Vite or Next.js app with
'forge project new --in-place --kind site' ('forge skill load
deploy/static-site').

An app with an API adds Go services defined in proto and wired over Connect
RPC — generated handlers, typed frontend hooks, mocks and middleware, one
consistent interface pattern through the stack. 'forge start' walks that path
from an empty directory to authored protos, in one call.`,
		Version: versionLine(),
		// SilenceErrors: cobra never prints the error itself — main()
		// owns the single, final "Error: ..." line. Without this every
		// failure printed twice (cobra's copy first, buried under the
		// usage block, then main's copy) — multi-line failure reports
		// (e.g. the Tier-1 stomp-guard report, journey fr-a04f8c0609)
		// appeared twice with usage spam sandwiched between the copies.
		// SilenceUsage is NOT set here: it is set in PersistentPreRun
		// (after flag/arg parsing succeeds) so runtime errors skip the
		// usage dump while genuine usage mistakes keep the help block.
		SilenceErrors: true,
		// PersistentPreRun fires once per invocation regardless of
		// which subcommand the user typed.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// A prior embedded command's launch privileges must never leak into
			// a subsequent diagnostic command in this process.
			kclContexts.Lock()
			clear(kclContexts.byTarget)
			kclContexts.Unlock()
			cmd.SetContext(cluster.WithKCLRunner(cmd.Context(), evalKCLSubprocess))
			// Install (or clear) the project resolution root before
			// anything below can resolve a project. Clearing on the
			// empty value matters for in-process embedders, which
			// call NewRootCmd repeatedly in one process: without it
			// a -C from an earlier invocation would leak into the
			// next one and silently target the wrong project.
			if err := cmdutil.SetProjectDir(projectDir); err != nil {
				return err
			}

			// Usage-dump suppression for RUNTIME errors only. This
			// hook runs after flag parsing and arg validation succeed,
			// so genuine usage mistakes (unknown flag, wrong arg
			// count) still print the usage block — but a pipeline-step
			// failure inside RunE (generate, add, build, …) no longer
			// buries the real error under 40 lines of flag help.
			cmd.SilenceUsage = true

			// Record how forge is mounted in the executing command
			// tree (standalone root vs a `reliant forge`-style
			// subcommand) so self-invocation (forgeExecCommand) and
			// next-step hints (cmdutil.Name) route correctly no matter
			// what the binary file is named.
			cmdutil.RecordCmdRoute(rootCmd)

			// Self-heal the clone's git setup — hook activation and the
			// generated-file merge driver (idempotent, best-effort). Only
			// commands that write generated or state files do it
			// (cmdutil.ConfiguresClone): a read-only or slot command must
			// never touch .git/config, which every linked worktree shares.
			// skipHookActivationAnnotation opts a writing command back out.
			if cmdutil.ConfiguresCloneFor(cmd) && !hookActivationSkipped(cmd) {
				if root, err := cmdutil.FindProjectRoot(); err == nil && root != "" {
					ensureGitHooksActivated(root)
					ensureGeneratedMergeDriver(root)
				}
			}

			// A project bridged to a local forge checkout compiles the
			// code generate writes against that checkout: say so, in one
			// line, when this binary was not built from it. Placed before
			// the experimental early-returns — it is not an experimental
			// nudge and must not be silenced as one.
			if bridgeSkewChecked(cmd, rootCmd) {
				if root, err := cmdutil.FindProjectRoot(); err == nil && root != "" {
					warnBridgeSkew(cmd.ErrOrStderr(), root)
				}
			}

			// Is this the forge the project pins? Warned for every
			// project command; refused for the ones that build, release
			// or deploy. See forge_pin_skew.go.
			allow, _ := cmd.Flags().GetBool(allowVersionSkewFlag)
			return checkForgePinSkew(cmd, rootCmd, cmd.ErrOrStderr(), allow)
		},
	}

	// There is no global --verbose. Verbosity is per-command because there
	// is nothing global to turn up: `forge generate --verbose` lists skipped
	// steps, `forge doctor -v` shows evidence for passing checks, and those
	// are different outputs, not two dials on one substrate. A root
	// PersistentFlag bound to a local nobody read used to accept `-v` on
	// EVERY command and do nothing with it — and, worse, shadowed
	// `forge generate -v`, so the one place a user would most expect it
	// silently produced ordinary output. Commands that want it register it.

	// --project-dir / -C is global because project resolution is global:
	// every command locates forge.yaml from one directory, and before this
	// flag the only way to choose that directory was the process CWD. That
	// is unusable for the in-process embedding this package exists to
	// support (see NewRootCmd's doc and cmdutil.SetProjectDir) — os.Chdir is
	// process-global and unsafe under concurrency. Short form -C matches
	// make and git.
	rootCmd.PersistentFlags().StringVarP(&projectDir, "project-dir", "C", "", "resolve the project from this directory instead of the current one")

	// Add all commands
	// `forge start` prints the greenfield brief. It is a top-level verb
	// rather than a flag on `skill load` because it must be reachable from
	// the ONE surface an agent that has never seen forge already reads —
	// the `forge --help` command list.
	rootCmd.AddCommand(newStartCmd())
	// The git merge driver forge configures for its generated files
	// (merge_generated.go). Hidden: git runs it, people do not.
	rootCmd.AddCommand(newMergeGeneratedCmd())
	rootCmd.AddCommand(newMergeDriverCmd())
	// `forge env up` was REMOVED. It was a thin alias over the same runUp that
	// `forge env up <env>` calls, and its one distinct feature — forwarding
	// tokens after `--` to the frontend dev servers — moved onto `env up`
	// itself: `forge env up dev -- --host 0.0.0.0`. Two spellings of one
	// lifecycle drifted (the alias took the env as an `--env` FLAG with a
	// `dev` default, where `env up` takes it as a required positional), so
	// "which env is running?" had two answers. Local is now one verb.
	rootCmd.AddCommand(newGenerateCmd())
	// (`forge unfork`, the legacy-fork migration tool, was removed after
	// its one-release deprecation window — the legacy-manifest migration
	// in `forge generate` converts forked entries to disowned
	// automatically.)
	rootCmd.AddCommand(newDBCmd())
	rootCmd.AddCommand(newBuildCmd())
	rootCmd.AddCommand(newRegistryCmd())
	rootCmd.AddCommand(newStorageCmd())
	// `forge slot`: the host-wide heavy-job semaphore (pkg/hostslot).
	rootCmd.AddCommand(newSlotCmd())
	// `forge test` was REMOVED. The suite a project runs is defined in its own
	// Taskfile.yml (`task test`, `task test:integration`, `task test:e2e`), and
	// that is what the generated CI workflow and the reliant one-shot gate now
	// invoke. Forge owning a second spelling of `go test` + `npm test` bought
	// nothing and cost correctness: the two definitions drifted, so `forge test`
	// could report green over a suite that was not the project's suite (and it
	// ran the whole Go suite twice — untagged, then under -tags=integration,
	// which ADDS files rather than filtering them). Contrast `forge lint`, which
	// stays because it orchestrates analyzers that exist only inside forge.
	//
	// newTestRemovedCmd below is a signpost, not a command: it only errors.
	rootCmd.AddCommand(newTestRemovedCmd())
	// `lint` migrated to the internal/cli/lint group (factory registry).
	rootCmd.AddCommand(newPackageCmd())
	// `debug` migrated to the internal/cli/debug group (factory registry).
	rootCmd.AddCommand(newSecretCmd())
	rootCmd.AddCommand(newLoginCmd())
	rootCmd.AddCommand(newLogoutCmd())
	rootCmd.AddCommand(newCloudCmd())
	rootCmd.AddCommand(newDomainCmd())
	rootCmd.AddCommand(newDoctorCmd())
	// `report-bug` files a defect in forge itself with the Reliant team. It
	// is the only way forge ever sends a report (internal/bugreport).
	rootCmd.AddCommand(newReportBugCmd())
	rootCmd.AddCommand(newVersionCmd())
	rootCmd.AddCommand(newProtocGenForgeCmd())
	// `component` migrated to the internal/cli/component group (registered
	// via the factory registry below).
	rootCmd.AddCommand(newSkillCmd())
	// `mcp` serves the introspection, skill, lint and generate verbs to an
	// agent harness over stdio (mcp.go).
	rootCmd.AddCommand(newMCPCmd())
	rootCmd.AddCommand(newCICmd())
	rootCmd.AddCommand(newToolsCmd())
	rootCmd.AddCommand(newClusterCmd())
	rootCmd.AddCommand(newAPICmd())
	// `kcl` is the KCL-file noun: evaluating ONE file of the project and
	// selecting out of it. An ENVIRONMENT's render is `env render <env>`,
	// which is env-shaped (image tags, digests, cluster routing, charts);
	// none of that applies to a library file declaring constants, which is
	// what scripts and tests read. See internal/cli/kcl_eval.go.
	rootCmd.AddCommand(newKCLCmd())
	// `env` is the environment noun: every env-REQUIRED lifecycle verb
	// (up/down/status/list/new/deploy/promote/smoke/secrets/devstack)
	// lives under it with the env as a positional argument. Commands where
	// env is an optional modifier (e.g. `forge build [env]`) stay at root.
	rootCmd.AddCommand(newEnvCmd())
	// `obs` reads back what the local observability stack collected.
	rootCmd.AddCommand(newObsCmd())
	// `release` is the release-ledger noun. Cutting a ledger stays on
	// `forge env build --release` and advancing one stays on `forge env
	// promote` — both act on the thing that owns them. What lives here is
	// the verb that acts on a LEDGER itself, with no environment involved:
	// `forge release verify` proves every artifact the ledger names really
	// exists in its public registry and matches the recorded bytes.
	rootCmd.AddCommand(newReleaseCmd())
	// `ledger` is the whole-ledger noun: the verbs that move or inspect a
	// ledger rather than one env's binding. `ledger import` is the only
	// way forward from the retired in-checkout ledger, and the only thing
	// that clears the refusal in ledger_unimported.go.
	rootCmd.AddCommand(newLedgerCmd())
	rootCmd.AddCommand(newGateCmd())
	// `frontend` operates on built frontends; today its one noun is the source
	// maps of a build forge did not run (`forge frontend sourcemaps push`).
	rootCmd.AddCommand(newFrontendGroupCmd())
	// `project` is the project-structure noun: the commands that create,
	// retire, or INSPECT a project as a whole (new/delete/disown/migrate/
	// upgrade/map/graph/introspect/features/annotations). The flat members
	// attach in newProjectCmd; `audit` attaches below via the factory
	// registry. Scaffolding is NOT here — it is the top-level `forge
	// scaffold` verb.
	projectCmd := newProjectCmd()
	rootCmd.AddCommand(projectCmd)

	// Dir-nested command groups (internal/cli/<group>) self-register a
	// command factory via init() — they are blank-imported in groups.go so
	// the registration runs. Range the registry and attach each one under
	// its home: `audit` is a project-inspection view, so it nests under
	// `forge project`; everything else (including `scaffold`) is a
	// top-level verb. The factory carries the shared I/O surface; group
	// commands still call package-level helpers in internal/cli directly.
	f := factory.New()
	for _, makeCmd := range factory.Registered() {
		c := makeCmd(f)
		switch c.Name() {
		case "audit":
			projectCmd.AddCommand(c)
		default:
			rootCmd.AddCommand(c)
		}
	}

	registerVersionSkewFlags(rootCmd)
	return rootCmd
}

// newVersionCmd creates the version subcommand so both `forge version` and
// `forge --version` work. Cobra's built-in --version flag handles the latter;
// this covers users who type the subcommand form.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the forge version and build identity",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "%s version %s\n", Name(), versionLine())
			fmt.Fprint(cmd.OutOrStdout(), buildinfo.Describe())
		},
	}
}

// versionLine renders this binary's forge identity. It reads from
// buildinfo, which derives identity from the binary's own
// runtime/debug build info when no ldflags stamp is present.
//
// This is what makes `forge` and `reliant forge` distinguishable. The
// embedded path (reliant mounting forgecli.NewRootCmd()) never calls
// SetVersion, so the old fmt.Sprintf over the raw vars rendered
// "forge version  (built , commit )" — an identity string with no
// identity in it, and the reason a toolchain mismatch was invisible.
func versionLine() string {
	buildinfo.SetBuildDate(buildDate)
	return buildinfo.Identity()
}
