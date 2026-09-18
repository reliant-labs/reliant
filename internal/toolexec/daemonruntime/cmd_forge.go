// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/version"
)

func init() {
	RegisterCommand("forge.topology", handleForgeTopology)
	RegisterCommand("forge.env_verify", handleForgeEnvVerify)
	RegisterCommand("forge.secret_list", handleForgeSecretList)
	RegisterCommand("forge.audit", handleForgeAudit)
	RegisterCommand("forge.env_status", handleForgeEnvStatus)
}

// =============================================================================
// forge.* — forge project/env/release/secret state, read on the DAEMON's disk.
//
// Reliant is distributed: the api-server, the temporal worker and the daemon
// gateway have no filesystem access. A forge project's state — the release
// ledger under .forge/, deploy/kcl/<env>/, the local secret store, and the
// kubectl context needed to read what a cluster is actually running — exists
// only on the user's machine. So the only component that can answer "what is
// deployed where" is the daemon, and these handlers are that hop:
//
//	web -> Connect RPC (api-server) -> SendDaemonCommand -> here -> forge
//
// THE DAEMON IS A TRANSPORT. Every handler returns forge's own JSON document
// essentially verbatim. It deliberately does NOT re-derive status semantics —
// no daemon-side notion of what counts as drift, what makes a secret missing,
// or when an environment is healthy. A second implementation of those rules
// would be free to disagree with forge's, and a UI painted from two disagreeing
// sources is worse than one painted from a stale single source.
// =============================================================================

// -----------------------------------------------------------------------------
// HOW FORGE IS INVOKED, AND WHY IT IS A SUBPROCESS OF OURSELVES
//
// Reliant embeds forge's whole CLI already (cmd/reliant/commands/forge.go is a
// 12-line wrapper around forgecli.NewRootCmd), so the obvious route is to drive
// that cobra tree in-process. That route was tried and rejected on evidence —
// three separate blockers, each independently fatal:
//
//  1. THE OUTPUT IS NOT CAPTURABLE. Four of the five commands we need write
//     their JSON with json.NewEncoder(os.Stdout) rather than
//     cmd.OutOrStdout(): env topology, env verify, env status and project
//     audit. cmd.SetOut() therefore captures nothing from them. Worse, the
//     daemon's own slog handler writes to os.Stdout too
//     (internal/logging.DefaultOutput), so an in-process call would interleave
//     a forge report into the daemon's log stream and hand us an empty buffer.
//     Redirecting the process-global os.Stdout to capture it would race every
//     other goroutine's logging.
//
//  2. PROJECT RESOLUTION IS CWD-BASED. Forge finds the project by walking up
//     from os.Getwd() (internal/cli/config.go findProjectConfigFile, wrapped by
//     projectDirForKCL). The daemon's CWD is not the project, and os.Chdir in a
//     long-lived multi-goroutine daemon is not safe at any cost.
//
//  3. A NON-ZERO VERDICT IS AN ERROR VALUE IN-PROCESS. Drift exits 1 and
//     unreachable exits 2 via the exitCodeError sentinel. In-process those
//     arrive as plain errors, and "drift found" would have to be reconstructed
//     from an error string.
//
// Running OUR OWN executable with the `forge` subcommand solves all three at
// once, and — this is the part that matters — it keeps the property the
// in-process route was chosen for in the first place. It does NOT run whatever
// `forge` is on PATH (this machine has v0.1.15+dirty on PATH while reliant
// pins v0.1.13 — two different forges). It re-executes THIS binary, so the
// forge that answers is exactly the one reliant was compiled against, byte for
// byte. Version identity is preserved; only the process boundary is added.
//
// What the boundary buys beyond capture: cmd.Dir sets the child's CWD, which
// makes blocker 2 disappear without a process-global flag and without os.Chdir.
// Concurrency is then free — two simultaneous requests for two different
// projects are two processes with two CWDs and no shared state, so there is NO
// mutex here and none is needed. That is also why this code does not pass the
// `-C` / `--project-dir` flag being added to forge: for the daemon the flag is
// redundant, and passing it would break every call against the pinned forge
// that does not have it yet, including the two commands (project audit, env
// status) that work TODAY.
// -----------------------------------------------------------------------------

