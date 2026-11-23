// Copyright (c) 2025 Reliant Labs
package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/daemoncmd"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
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
	// forgeTopologyTimeoutMs — no --verify, so no workload reads, but NOT the
	// file read it once was. `forge env status --json` renders EVERY declared
	// environment's KCL to resolve where it runs, and asks the hosted control
	// plane for a hosted env's status (forge's client allows that call 30s).
	// Measured on control-plane (four envs, one hosted) on a loaded cloud
	// machine: 26–30s. The old 15s budget killed every run, and both daemon
	// tabs of the env page gate on this call (prod, 2026-10-15). 60s is twice
	// the measurement and half the daemon's cap.
	forgeTopologyTimeoutMs = 60_000

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

	// forgeEnvStatusTimeoutMs — one environment's runtime AND release halves.
	// The argv is the same `env status <env> --json` that forge.env_verify
	// runs (forge v0.1.42 folded verify into status), so it gets the same
	// budget: above forge's own 60s release-half timeout, so forge's
	// UNREACHABLE verdict wins over ours. Measured on control-plane's prod:
	// 24–51s on a loaded machine, past the 45s this used to be.
	forgeEnvStatusTimeoutMs = forgeEnvVerifyTimeoutMs

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

// forgeRPCHeadroom is how much longer the HTTP request may live than the
// daemon dispatch it carries. The handler's OWN budget must fire first: it is
// what turns a slow cluster read into a classified UNREACHABLE answer, whereas
// the request deadline firing first just cancels the context and the user sees
// a bare DeadlineExceeded with no meta at all.
const forgeRPCHeadroom = 5 * time.Second

// ForgeRPCDeadlines is the request deadline every ForgeService procedure needs,
// for the api-server's Deadline slot (interceptors.ProcedureTimeouts).
//
// WITHOUT THIS, EVERY BUDGET ABOVE WAS DEAD CODE. The interceptor's default is
// 10s and it had no forge entries, so `forge env status <env>` (about 5s cold on
// control-plane, much more under load) was cancelled at 10s regardless of its
// 45s budget — and because a cancelled cluster read is classified as
// UNREACHABLE with an empty reply, the web then rendered "this project has no
// forge.yaml" for a project that has one. Deriving the map from the same
// constants the handlers dispatch with is what keeps the two from drifting;
// TestForgeRPCDeadlinesCoverEveryProcedure pins it.
func ForgeRPCDeadlines() map[string]time.Duration {
	ms := func(budget int32) time.Duration {
		return time.Duration(budget)*time.Millisecond + forgeRPCHeadroom
	}
	return map[string]time.Duration{
		reliantv1connect.ForgeServiceGetTopologyProcedure:     ms(forgeTopologyVerifyTimeoutMs),
		reliantv1connect.ForgeServiceVerifyEnvProcedure:       ms(forgeEnvVerifyTimeoutMs),
		reliantv1connect.ForgeServiceListSecretsProcedure:     ms(forgeSecretListTimeoutMs),
		reliantv1connect.ForgeServiceGetEnvStatusProcedure:    ms(forgeEnvStatusTimeoutMs),
		reliantv1connect.ForgeServiceGetEnvShapeProcedure:     ms(forgeEnvStatusTimeoutMs),
		reliantv1connect.ForgeServicePlanPromoteProcedure:     ms(forgePromotePlanTimeoutMs),
		reliantv1connect.ForgeServiceApplyPromoteProcedure:    ms(forgePromoteApplyTimeoutMs),
		reliantv1connect.ForgeServicePlanDeployProcedure:      ms(forgeDeployPlanTimeoutMs),
		reliantv1connect.ForgeServiceStartDeployPlanProcedure: ms(forgeDeployStartTimeoutMs),
		reliantv1connect.ForgeServiceStartDeployProcedure:     ms(forgeDeployStartTimeoutMs),
		reliantv1connect.ForgeServiceGetDeployStatusProcedure: ms(forgeDeployStatusTimeoutMs),
	}
}

