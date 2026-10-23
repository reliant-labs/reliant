// Package cli — `forge doctor` host-tool checks.
//
// Forge shells out to a handful of third-party binaries (kcl, kubectl,
// docker, go, buf, k3d, git, npm, mkcert, ...). When one of those is
// missing the user sees a cryptic `exec: "kcl": not found` from
// wherever the binary was first invoked — usually deep inside a
// multi-phase pipeline that has already partially run. Doctor's job
// is to surface the gap up front with an OS-specific install hint so
// the user can fix the toolchain before the failure cascades.
//
// The check decision core (runToolChecks) is pure: it takes function
// values for "is binary on PATH" and "run version command" so unit
// tests cover every status branch without exec-ing real processes.
//
// The set of tools required for a given project depends on which
// `features.*` blocks are enabled (deploy needs kcl + kubectl;
// frontend needs npm; ingress with mkcert TLS needs mkcert host-side;
// etc.). Each toolCheck carries a Required(cfg, projectDir) predicate
// so the report skips tools the project doesn't actually use rather
// than emitting "missing optional tool" noise.
package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/doctor"
	"github.com/reliant-labs/forge/internal/hostlaunch"
	"github.com/reliant-labs/forge/internal/templates"
)

// toolCheck declares a host binary forge depends on plus the metadata
// doctor needs to verify it and emit a helpful failure hint.
//
// MinVersion is the floor we have confidence in. Empty string means
// "any present version is fine" — we don't want to emit warns for
// tools where forge is genuinely version-agnostic. VersionArgs is the
// argv tail passed to the binary to extract a version string (e.g.
// `[]string{"version", "--client=true"}` for kubectl); the parser
// scrapes the first dotted version out of stdout+stderr so it works
// across kubectl/kcl/docker/buf/go etc. without per-tool parsing.
//
// InstallHints is keyed by runtime.GOOS. Missing entries fall back to
// a generic "see <upstream-url>" line so we don't lie about
// platforms we haven't pinned.
type toolCheck struct {
	Name        string
	Description string
	Required    func(cfg *config.ProjectConfig, projectDir string) bool
	VersionArgs []string
	MinVersion  string
	// MinVersionByOS raises the floor on one GOOS, for a tool forge relies
	// on a platform-specific fix or feature of (air's [build.windows]
	// overrides; Task's Windows core utils). The effective floor is the
	// higher of MinVersion and this entry — see forOS.
	MinVersionByOS map[string]string
	InstallHints   map[string]string
	// UpstreamURL backs the fallback hint for unknown OSes; also
	// included in the evidence detail when the OS-specific hint is
	// unavailable.
	UpstreamURL string
}

// binaryLookupFunc reports whether a binary is resolvable on PATH.
// Mirrors exec.LookPath's signature so production can pass it
// directly and tests can inject a map-backed stub.
type binaryLookupFunc func(name string) (string, error)

// versionRunnerFunc runs `<name> <args...>` and returns combined
// stdout+stderr plus any error. Doctor only ever interprets the
// output as a version string, so combined output is the right shape:
// some tools (notably kubectl --client) emit on stderr.
type versionRunnerFunc func(ctx context.Context, name string, args []string) (string, error)

// realBinaryLookup is the production binaryLookupFunc.
func realBinaryLookup(name string) (string, error) { return exec.LookPath(name) }

// realVersionRunner is the production versionRunnerFunc. We give the
// command a short fixed deadline because some tools (helm, kcl) can
// hang for tens of seconds when their config dir is unreadable or
// blocks on a network probe; doctor must stay snappy.
func realVersionRunner(ctx context.Context, name string, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// requiredAlways is the predicate for tools every forge project needs
// (go, git) regardless of features.* state.
func requiredAlways(_ *config.ProjectConfig, _ string) bool { return true }

// requiredWhen folds a feature getter into the predicate shape.
func requiredWhen(getter func(config.FeaturesConfig) bool) func(*config.ProjectConfig, string) bool {
	return func(cfg *config.ProjectConfig, _ string) bool {
		if cfg == nil {
			return false
		}
		return getter(cfg.Features)
	}
}

// requiredForAirConfig: air is needed by a project that scaffolds an
// .air.toml (the host hot-reload runner config). A project without one
// never launches air, whatever its features say.
func requiredForAirConfig(_ *config.ProjectConfig, projectDir string) bool {
	if projectDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(projectDir, hostlaunch.DefaultAirConfig))
	return err == nil
}