// forgeCommandResult is one forge invocation's raw outcome.
type forgeCommandResult struct {
	// Stdout is the report document. Kept separate from stderr so a
	// progress line or an experimental-features warning can never be
	// spliced into the JSON we hand upstream.
	Stdout []byte
	// Stderr is diagnostic text only. It is classified (see
	// forgeSupportsCommand) and, for the secret path, never echoed.
	Stderr []byte
	// ExitCode is forge's verdict channel: 0 ok, 1 drift/missing,
	// 2 unreachable. A non-zero code with a parseable report is DATA.
	ExitCode int
}

// forgeCommandRunner is the seam. Tests substitute a function that returns
// canned bytes so the whole test suite runs without forge, without a forge
// project on disk, and without a subprocess — which is what lets these tests
// keep passing while the JSON flags they describe are still unreleased.
type forgeCommandRunner func(ctx context.Context, projectDir string, args []string) (forgeCommandResult, error)

// runForge is the indirection point the tests swap. Not guarded by a mutex:
// it is written once at init and only ever replaced by a test's t.Cleanup-
// restored assignment, never concurrently with a live daemon.
var runForge forgeCommandRunner = runForgeSelfExec

// forgeInvocationTimeout bounds a single forge call. Generous because
// `env topology --verify` and `env verify` read live clusters — a kubectl
// round-trip per workload against a cloud cluster is slow but legitimate.
// The caller's context still wins when it is shorter.
const forgeInvocationTimeout = 2 * time.Minute

// forgeStderrExcerptLimit caps how much stderr reaches an error string. Errors
// cross the daemon transport as plain strings and land in a UI; an unbounded
// tail of a forge failure is not something to paste into a toast.
const forgeStderrExcerptLimit = 800

// runForgeSelfExec re-executes this binary as `<self> forge <args...>` with the
// child's working directory set to the project. See the block comment above for
// why this is a subprocess of ourselves rather than an in-process cobra call.
func runForgeSelfExec(ctx context.Context, projectDir string, args []string) (forgeCommandResult, error) {
	self, err := os.Executable()
	if err != nil {
		return forgeCommandResult{}, fmt.Errorf("resolve own executable: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, forgeInvocationTimeout)
	defer cancel()

	// --silence-experimental keeps forge's experimental banner off stderr.
	// It is a root persistent flag and is present in the pinned forge, so
	// unlike --json it cannot itself trigger an unknown-flag failure.
	full := append([]string{"forge", "--silence-experimental"}, args...)
	cmd := exec.CommandContext(ctx, self, full...)
	cmd.Dir = projectDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	result := forgeCommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}

	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		result.ExitCode = 0
	case errors.As(runErr, &exitErr):
		// A non-zero exit is forge's verdict channel, not a transport
		// failure. Record the code and let the caller decide, based on
		// whether a report came with it, whether this is data or an error.
		result.ExitCode = exitErr.ExitCode()
	default:
		// The process could not be run or was killed (timeout, missing
		// binary). No verdict exists to report.
		return result, fmt.Errorf("run forge: %w", runErr)
	}
	return result, nil
}

// -----------------------------------------------------------------------------
// Error prefixes.
//
// Daemon-command errors cross the transport as PLAIN STRINGS, not wrapped Go
// errors, so a stable prefix is the only thing the proxy can match on to pick
// the right Connect code. Same convention as pkgDirNotExistPrefix in
// cmd_pkg.go; keep these in sync with the proxy that maps them.
// -----------------------------------------------------------------------------
const (
	// forgeProjectDirNotExistPrefix -> NotFound. The path is gone, which is
	// a different thing from the path not being a forge project.
	forgeProjectDirNotExistPrefix = "forge project dir does not exist"
	// forgeCommandFailedPrefix -> Internal. Forge ran, exited non-zero, and
	// produced no report to interpret.
	forgeCommandFailedPrefix = "forge command failed"
)