// -----------------------------------------------------------------------------
// Error-text markers.
//
// Daemon-command errors cross the transport as PLAIN STRINGS, not wrapped Go
// errors (SendDaemonCommand flattens every failure into one opaque error whose
// text is the only discriminator). So substring matching is the only available
// mechanism, exactly as daemonerr's dirNotExistMarker does for pkg.*. These mirror
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
	// forgeCommandFailedPrefix -> FailedPrecondition: forge ran, exited
	// non-zero, and produced no report to interpret — distinct from a non-zero
	// exit that DID produce a report, which the daemon returns as data because
	// that report is the answer. See remapForgeDispatchError for the code.
	forgeErrCommandFailedMarker = "forge command failed"

	// forgeErrCommandTimedOutMarker mirrors cmd_forge.go's
	// forgeCommandTimedOutPrefix: the daemon stopped forge at this command's
	// budget, before it answered. A timeout, never a forge failure — on a
	// cluster read it is UNREACHABLE data (isForgeClusterReadTimeout matches
	// its "timed out"), elsewhere DeadlineExceeded (remapForgeDispatchError).
	forgeErrCommandTimedOutMarker = "forge command timed out"
)

// daemon forwards a forge.* command to the machine holding the project's
// checkout (forgeTarget), mapping every failure onto a loud Connect error
// (daemoncmd).
func (s *Service) daemon(t projectcheckout.Target) daemoncmd.Service {
	return daemoncmd.NewInstrumented(daemoncmd.Deps{Sender: t.Pinned(s.deps.Router)})
}

// forgeReportReply mirrors daemonruntime.forgeReportResponse.
//
// Report is json.RawMessage, and it MUST match the daemon's field type rather
// than the proto's. The daemon sends forge's document verbatim as a JSON
// OBJECT (daemonruntime.forgeReportResponse.Report is json.RawMessage); a
// `string` here made every forge.* RPC fail at the decode with
//
//	cannot unmarshal object into Go struct field forgeReportReply.report of type string
//
// which surfaced as the forge UI silently not appearing: the sidebar entry is
// gated on GetTopology succeeding, so a decode failure reads as "not a forge
// project" rather than as an error anyone sees.
//
// It stays OPAQUE — this layer never parses forge's document, so a newer forge
// that adds a field needs no change here. RawMessage preserves that while
// being the type the wire actually carries; reportJSON() renders it for the
// proto's string field.
type forgeReportReply struct {
	IsForgeProject    bool            `json:"is_forge_project"`
	Supported         bool            `json:"supported"`
	ForgeVersion      string          `json:"forge_version"`
	UnsupportedReason string          `json:"unsupported_reason"`
	ExitCode          int32           `json:"exit_code"`
	Report            json.RawMessage `json:"report"`
}

// reportJSON renders the opaque report for the proto's report_json string
// field. Empty (not "null") when forge returned no document, so a caller
// checking for emptiness does not have to special-case JSON's null literal.
func (r forgeReportReply) reportJSON() string {
	if len(r.Report) == 0 || string(r.Report) == "null" {
		return ""
	}
	return string(r.Report)
}

// forgePurpose finishes projectcheckout's "clone it onto a machine to …".
const forgePurpose = "use forge with it"

// forgeTarget resolves a project id to the machine a forge.* command runs on
// and the project's path THERE (projectcheckout.Resolve), enforcing that the
// caller owns the project. Every command is then pinned to that machine
// (daemon), so the path always names a directory on the machine that
// receives it — never projects.path, which is the creating machine's.
func (s *Service) forgeTarget(ctx context.Context, projectID, userID string) (projectcheckout.Target, error) {
	project, err := s.forgeProject(ctx, projectID, userID)
	if err != nil {
		return projectcheckout.Target{}, err
	}
	return projectcheckout.Resolve(ctx, s.deps.Repo, s.deps.Router, userID, project, forgePurpose)
}

// forgeProject resolves a project id to its row, enforcing that the caller
// owns it.
func (s *Service) forgeProject(ctx context.Context, projectID, userID string) (*db.Project, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	project, err := s.deps.Projects.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		// The store's NotFound (core.ErrProjectNotFound) covers a project that
		// does not exist and one that is someone else's alike.
		return nil, svcerr.WrapOr(err, svcerr.Internal("database error"))
	}
	return project, nil
}