// requiredForMkcert reads every env's KCL to see whether any Gateway
// declares tls.mode = "mkcert". mkcert is host-side-only — it
// generates a cert against the host's mkcert CA and the cert flows
// into the cluster as a Secret — so we only flag it as required
// when the user has actually opted into mkcert TLS.
//
// Render failures degrade to "not required" rather than "required":
// the ingress doctor check will surface render errors separately, and
// we don't want a duplicate fail line for the same root cause.
func requiredForMkcert(cfg *config.ProjectConfig, projectDir string) bool {
	if cfg == nil || !cfg.Features.IngressEnabled() {
		return false
	}
	envs, err := ListEnvs(projectDir)
	if err != nil || len(envs) == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, env := range envs {
		entities, err := RenderKCL(ctx, projectDir, env)
		if err != nil {
			continue
		}
		for _, gw := range entities.Gateways {
			if gw.TLS != nil && strings.EqualFold(strings.TrimSpace(gw.TLS.Mode), "mkcert") {
				return true
			}
		}
	}
	return false
}

// defaultToolChecks is the canonical tool list. Adding a new tool
// here is the entire change needed to teach `forge doctor` about it.
//
// Why not gate kcl/kubectl on deploy/ingress? kcl is also used by
// audit + map for any project that has a deploy/ dir, even if
// deploy is feature-disabled, and kubectl is the cluster lingua
// franca. So they're scoped to "deploy enabled" (the dominant
// usage) and skipped cleanly otherwise.
func defaultToolChecks(goos string) []toolCheck {
	checks := allToolChecks()
	for i := range checks {
		checks[i] = checks[i].forOS(goos)
	}
	return checks
}

// forOS folds MinVersionByOS[goos] into MinVersion, so the comparison path
// stays one field and a platform floor can only ever RAISE the bar.
func (tc toolCheck) forOS(goos string) toolCheck {
	floor, ok := tc.MinVersionByOS[goos]
	if !ok || floor == "" {
		return tc
	}
	if tc.MinVersion == "" {
		tc.MinVersion = floor
		return tc
	}
	if cmp, ok := compareVersions(floor, tc.MinVersion); ok && cmp > 0 {
		tc.MinVersion = floor
	}
	return tc
}

// airToolCheck is air, split out of allToolChecks because it is the one tool
// whose floor depends on the scaffolded config's shape and on the OS.
func airToolCheck() toolCheck {
	return toolCheck{
		// air is the default host runner for `forge env up` and
		// `task dev`, launched with the scaffolded .air.toml. Floors
		// (verified against air's git history):
		//   - every OS: the config names its binary with `entrypoint`
		//     (no `bin`), which air gained in v1.63.2 — older air has
		//     nothing to run;
		//   - windows: `[build.windows]` arrived in v1.64.1, and the
		//     in-place .exe build needs v1.65.3, which restored stopping
		//     the app before building on Windows (v1.65.2 builds while
		//     it runs, and Windows locks a running .exe).
		Name:           "air",
		Description:    "air — hot-reload host runner for `forge env up` / `task dev` (.air.toml)",
		Required:       requiredForAirConfig,
		VersionArgs:    []string{"-v"},
		MinVersion:     "1.63.2",
		MinVersionByOS: map[string]string{"windows": "1.65.3"},
		InstallHints: map[string]string{
			"darwin":  "go install github.com/air-verse/air@latest",
			"linux":   "go install github.com/air-verse/air@latest",
			"windows": "go install github.com/air-verse/air@latest",
		},
		UpstreamURL: "https://github.com/air-verse/air#installation",
	}
}