// forgeResponseMeta is stamped on every response.
//
// ForgeVersion is what lets the UI say "your forge is too old for this view"
// instead of rendering a blank screen. It is read from the module graph
// (version.Forge) rather than from a second hand-maintained constant, so it
// cannot disagree with the forge actually linked into this binary.
type forgeResponseMeta struct {
	// IsForgeProject false means there is no forge.yaml at the path. This
	// is a NORMAL answer, not an error: most reliant projects are not forge
	// projects, and erroring here would light up the UI for every ordinary
	// repository. Precedent: forge_memory.go stats forge.yaml and returns
	// unchanged when it is absent.
	IsForgeProject bool `json:"is_forge_project"`

	// Supported false means the forge this binary carries does not have the
	// command or flag that was asked for. Structured and version-stamped
	// rather than a crash, and distinct from an empty success — an empty
	// success would render as "you have no environments", which is a lie.
	Supported bool `json:"supported"`

	// ForgeVersion is the forge reliant was built against.
	ForgeVersion string `json:"forge_version"`

	// UnsupportedReason is forge's own short complaint ("unknown flag:
	// --json"), present only when Supported is false. Never populated on
	// the secret path — see handleForgeSecretList.
	UnsupportedReason string `json:"unsupported_reason,omitempty"`

	// ExitCode is forge's exit status when a report was produced: 0 ok,
	// 1 drift/missing, 2 unreachable. Carried so the UI can distinguish
	// "checked, all good" from "checked, found problems" without
	// re-deriving either from the report body.
	ExitCode int `json:"exit_code"`
}

// forgeReportResponse is the shape every forge.* command returns.
//
// Report is json.RawMessage — forge's document is passed through untouched
// rather than decoded into daemon-side structs. That is the transport rule
// made structural: there is no field here for a daemon-computed status,
// because there is no daemon-side status to compute. It also means a newer
// forge that adds a field does not need a change here to surface it.
type forgeReportResponse struct {
	forgeResponseMeta
	Report json.RawMessage `json:"report,omitempty"`
}

// forgeInvocation describes one forge call. Built per handler, run by
// invokeForgeReport, so all five commands share one set of state transitions
// (path missing / not a project / unsupported / verdict-as-data / failed).
type forgeInvocation struct {
	// ProjectPath is an absolute path on the DAEMON's filesystem.
	ProjectPath string
	// Args are the forge arguments after the `forge` verb itself.
	Args []string
	// WithholdStderr suppresses forge's stderr from every response and
	// error this invocation can produce. Set on the secret path. See
	// handleForgeSecretList for why that path is treated differently.
	WithholdStderr bool
}

// invokeForgeReport runs one forge command and maps its outcome onto the four
// states a caller must be able to tell apart.
func invokeForgeReport(ctx context.Context, inv forgeInvocation) ([]byte, error) {
	if inv.ProjectPath == "" {
		return nil, fmt.Errorf("project_path is required")
	}

	meta := forgeResponseMeta{ForgeVersion: version.Forge()}

	// STATE 2: the path is gone. A loud, prefix-matched failure rather than
	// an empty report, so the proxy answers NotFound and the UI does not
	// render "this project has no environments" for a deleted directory.
	info, err := os.Stat(inv.ProjectPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s: %s", forgeProjectDirNotExistPrefix, inv.ProjectPath)
		}
		return nil, fmt.Errorf("stat forge project dir %s: %w", inv.ProjectPath, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s: %s (not a directory)", forgeProjectDirNotExistPrefix, inv.ProjectPath)
	}

	// STATE 1: not a forge project. Normal, cheap to detect, and answered
	// without spawning forge at all.
	if _, err := os.Stat(filepath.Join(inv.ProjectPath, "forge.yaml")); err != nil {
		return json.Marshal(forgeReportResponse{forgeResponseMeta: meta})
	}
	meta.IsForgeProject = true

	res, err := runForge(ctx, inv.ProjectPath, inv.Args)
	if err != nil {
		// Could not run or was killed. No verdict, no report.
		//
		// On the secret path the underlying error text is withheld too,
		// not just stderr. The guard is only worth having if it is
		// absolute: an error built by wrapping forge's output is exactly
		// the kind of string that could carry a value, and reasoning
		// case-by-case about which wrapped errors are "safe" is how the
		// exception eventually gets added.
		if inv.WithholdStderr {
			return nil, fmt.Errorf("%s: forge could not be run", forgeCommandFailedPrefix)
		}
		return nil, fmt.Errorf("%s: %v%s", forgeCommandFailedPrefix, err, stderrExcerpt(res.Stderr))
	}

	// STATE 3: this forge is too old for what was asked. Detected from
	// forge's own complaint, and folded into ONE mechanism for both a
	// missing subcommand (`env topology`) and a missing flag (`--json`),
	// because to a caller they are the same condition: the pinned forge
	// cannot answer this question.
	if reason, ok := forgeUnsupportedReason(res); ok {
		if !inv.WithholdStderr {
			meta.UnsupportedReason = reason
		}
		meta.ExitCode = res.ExitCode
		return json.Marshal(forgeReportResponse{forgeResponseMeta: meta})
	}

	// STATE 4: forge produced a report. A non-zero exit here means drift,
	// missing secrets or an unreachable cluster — all of which are the
	// ANSWER to the question asked, so they are returned as data with the
	// exit code alongside. Swallowing them as errors would make "your prod
	// has drifted" indistinguishable from "the daemon broke", which is
	// exactly backwards: the drift verdict is the most valuable thing this
	// whole path exists to deliver.
	report := bytes.TrimSpace(res.Stdout)
	if json.Valid(report) && len(report) > 0 {
		meta.Supported = true
		meta.ExitCode = res.ExitCode
		return json.Marshal(forgeReportResponse{forgeResponseMeta: meta, Report: report})
	}

	// Non-zero with nothing parseable: a genuine failure.
	if res.ExitCode != 0 {
		if inv.WithholdStderr {
			return nil, fmt.Errorf("%s: exit %d", forgeCommandFailedPrefix, res.ExitCode)
		}
		return nil, fmt.Errorf("%s: exit %d%s", forgeCommandFailedPrefix, res.ExitCode, stderrExcerpt(res.Stderr))
	}

	// Exit 0 with unparseable output. Reported rather than passed on: a
	// truncated or non-JSON body handed upstream as a report would surface
	// as a UI parse error far from its cause.
	if inv.WithholdStderr {
		return nil, fmt.Errorf("%s: exit 0 but output was not valid JSON", forgeCommandFailedPrefix)
	}
	return nil, fmt.Errorf("%s: exit 0 but output was not valid JSON%s",
		forgeCommandFailedPrefix, stderrExcerpt(res.Stderr))
}

