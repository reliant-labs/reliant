//forge:exclude-contract: pure command-construction matrix + env-file parser shared by `forge env up`; its only methods are on the RunnerSpec value type, it runs nothing itself, and nothing substitutes it

// Package hostlaunch composes exec.Cmds for host-mode services and
// frontends, plus the small env-file helpers both call sites need.
//
// Two CLI surfaces target the same dispatch matrix:
//
//   - `forge run <svc>` — single-service host runner (foreground or
//     background; backed by a per-service PID file).
//   - `forge env up` host phase — N-service loop that hangs the cmds off
//     a process registry for cascade teardown.
//
// Both pick a runner (go-run / air / binary / delve), default the env
// file to `.env.<env>`, and layer the env-file values onto the child
// process. Before this package existed, the dispatch was duplicated
// across `internal/cli/run.go` (buildRunHostCmd / runHostService) and
// `internal/cli/up.go` (buildHostServiceCmd / upHostServices) — same
// runner table, two implementations.
//
// The package intentionally does NOT own the process lifecycle:
//
//   - foreground stream-prefix + signal handling lives in the single-
//     service `forge env up` path because it has different semantics
//     (one process, persistent PID file, `stop` subcommand);
//   - the N-process registry that `forge env up` uses for cascade
//     teardown stays in `internal/cli/up.go` for the same reason.
//
// What's shared here is the pure command-construction matrix plus the
// minimal env-file parser. Anything that tracks PIDs / streams output
// / handles signals stays at the call site.
package hostlaunch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/reliant-labs/forge/internal/envutil"
)

// Defaults pinned by the runner dispatch. Exported so tests and CLI
// help text can reference them without re-deriving the magic numbers.
const (
	// DefaultDelvePort is the dlv --listen=127.0.0.1:<port> default when KCL
	// doesn't pin one explicitly. Matches dlv's own conventional port.
	DefaultDelvePort = 2345

	// DefaultAirConfig is the `air -c <path>` default when KCL doesn't
	// pin one explicitly. Mirrors the `air` tool's own default —
	// `.air.toml` at the project root.
	DefaultAirConfig = ".air.toml"
)

// RunnerSpec is the dispatch input: a host workload's runtime block
// (forge.Host{runner, air_config, delve_port, working_dir}) plus the two
// runtime-independent facts the argv is DERIVED from — its Go build and
// its args. Env composition is layered by the caller via [LayerHostEnv]
// so this package stays vendor-neutral about how config gets sourced.
//
// WorkingDir + ProjectDir control the subprocess's working directory.
// When WorkingDir is empty, the subprocess inherits the parent's cwd
// (the project root, where forge was invoked). When WorkingDir is set:
//
//   - absolute paths are used as-is;
//   - relative paths resolve against ProjectDir (which the CLI sets to
//     the forge project root).
//
// The cross-repo Air case is the load-bearing example: a host workload
// declares `working_dir = "../sibling-repo"` so an Air config that lives
// in the sibling repo and references build paths relative to ITS own
// repo root resolves correctly even though forge itself runs from the
// caller's project root.
type RunnerSpec struct {
	Runner    string // "" (= go-run) | "go-run" | "air" | "binary" | "delve"
	AirConfig string // path relative to the working dir; default DefaultAirConfig
	DelvePort int    // dlv --listen=127.0.0.1:<port>; default DefaultDelvePort

	// GoPkg is the workload's GoBuild.cmd (e.g. "./cmd/acme"): the package
	// go-run runs and binary/delve were built from. Empty means the
	// workload has no Go build, and those runners have nothing to derive
	// an argv from.
	GoPkg string
	// OutputName is the GoBuild.output_name — the ./bin/<name> binary
	// and delve execute. Empty falls back to the workload name, which is
	// what `forge build` names a binary with no output_name.
	OutputName string

	// Command, when non-empty, is the workload's spec.command: an explicit
	// entrypoint run verbatim with Args, for any runner but air. It is how
	// a host workload runs something that is not this project's Go binary
	// — a sibling repo's `go run ./cmd/reliant`, a vendored tool. Relative
	// paths resolve against the effective cmd.Dir (WorkingDir), matching
	// shell semantics.
	Command []string
	// Args is the workload's spec.args — the same args its container gets
	// (the project binary's subcommand). Stated once for every runtime.
	Args []string

	// WorkingDir is the subprocess cwd override. Empty = inherit parent.
	// Relative paths resolve against ProjectDir; absolute paths are
	// used verbatim.
	WorkingDir string
	// ProjectDir is the forge project root used to resolve a relative
	// WorkingDir. Ignored when WorkingDir is empty or absolute. Empty
	// ProjectDir + relative WorkingDir falls through to the parent's
	// cwd interpretation (exec.Cmd default).
	ProjectDir string
}