func allToolChecks() []toolCheck {
	return []toolCheck{
		{
			Name:        "go",
			Description: "Go toolchain — required for forge build, generate, vendored binaries",
			Required:    requiredAlways,
			VersionArgs: []string{"version"},
			MinVersion:  "1.22.0",
			InstallHints: map[string]string{
				"darwin":  "brew install go",
				"linux":   "see https://go.dev/dl/  (or your distro: `apt install golang-go`, `dnf install golang`)",
				"windows": "choco install golang   (or `scoop install go`)",
			},
			UpstreamURL: "https://go.dev/dl/",
		},
		{
			Name:        "git",
			Description: "git — required for project scaffolding, version stamping, CI integration",
			Required:    requiredAlways,
			VersionArgs: []string{"--version"},
			InstallHints: map[string]string{
				"darwin":  "xcode-select --install   (ships git) or `brew install git`",
				"linux":   "apt install git   (or `dnf install git`)",
				"windows": "winget install Git.Git   (or `choco install git`)",
			},
			UpstreamURL: "https://git-scm.com/downloads",
		},
		{
			// task runs the project's Taskfile.yml, which is where the test
			// suite is DEFINED — `task test:short` is the local edit loop and
			// `task test` is what the generated CI test job runs. That makes
			// it a real prerequisite, not a convenience: without it there is
			// no way to run the project's tests as the project defines them.
			//
			// requiredAlways because every project kind (service / cli /
			// library) is scaffolded with a Taskfile. The cloud workspace
			// image already installs it (control-plane
			// docker/Dockerfile.workspace-base, TASK_VERSION) — this check is
			// what makes that image's tool manifest verifiable from forge.
			Name:        "task",
			Description: "Task — runs the project's Taskfile.yml (`task test` is the test suite, `task test:short` the inner loop)",
			Required:    requiredAlways,
			VersionArgs: []string{"--version"},
			// Windows has no rm/mkdir/cp/mv; scaffolded Taskfiles rely on
			// Task's built-in core utils (v3.45.3, default-on for Windows),
			// and `rm -f` there was fixed in v3.45.5 (go-task #2506).
			MinVersionByOS: map[string]string{"windows": "3.45.5"},
			InstallHints: map[string]string{
				"darwin":  "brew install go-task/tap/go-task",
				"linux":   "go install github.com/go-task/task/v3/cmd/task@latest   (or see https://taskfile.dev/installation/)",
				"windows": "winget install Task.Task   (or `scoop install task`)",
			},
			UpstreamURL: "https://taskfile.dev/installation/",
		},
		{
			Name:        "buf",
			Description: "buf — proto codegen orchestrator (buf.gen.yaml)",
			Required:    requiredWhen(func(f config.FeaturesConfig) bool { return f.CodegenEnabled() }),
			VersionArgs: []string{"--version"},
			InstallHints: map[string]string{
				"darwin":  "brew install bufbuild/buf/buf",
				"linux":   "see https://buf.build/docs/installation  (or `go install github.com/bufbuild/buf/cmd/buf@latest`)",
				"windows": "scoop install buf",
			},
			UpstreamURL: "https://buf.build/docs/installation",
		},
		{
			Name:        "kcl",
			Description: "KCL — renders deploy/kcl/<env>/main.k into Kubernetes manifests",
			Required:    requiredWhen(func(f config.FeaturesConfig) bool { return f.DeployEnabled() }),
			VersionArgs: []string{"version"},
			InstallHints: map[string]string{
				"darwin":  "brew install kcl-lang/tap/kcl",
				"linux":   "curl -fsSL https://kcl-lang.io/script/install-cli.sh | KCL_VERSION=" + templates.KCLVersion + " bash",
				"windows": "scoop install kcl   (or see https://kcl-lang.io/docs/user_docs/getting-started/install)",
			},
			UpstreamURL: "https://kcl-lang.io/docs/user_docs/getting-started/install",
		},
		{
			Name:        "kubectl",
			Description: "kubectl — cluster CLI; every cluster-side operation",
			Required:    requiredWhen(func(f config.FeaturesConfig) bool { return f.DeployEnabled() }),
			VersionArgs: []string{"version", "--client=true"},
			InstallHints: map[string]string{
				"darwin":  "brew install kubectl",
				"linux":   "see https://kubernetes.io/docs/tasks/tools/install-kubectl-linux/",
				"windows": "choco install kubernetes-cli   (or `scoop install kubectl`)",
			},
			UpstreamURL: "https://kubernetes.io/docs/tasks/tools/",
		},
		{
			Name:        "docker",
			Description: "docker — image build + local container runtime for k3d",
			Required:    requiredWhen(func(f config.FeaturesConfig) bool { return f.BuildEnabled() }),
			VersionArgs: []string{"--version"},
			InstallHints: map[string]string{
				"darwin":  "brew install --cask docker   (or install Docker Desktop / OrbStack)",
				"linux":   "see https://docs.docker.com/engine/install/",
				"windows": "winget install Docker.DockerDesktop",
			},
			UpstreamURL: "https://docs.docker.com/engine/install/",
		},
		{
			Name:        "k3d",
			Description: "k3d — local k3s-in-docker dev cluster (forge cluster up)",
			Required:    requiredWhen(func(f config.FeaturesConfig) bool { return f.DeployEnabled() }),
			VersionArgs: []string{"version"},
			InstallHints: map[string]string{
				"darwin":  "brew install k3d",
				"linux":   "curl -s https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash",
				"windows": "choco install k3d   (or `scoop install k3d`)",
			},
			UpstreamURL: "https://k3d.io/#installation",
		},
		{
			Name:        "helm",
			Description: "Helm — renders declarative platform charts into the forge apply pipeline",
			Required:    requiredWhen(func(f config.FeaturesConfig) bool { return f.DeployEnabled() }),
			VersionArgs: []string{"version", "--short"},
			MinVersion:  "3.0.0",
			InstallHints: map[string]string{
				"darwin":  "brew install helm",
				"linux":   "see https://helm.sh/docs/intro/install/",
				"windows": "choco install kubernetes-helm   (or `scoop install helm`)",
			},
			UpstreamURL: "https://helm.sh/docs/intro/install/",
		},
		airToolCheck(),
		{
			Name:        "npm",
			Description: "npm — frontend package manager (frontends/<name>/, proto-es codegen)",
			Required:    requiredWhen(func(f config.FeaturesConfig) bool { return f.FrontendEnabled() }),
			VersionArgs: []string{"--version"},
			InstallHints: map[string]string{
				"darwin":  "brew install node   (ships npm)",
				"linux":   "see https://nodejs.org/en/download/  (or use nvm/fnm/volta)",
				"windows": "winget install OpenJS.NodeJS   (or `choco install nodejs`)",
			},
			UpstreamURL: "https://nodejs.org/en/download/",
		},
		{
			Name:        "mkcert",
			Description: "mkcert — host-side TLS cert generator for dev clusters using tls.mode=\"mkcert\"",
			Required:    requiredForMkcert,
			VersionArgs: []string{"-version"},
			InstallHints: map[string]string{
				"darwin":  "brew install mkcert   (then `mkcert -install` once per machine)",
				"linux":   "see https://github.com/FiloSottile/mkcert#installation  (then `mkcert -install` once)",
				"windows": "choco install mkcert   (or `scoop install mkcert`; then `mkcert -install` once)",
			},
			UpstreamURL: "https://github.com/FiloSottile/mkcert#installation",
		},
	}
}

