// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/version"
)

// =============================================================================
// ForgeService — the web tier's view of a forge project's release/env state.
//
// Reliant is distributed: the api-server has no filesystem access. A forge
// project's release ledger, deploy/kcl/<env>/, local secret store and kubectl
// context exist only on the DAEMON's disk, so each RPC here is one hop onto the
// matching forge.* daemon command:
//
//	web -> here (api-server) -> SendDaemonCommand -> daemon -> forge
//
// THIS LAYER IS A TRANSLATOR, NOT A THIRD OPINION. It validates the request,
// resolves the project to a daemon-side path, dispatches, and maps failures
// onto Connect codes. It does NOT interpret forge's report: no notion here of
// what counts as drift, what makes a secret missing, or when an environment is
// healthy. Forge owns those rules and the daemon passes them through verbatim;
// a third implementation would be free to disagree with both, and a UI painted
// from disagreeing sources is worse than one painted from a stale single
// source. That is why every response carries report_json rather than a
// field-by-field proto mirror.
// =============================================================================

// -----------------------------------------------------------------------------
// TIMEOUTS ARE PER-COMMAND, BECAUSE THE COMMANDS ARE NOT ALIKE.
//
// A single shared constant (project.go uses 30s for everything) is wrong on
// both ends here: it is ~30x too generous for a local JSON read, and too tight
// for a verify that makes a kubectl round-trip per workload against a cloud API
// server. Too tight is the harmful direction — it converts a legitimately slow
// cluster read into a timeout, and a timeout is indistinguishable at this layer
// from a cluster that is genuinely unreachable. That is how a VPN blip gets
// reported as a release defect.
//
// Each budget below is set ABOVE the cost of the work forge actually does, so
// that hitting it means something is really wrong rather than merely slow. The
// daemon separately caps any single forge invocation at 2 minutes
// (forgeInvocationTimeout), so no budget here may exceed that — a client-side
// budget longer than the daemon's would just wait for a process the daemon has
// already killed.
// -----------------------------------------------------------------------------
const (
	// forgeTopologyTimeoutMs — ledger only. Reads .forge/ JSON and the KCL env
	// declarations off local disk; no network. 15s is already generous for a
	// filesystem read and leaves room for a cold page cache on a big project.
	forgeTopologyTimeoutMs = 15_000

	// forgeTopologyVerifyTimeoutMs — the same walk, but with --verify it reads
	// EVERY declared environment's live cluster. Cost scales with the number of
	// environments, each a cloud API-server round trip. forge's own per-env
	// verify budget is 60s (defaultEnvVerifyTimeout), so a 4-env project can
	// legitimately outlast any single-env figure. 110s keeps this the binding
	// constraint rather than the daemon's 2-minute cap, so the failure arrives
	// here as a classifiable timeout instead of a killed subprocess.
	forgeTopologyVerifyTimeoutMs = 110_000

	// forgeEnvVerifyTimeoutMs — one environment against its live cluster.
	// forge's own default for exactly this operation is 60s; 75s leaves headroom
	// for the daemon hop and process spawn on top of forge's own budget, so
	// forge gets to return its UNREACHABLE verdict (exit 2) rather than being
	// cut off by us. Preferring forge's verdict to our timeout matters: forge's
	// is a considered answer, ours is an absence of one.
	forgeEnvVerifyTimeoutMs = 75_000

	// forgeSecretListTimeoutMs — reads declared secrets and probes whether each
	// has a value. Local store plus, for cloud-backed providers, a metadata
	// existence check. No cluster workload reads. 30s.
	forgeSecretListTimeoutMs = 30_000

	// forgeAuditTimeoutMs — a whole-project rollup: parses every proto,
	// migration and handler. No network, but genuinely CPU- and IO-heavy and it
	// grows with the project (58KB of JSON for control-plane). 60s.
	forgeAuditTimeoutMs = 60_000

	// forgeEnvStatusTimeoutMs — runtime checks for one environment. Several
	// cluster reads, but shallower than a full verify (no per-workload image
	// reconciliation). 45s.
	forgeEnvStatusTimeoutMs = 45_000

	// forgePromotePlanTimeoutMs — the read-only promote preview. Reads two
	// release ledgers and the binding ledger off local disk, then one
	// `git rev-list` over the commit range between them. No cluster reads and no
	// network at all, so this belongs at the SHORT end with topology rather than
	// anywhere near the verify budgets. 20s: the git walk is the only variable
	// cost and it is bounded by forge's own promoteGitTimeout, so a fifth of the
	// way to the daemon's cap is already generous for a cold page cache on a
	// large history.
	forgePromotePlanTimeoutMs = 20_000

	// forgePromoteApplyTimeoutMs — the guarded write.
	//
	// PROMOTE DOES NOT DEPLOY. It writes ONE JSON file (the binding ledger);
	// not a byte reaches a cluster until `forge env deploy` runs. So this is
	// emphatically not a cluster-budget operation, and giving it one would be a
	// mistake in the harmful direction: a long budget on a write invites a
	// retry against a promote that is still in flight.
	//
	// 45s because the apply path runs forge TWICE — the read-only guard plan,
	// then the real promote (which computes its own plan internally from the
	// same function). That is roughly two plan budgets plus the single file
	// write, and it stays well inside the daemon's 2-minute per-invocation cap.
	forgePromoteApplyTimeoutMs = 45_000

	// forgeDeployPlanTimeoutMs — the read-only deploy preview
	// (`env deploy --dry-run --json`). It renders the whole env through KCL and
	// runs the deployability preflight, which reads Secrets and image metadata
	// off the LIVE target. So unlike the promote plan this DOES touch a cluster,
	// and it sits with the cluster-reading budgets rather than the local ones.
	// 100s keeps it the binding constraint rather than the daemon's 2-minute cap,
	// so a slow cloud API server arrives here as a classifiable timeout instead
	// of a killed subprocess.
	forgeDeployPlanTimeoutMs = 100_000

	// forgeDeployStartTimeoutMs — the guarded START, and this budget bounds
	// ONLY the start, never the deploy.
	//
	// THE DEPLOY ITSELF HAS NO BUDGET HERE, WHICH IS THE ENTIRE POINT. The
	// daemon caps a single forge invocation at 2 minutes; `forge env deploy`
	// waits up to 5 minutes PER RESOURCE, so a synchronous apply would be killed
	// mid-rollout and this RPC would report a timeout for a deploy that landed.
	// So the daemon detaches the apply and returns a handle, and what this budget
	// covers is the part that happens before that: the guard plan (a full render
	// plus a live preflight, i.e. the plan budget above) plus spawning the job.
	// 110s is the plan's budget with headroom for the hop, and it stays inside
	// the daemon's cap because the START — unlike the deploy — really does
	// complete in one invocation's worth of work.
	forgeDeployStartTimeoutMs = 110_000

	// forgeDeployStatusTimeoutMs — a poll. Reads one entry out of the daemon's
	// in-memory job registry: no forge process, no cluster read, no disk. 15s is
	// already absurdly generous and exists only to bound the transport hop.
	// Deliberately SHORT: a poll is on a UI's repeat loop, and a long budget
	// there would stack requests behind an unresponsive daemon.
	forgeDeployStatusTimeoutMs = 15_000
)