// IgnoresArgs reports whether declared Args cannot reach the process: under
// air the air config's entrypoint / args_bin owns the argv. Callers warn on
// it, because an arg that is silently not passed looks exactly like an arg
// the program ignored.
func (s RunnerSpec) IgnoresArgs() bool {
	return strings.TrimSpace(s.Runner) == "air" && len(s.Args) > 0
}

// AirRunsArgs reports whether the air config this spec launches with runs
// the binary it builds with exactly the declared Args — its `[build]
// entrypoint` is `[<binary>, <Args>...]` — so the args are honoured after
// all, by the config rather than by forge. The scaffolded `.air.toml` runs
// `<bin> server`, which is what a dev env's API workload declares.
//
// false when the config cannot be read or parsed, names no entrypoint, or
// passes different args: the caller then says the declared args are not
// passed, because nothing proves they are.
func (s RunnerSpec) AirRunsArgs() bool {
	cfg, _ := s.airPaths()
	var doc struct {
		Build struct {
			Entrypoint []string `toml:"entrypoint"`
		} `toml:"build"`
	}
	if _, err := toml.DecodeFile(cfg, &doc); err != nil || len(doc.Build.Entrypoint) == 0 {
		return false
	}
	return slices.Equal(doc.Build.Entrypoint[1:], s.Args)
}

// airPaths resolves the air config this spec launches with, and the
// directory air runs in (the workload's working dir, else the project dir),
// which is what air resolves a relative `root` against.
func (s RunnerSpec) airPaths() (cfg, base string) {
	base = resolveWorkingDir(s.WorkingDir, s.ProjectDir)
	if base == "" {
		base = s.ProjectDir
	}
	cfg = s.AirConfig
	if cfg == "" {
		cfg = DefaultAirConfig
	}
	if !filepath.IsAbs(cfg) {
		cfg = filepath.Join(base, cfg)
	}
	return cfg, base
}