// runToolChecks is the pure decision core. Given a tool list,
// project state, and the two seam functions (lookup + version-runner),
// it returns one doctor.CheckResult per tool. No I/O of its own —
// everything goes through the seams.
//
// Status mapping:
//   - !Required(cfg)              → StatusSkip ("not needed for this project")
//   - Required & lookup fails     → StatusFail (with OS-specific install hint)
//   - Required & no MinVersion    → StatusPass
//   - Required & MinVersion below → StatusWarn (forge MAY work; not pinned)
//   - Required & all good         → StatusPass with resolved version in evidence
func runToolChecks(ctx context.Context, checks []toolCheck, cfg *config.ProjectConfig, projectDir string, lookup binaryLookupFunc, runVersion versionRunnerFunc) []doctor.CheckResult {
	if lookup == nil {
		lookup = realBinaryLookup
	}
	if runVersion == nil {
		runVersion = realVersionRunner
	}
	results := make([]doctor.CheckResult, 0, len(checks))
	for _, tc := range checks {
		results = append(results, evaluateToolCheck(ctx, tc, cfg, projectDir, lookup, runVersion))
	}
	// Stable order: sort by name so JSON output is deterministic
	// across runs and platforms.
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	return results
}

// evaluateToolCheck is the per-tool decision. Split out so the
// runToolChecks loop reads as policy ("for every tool, evaluate")
// and this function holds the branches.
func evaluateToolCheck(ctx context.Context, tc toolCheck, cfg *config.ProjectConfig, projectDir string, lookup binaryLookupFunc, runVersion versionRunnerFunc) doctor.CheckResult {
	name := "tool: " + tc.Name
	start := time.Now()
	result := doctor.CheckResult{Name: name}

	required := tc.Required != nil && tc.Required(cfg, projectDir)
	if !required {
		result.Status = doctor.StatusSkip
		result.Message = "not required for this project"
		result.Duration = time.Since(start)
		return result
	}

	path, err := lookup(tc.Name)
	if err != nil {
		result.Status = doctor.StatusFail
		result.Message = fmt.Sprintf("%s not found on PATH", tc.Name)
		hint := installHintForOS(tc, runtime.GOOS)
		lines := []string{
			fmt.Sprintf("error: %s — %s", tc.Description, tc.Name),
			"install: " + hint,
		}
		result.Evidence = strings.Join(lines, "\n")
		result.Duration = time.Since(start)
		return result
	}

	version, vErr := captureVersion(ctx, tc, runVersion)
	if vErr != nil || version == "" {
		// Tool exists but we couldn't get a version. Treat as pass —
		// doctor's job is presence, not strict version enforcement.
		// We still surface the resolved path so users can confirm
		// the right binary is being picked up.
		result.Status = doctor.StatusPass
		result.Message = fmt.Sprintf("%s present (version unknown)", tc.Name)
		result.Evidence = "path: " + path
		result.Duration = time.Since(start)
		return result
	}

	if tc.MinVersion == "" {
		result.Status = doctor.StatusPass
		result.Message = fmt.Sprintf("%s present (%s)", tc.Name, version)
		result.Evidence = "path: " + path
		result.Duration = time.Since(start)
		return result
	}

	cmp, cmpOK := compareVersions(version, tc.MinVersion)
	switch {
	case !cmpOK:
		// Couldn't parse a comparable version — same disposition as
		// "version unknown": pass with the raw string in evidence so
		// users can eyeball it. Avoids false-warn noise on tools with
		// quirky version output we don't yet parse.
		result.Status = doctor.StatusPass
		result.Message = fmt.Sprintf("%s present (%s; version unparsable)", tc.Name, version)
		result.Evidence = "path: " + path
	case cmp < 0:
		result.Status = doctor.StatusWarn
		result.Message = fmt.Sprintf("%s %s is below tested floor %s", tc.Name, version, tc.MinVersion)
		hint := installHintForOS(tc, runtime.GOOS)
		result.Evidence = strings.Join([]string{
			fmt.Sprintf("warn: %s %s < %s", tc.Name, version, tc.MinVersion),
			"path: " + path,
			"upgrade: " + hint,
		}, "\n")
	default:
		result.Status = doctor.StatusPass
		result.Message = fmt.Sprintf("%s %s (>= %s)", tc.Name, version, tc.MinVersion)
		result.Evidence = "path: " + path
	}
	result.Duration = time.Since(start)
	return result
}