// forgeUnsupportedMarkers are cobra's own phrasings for "this command tree
// does not have what you named". Matched on stderr because that is where cobra
// writes them and because the alternative — enumerating which forge version
// gained which flag — is a second copy of forge's history that would rot.
//
// Verified against the pinned forge v0.1.13 via the reliant binary:
//
//	reliant forge env topology --json  -> stderr "unknown flag: --json", exit 1
//	reliant forge env topology         -> stderr `unknown command "topology" for "reliant forge env"`, exit 1
var forgeUnsupportedMarkers = []string{
	"unknown command",
	"unknown flag",
	"unknown shorthand flag",
}

// forgeUnsupportedReason reports whether this outcome is a version-capability
// miss, and forge's first line of complaint if so.
//
// Requires stdout to be empty. A forge that emitted a full report and then
// complained about something on stderr has answered the question, and must not
// be downgraded to "unsupported" — that would discard a valid report.
func forgeUnsupportedReason(res forgeCommandResult) (string, bool) {
	if res.ExitCode == 0 || len(bytes.TrimSpace(res.Stdout)) > 0 {
		return "", false
	}
	for _, line := range strings.Split(string(res.Stderr), "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		for _, marker := range forgeUnsupportedMarkers {
			if strings.HasPrefix(lower, marker) {
				return trimmed, true
			}
		}
	}
	return "", false
}

// stderrExcerpt renders a bounded, single-line stderr tail for an error
// message, prefixed with ": " when non-empty so callers can concatenate it.
func stderrExcerpt(stderr []byte) string {
	text := strings.TrimSpace(string(stderr))
	if text == "" {
		return ""
	}
	// Flatten BEFORE truncating. Newline replacement expands the string
	// (one byte becomes three), so truncating first overshoots the limit by
	// up to 3x on newline-dense output — which is precisely what a forge
	// failure looks like.
	text = strings.ReplaceAll(text, "\n", " | ")
	if len(text) > forgeStderrExcerptLimit {
		text = text[len(text)-forgeStderrExcerptLimit:]
	}
	return ": " + text
}

// --- forge.topology ---