// -----------------------------------------------------------------------------
// Error-text markers.
//
// Daemon-command errors cross the transport as PLAIN STRINGS, not wrapped Go
// errors (SendDaemonCommand flattens every failure into one opaque error whose
// text is the only discriminator). So substring matching is the only available
// mechanism, exactly as daemonErrDirNotExistMarker does for pkg.*. These mirror
// the prefixes daemonruntime/cmd_forge.go emits; the two live in different
// packages because the proxy must not import the daemon runtime, which makes
// the error text the wire contract between them.
// -----------------------------------------------------------------------------
const (
	// forgeErrProjectDirNotExistMarker mirrors cmd_forge.go's
	// forgeProjectDirNotExistPrefix -> NotFound. A path that is GONE is a
	// missing resource, not an outage, and must not read as retryable.
	forgeErrProjectDirNotExistMarker = "forge project dir does not exist"

	// forgeErrCommandFailedMarker mirrors cmd_forge.go's
	// forgeCommandFailedPrefix: forge ran, exited non-zero, and produced no
	// report to interpret. A genuine internal failure — distinct from a
	// non-zero exit that DID produce a report, which the daemon returns as
	// data because that report is the answer.
	forgeErrCommandFailedMarker = "forge command failed"
)

// forgeProjectLookup is the only database capability this service needs: resolve
// a project id to its row, enforcing that the caller owns it.
//
// Declared here, at the CONSUMER, rather than depending on the whole
// db.Repository surface. One method is all this service uses, and a one-method
// dependency is what makes the handler testable without a database — which is
// what lets the secret-safety and reachability tests below run as plain unit
// tests. db.Repository satisfies this implicitly.
type forgeProjectLookup interface {
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
}

// ForgeService serves forge project state to the web tier by forwarding to the
// user's daemon.
type ForgeService struct {
	daemonProxyBase
	projects forgeProjectLookup
}

// NewForgeService creates a ForgeService bound to a daemon router and database.
func NewForgeService(router toolexec.DaemonRouter, projects forgeProjectLookup) *ForgeService {
	return &ForgeService{
		daemonProxyBase: daemonProxyBase{router: router},
		projects:        projects,
	}
}

// forgeReportReply mirrors daemonruntime.forgeReportResponse.
//
// Report is a raw string rather than a decoded structure: this layer never
// parses forge's document, so it has no reason to know its shape, and keeping
// it opaque means a newer forge that adds a field needs no change here. It is
// declared as string (not json.RawMessage) because it goes straight into the
// proto's report_json field.
type forgeReportReply struct {
	IsForgeProject    bool   `json:"is_forge_project"`
	Supported         bool   `json:"supported"`
	ForgeVersion      string `json:"forge_version"`
	UnsupportedReason string `json:"unsupported_reason"`
	ExitCode          int32  `json:"exit_code"`
	Report            string `json:"report"`
}

// forgeProjectPath resolves a project id to its path on the DAEMON's
// filesystem, enforcing that the caller owns it.
func (s *ForgeService) forgeProjectPath(ctx context.Context, projectID, userID string) (string, error) {
	if strings.TrimSpace(projectID) == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}
	project, err := s.projects.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "access denied") {
			return "", connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
		}
		return "", connect.NewError(connect.CodeInternal, fmt.Errorf("database error"))
	}
	return project.Path, nil
}

// forgeEnvArg validates an environment name.
func forgeEnvArg(env string) (string, error) {
	trimmed := strings.TrimSpace(env)
	if trimmed == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("env is required"))
	}
	return trimmed, nil
}

// -----------------------------------------------------------------------------
// UNREACHABLE IS DATA, NOT AN ERROR.
//
// Forge distinguishes "could not reach the cluster" (exit 2, UNREACHABLE) from
// "the release is wrong" (exit 1, DRIFT) on purpose: a VPN blip is not evidence
// of a release defect, and a check that cries wolf is a check that gets turned
// off. Preserving that across this boundary needs care in BOTH directions.
//
// Direction 1 — forge answered. Exit 2 arrives from the daemon as a normal
// response with a report. It is mapped to reachability=UNREACHABLE and returned
// as a SUCCESSFUL RPC. Failing the RPC here would flatten a considered verdict
// into the same shape as "the daemon broke".
//
// Direction 2 — nobody answered. Our own dispatch timed out on a command that
// reads a cluster. There is no report, but the CONCLUSION a user should draw is
// identical to exit 2: live state is unknown. So this also returns a successful
// RPC carrying UNREACHABLE with an empty report, and the UI renders "unknown"
// rather than a red banner. This is the case that would otherwise be
// indistinguishable from a genuine error — and misrendering it as one is how a
// slow cloud API server gets reported as a broken release.
//
// Everything that is NOT about cluster reachability still fails loudly: a
// missing project dir is NotFound, an unresponsive daemon on a local-only
// command is Unavailable, a malformed response is Internal.
// -----------------------------------------------------------------------------