// installHintForOS returns the OS-specific install hint, falling back
// to the upstream URL line for OSes we haven't pinned.
func installHintForOS(tc toolCheck, goos string) string {
	if hint, ok := tc.InstallHints[goos]; ok && hint != "" {
		return hint
	}
	if tc.UpstreamURL != "" {
		return "see " + tc.UpstreamURL
	}
	return fmt.Sprintf("install %s for your platform", tc.Name)
}

// captureVersion runs the tool's version command and extracts a
// dotted version string. Returns ("", err) when the runner fails;
// returns ("", nil) when the runner ran but no version pattern
// matched (the caller treats both as "version unknown").
func captureVersion(ctx context.Context, tc toolCheck, runVersion versionRunnerFunc) (string, error) {
	if len(tc.VersionArgs) == 0 {
		return "", nil
	}
	out, err := runVersion(ctx, tc.Name, tc.VersionArgs)
	if err != nil && out == "" {
		return "", err
	}
	v := extractVersion(out)
	return v, nil
}

// extractVersion finds the first dotted version triple (or pair)
// in the output and returns it stripped of a leading "v". Works
// across kubectl/kcl/docker/buf/go/k3d/git/npm/mkcert version
// output without per-tool parsers. Two-segment versions (e.g.
// "1.21") are returned as "1.21.0" to make comparison uniform.
func extractVersion(s string) string {
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			continue
		}
		// Walk forward greedily over digits and dots.
		j := i
		dots := 0
		for j < len(s) && (isDigit(s[j]) || s[j] == '.') {
			if s[j] == '.' {
				dots++
			}
			j++
		}
		// Trim trailing dots (e.g. "v1.2." should yield "1.2").
		for j > i && s[j-1] == '.' {
			j--
			dots--
		}
		if dots >= 1 {
			v := s[i:j]
			if dots == 1 {
				v += ".0"
			}
			return v
		}
		i = j
	}
	return ""
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// compareVersions returns -1/0/+1 for a<b / a==b / a>b. ok=false
// indicates one of the inputs failed to parse — caller treats that
// as "skip version enforcement" rather than warn.
//
// Supports dotted ints only (1.2.3); pre-release suffixes are
// stripped before comparison so "1.34.0-rc.1" == "1.34.0" for our
// purposes. Acceptable because doctor's a presence check, not a
// release-manager.
func compareVersions(a, b string) (int, bool) {
	pa, ok := parseVersion(a)
	if !ok {
		return 0, false
	}
	pb, ok := parseVersion(b)
	if !ok {
		return 0, false
	}
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x < y {
			return -1, true
		}
		if x > y {
			return 1, true
		}
	}
	return 0, true
}

