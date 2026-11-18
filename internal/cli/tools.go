package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cli/lint"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/toolpin"
)

// frontendTSPluginPackage is the npm package providing the local TS codegen
// binary that buf invokes via `local: ./<frontend>/node_modules/.bin/protoc-gen-es`.
const frontendTSPluginPackage = "@bufbuild/protoc-gen-es"

// frontendTSPluginRange is the devDependency range every scaffolded
// frontend's package.json declares for frontendTSPluginPackage — the major
// kept in step with @bufbuild/protobuf. It is what the undeclared-plugin
// runbook tells a user to add, so a test pins it to the templates.
const frontendTSPluginRange = "^2.5.0"

// requiredProtoTools lists the proto codegen plugins forge expects on PATH
// when buf.gen.yaml uses `local:` plugins (the default since the BSR-auth
// fix). Both packages produce binaries whose names match their `cmd/` dir.
//
// Keep this list aligned with:
//   - internal/templates/project/buf.gen.yaml (template default)
//   - internal/cli/generate_buf.go writeDefaultBufGenYaml (runtime fallback)
//   - internal/templates/project/Taskfile.yml.tmpl (preflight checks)
//   - scripts/bootstrap.sh (devcontainer bootstrap)
//   - internal/cli/generate_toolpins.go (generate pins each tool to the
//     version resolveToolVersion reports, installing into a forge cache)
var requiredProtoTools = []protoTool{
	{
		Binary:        "protoc-gen-go",
		Module:        "google.golang.org/protobuf/cmd/protoc-gen-go",
		VersionModule: "google.golang.org/protobuf",
	},
	{
		Binary:        "protoc-gen-connect-go",
		Module:        "connectrpc.com/connect/cmd/protoc-gen-connect-go",
		VersionModule: "connectrpc.com/connect",
	},
	// goimports formats every Go file forge generates. It is not a buf
	// plugin, but it shapes committed output the same way: `forge generate`
	// without it skips the pass, so a regenerate on a machine that lacks it
	// (a CI runner) produces different bytes than one that has it.
	{
		Binary:        "goimports",
		Module:        "golang.org/x/tools/cmd/goimports",
		VersionModule: "golang.org/x/tools",
	},
}

type protoTool struct {
	Binary string
	Module string
	// VersionModule is the module whose version go.mod resolves for this
	// tool. protoc-gen-go's output names its own version in every file it
	// writes, so installing the version the project's runtime library is
	// pinned at is what makes a regenerate reproduce the committed bytes.
	VersionModule string
}

// resolveToolVersion picks the version to `go install` for a tool: an
// explicit --version wins; otherwise the version projectDir's go.mod
// resolves for the tool's module (the one the committed code was generated
// against), and `latest` only when the module graph does not contain it.
func resolveToolVersion(ctx context.Context, projectDir string, t protoTool, override string) string {
	if override != "" {
		return override
	}
	cmd := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Version}}", t.VersionModule)
	cmd.Dir = projectDir
	// The module graph CI sees: a developer's go.work may bridge a local
	// checkout, which is not what the committed stubs were built against.
	// Under a caller's -mod=mod this `go list -m` would also write go.sum.
	cmd.Env = goexec.Env("GOWORK=off")
	out, err := cmd.Output()
	if v := strings.TrimSpace(string(out)); err == nil && v != "" {
		return v
	}
	return "latest"
}

func newToolsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tools",
		Short: "Manage developer tooling forge depends on (proto plugins, linters, etc.)",
		Long: `Manage developer tooling that forge expects on PATH but does not ship.

Subcommands:
  install   Install the codegen tools forge runs (protoc-gen-go,
            protoc-gen-connect-go, goimports) via 'go install', at the
            versions this project's go.mod resolves, and check that every
            frontend declares its TypeScript plugin. Name a tool to install
            just it — including the lint tools 'forge lint' gates with
            (golangci-lint, buf), at forge's pins.
  pins      Print the tool versions this forge pins, for CI to read.

Forge scaffolds buf.gen.yaml with 'local:' plugins by default so that
'forge generate' works without any BSR (buf.build) authentication.
Those local plugins must be on PATH; this command installs them.`,
	}

	cmd.AddCommand(newToolsInstallCmd())
	cmd.AddCommand(newToolsPinsCmd())

	return cmdutil.StrictGroup(cmd)
}