// forgeEnvArg validates an environment name.
func forgeEnvArg(env string) (string, error) {
	trimmed := strings.TrimSpace(env)
	if trimmed == "" {
		return "", svcerr.InvalidArgument("env is required")
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
func (s *Service) forgeDispatch(
	ctx context.Context,
	userID string,
	target projectcheckout.Target,
	commandType string,
	payload any,
	timeoutMs int32,
	readsCluster bool,
) (reply forgeReportReply, unreachableReason string, err error) {
	if err := s.daemon(target).Dispatch(ctx, userID, commandType, payload, &reply, timeoutMs); err != nil {
		if readsCluster && isForgeClusterReadTimeout(err) {
			// Nobody answered in time on a path whose whole job is reading a
			// remote cluster. Report UNKNOWN, not broken.
			return forgeReportReply{}, forgeTimeoutReason(commandType, timeoutMs), nil
		}
		return forgeReportReply{}, "", s.dispatchError(ctx, target, commandType, err)
	}
	return reply, "", nil
}

// dispatchError maps a failed forge.* dispatch: a checkout the machine no
// longer has is said in terms of that machine (projectcheckout.GoneError);
// anything else is remapForgeDispatchError's.
func (s *Service) dispatchError(ctx context.Context, target projectcheckout.Target, commandType string, err error) error {
	if strings.Contains(forgeDaemonText(err), forgeErrProjectDirNotExistMarker) {
		return projectcheckout.GoneError(ctx, s.deps.Repo, target, err)
	}
	return remapForgeDispatchError(commandType, err)
}

// remapForgeDispatchError reclassifies the two forge-specific failures the
// shared mapper cannot recognise, and which it therefore files under a
// retryable Unavailable:
//
//   - forgeErrProjectDirNotExistMarker -> NotFound. The path is gone; retrying
//     a directory that no longer exists only delays saying so.
//   - forgeErrCommandFailedMarker -> FailedPrecondition. The daemon ANSWERED,
//     ran forge, and forge exited non-zero with no report — a KCL render
//     error, a provider the command refuses, missing credentials. That is a
//     fact about the project's state, not a transport fault.
//
// WHY FailedPrecondition AND NOT Internal for the second. Both stop the retry,
// but Internal claims Reliant is broken: the web transport reports it to Sentry
// and a reader files a bug against us for what is almost always a project the
// user can fix. FailedPrecondition is gRPC's "do not retry until the system's
// state has been fixed", which is exactly the situation, and it is the same
// call daemonerr.Outdated makes for version skew. Unavailable, the old
// answer, was the worst of the three: the UI read it as "your daemon is
// offline" and retried a failure that retrying cannot change.
//
// It is applied in THIS package rather than taught to daemonerr
// because the markers are forge's wire contract with its own daemon commands
// (see the constants above); the shared mapper stays ignorant of every command
// family's prefixes. Every forge dispatch — the shared envelope and the three
// that dispatch directly — routes through here, so the classification cannot
// differ between RPCs of the same surface.
//
// The message is carried over verbatim from the shared mapper's: what a client
// reads of err (svcerr.ToConnect), which is the daemon's text and never the
// router's. On the secret path the daemon has already withheld forge's stderr,
// so re-classifying adds nothing to it. err stays the logged cause.
func remapForgeDispatchError(commandType string, err error) error {
	if svcerr.Code(err) != connect.CodeUnavailable {
		return err
	}
	text := forgeDaemonText(err)
	message := svcerr.ToConnect(err).Message()
	switch {
	case strings.Contains(text, forgeErrProjectDirNotExistMarker):
		return svcerr.WithCause(svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("forge project path not found on the daemon: %s", commandType)), err)
	case strings.Contains(text, forgeErrCommandTimedOutMarker):
		// The daemon answered: forge ran out of this command's budget. Not
		// an outage (Unavailable reads as "daemon offline") and not a
		// project defect (FailedPrecondition): a deadline.
		return svcerr.WithCause(svcerr.DeadlineExceeded(message), err)
	case strings.Contains(text, forgeErrCommandFailedMarker):
		return svcerr.WithCause(svcerr.FailedPrecondition(message), err)
	}
	return err
}

// forgeDaemonText is the text forge's markers are matched in: the daemon's own
// report when the router carried one (toolexec.DaemonReportedText), else err's
// text. The markers are the daemon's words, so they survive either way, and
// the router's words around them never decide a class.
func forgeDaemonText(err error) string {
	if text, ok := toolexec.DaemonReportedText(err); ok {
		return text
	}
	return err.Error()
}

// isForgeClusterReadTimeout reports whether a dispatch failure was a timeout or
// cancellation rather than a substantive failure.
//
// Deliberately narrow. A missing project directory, an unregistered command or
// a malformed reply are all real errors and must keep failing loudly — only a
// deadline, which carries no information about the cluster's actual state, is
// eligible to be reinterpreted as "unknown".
//
// The router's timeout is recognised by its sentinel (toolexec.ErrDaemonTimeout:
// the machine did not answer before the NATS deadline) and by the code the
// dispatch gave it, never by the router's words: daemonerr withholds those from
// the error's text. The daemon's own words still decide the rest — forge's
// "forge command timed out", or a deadline it reported — because
// SendDaemonCommand carries a daemon's failure as text only.
func isForgeClusterReadTimeout(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(forgeDaemonText(err))

	// Never reinterpret a substantive, separately-classified failure as a
	// reachability verdict, even if its text happens to mention a timeout
	// (the NATS error string embeds the timeout duration).
	if strings.Contains(text, strings.ToLower(forgeErrProjectDirNotExistMarker)) ||
		strings.Contains(text, strings.ToLower(forgeErrCommandFailedMarker)) {
		return false
	}

	if toolexec.IsDaemonTimeout(err) || errors.Is(err, context.DeadlineExceeded) ||
		svcerr.Code(err) == connect.CodeDeadlineExceeded {
		return true
	}
	for _, marker := range []string{
		"context deadline exceeded", // a deadline the daemon reported
		"deadlineexceeded",
		"timed out", // forgeErrCommandTimedOutMarker, and forge's own timeouts
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

// forgeReleaseArg validates a release version argument.
func forgeReleaseArg(release string) (string, error) {
	trimmed := strings.TrimSpace(release)
	if trimmed == "" {
		return "", svcerr.InvalidArgument("release is required")
	}
	return trimmed, nil
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

	Refused *forgeDeployRefusalReply `json:"deploy_refused"`
}

// forgeDeployRefusalReply is the daemon's refusal, as it crosses the wire.
//
// Named rather than inline so both start paths can hand it to one mapper. An
// inline struct would force a second copy of the mapping at the second call
// site, and the second copy is the one that drifts.
type forgeDeployRefusalReply struct {
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
	// forge's own tokens, for a refusal it raised after recomputing the plan.
	case "plan_stale":
		return reliantv1.ForgeDeployRefusalReason_FORGE_DEPLOY_REFUSAL_REASON_PLAN_STALE
	case "plan_unacknowledged":
		return reliantv1.ForgeDeployRefusalReason_FORGE_DEPLOY_REFUSAL_REASON_PLAN_UNACKNOWLEDGED
	default:
		return reliantv1.ForgeDeployRefusalReason_FORGE_DEPLOY_REFUSAL_REASON_UNSPECIFIED
	}
}

// forgeDeployRefusedError turns a daemon refusal into the FailedPrecondition
// error every client fails closed on, carrying the structured facts as a detail.
//
// A REFUSAL IS AN ERROR AND NOT A FIELD. The daemon reports it as data on a
// successful response; this is where that becomes an error, because a refusal
// returned as a 200 with a nullable field is safe only if every client
// remembers to check it — and a client that forgets shows a progress spinner
// for a deploy that was never started.
//
// Shared by StartDeploy and StartDeployPlan: the two refuse for the same
// reasons (the target token did not hold, a job is already in flight) and a
// second copy would be the one that fell behind.
func forgeDeployRefusedError(refused *forgeDeployRefusalReply) error {
	detail := &reliantv1.ForgeDeployRefusal{
		Reason:                  forgeDeployRefusalReason(refused.Reason),
		Detail:                  refused.Detail,
		ExpectedDeclaredContext: refused.ExpectedDeclaredContext,
		ActualDeclaredContext:   refused.ActualDeclaredContext,
		ExpectedCurrentRelease:  refused.ExpectedCurrentRelease,
		ExpectedUnbound:         refused.ExpectedUnbound,
		ActualCurrentRelease:    refused.ActualCurrentRelease,
		ActualBound:             refused.ActualBound,
		GuardVerdict:            refused.GuardVerdict,
		GuardReason:             refused.GuardReason,
		GuardFix:                refused.GuardFix,
		RunningHandle:           refused.RunningHandle,
	}

	// The structured detail is what makes this recoverable without a blind
	// retry against a cluster. If it cannot be attached the error still
	// stands — failing closed must not depend on the detail encoding
	// (svcerr.WithDetail attaches it best-effort).
	return svcerr.WithDetail(svcerr.FailedPrecondition(fmt.Sprintf("deploy refused, nothing was applied: %s", refused.Detail)), detail)
}