// forgeDispatch runs one forge.* daemon command.
//
// readsCluster declares whether this invocation makes live cluster reads. It
// selects the timeout semantics above: when true, a timeout is a reachability
// verdict (unreachable=true, no error); when false, a timeout is a real failure
// and is returned as a Connect error.
func (s *ForgeService) forgeDispatch(
	ctx context.Context,
	userID, commandType string,
	payload any,
	timeoutMs int32,
	readsCluster bool,
) (reply forgeReportReply, unreachableReason string, err error) {
	if err := s.dispatch(ctx, userID, commandType, payload, &reply, timeoutMs); err != nil {
		if readsCluster && isForgeClusterReadTimeout(err) {
			// Nobody answered in time on a path whose whole job is reading a
			// remote cluster. Report UNKNOWN, not broken.
			return forgeReportReply{}, forgeTimeoutReason(commandType, timeoutMs), nil
		}
		// The shared mapper only recognises the pkg.* NotFound marker
		// ("working dir does not exist"), so forge's own missing-path prefix
		// would otherwise arrive as a retryable Unavailable — telling the UI to
		// keep retrying a directory that is gone. Re-map it here rather than
		// teaching the shared mapper every command family's prefix.
		if connect.CodeOf(err) == connect.CodeUnavailable &&
			strings.Contains(err.Error(), forgeErrProjectDirNotExistMarker) {
			return forgeReportReply{}, "", connect.NewError(connect.CodeNotFound,
				fmt.Errorf("forge project path not found on the daemon: %s", commandType))
		}
		return forgeReportReply{}, "", err
	}
	return reply, "", nil
}

// isForgeClusterReadTimeout reports whether a dispatch failure was a timeout or
// cancellation rather than a substantive failure.
//
// Deliberately narrow. A missing project directory, an unregistered command or
// a malformed reply are all real errors and must keep failing loudly — only a
// deadline, which carries no information about the cluster's actual state, is
// eligible to be reinterpreted as "unknown". Matching is by substring because
// SendDaemonCommand flattens its failures into opaque error text (see
// mapDaemonDispatchError).
func isForgeClusterReadTimeout(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())

	// Never reinterpret a substantive, separately-classified failure as a
	// reachability verdict, even if its text happens to mention a timeout
	// (the NATS error string embeds the timeout duration).
	if strings.Contains(text, strings.ToLower(forgeErrProjectDirNotExistMarker)) ||
		strings.Contains(text, strings.ToLower(forgeErrCommandFailedMarker)) {
		return false
	}

	for _, marker := range []string{
		"nats: timeout",             // NATS request deadline (distributed mode)
		"context deadline exceeded", // caller/dispatch deadline
		"deadlineexceeded",
		"timed out",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// forgeTimeoutReason renders the human-facing diagnostic for an unreachable
// verdict produced by our own timeout. Clients branch on the reachability enum;
// this string is for a person reading a tooltip.
func forgeTimeoutReason(commandType string, timeoutMs int32) string {
	return fmt.Sprintf("cluster read did not complete within %ds (%s); live state is unknown",
		timeoutMs/1000, commandType)
}

// forgeMeta converts a daemon reply into response metadata.
//
// readsCluster and unreachableReason together decide reachability:
//   - a non-empty unreachableReason means OUR dispatch timed out;
//   - forge exit code 2 means FORGE could not reach the cluster;
//   - otherwise, a cluster-reading command that produced a report observed it,
//     and a command that reads no cluster reports UNSPECIFIED (not applicable)
//     rather than OK, because claiming OK would imply an observation nobody made.
func forgeMeta(reply forgeReportReply, readsCluster bool, unreachableReason string) *reliantv1.ForgeReportMeta {
	meta := &reliantv1.ForgeReportMeta{
		IsForgeProject:    reply.IsForgeProject,
		Supported:         reply.Supported,
		ForgeVersion:      reply.ForgeVersion,
		UnsupportedReason: reply.UnsupportedReason,
		ExitCode:          reply.ExitCode,
		Reachability:      reliantv1.ForgeReachability_FORGE_REACHABILITY_UNSPECIFIED,
	}

	switch {
	case unreachableReason != "":
		meta.Reachability = reliantv1.ForgeReachability_FORGE_REACHABILITY_UNREACHABLE
		meta.UnreachableReason = unreachableReason
		// A dispatch timeout yields no forge version from the reply; stamp the
		// one this binary carries so the UI still has it.
		if meta.ForgeVersion == "" {
			meta.ForgeVersion = version.Forge()
		}
	case reply.ExitCode == forgeExitUnreachable:
		meta.Reachability = reliantv1.ForgeReachability_FORGE_REACHABILITY_UNREACHABLE
		meta.UnreachableReason = "forge could not reach the cluster (exit 2); live state is unknown"
	case readsCluster && reply.Supported:
		meta.Reachability = reliantv1.ForgeReachability_FORGE_REACHABILITY_OK
	}

	return meta
}

// forgeExitUnreachable is forge's exit code for "could not reach the cluster".
// Its sibling, exit 1, means drift or a missing secret — a substantive verdict
// about state that WAS observed, which is why only 2 maps to UNREACHABLE.
const forgeExitUnreachable = 2

// =============================================================================
// RPCs
// =============================================================================

// GetTopology returns the release/environment topology.
func (s *ForgeService) GetTopology(
	ctx context.Context,
	req *connect.Request[reliantv1.GetForgeTopologyRequest],
) (*connect.Response[reliantv1.GetForgeTopologyResponse], error) {
	userID, err := s.userID(ctx)
	if err != nil {
		return nil, err
	}
	path, err := s.forgeProjectPath(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, err
	}

	// --verify is what turns this from a local file read into a fleet of
	// cluster round trips, so it selects both the budget and whether a
	// timeout is a reachability verdict.
	verify := req.Msg.Verify
	timeout := int32(forgeTopologyTimeoutMs)
	if verify {
		timeout = forgeTopologyVerifyTimeoutMs
	}

	payload := struct {
		ProjectPath string   `json:"project_path"`
		Verify      bool     `json:"verify,omitempty"`
		Envs        []string `json:"envs,omitempty"`
	}{ProjectPath: path, Verify: verify, Envs: req.Msg.Envs}

	reply, unreachable, err := s.forgeDispatch(ctx, userID, "forge.topology", payload, timeout, verify)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.GetForgeTopologyResponse{
		Meta:       forgeMeta(reply, verify, unreachable),
		ReportJson: reply.Report,
	}), nil
}