func newToolsInstallCmd() *cobra.Command {
	var (
		version string
		force   bool
	)

	cmd := &cobra.Command{
		Use:   "install [tool...]",
		Short: "Install codegen tools (protoc-gen-go, protoc-gen-connect-go, goimports), or the named tools (golangci-lint, buf)",
		Long: `Install the codegen tools forge needs on PATH for the default
local:-plugin buf.gen.yaml workflow, plus goimports, which formats every Go
file forge generates. Name tools to install just those, including the lint
tools 'forge lint' gates with: golangci-lint and buf.

Each codegen tool is installed at the version this project's go.mod resolves
for it (google.golang.org/protobuf, connectrpc.com/connect,
golang.org/x/tools) — the version the committed generated code was produced
with — falling back to @latest when go.mod does not contain the module.
--version overrides that for every codegen tool.

The lint tools install at forge's pins ('forge tools pins'), never at
--version. golangci-lint is built with the Go toolchain this module compiles
with: one built with an older Go cannot analyze it. 'forge lint' builds that
same golangci-lint into its own cache when PATH has none, so installing it
only saves that one-time build — a CI job needs no install step for it.

By default, tools already present on PATH are skipped — golangci-lint only
when it is forge's pin built with a new-enough Go. Use --force to re-install
them.

The TypeScript plugin, @bufbuild/protoc-gen-es, is not installed here. It
is an ordinary devDependency of each frontend, so the frontend's own
'npm ci' installs it at the version its lockfile pins. With no tool named,
this command checks that every frontend whose buf.gen.yaml runs it declares
it, and fails with the edit to make when one does not. It never runs npm and
never writes a frontend's package.json or package-lock.json — with or
without --force.

Examples:
  forge tools install
  forge tools install --version v1.34.2
  forge tools install --force
  forge tools install golangci-lint
  forge tools install golangci-lint buf`,
		RunE: func(cmd *cobra.Command, args []string) error {
			goErr := runToolsInstall(cmd.Context(), version, force, args...)
			if len(args) > 0 {
				return goErr
			}
			return errors.Join(goErr, checkFrontendTSPlugins())
		},
	}

	cmd.Flags().StringVar(&version, "version", "", "Version passed to 'go install' for every codegen tool (e.g. latest, v1.34.2); default: the version go.mod resolves, else latest. The lint tools always install at forge's pins")
	cmd.Flags().BoolVar(&force, "force", false, "Reinstall the Go tools even when already on PATH (never touches a frontend's npm dependencies)")

	return cmd
}

// checkFrontendTSPlugins checks the TypeScript plugin of every frontend in
// the enclosing forge project. Outside a project there is nothing to check.
func checkFrontendTSPlugins() error {
	configPath, err := findProjectConfigFile()
	if errors.Is(err, ErrProjectConfigNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	store, err := loadProjectStoreFrom(configPath)
	if err != nil {
		return err
	}
	return checkFrontendTSPluginsIn(filepath.Dir(configPath), store.Config().Frontends)
}

// checkFrontendTSPluginsIn verifies that each frontend whose buf.gen.yaml
// runs the local protoc-gen-es DECLARES it, and installs nothing.
//
// It used to run `npm install --save-dev @bufbuild/protoc-gen-es` — in every
// frontend under --force, which the scaffolded verify-generated job passes.
// npm re-resolves the whole tree when it saves, so on a Linux runner it
// rewrote a lockfile a macOS npm had written (dropping its "libc" entries),
// and verify-generated reported the user's package-lock.json as generated
// code drift. Committing either OS's lockfile only moved the failure to the
// other OS. A frontend's manifest and lockfile are the user's: a declared
// plugin is installed by the frontend's own `npm ci`, and an undeclared one
// is an edit for the user to make and commit, not one forge makes for them.
func checkFrontendTSPluginsIn(root string, frontends []config.FrontendConfig) error {
	var undeclared []string
	for _, feDir := range localTSPluginFrontendDirs(root, frontends) {
		manifest := feDir + "/package.json"
		declared, err := declaresTSPlugin(filepath.Join(root, manifest))
		if err != nil {
			return fmt.Errorf("read %s: %w", manifest, err)
		}
		if !declared {
			undeclared = append(undeclared, manifest)
			continue
		}
		// Both npm layouts count: under forge's dev workspace bridge npm
		// hoists the plugin to <project>/node_modules and creates no
		// frontend-local node_modules at all. resolveLocalTSPluginRel is
		// the resolver the buf pass uses, so the two cannot disagree.
		if _, ok := resolveLocalTSPluginRel(root, feDir); ok {
			fmt.Printf("✅ %-26s declared and installed for %s/\n", "protoc-gen-es", feDir)
		} else {
			fmt.Printf("ℹ️  %-26s declared in %s, not installed yet — `npm ci` in %s/ installs it\n", "protoc-gen-es", manifest, feDir)
		}
	}
	if len(undeclared) == 0 {
		return nil
	}
	verb := "does not declare"
	if len(undeclared) > 1 {
		verb = "do not declare"
	}
	return cliutil.UserErr("forge tools install",
		fmt.Sprintf("%s %s %s, the plugin its buf.gen.yaml runs to generate TypeScript stubs "+
			"(forge never edits a frontend's package.json or lockfile, so it will not add it)",
			strings.Join(undeclared, ", "), verb, frontendTSPluginPackage),
		"",
		fmt.Sprintf("add %q: %q to devDependencies in %s, run `npm install` beside it, "+
			"and commit package.json together with package-lock.json",
			frontendTSPluginPackage, frontendTSPluginRange, strings.Join(undeclared, " and ")))
}

// declaresTSPlugin reports whether the package.json at path lists the
// protoc-gen-es package as a dependency of either kind — both are installed
// by `npm ci`. A missing package.json declares nothing.
func declaresTSPlugin(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var manifest struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return false, err
	}
	_, dev := manifest.DevDependencies[frontendTSPluginPackage]
	_, prod := manifest.Dependencies[frontendTSPluginPackage]
	return dev || prod, nil
}