// parseVersion splits a dotted-int version string into its
// components. Bails on the first non-digit (after a leading "v" or
// "V") so "1.34.0-rc.1" parses as [1,34,0].
func parseVersion(s string) ([]int, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	if s == "" {
		return nil, false
	}
	parts := strings.Split(s, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		// Trim trailing non-digit suffix (e.g. "0-rc.1" -> "0").
		end := 0
		for end < len(p) && isDigit(p[end]) {
			end++
		}
		if end == 0 {
			if len(out) == 0 {
				return nil, false
			}
			break
		}
		var n int
		for k := 0; k < end; k++ {
			n = n*10 + int(p[k]-'0')
		}
		out = append(out, n)
		if end < len(p) {
			break
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// runToolDoctorChecks is the side-effecting wrapper invoked from
// runDoctor. It runs only in the unfiltered pass — there is no "tools"
// signal, and doctor.RunFiltered rejects one before this is reached.
func runToolDoctorChecks(ctx context.Context, cfg *config.ProjectConfig, projectDir, signal string) []doctor.CheckResult {
	// Only the unfiltered pass. `--signal` selects among the values
	// doctor.RunFiltered accepts (metrics/traces/logs/profiles/deploy); it
	// REJECTS anything else before a check runs, so a guard naming its own
	// signal here could never be true and advertised a filter that does not
	// exist.
	if signal != "" {
		return nil
	}
	return runToolChecks(ctx, defaultToolChecks(runtime.GOOS), cfg, projectDir, realBinaryLookup, realVersionRunner)
}