// VerifyEnv reconciles one environment against its live cluster.
func (s *ForgeService) VerifyEnv(
	ctx context.Context,
	req *connect.Request[reliantv1.VerifyForgeEnvRequest],
) (*connect.Response[reliantv1.VerifyForgeEnvResponse], error) {
	userID, err := s.userID(ctx)
	if err != nil {
		return nil, err
	}
	env, err := forgeEnvArg(req.Msg.Env)
	if err != nil {
		return nil, err
	}
	path, err := s.forgeProjectPath(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, err
	}

	payload := struct {
		ProjectPath string `json:"project_path"`
		Env         string `json:"env"`
	}{ProjectPath: path, Env: env}

	reply, unreachable, err := s.forgeDispatch(ctx, userID, "forge.env_verify", payload, forgeEnvVerifyTimeoutMs, true)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.VerifyForgeEnvResponse{
		Meta:       forgeMeta(reply, true, unreachable),
		ReportJson: reply.Report,
	}), nil
}

// ListSecrets returns declared/present/inert secrets for an environment.
//
// SECRET-SAFETY IS THE POINT OF THIS METHOD'S SHAPE. `forge secret list --json`
// cannot emit a secret value (its report type graph holds only names, booleans
// and coordinates, pinned by a reflection test), and the daemon withholds
// forge's stderr entirely on this path so a diagnostic line cannot become the
// leak channel. This layer is the last link, so it holds the same line:
//
//   - the response body is NEVER logged, here or anywhere on this path — not at
//     debug level, not in an error, not in telemetry;
//   - no field is added beyond meta + forge's document, so there is nowhere to
//     put a value even by accident
//     (TestListForgeSecretsResponseCannotCarrySecretValues pins that);
//   - dispatch errors are returned as-is from the shared mapper and are built
//     from transport text, not from the report body.
func (s *ForgeService) ListSecrets(
	ctx context.Context,
	req *connect.Request[reliantv1.ListForgeSecretsRequest],
) (*connect.Response[reliantv1.ListForgeSecretsResponse], error) {
	userID, err := s.userID(ctx)
	if err != nil {
		return nil, err
	}
	env, err := forgeEnvArg(req.Msg.Env)
	if err != nil {
		return nil, err
	}
	path, err := s.forgeProjectPath(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, err
	}

	payload := struct {
		ProjectPath string `json:"project_path"`
		Env         string `json:"env"`
	}{ProjectPath: path, Env: env}

	// readsCluster=false: secret presence is resolved from the declaration and
	// the provider's metadata, not by reading cluster workloads, so a timeout
	// here is a real failure rather than a reachability verdict.
	reply, unreachable, err := s.forgeDispatch(ctx, userID, "forge.secret_list", payload, forgeSecretListTimeoutMs, false)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.ListForgeSecretsResponse{
		Meta:       forgeMeta(reply, false, unreachable),
		ReportJson: reply.Report,
	}), nil
}

// GetAudit returns the project audit rollup.
func (s *ForgeService) GetAudit(
	ctx context.Context,
	req *connect.Request[reliantv1.GetForgeAuditRequest],
) (*connect.Response[reliantv1.GetForgeAuditResponse], error) {
	userID, err := s.userID(ctx)
	if err != nil {
		return nil, err
	}
	path, err := s.forgeProjectPath(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, err
	}

	payload := struct {
		ProjectPath string `json:"project_path"`
	}{ProjectPath: path}

	// Audit is a static-analysis rollup over local files: no cluster reads, so
	// a timeout is a genuine failure.
	reply, unreachable, err := s.forgeDispatch(ctx, userID, "forge.audit", payload, forgeAuditTimeoutMs, false)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.GetForgeAuditResponse{
		Meta:       forgeMeta(reply, false, unreachable),
		ReportJson: reply.Report,
	}), nil
}

// GetEnvStatus returns runtime checks for an environment.
func (s *ForgeService) GetEnvStatus(
	ctx context.Context,
	req *connect.Request[reliantv1.GetForgeEnvStatusRequest],
) (*connect.Response[reliantv1.GetForgeEnvStatusResponse], error) {
	userID, err := s.userID(ctx)
	if err != nil {
		return nil, err
	}
	env, err := forgeEnvArg(req.Msg.Env)
	if err != nil {
		return nil, err
	}
	path, err := s.forgeProjectPath(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, err
	}

	payload := struct {
		ProjectPath string `json:"project_path"`
		Env         string `json:"env"`
	}{ProjectPath: path, Env: env}

	reply, unreachable, err := s.forgeDispatch(ctx, userID, "forge.env_status", payload, forgeEnvStatusTimeoutMs, true)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.GetForgeEnvStatusResponse{
		Meta:       forgeMeta(reply, true, unreachable),
		ReportJson: reply.Report,
	}), nil
}

// =============================================================================
// PROMOTE — the first state-changing path on this surface.
//
// Two methods, PlanPromote (read-only) and ApplyPromote (the write), and the
// split is the safety design rather than a naming preference. A single method
// with a `dry_run` bool would make FALSE — the destructive value — the default
// for any caller that omits the field: a client written before the field
// existed, a payload that loses it, a hand-built request. Two method names
// cannot fail that way. The destructive call is reachable only by naming it,
// and which one a user triggered is legible from the method in a log line.
//
// Neither method deploys, and nothing here may imply otherwise. Promote writes
// one pointer into the binding ledger; not one byte reaches a cluster until
// `forge env deploy` runs. forge says so in every plan document
// (ships_nothing, next_step) and both handlers pass those fields through
// verbatim in report_json.
//
// There is deliberately NO Deploy RPC. Deploying mutates a live cluster rather
// than a file in the repo — a different risk class, and a separate decision.
// =============================================================================

// forgePromoteReply is forge.promote_apply's daemon reply: the standard forge
// envelope plus the refusal the concurrency guard may produce.
//
// Refused is a pointer so "absent" is distinguishable from a zero-valued
// refusal. The daemon returns a refusal as a SUCCESSFUL response (it is
// structured data, and daemon errors cross as flat strings that would lose the
// binding it found); converting it into a failed RPC is this layer's job, where
// failing closed is what a browser client needs.
type forgePromoteReply struct {
	forgeReportReply
	Refused *struct {
		Reason                 string `json:"reason"`
		ExpectedCurrentRelease string `json:"expected_current_release"`
		ExpectedUnbound        bool   `json:"expected_unbound"`
		ActualBound            bool   `json:"actual_bound"`
		ActualCurrentRelease   string `json:"actual_current_release"`
		ActualPromotedAt       string `json:"actual_promoted_at"`
		Detail                 string `json:"detail"`
	} `json:"promote_refused"`
}