// lintTools are the tools `forge tools install <name>` installs at forge's
// pins on request: the binaries `forge lint`'s gating lanes run. They are not
// in the no-argument default, which installs only what `forge generate` needs
// (and runs in `forge project new` and every scaffolded verify-generated job);
// `forge lint` builds its own pinned golangci-lint when PATH has none, so
// installing it is an optimisation, never a prerequisite.
var lintTools = []string{"buf", "golangci-lint"}

// runToolsInstall installs the named tools — the required codegen tools when
// none are named. Returns the first install error (if any) but always tries
// every tool so users see the full picture.
func runToolsInstall(ctx context.Context, version string, force bool, names ...string) error {
	if _, err := exec.LookPath("go"); err != nil {
		return fmt.Errorf("'go' not found on PATH — install Go before running '%s tools install'", Name())
	}
	known := map[string]func() error{
		"buf": func() error {
			// The buf this project pins (go.mod's, else forge's): the one
			// `forge generate` and `forge ci gate` run.
			root, _ := projectRoot()
			return goInstallTool(ctx, "buf", bufModulePath+"@"+toolpin.BufFor(root).Version, force)
		},
		"golangci-lint": func() error { return installPinnedGolangciLint(ctx, force) },
	}
	var order []string
	for _, t := range requiredProtoTools {
		known[t.Binary] = func() error {
			return goInstallTool(ctx, t.Binary, t.Module+"@"+resolveToolVersion(ctx, ".", t, version), force)
		}
		order = append(order, t.Binary)
	}
	if len(names) == 0 {
		names = order
	}
	for _, n := range names {
		if known[n] == nil {
			return cliutil.UserErr("forge tools install", fmt.Sprintf("no installable tool named %q", n), "",
				"one of: "+strings.Join(append(order, lintTools...), ", "))
		}
	}

	var firstErr error
	for _, n := range names {
		if err := known[n](); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return firstErr
	}
	fmt.Println()
	fmt.Println("✅ All requested tools installed.")
	return nil
}

// bufModulePath is the package `go install` builds buf from.
const bufModulePath = "github.com/bufbuild/buf/cmd/buf"

// goInstallTool installs spec with `go install` unless binary is already on
// PATH (and force is unset), and checks that it landed on PATH.
func goInstallTool(ctx context.Context, binary, spec string, force bool) error {
	if !force {
		if _, err := exec.LookPath(binary); err == nil {
			fmt.Printf("✅ %-26s already installed (use --force to reinstall)\n", binary)
			return nil
		}
	}
	fmt.Printf("📦 Installing %-26s (go install %s)\n", binary, spec)
	gobin, err := goBinDir(ctx)
	if err != nil {
		return fmt.Errorf("install %s: %w", binary, err)
	}
	if err := installIntoGOBIN(ctx, binary, spec, gobin); err != nil {
		fmt.Fprintf(os.Stderr, "  ❌ go install %s failed: %v\n", spec, err)
		return fmt.Errorf("install %s: %w", binary, err)
	}
	// Verify it landed on PATH (catches GOBIN/GOPATH-not-on-PATH).
	if _, err := exec.LookPath(binary); err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠️  Installed %s but it is not on PATH. %s\n", binary, goBinHint)
		return fmt.Errorf("%s installed but not on PATH", binary)
	}
	fmt.Printf("  ✅ %s installed\n", binary)
	return nil
}