type forgeTopologyRequest struct {
	ProjectPath string `json:"project_path"`
	// Verify opts in to reading every environment's live cluster. Off by
	// default in forge for a load-bearing reason this handler must not
	// paper over: without it every image's state is "not_verified", which
	// means UNKNOWN, not OK. A caller that renders not_verified as green
	// paints a clean bill of health over an environment nobody looked at.
	Verify bool `json:"verify,omitempty"`
	// Envs narrows the report. Empty means every declared environment.
	Envs []string `json:"envs,omitempty"`
}

func handleForgeTopology(ctx context.Context, payload []byte) ([]byte, error) {
	var req forgeTopologyRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	args := []string{"env", "topology", "--json"}
	if req.Verify {
		args = append(args, "--verify")
	}
	for _, env := range req.Envs {
		if env = strings.TrimSpace(env); env != "" {
			args = append(args, env)
		}
	}

	return invokeForgeReport(ctx, forgeInvocation{ProjectPath: req.ProjectPath, Args: args})
}

// --- forge.env_verify ---

type forgeEnvVerifyRequest struct {
	ProjectPath string `json:"project_path"`
	Env         string `json:"env"`
}

func handleForgeEnvVerify(ctx context.Context, payload []byte) ([]byte, error) {
	var req forgeEnvVerifyRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if req.Env == "" {
		return nil, fmt.Errorf("env is required")
	}

	// Exit 1 (drift/missing) and exit 2 (unreachable) both come back as a
	// successful response carrying the report and the code — see
	// invokeForgeReport. An unbound environment is forge's exit 0 with
	// bound:false, which needs no special handling here.
	return invokeForgeReport(ctx, forgeInvocation{
		ProjectPath: req.ProjectPath,
		Args:        []string{"env", "verify", req.Env, "--json"},
	})
}

// --- forge.secret_list ---

type forgeSecretListRequest struct {
	ProjectPath string `json:"project_path"`
	Env         string `json:"env"`
}

// handleForgeSecretList lists declared secrets and whether each HAS a value.
//
// `forge secret list --json` is built so that no field in its report can carry
// a secret VALUE — the report type graph holds only names, booleans and
// coordinates, and forge pins that with a reflection test over an allow-list.
// This handler's job is to not be the thing that undermines it:
//
//   - the report body is never logged, here or anywhere on this path;
//   - WithholdStderr keeps forge's stderr out of every response and every
//     error string this invocation can produce, so a diagnostic line cannot
//     become the one channel that leaks a value;
//   - no field is added alongside forge's document — the passthrough is
//     json.RawMessage, so this handler has no place to put a value even by
//     accident. TestForgeResponseStructsCarryNoSecretValueFields pins that.
//
// Exit 1 (a declared secret has no value) is a verdict, not a failure: it
// returns as data with ok:false in forge's report.
func handleForgeSecretList(ctx context.Context, payload []byte) ([]byte, error) {
	var req forgeSecretListRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if req.Env == "" {
		return nil, fmt.Errorf("env is required")
	}

	return invokeForgeReport(ctx, forgeInvocation{
		ProjectPath:    req.ProjectPath,
		Args:           []string{"secret", "list", req.Env, "--json"},
		WithholdStderr: true,
	})
}

// --- forge.audit ---

type forgeAuditRequest struct {
	ProjectPath string `json:"project_path"`
}

func handleForgeAudit(ctx context.Context, payload []byte) ([]byte, error) {
	var req forgeAuditRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	return invokeForgeReport(ctx, forgeInvocation{
		ProjectPath: req.ProjectPath,
		Args:        []string{"project", "audit", "--json"},
	})
}

// --- forge.env_status ---

type forgeEnvStatusRequest struct {
	ProjectPath string `json:"project_path"`
	Env         string `json:"env"`
}

// handleForgeEnvStatus reports an environment's runtime state.
//
// The report's checks carry pass/fail/warn/skip/unknown. UNKNOWN means the
// check could not be measured — it is emphatically not a pass and not a fail,
// and nothing on this path collapses it into either. The passthrough keeps
// that guarantee by construction: the daemon never reads a check's status, so
// it has no opportunity to reinterpret one.
func handleForgeEnvStatus(ctx context.Context, payload []byte) ([]byte, error) {
	var req forgeEnvStatusRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if req.Env == "" {
		return nil, fmt.Errorf("env is required")
	}

	return invokeForgeReport(ctx, forgeInvocation{
		ProjectPath: req.ProjectPath,
		Args:        []string{"env", "status", req.Env, "--json"},
	})
}