// forgeReleaseArg validates a release version argument.
func forgeReleaseArg(release string) (string, error) {
	trimmed := strings.TrimSpace(release)
	if trimmed == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("release is required"))
	}
	return trimmed, nil
}

// PlanPromote previews binding an environment to a release. WRITES NOTHING.
//
// `forge env promote --plan` computes the whole change set and stops before
// forge's only write, so this is safe and idempotent and needs no confirmation
// token — there is nothing to confirm. It is what a UI calls to render the diff
// before asking a human to approve it.
//
// readsCluster=false: the plan is local ledger files plus a git range walk. A
// timeout here is therefore a real failure, not a reachability verdict —
// claiming "cluster unknown" for a failed local read would be a diagnosis of
// the wrong thing.
func (s *ForgeService) PlanPromote(
	ctx context.Context,
	req *connect.Request[reliantv1.PlanForgePromoteRequest],
) (*connect.Response[reliantv1.PlanForgePromoteResponse], error) {
	userID, err := s.userID(ctx)
	if err != nil {
		return nil, err
	}
	env, err := forgeEnvArg(req.Msg.Env)
	if err != nil {
		return nil, err
	}
	release, err := forgeReleaseArg(req.Msg.Release)
	if err != nil {
		return nil, err
	}
	path, err := s.forgeProjectPath(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, err
	}

	payload := struct {
		ProjectPath string `json:"project_path"`
		Env         string `json:"env"`
		Release     string `json:"release"`
	}{ProjectPath: path, Env: env, Release: release}

	reply, unreachable, err := s.forgeDispatch(
		ctx, userID, "forge.promote_plan", payload, forgePromotePlanTimeoutMs, false)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.PlanForgePromoteResponse{
		Meta:       forgeMeta(reply, false, unreachable),
		ReportJson: reply.Report,
	}), nil
}

// ApplyPromote WRITES the binding, guarded by the caller's view of current
// state.
//
// The confirmation token is expected_current_release (or expect_unbound for a
// first promote). The daemon re-plans, compares, and writes only if the ledger
// still holds what the caller said it last saw. A disagreement means someone
// promoted in between, and it matters because a forge EnvBinding keeps no
// history: {release, resolved, promoted_at}, overwritten in place, with nothing
// to undo from except git.
//
// A REFUSAL FAILS THE RPC. The daemon reports it as structured data on a
// successful response, and this is where that becomes a FailedPrecondition
// carrying a ForgePromoteRefusal detail. The reason is fail-closed behaviour: a
// refusal returned as a 200 with a nullable field is safe only if every client
// remembers to check it, and a client that forgets tells a user the promote
// worked. On the error path the default behaviour of every client is to treat it
// as a failure, and the structured facts still survive in the detail.
func (s *ForgeService) ApplyPromote(
	ctx context.Context,
	req *connect.Request[reliantv1.PromoteForgeEnvRequest],
) (*connect.Response[reliantv1.PromoteForgeEnvResponse], error) {
	userID, err := s.userID(ctx)
	if err != nil {
		return nil, err
	}
	env, err := forgeEnvArg(req.Msg.Env)
	if err != nil {
		return nil, err
	}
	release, err := forgeReleaseArg(req.Msg.Release)
	if err != nil {
		return nil, err
	}

	// The confirmation token must be an unambiguous claim about current state,
	// checked BEFORE the project lookup so an unauthorised request costs
	// nothing. An unset expected_current_release must never be readable as "I
	// saw nothing" — that is how an empty request would authorise a blind
	// overwrite of a bound environment.
	expected := strings.TrimSpace(req.Msg.ExpectedCurrentRelease)
	switch {
	case req.Msg.ExpectUnbound && expected != "":
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("expect_unbound and expected_current_release %q are contradictory: assert either "+
				"that the environment was unbound or which release it was bound to", expected))
	case !req.Msg.ExpectUnbound && expected == "":
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("expected_current_release is required (or set expect_unbound for a first promote): "+
				"promoting overwrites %q's binding irrecoverably, so the request must state the release it "+
				"expects to replace", env))
	}

	path, err := s.forgeProjectPath(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, err
	}

	payload := struct {
		ProjectPath            string `json:"project_path"`
		Env                    string `json:"env"`
		Release                string `json:"release"`
		ExpectedCurrentRelease string `json:"expected_current_release,omitempty"`
		ExpectUnbound          bool   `json:"expect_unbound,omitempty"`
	}{
		ProjectPath:            path,
		Env:                    env,
		Release:                release,
		ExpectedCurrentRelease: expected,
		ExpectUnbound:          req.Msg.ExpectUnbound,
	}

	// The apply reply has a field the shared forgeDispatch envelope does not
	// carry, so it is dispatched directly. Error mapping is reproduced rather
	// than shared: readsCluster is false here, which means the reachability
	// escape hatch does not apply at all, and this path must never reinterpret
	// a timeout as "unknown" — a promote that timed out may or may not have
	// written, and calling that a reachability verdict would hide it.
	var reply forgePromoteReply
	if err := s.dispatch(ctx, userID, "forge.promote_apply", payload, &reply, forgePromoteApplyTimeoutMs); err != nil {
		if connect.CodeOf(err) == connect.CodeUnavailable &&
			strings.Contains(err.Error(), forgeErrProjectDirNotExistMarker) {
			return nil, connect.NewError(connect.CodeNotFound,
				fmt.Errorf("forge project path not found on the daemon: forge.promote_apply"))
		}
		return nil, err
	}

	// THE GUARD REFUSED: nothing was written. Fail closed.
	if reply.Refused != nil {
		detail := &reliantv1.ForgePromoteRefusal{
			Reason:                 forgePromoteRefusalReason(reply.Refused.Reason),
			ExpectedCurrentRelease: reply.Refused.ExpectedCurrentRelease,
			ExpectedUnbound:        reply.Refused.ExpectedUnbound,
			ActualBound:            reply.Refused.ActualBound,
			ActualCurrentRelease:   reply.Refused.ActualCurrentRelease,
			ActualPromotedAt:       reply.Refused.ActualPromotedAt,
			Detail:                 reply.Refused.Detail,
		}

		connectErr := connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("promote refused, nothing was written: %s", reply.Refused.Detail))
		// The structured detail is what makes this recoverable without a blind
		// retry. If it cannot be attached the error still stands — failing
		// closed is the property that must not depend on the detail encoding.
		if errDetail, detailErr := connect.NewErrorDetail(detail); detailErr == nil {
			connectErr.AddDetail(errDetail)
		}
		return nil, connectErr
	}

	// Applied. report_json is the plan forge ACTUALLY applied, not the preview,
	// and it still carries ships_nothing/next_step — this promote moved a
	// pointer and deployed nothing.
	return connect.NewResponse(&reliantv1.PromoteForgeEnvResponse{
		Meta:       forgeMeta(reply.forgeReportReply, false, ""),
		ReportJson: reply.Report,
	}), nil
}