const goBinHint = "Add $(go env GOBIN) (or $(go env GOPATH)/bin if GOBIN is unset) to PATH."

// installIntoGOBIN installs spec's binary into gobin atomically: `go install`
// writes into a temp dir inside gobin (one filesystem), and the finished
// binary is renamed over any old one. `go install` straight into GOBIN
// rewrites the file in place, so a process starting it — a parallel forge
// generate's buf or protoc-gen-go — could run a half-written binary. The
// same fix as the pinned-tool cache (installPinnedTool).
func installIntoGOBIN(ctx context.Context, binary, spec, gobin string) error {
	if err := os.MkdirAll(gobin, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(gobin, ".forge-install-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := goInstallPinned(ctx, spec, tmp); err != nil {
		return err
	}
	// atomicfile.Replace, not os.Rename: Windows refuses to replace a file
	// any process has open, and a fresh binary in GOBIN is exactly what a
	// virus scanner or a parallel forge opens.
	name := binary + exeSuffix()
	return atomicfile.Replace(filepath.Join(tmp, name), filepath.Join(gobin, name))
}

// installPinnedGolangciLint installs forge's pinned golangci-lint into GOBIN,
// built with the toolchain that compiles this module — the only build that
// can analyze it. Unlike the other tools, a golangci-lint already on PATH is
// kept only when it IS that build: another version reports other findings
// than CI's, and one built with an older Go cannot lint the module at all.
func installPinnedGolangciLint(ctx context.Context, force bool) error {
	const binary = "golangci-lint"
	pin := lint.GolangciLintPin()
	path, why := lint.GolangciLintOnPath(".")
	if why == "" && !force {
		fmt.Printf("✅ %-26s %s already installed at %s (use --force to reinstall)\n", binary, pin, path)
		return nil
	}
	gobin, err := goBinDir(ctx)
	if err != nil {
		return fmt.Errorf("install %s: %w", binary, err)
	}
	reason := ""
	if path != "" && why != "" {
		reason = " — " + path + " " + why
	}
	fmt.Printf("📦 Installing %-26s (go install %s@%s with this module's Go toolchain, into %s)%s\n",
		binary, lint.GolangciLintModule(), pin, gobin, reason)
	toolchain, err := lint.InstallGolangciLint(ctx, ".", gobin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ❌ building golangci-lint %s with %s failed: %v\n", pin, toolchain, err)
		return fmt.Errorf("install %s: %w", binary, err)
	}
	path, why = lint.GolangciLintOnPath(".")
	switch {
	case path == "":
		fmt.Fprintf(os.Stderr, "  ⚠️  Installed %s into %s but it is not on PATH. %s\n", binary, gobin, goBinHint)
		return fmt.Errorf("%s installed but not on PATH", binary)
	case why != "":
		// Another golangci-lint ahead of GOBIN on PATH. forge lint still
		// copes (it builds its own when that one cannot lint the module),
		// but it is not the pin.
		fmt.Fprintf(os.Stderr, "  ⚠️  Installed %s %s into %s, but PATH resolves %s first, which %s — put %s ahead of it on PATH.\n",
			binary, pin, gobin, path, why, gobin)
	default:
		fmt.Printf("  ✅ %s %s installed (built with %s)\n", binary, pin, toolchain)
	}
	return nil
}

// goBinDir is where `go install` puts binaries: GOBIN, else the first
// GOPATH entry's bin.
func goBinDir(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "env", "GOBIN", "GOPATH")
	cmd.Env = goexec.Env()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go env GOBIN GOPATH: %w", err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) != "" {
		return strings.TrimSpace(lines[0]), nil
	}
	if len(lines) > 1 {
		if first := filepath.SplitList(strings.TrimSpace(lines[1])); len(first) > 0 && first[0] != "" {
			return filepath.Join(first[0], "bin"), nil
		}
	}
	return "", errors.New("go env reports neither GOBIN nor GOPATH")
}