// AirDirs lists the directories the air config this spec launches with
// writes into: its `tmp_dir` (air's default `tmp`), and the directory of the
// binary it builds and runs — `[build] bin`, the first `entrypoint` element,
// and the `-o` output of a `go build` in `[build] cmd`. Each is resolved the
// way air resolves it, against the config's `root`.
//
// Only directories inside the directory air runs in are listed: a path
// outside it is somewhere forge has no business creating. An unreadable or
// unparsable config yields an error, and air reports the same config itself.
func (s RunnerSpec) AirDirs() ([]string, error) {
	cfgPath, base := s.airPaths()
	var doc struct {
		Root   string `toml:"root"`
		TmpDir string `toml:"tmp_dir"`
		Build  struct {
			Cmd        string   `toml:"cmd"`
			Bin        string   `toml:"bin"`
			Entrypoint []string `toml:"entrypoint"`
		} `toml:"build"`
	}
	if _, err := toml.DecodeFile(cfgPath, &doc); err != nil {
		return nil, err
	}
	root := base
	if r := strings.TrimSpace(doc.Root); r != "" && r != "." {
		root = joinUnder(base, r)
	}
	tmp := strings.TrimSpace(doc.TmpDir)
	if tmp == "" {
		tmp = "tmp"
	}
	candidates := []string{joinUnder(root, tmp)}
	for _, bin := range []string{doc.Build.Bin, firstOf(doc.Build.Entrypoint), goBuildOutput(doc.Build.Cmd)} {
		bin = strings.TrimSpace(bin)
		// A bare name (`entrypoint = ["app"]`) is resolved on PATH, not
		// written anywhere; only a path names a directory.
		if bin == "" || !strings.ContainsAny(bin, `/\`) {
			continue
		}
		candidates = append(candidates, filepath.Dir(joinUnder(root, bin)))
	}
	var dirs []string
	for _, d := range candidates {
		rel, err := filepath.Rel(base, d)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	return dirs, nil
}

// PrepareAirDirs creates every directory AirDirs lists. air before v1.63.7
// creates its tmp_dir with a non-recursive mkdir, so in a fresh checkout,
// where no tmp/ exists yet, a nested `tmp_dir = "tmp/api"` failed with
// `mkdir tmp/api: no such file or directory` and the workload never started.
// Creating the directories first makes every air release work in a fresh
// checkout, and is a no-op where they exist.
func (s RunnerSpec) PrepareAirDirs() error {
	dirs, err := s.AirDirs()
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// joinUnder is air's joinPath: an absolute path stands, a relative one is
// joined to root.
func joinUnder(root, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(root, p)
}

func firstOf(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// goBuildOutput returns the -o output of a `go build` command line, or ""
// when the command is not a go build or names no output. Only go build is
// parsed: `-o` means something else to every other tool.
func goBuildOutput(cmd string) string {
	f := strings.Fields(cmd)
	i := slices.Index(f, "go")
	if i < 0 || i+1 >= len(f) || f[i+1] != "build" {
		return ""
	}
	for j := i + 2; j < len(f); j++ {
		if f[j] == "&&" || f[j] == ";" || f[j] == "||" {
			break
		}
		if f[j] == "-o" && j+1 < len(f) {
			return f[j+1]
		}
		if v, ok := strings.CutPrefix(f[j], "-o="); ok {
			return v
		}
	}
	return ""
}

// BuildCmd composes the *exec.Cmd for a host workload. The argv is DERIVED,
// never authored per runtime (ADR 0002 §2):
//
//	explicit Command (any runner but air)  Command... Args...
//	go-run / ""                            go run <GoPkg> Args...
//	binary                                 ./bin/<OutputName|name> Args...
//	delve                                  dlv exec --headless --listen=127.0.0.1:<port> --api-version=2
//	                                         --accept-multiclient --continue ./bin/<out> [-- Args...]
//	air                                    air -c <AirConfig|.air.toml>   (Args: see IgnoresArgs)
//
// go-run, binary and delve with no Go build and no Command are an ERROR,
// as is an unknown runner. There is no fallback: the historical default
// (`go run ./cmd server <name>`) launched a command no declaration said,
// and every "falls through to go-run" hid a typo behind a process that
// started and did the wrong thing.
func BuildCmd(ctx context.Context, name string, spec RunnerSpec) (*exec.Cmd, error) {
	return buildCmdFor(ctx, runtime.GOOS, name, spec)
}

// AirEnvDefaults are the environment defaults forge applies to a workload run
// under air, at the lowest precedence (see LayerHostEnvConflicts). The
// scaffolded .air.toml cannot carry them: air runs its commands through
// PowerShell on Windows, where a POSIX `VAR=val cmd` prefix does not parse,
// and air hands its own environment to the server it launches.
var AirEnvDefaults = map[string]string{"ENVIRONMENT": "development"}

// ExeName is the on-disk name of a Go binary built for goos: Windows
// executables must end in .exe (CreateProcess, dlv and air all key off it),
// every other OS uses the bare name. It is the single spelling `forge build`
// writes and BuildCmd runs, so the two cannot drift.
func ExeName(goos, name string) string {
	if goos == "windows" && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		return name + ".exe"
	}
	return name
}

func buildCmdFor(ctx context.Context, goos, name string, spec RunnerSpec) (*exec.Cmd, error) {
	runner := strings.TrimSpace(spec.Runner)
	if !IsKnownRunner(runner) {
		return nil, fmt.Errorf("host workload %s: unknown host runner %q (want go-run, air, binary or delve)", name, spec.Runner)
	}
	var argv []string
	switch {
	case runner == "air":
		cfg := spec.AirConfig
		if cfg == "" {
			cfg = DefaultAirConfig
		}
		argv = []string{"air", "-c", cfg}
	case len(spec.Command) > 0:
		argv = append(append([]string{}, spec.Command...), spec.Args...)
	case strings.TrimSpace(spec.GoPkg) == "":
		return nil, fmt.Errorf("host workload %s: runner %q derives its command from a Go build, and the workload declares no Go build and no command.\n"+
			"  fix: give it `build = forge.GoBuild {cmd = \"./cmd/<binary>\"}`, or a `command` to run verbatim", name, runnerOrDefault(runner))
	default:
		bin := "./bin/" + ExeName(goos, name)
		if spec.OutputName != "" {
			bin = "./bin/" + ExeName(goos, spec.OutputName)
		}
		switch runner {
		case "binary":
			argv = append([]string{bin}, spec.Args...)
		case "delve":
			port := spec.DelvePort
			if port <= 0 {
				port = DefaultDelvePort
			}
			// Loopback only: dlv's API runs arbitrary code in the target,
			// and --listen=:<port> would serve it to the whole network.
			// `forge debug connect 127.0.0.1:<port>` drives it.
			argv = []string{"dlv", "exec", "--headless", fmt.Sprintf("--listen=127.0.0.1:%d", port),
				"--api-version=2", "--accept-multiclient", "--continue", bin}
			if len(spec.Args) > 0 {
				argv = append(append(argv, "--"), spec.Args...)
			}
		default: // "" | "go-run"
			argv = append([]string{"go", "run", strings.TrimSpace(spec.GoPkg)}, spec.Args...)
		}
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- argv is derived from the project's own KCL
	if dir := resolveWorkingDir(spec.WorkingDir, spec.ProjectDir); dir != "" {
		cmd.Dir = dir
	}
	return cmd, nil
}

func runnerOrDefault(r string) string {
	if r == "" {
		return "go-run"
	}
	return r
}

// resolveWorkingDir returns the effective cmd.Dir for the given spec.
// Empty workingDir = empty result (inherit parent cwd). Absolute
// workingDir = workingDir verbatim. Relative workingDir + non-empty
// projectDir = filepath.Join(projectDir, workingDir) so cross-repo
// configs (e.g. WorkingDir="../sibling") resolve against the forge
// project root rather than wherever the parent shell happens to live.
// Relative workingDir + empty projectDir falls through to workingDir
// verbatim (the exec.Cmd default — interpret against caller cwd).
func resolveWorkingDir(workingDir, projectDir string) string {
	if workingDir == "" {
		return ""
	}
	if filepath.IsAbs(workingDir) || projectDir == "" {
		return workingDir
	}
	return filepath.Join(projectDir, workingDir)
}

// IsKnownRunner reports whether the runner name is one of the
// supported dispatch keys ("" means go-run).
func IsKnownRunner(runner string) bool {
	switch strings.TrimSpace(runner) {
	case "", "go-run", "air", "binary", "delve":
		return true
	}
	return false
}

// LoadSecretsFile reads a gitignored secrets dotenv into a map. Returns
// (nil, nil) when path is empty so the caller can unconditionally call
// this. Missing-file is logged via the returned warn-only error wrapping
// os.ErrNotExist; permission / parse errors propagate.
//
// Distinct from the legacy "env_file" load: the secrets-file contract
// is "if present, layer first; KCL env_vars override on conflict" — see
// [LayerHostEnv] for the composition.
func LoadSecretsFile(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	out, err := envutil.ParseDotEnv(path)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// LayerHostEnv composes the env for a host-mode subprocess.
//
//	final = base ⊕ projectConfig ⊕ secrets ⊕ envVars
//
// Order (each later layer overrides earlier on key conflict among the
// three map layers — base os.Environ() always wins so a developer's
// shell override beats them all):
//
//  1. base — the parent process env (typically os.Environ()). Wins last.
//  2. projectConfig — forge.yaml `environments[<env>].config` projected
//     to env-var strings. Same non-secret values cluster-mode services
//     see via the ConfigMap projection, layered here so host-mode
//     services don't drift from cluster-mode behavior. Lowest precedence
//     among the extra layers: secrets and envVars both override on
//     conflict because dev-local overrides (secrets) and KCL pins
//     (envVars) are more specific.
//  3. secrets — KEY=VALUE pairs from the gitignored secrets_file
//     (`.env.<env>`). Wins over projectConfig so a developer can
//     override forge.yaml values locally without editing committed
//     config.
//  4. envVars — KCL-declared per-env config. Wins over secrets so
//     reproducible per-env config can't drift across machines.
//
// Returns a fresh []string safe to assign to cmd.Env. A nil
// projectConfig is treated as an empty layer so the pre-extension
// callers stay terse.
func LayerHostEnv(base []string, projectConfig, secrets, envVars map[string]string) []string {
	// Build the merged extra map in precedence order: projectConfig
	// first, then secrets on top, then envVars on top of that.
	extra := make(map[string]string, len(projectConfig)+len(secrets)+len(envVars))
	for k, v := range projectConfig {
		extra[k] = v
	}
	for k, v := range secrets {
		extra[k] = v
	}
	for k, v := range envVars {
		extra[k] = v
	}
	out, _ := LayerHostEnvConflicts(base, projectConfig, secrets, envVars)
	return out
}

// ShellWinsEnvVar names the parent-env variable holding the comma-separated
// list of keys the developer's shell may still override, e.g.
//
//	FORGE_ENV_OVERRIDE=DATABASE_URL,LOG_LEVEL forge env up dev
//
// It exists because the declared-wins default (see envutil.MergeDeclaredWins)
// removes the ad-hoc override the old base-wins default provided by accident.
// Keeping that affordance EXPLICIT is the point: an override you typed is a
// decision, an override you inherited is an accident.
const ShellWinsEnvVar = "FORGE_ENV_OVERRIDE"

// LayerHostEnvConflicts is LayerHostEnv with the resolved conflicts returned so
// the caller can report them. A declared value that silently loses to an
// ambient one is the failure mode this reporting exists to end.
func LayerHostEnvConflicts(base []string, projectConfig, secrets, envVars map[string]string) ([]string, []envutil.EnvConflict) {
	extra := make(map[string]string, len(projectConfig)+len(secrets)+len(envVars))
	for k, v := range projectConfig {
		extra[k] = v
	}
	for k, v := range secrets {
		extra[k] = v
	}
	for k, v := range envVars {
		extra[k] = v
	}
	shellWins := envutil.ParseShellWins(envutil.Lookup(base, ShellWinsEnvVar))
	return envutil.MergeDeclaredWins(base, extra, shellWins)
}

// PIDPath returns the canonical per-service PID file path:
//
//	$HOME/.cache/forge/run/<service>.pid
//
// Canonical convention. Used by `forge run <svc>` (foreground cleanup
// + background detach + stop subcommand). `forge env up` uses its own
// per-env state file under $HOME/.cache/forge/up/<env>.pids because it
// tracks N processes (services + frontends + port-forwards) and the
// per-env grouping is the unit of teardown there; the two conventions
// coexist deliberately.
func PIDPath(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".cache", "forge", "run", name+".pid"), nil
}