// forgePromoteRefusalReason maps the daemon's stable token onto the enum.
//
// An unrecognised token yields UNSPECIFIED rather than a guess, and the RPC
// still fails — a refusal this layer cannot classify is a refusal, not a
// success. That ordering is what keeps a newer daemon's additional reason from
// being laundered into an applied promote.
func forgePromoteRefusalReason(token string) reliantv1.ForgePromoteRefusalReason {
	switch token {
	case "stale_current_release":
		return reliantv1.ForgePromoteRefusalReason_FORGE_PROMOTE_REFUSAL_REASON_STALE_CURRENT_RELEASE
	default:
		return reliantv1.ForgePromoteRefusalReason_FORGE_PROMOTE_REFUSAL_REASON_UNSPECIFIED
	}
}

// =============================================================================
// DEPLOY — the only path on this surface that mutates a LIVE CLUSTER.
//
// Promote writes one pointer into a file in the repo; git can recover it. Deploy
// applies manifests to Kubernetes, and for a cloud environment nothing can. So
// this path is more conservative than promote's in three respects.
//
// THREE METHODS, NOT TWO, BECAUSE AN APPLY CANNOT BE SYNCHRONOUS. The daemon
// caps a single forge invocation at 2 minutes, a budget shared with five read
// commands. `forge env deploy` defaults to rollout mode `wait` with a FIVE-MINUTE
// budget PER RESOURCE, and a real environment has a dozen or more deployments.
// A synchronous RPC would have the child killed mid-rollout: a half-converged
// cluster, and an RPC reporting a timeout while the apply in fact landed — so
// nobody knows what shipped. Lowering forge's rollout budget to fit the cap would
// make forge stop watching before Kubernetes converges; raising the daemon's cap
// would change the budget for five commands that do not want it. So the daemon
// detaches the apply, StartDeploy returns a handle, and GetDeployStatus polls.
//
// THE CONFIRMATION TOKEN CARRIES THE CLUSTER, not just the release. That is the
// addition over promote, and the reason for it is specific: forge deploys to the
// context the env's KCL DECLARES and never to the ambient current-context, so the
// declared context is the only thing that decides where bytes land — and it comes
// from a file anyone can edit between a preview and a confirmation.
//
// TWO ESCAPE HATCHES ARE UNREACHABLE FROM HERE AND MUST STAY SO. Neither
// `--skip-preflight` nor `--no-digest` has a field on this surface. The preflight
// verifies referenced Secret keys and container images exist on the live target
// BEFORE anything is applied; digest pinning stops a re-tagged layer shipping in
// place of the bytes that were built. Both are for a human who has weighed the
// consequence, and an RPC field for either would be set once as a workaround and
// then stay set. TestForgeService_DeployRequestsCarryNoEscapeHatches pins it.
//
// The transport rule holds throughout: nothing here reads or reinterprets a
// rollout state. forge reports ready / failed / timed_out / not_waited, the last
// two being the ABSENCE of an answer, and they reach the caller as themselves.
// =============================================================================

// forgeDeployStartReply is forge.deploy_start's daemon reply: the standard forge
// envelope, plus the handle on a successful start or the refusal on a guarded
// one. Exactly one of the two is present.
//
// Refused is a pointer so "absent" is distinguishable from a zero-valued
// refusal. The daemon returns a refusal as a SUCCESSFUL response — it is
// structured data, and daemon errors cross as flat strings that would lose the
// cluster it found — and converting it into a failed RPC is this layer's job,
// where failing closed is what a browser client needs.
type forgeDeployStartReply struct {
	forgeReportReply

	Handle    string `json:"handle"`
	Env       string `json:"env"`
	JobStatus string `json:"job_status"`
	StartedAt string `json:"started_at"`

	Refused *struct {
		Reason string `json:"reason"`
		Detail string `json:"detail"`

		ExpectedDeclaredContext string `json:"expected_declared_context"`
		ActualDeclaredContext   string `json:"actual_declared_context"`

		ExpectedCurrentRelease string `json:"expected_current_release"`
		ExpectedUnbound        bool   `json:"expected_unbound"`
		ActualCurrentRelease   string `json:"actual_current_release"`
		ActualBound            bool   `json:"actual_bound"`

		GuardVerdict string `json:"guard_verdict"`
		GuardReason  string `json:"guard_reason"`
		GuardFix     string `json:"guard_fix"`

		RunningHandle string `json:"running_handle"`
	} `json:"deploy_refused"`
}

// forgeDeployStatusReply is forge.deploy_status's daemon reply.
type forgeDeployStatusReply struct {
	forgeReportReply

	Handle          string `json:"handle"`
	Env             string `json:"env"`
	JobStatus       string `json:"job_status"`
	JobStatusDetail string `json:"job_status_detail"`
	StartedAt       string `json:"started_at"`
	FinishedAt      string `json:"finished_at"`
}

// forgeDeployJobStatus maps the daemon's token onto the enum.
//
// AN UNRECOGNISED TOKEN BECOMES UNKNOWN, NEVER COMPLETED. That direction is the
// whole safety property of this function: a newer daemon's additional status
// decoded as "completed" would report a deploy of indeterminate outcome as one
// that finished, which is the green-deploy-over-a-broken-environment failure in a
// different costume. UNSPECIFIED is not used as the fallback either, because a
// client is entitled to treat UNSPECIFIED as "the server did not say" and this
// server did say — it said something this binary cannot classify, and the honest
// classification of that is UNKNOWN.
func forgeDeployJobStatus(token string) reliantv1.ForgeDeployJobStatus {
	switch token {
	case "running":
		return reliantv1.ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_RUNNING
	case "completed":
		return reliantv1.ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_COMPLETED
	case "failed":
		return reliantv1.ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_FAILED
	case "unknown":
		return reliantv1.ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_UNKNOWN
	case "":
		return reliantv1.ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_UNSPECIFIED
	default:
		return reliantv1.ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_UNKNOWN
	}
}

// forgeDeployRefusalReason maps the daemon's stable token onto the enum.
//
// An unrecognised token yields UNSPECIFIED and the RPC still fails — a refusal
// this layer cannot classify is a refusal, not a started deploy. That ordering is
// what keeps a newer daemon's additional reason from being laundered into a
// deploy that never began.
func forgeDeployRefusalReason(token string) reliantv1.ForgeDeployRefusalReason {
	switch token {
	case "stale_declared_context":
		return reliantv1.ForgeDeployRefusalReason_FORGE_DEPLOY_REFUSAL_REASON_STALE_DECLARED_CONTEXT
	case "stale_current_release":
		return reliantv1.ForgeDeployRefusalReason_FORGE_DEPLOY_REFUSAL_REASON_STALE_CURRENT_RELEASE
	case "guard_refused":
		return reliantv1.ForgeDeployRefusalReason_FORGE_DEPLOY_REFUSAL_REASON_GUARD_REFUSED
	case "already_running":
		return reliantv1.ForgeDeployRefusalReason_FORGE_DEPLOY_REFUSAL_REASON_ALREADY_RUNNING
	default:
		return reliantv1.ForgeDeployRefusalReason_FORGE_DEPLOY_REFUSAL_REASON_UNSPECIFIED
	}
}

// PlanDeploy previews a deploy. APPLIES NOTHING.
//
// `forge env deploy <env> --dry-run` renders the env, evaluates the
// declared-cluster guard, runs the preflight against the live target and returns
// before the first kubectl apply. Read-only and idempotent, so it needs no
// confirmation token — there is nothing to confirm. It is what a UI calls to put
// the cluster name, the namespace, the image pinning and the resource list in
// front of a human before asking them to approve it.
//
// readsCluster=true, which is the difference from PlanPromote: the preflight
// really does read Secrets and image metadata off the live target, so a timeout
// here means live state is UNKNOWN rather than that something broke.
func (s *ForgeService) PlanDeploy(
	ctx context.Context,
	req *connect.Request[reliantv1.PlanForgeDeployRequest],
) (*connect.Response[reliantv1.PlanForgeDeployResponse], error) {
	userID, err := s.userID(ctx)
	if err != nil {
		return nil, err
	}
	env, err := forgeEnvArg(req.Msg.Env)
	if err != nil {
		return nil, err
	}
	path, err := s.forgeProjectPath(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, err
	}

	payload := struct {
		ProjectPath string `json:"project_path"`
		Env         string `json:"env"`
	}{ProjectPath: path, Env: env}

	reply, unreachable, err := s.forgeDispatch(
		ctx, userID, "forge.deploy_plan", payload, forgeDeployPlanTimeoutMs, true)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.PlanForgeDeployResponse{
		Meta:       forgeMeta(reply, true, unreachable),
		ReportJson: reply.Report,
	}), nil
}

// StartDeploy begins a real deploy as a background job and returns its handle.
//
// The confirmation token is expected_declared_context plus either
// expected_current_release or expect_unbound. The daemon re-plans, compares, and
// starts nothing unless every claim still holds. Both halves are validated here
// BEFORE the project lookup so an unauthorised request costs nothing — and the
// validation is duplicated on the daemon rather than trusted from here, because
// the daemon's command must not have an unguarded spelling.
//
// A REFUSAL FAILS THE RPC with FailedPrecondition carrying a ForgeDeployRefusal
// detail. The daemon reports it as data on a successful response; this is where
// that becomes an error, because a refusal returned as a 200 with a nullable
// field is safe only if every client remembers to check it — and a client that
// forgets shows a progress spinner for a deploy that was never started.
//
// THIS RPC RETURNING OK MEANS A DEPLOY IS IN FLIGHT. It emphatically does not
// mean one succeeded; nothing is known about the outcome yet. Poll
// GetDeployStatus.
func (s *ForgeService) StartDeploy(
	ctx context.Context,
	req *connect.Request[reliantv1.StartForgeDeployRequest],
) (*connect.Response[reliantv1.StartForgeDeployResponse], error) {
	userID, err := s.userID(ctx)
	if err != nil {
		return nil, err
	}
	env, err := forgeEnvArg(req.Msg.Env)
	if err != nil {
		return nil, err
	}

	// THE CLUSTER CLAIM IS MANDATORY, and there is no spelling of this request
	// that means "I did not populate it". A deploy whose target cluster the
	// operator never saw is the accident this whole path is built around.
	expectedContext := strings.TrimSpace(req.Msg.ExpectedDeclaredContext)
	if expectedContext == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("expected_declared_context is required: deploying to %q applies manifests to a "+
				"live cluster, and forge applies to the context the environment DECLARES — which can "+
				"change between a preview and this confirmation — so the request must state the "+
				"cluster the operator saw named", env))
	}

	// And the release claim, in the same three-way shape promote uses: an unset
	// expected_current_release must never be readable as "I saw no binding".
	expectedRelease := strings.TrimSpace(req.Msg.ExpectedCurrentRelease)
	switch {
	case req.Msg.ExpectUnbound && expectedRelease != "":
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("expect_unbound and expected_current_release %q are contradictory: assert "+
				"either that the environment had no release binding or which release it was bound to",
				expectedRelease))
	case !req.Msg.ExpectUnbound && expectedRelease == "":
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("expected_current_release is required (or set expect_unbound for an "+
				"environment with no binding): a deploy of %q ships the bound release's pinned "+
				"digests, so the request must state the release it expects to ship", env))
	}

	path, err := s.forgeProjectPath(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, err
	}

	payload := struct {
		ProjectPath             string `json:"project_path"`
		Env                     string `json:"env"`
		ExpectedDeclaredContext string `json:"expected_declared_context"`
		ExpectedCurrentRelease  string `json:"expected_current_release,omitempty"`
		ExpectUnbound           bool   `json:"expect_unbound,omitempty"`
	}{
		ProjectPath:             path,
		Env:                     env,
		ExpectedDeclaredContext: expectedContext,
		ExpectedCurrentRelease:  expectedRelease,
		ExpectUnbound:           req.Msg.ExpectUnbound,
	}

	// Dispatched directly rather than through forgeDispatch: the reply carries
	// fields the shared envelope does not, and — more importantly — the
	// reachability escape hatch must NOT apply here. A start that timed out may
	// or may not have got a deploy underway, and reporting that as
	// "cluster unknown" would file a possibly-live deploy under a diagnosis of
	// the network.
	var reply forgeDeployStartReply
	if err := s.dispatch(ctx, userID, "forge.deploy_start", payload, &reply, forgeDeployStartTimeoutMs); err != nil {
		if connect.CodeOf(err) == connect.CodeUnavailable &&
			strings.Contains(err.Error(), forgeErrProjectDirNotExistMarker) {
			return nil, connect.NewError(connect.CodeNotFound,
				fmt.Errorf("forge project path not found on the daemon: forge.deploy_start"))
		}
		return nil, err
	}

	// THE GUARD REFUSED: nothing was applied and no job exists. Fail closed.
	if reply.Refused != nil {
		detail := &reliantv1.ForgeDeployRefusal{
			Reason:                  forgeDeployRefusalReason(reply.Refused.Reason),
			Detail:                  reply.Refused.Detail,
			ExpectedDeclaredContext: reply.Refused.ExpectedDeclaredContext,
			ActualDeclaredContext:   reply.Refused.ActualDeclaredContext,
			ExpectedCurrentRelease:  reply.Refused.ExpectedCurrentRelease,
			ExpectedUnbound:         reply.Refused.ExpectedUnbound,
			ActualCurrentRelease:    reply.Refused.ActualCurrentRelease,
			ActualBound:             reply.Refused.ActualBound,
			GuardVerdict:            reply.Refused.GuardVerdict,
			GuardReason:             reply.Refused.GuardReason,
			GuardFix:                reply.Refused.GuardFix,
			RunningHandle:           reply.Refused.RunningHandle,
		}

		connectErr := connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("deploy refused, nothing was applied: %s", reply.Refused.Detail))
		// The structured detail is what makes this recoverable without a blind
		// retry against a cluster. If it cannot be attached the error still
		// stands — failing closed must not depend on the detail encoding.
		if errDetail, detailErr := connect.NewErrorDetail(detail); detailErr == nil {
			connectErr.AddDetail(errDetail)
		}
		return nil, connectErr
	}

	// A reply with neither a refusal nor a handle means the daemon answered
	// something this layer cannot act on — most plausibly a forge too old for
	// `env deploy --json`, which arrives as supported:false with no job. Report
	// it as the structured not-supported answer rather than handing back an empty
	// handle a client would poll forever.
	if strings.TrimSpace(reply.Handle) == "" {
		return connect.NewResponse(&reliantv1.StartForgeDeployResponse{
			Meta:       forgeMeta(reply.forgeReportReply, false, ""),
			Env:        env,
			JobStatus:  reliantv1.ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_UNSPECIFIED,
			ReportJson: reply.Report,
		}), nil
	}

	// Started. report_json is the GUARD PLAN this deploy was authorised against
	// — the read-only document, mode "dry_run" — not an apply report. No apply
	// report exists yet.
	return connect.NewResponse(&reliantv1.StartForgeDeployResponse{
		Meta:       forgeMeta(reply.forgeReportReply, false, ""),
		Handle:     reply.Handle,
		Env:        env,
		JobStatus:  forgeDeployJobStatus(reply.JobStatus),
		StartedAt:  reply.StartedAt,
		ReportJson: reply.Report,
	}), nil
}

// GetDeployStatus polls a running or finished deploy by handle.
//
// readsCluster=false, and that is deliberate despite the deploy itself very much
// touching a cluster: this call reads one entry from the daemon's in-memory job
// registry and nothing else. Claiming "cluster unreachable" because a poll timed
// out would diagnose the wrong thing entirely — the cluster is not what failed to
// answer, the daemon is.
//
// A HANDLE THE DAEMON DOES NOT RECOGNISE IS NOT AN ERROR. The registry is
// in-memory, so a daemon that restarted mid-apply has lost the handle for a
// deploy that may well have landed. The daemon answers that poll with job_status
// UNKNOWN and a detail saying so, and this layer passes it through as a
// successful RPC. Failing the RPC instead would render "something broke" over a
// cluster that is converging; reporting FAILED would invite a retry.
func (s *ForgeService) GetDeployStatus(
	ctx context.Context,
	req *connect.Request[reliantv1.GetForgeDeployStatusRequest],
) (*connect.Response[reliantv1.GetForgeDeployStatusResponse], error) {
	userID, err := s.userID(ctx)
	if err != nil {
		return nil, err
	}
	handle := strings.TrimSpace(req.Msg.Handle)
	if handle == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("handle is required"))
	}
	// The project is resolved even though the daemon keys jobs by handle alone:
	// it is what enforces that the caller owns the project whose daemon is being
	// asked, and it is how the request reaches the right daemon at all.
	if _, err := s.forgeProjectPath(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	payload := struct {
		Handle string `json:"handle"`
	}{Handle: handle}

	var reply forgeDeployStatusReply
	if err := s.dispatch(ctx, userID, "forge.deploy_status", payload, &reply, forgeDeployStatusTimeoutMs); err != nil {
		if connect.CodeOf(err) == connect.CodeUnavailable &&
			strings.Contains(err.Error(), forgeErrProjectDirNotExistMarker) {
			return nil, connect.NewError(connect.CodeNotFound,
				fmt.Errorf("forge project path not found on the daemon: forge.deploy_status"))
		}
		return nil, err
	}

	return connect.NewResponse(&reliantv1.GetForgeDeployStatusResponse{
		Meta:            forgeMeta(reply.forgeReportReply, false, ""),
		Handle:          reply.Handle,
		Env:             reply.Env,
		JobStatus:       forgeDeployJobStatus(reply.JobStatus),
		JobStatusDetail: reply.JobStatusDetail,
		StartedAt:       reply.StartedAt,
		FinishedAt:      reply.FinishedAt,
		ReportJson:      reply.Report,
	}), nil
}
