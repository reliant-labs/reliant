// Copyright (c) 2025 Reliant Labs

/**
 * ForgeService's frontend model. Pure: no React, no client. The reads and
 * writes are the generated hooks, composed in `forge-queries.ts`; this module
 * is what their reads `select` through, what their writes' requests are built
 * from, and how a write's refusal becomes data.
 *
 * Each response is CLASSIFIED into a ForgeOutcome — see
 * services/forge/topology.ts. Doing that here rather than in a component means
 * the five non-error outcomes (not a forge project, forge too old, cluster
 * unreachable, malformed body, a real report) are resolved once, in one place,
 * and every caller has to handle them by construction instead of destructuring
 * `report_json` and hoping.
 *
 * Note what this module does NOT do: it does not interpret forge's verdicts.
 * Forge owns what counts as drift and when an environment is healthy; the
 * api-server passes the document through verbatim precisely so there is no
 * second opinion, and adding one here would recreate the disagreement that
 * design avoids.
 */

import type { MessageInitShape } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  ForgeDeployJobStatus,
  ForgeDeployRefusalReason,
  ForgeDeployRefusalSchema,
  ForgePromoteRefusalReason,
  ForgePromoteRefusalSchema,
  type GetForgeDeployStatusResponse,
  type GetForgeEnvShapeResponse,
  type GetForgeEnvStatusResponse,
  type GetForgeTopologyResponse,
  type PlanForgeDeployResponse,
  type PlanForgePromoteResponse,
  type PromoteForgeEnvRequestSchema,
  type PromoteForgeEnvResponse,
  type StartForgeDeployRequestSchema,
  type StartForgeDeployResponse,
  type VerifyForgeEnvResponse,
} from "../gen/reliant/v1/forge_pb";
import type {
  DeployConfirmationToken,
  DeployJobDisposition,
  DeployRefusal,
  DeployRefusalReason,
  ForgeDeployReport,
} from "../services/forge/deploy";
import type { DeployPlanReport, PlanApproval } from "../services/forge/deployPlan";
import type { ForgePromotePlan, PromoteConfirmationToken, PromoteRefusal } from "../services/forge/promote";
import type { ForgeEnvShapeReport } from "../services/forge/register";
import { normalizeEnvStatus, type ForgeEnvStatusReport } from "../services/forge/status";
import {
  classifyForgeResponse,
  mergeVerifyIntoTopology,
  type ForgeOutcome,
  type ForgeTopologyReport,
  type ForgeVerifyReport,
} from "../services/forge/topology";

// ── Retry policy ────────────────────────────────────────────────────────────

/**
 * Codes on which asking the daemon again cannot change the answer.
 *
 * FailedPrecondition is the one that matters: it is what the api-server now
 * answers when forge RAN and failed (a KCL render error, a provider the
 * command refuses) and when the daemon is too old for the command. Both are
 * facts about state a retry does not touch, and retrying them only delays the
 * sentence that says what to fix.
 *
 * DeadlineExceeded is a forge command that ran out of its budget — up to a
 * minute or more of forge on the user's machine. A second identical request
 * costs the same and almost always ends the same way, doubling the wait before
 * the tab can say so.
 */
const NON_RETRYABLE_FORGE_CODES: ReadonlySet<Code> = new Set([
  Code.DeadlineExceeded,
  Code.FailedPrecondition,
  Code.NotFound,
  Code.InvalidArgument,
  Code.PermissionDenied,
  Code.Unauthenticated,
]);

/**
 * forgeRetry: at most ONE retry, and none for an answer. A thrown error on a
 * forge query is a transport or daemon problem (every meaningful non-success
 * arrives as data — see the useForgeTopology comment), and hammering a daemon
 * that is not answering only delays telling the user.
 */
export function forgeRetry(failureCount: number, error: unknown): boolean {
  if (error instanceof ConnectError && NON_RETRYABLE_FORGE_CODES.has(error.code)) return false;
  return failureCount < 1;
}

// ── Reads: what each one `select`s through ──────────────────────────────────
//
// Module-level functions, not closures, so React Query re-runs a select only
// when the cached response changes: a topology report is a large document, and
// every forge screen observes it.

export function topologyOutcomeOf(res: GetForgeTopologyResponse): ForgeOutcome<ForgeTopologyReport> {
  return classifyForgeResponse<ForgeTopologyReport>(res.meta, res.reportJson);
}

export function verifyOutcomeOf(res: VerifyForgeEnvResponse): ForgeOutcome<ForgeVerifyReport> {
  return classifyForgeResponse<ForgeVerifyReport>(res.meta, res.reportJson);
}

/**
 * One environment's status with forge's `runtime` block lifted to the top
 * level — see normalizeEnvStatus for why.
 */
export function envStatusOutcomeOf(res: GetForgeEnvStatusResponse): ForgeOutcome<ForgeEnvStatusReport> {
  const outcome = classifyForgeResponse<Record<string, unknown>>(res.meta, res.reportJson);
  if (outcome.kind !== "report") return outcome;
  return { ...outcome, report: normalizeEnvStatus(outcome.report) };
}

/**
 * Forge's own projection of ONE environment's render —
 * `{project, env, kind, shape, provenance}`, the same projection
 * `forge env build` records. `shape.secrets[]` carries names and providers
 * only (F-13); there is no field on this path that could hold a value.
 */
export function envShapeOutcomeOf(res: GetForgeEnvShapeResponse): ForgeOutcome<ForgeEnvShapeReport> {
  return classifyForgeResponse<ForgeEnvShapeReport>(res.meta, res.reportJson);
}

/** A promote preview. `dry_run` is true and `applied` false in every document it returns. */
export function promotePlanOutcomeOf(res: PlanForgePromoteResponse): ForgeOutcome<ForgePromotePlan> {
  return classifyForgeResponse<ForgePromotePlan>(res.meta, res.reportJson);
}

/** A deploy preview. Every document it returns has mode "dry_run". */
export function deployPlanOutcomeOf(res: PlanForgeDeployResponse): ForgeOutcome<ForgeDeployReport> {
  return classifyForgeResponse<ForgeDeployReport>(res.meta, res.reportJson);
}

/**
 * The cached topology RESPONSE with one environment's verification merged into
 * its report — see useVerifyForgeEnv for why the merge is this narrow.
 *
 * The cache holds the server's response and every reader selects through
 * topologyOutcomeOf, so the merge is written back as the response's
 * report_json. A response whose report did not classify as a report is
 * returned untouched: there is nothing to merge into.
 */
export function withVerifiedEnv(
  previous: GetForgeTopologyResponse,
  env: string,
  verify: ForgeVerifyReport
): GetForgeTopologyResponse {
  const outcome = topologyOutcomeOf(previous);
  if (outcome.kind !== "report") return previous;
  return { ...previous, reportJson: JSON.stringify(mergeVerifyIntoTopology(outcome.report, env, verify)) };
}

// ── Promote: the write, and its refusal as data ──────────────────────────────
//
// PlanPromote and ApplyPromote stay two clearly-named rpcs with two hooks. They
// are NOT unified behind one call with a `dryRun` flag, and the reason is the
// same one that shaped the proto: a boolean defaults to false, false would be
// the destructive value, and every call site that forgot the argument would
// write. There is a backend test that fails if anyone adds a dry_run field to
// the request; collapsing them on this side would reintroduce exactly what that
// test defends, one layer up.

/**
 * The outcome of an attempted write. A refusal is not an error to be shown as a
 * toast and it is not a success — it is its own third thing, so it is a variant
 * here rather than a rethrown ConnectError.
 */
export type ApplyPromoteResult =
  /** The binding was WRITTEN. The document is the plan forge actually applied. */
  | { kind: "applied"; outcome: ForgeOutcome<ForgePromotePlan> }
  /** The guard refused. NOTHING WAS WRITTEN and the binding moved underneath us. */
  | { kind: "refused"; refusal: PromoteRefusal };

/**
 * The ApplyPromote request. The token is a required argument of a type that
 * cannot express "neither claim" — see PromoteConfirmationToken — and the
 * caller cannot construct one except from a plan it rendered, because
 * confirmationTokenFor is the only producer.
 */
export function applyPromoteRequest(args: {
  projectId: string;
  env: string;
  release: string;
  token: PromoteConfirmationToken;
}): MessageInitShape<typeof PromoteForgeEnvRequestSchema> {
  return {
    projectId: args.projectId,
    env: args.env,
    release: args.release,
    // Exactly one of these is set, enforced by the token's type. The server
    // rejects neither-set as InvalidArgument and both-set as contradictory.
    expectedCurrentRelease: args.token.expectUnbound ? "" : args.token.expectedCurrentRelease,
    expectUnbound: args.token.expectUnbound === true,
  };
}

export function appliedPromoteOf(res: PromoteForgeEnvResponse): ApplyPromoteResult {
  return { kind: "applied", outcome: classifyForgeResponse<ForgePromotePlan>(res.meta, res.reportJson) };
}

/**
 * promoteRefusalOf pulls the structured refusal off a connect error.
 *
 * A refusal arrives as a FailedPrecondition carrying a ForgePromoteRefusal
 * detail, and the domain hook returns it as data. Letting it propagate as an
 * error would leave every call site to re-derive that nothing was written from
 * an error string; lifting it means the caller handles all three outcomes by
 * construction.
 *
 * THE DETAIL IS READ, NOT THE MESSAGE. The server's text is "promote refused,
 * nothing was written: <detail>", and matching on that string would break the
 * moment the wording is improved while silently downgrading a refusal to a
 * generic failure — which is the one presentation this outcome must never get.
 *
 * A FailedPrecondition whose detail cannot be decoded still returns null and so
 * still surfaces as an error. That is deliberate: the server attaches the detail
 * on a best-effort basis and fails closed without it, so a missing detail means
 * "refused but unexplained", and inventing an empty refusal panel would claim
 * facts about the binding that never arrived.
 */
export function promoteRefusalOf(error: unknown): PromoteRefusal | null {
  if (!(error instanceof ConnectError)) return null;
  if (error.code !== Code.FailedPrecondition) return null;

  for (const detail of error.findDetails(ForgePromoteRefusalSchema)) {
    return {
      reason:
        detail.reason === ForgePromoteRefusalReason.STALE_CURRENT_RELEASE
          ? "stale-current-release"
          : "unknown",
      expectedCurrentRelease: detail.expectedCurrentRelease,
      expectedUnbound: detail.expectedUnbound,
      actualBound: detail.actualBound,
      actualCurrentRelease: detail.actualCurrentRelease,
      actualPromotedAt: detail.actualPromotedAt,
      detail: detail.detail,
    };
  }
  return null;
}

// ── Deploy: three rpcs, and the naming is the guard ─────────────────────────
//
// PlanDeploy, StartDeploy and GetDeployStatus are three rpcs with three hooks.
// There is deliberately NO hook that wraps plan-then-start, and no `deploy()`
// of any kind: a single call that both previews and applies is the shape this
// whole path is built to make unavailable, and a backend reflection test
// (TestNoUnguardedDeployRPCExists) fails if a fourth deploy-ish method appears
// on the service. The write is StartDeploy — named for what it does, so it
// cannot be reached by autocompleting the generic verb.

/**
 * The outcome of an attempted start. A refusal is not an error to be shown as a
 * toast and it is emphatically not a started deploy — it is its own third thing,
 * so it is a variant here rather than a rethrown ConnectError.
 */
export type StartDeployResult =
  /**
   * A DEPLOY IS IN FLIGHT. This does not mean one succeeded — nothing is known
   * about the outcome yet. Poll the handle.
   */
  | {
      kind: "started";
      handle: string;
      env: string;
      startedAt: string;
      jobStatus: DeployJobDisposition;
      /**
       * The GUARD PLAN this deploy was authorised against (mode "dry_run"), or
       * null when the job is one that COMPUTES a plan rather than consuming one
       * — there is nothing to show yet, and a document here would be mistaken
       * for the answer.
       */
      guardPlan: ForgeOutcome<ForgeDeployReport> | null;
    }
  /** The guard refused. NOTHING WAS APPLIED and no job exists. */
  | { kind: "refused"; refusal: DeployRefusal }
  /**
   * The RPC returned without a handle and without a refusal — in practice a
   * pinned forge too old for the command. Nothing was started, and there is
   * nothing to poll.
   */
  | { kind: "not-started"; outcome: ForgeOutcome<ForgeDeployReport> };

/**
 * StartDeployPlan's answer. That rpc begins working out WHAT A DEPLOY WOULD
 * SHIP, as a background job: it builds from the chosen checkout, pushes, and
 * cuts a release, writes no promotion and applies nothing. Its result is NOT a
 * deploy report, and the status it is polled through marks that explicitly
 * (planOnly).
 */
export function startDeployPlanResultOf(res: StartForgeDeployResponse): StartDeployResult {
  if ((res.handle ?? "").trim() === "") {
    return {
      kind: "not-started",
      outcome: classifyForgeResponse<ForgeDeployReport>(res.meta, res.reportJson),
    };
  }
  return {
    kind: "started",
    handle: res.handle,
    env: res.env,
    startedAt: res.startedAt,
    jobStatus: deployJobDispositionOf(res.jobStatus),
    // No plan exists yet — that is what this job is computing. A guard plan
    // here would be a document the caller could mistake for the answer.
    guardPlan: null,
  };
}

/**
 * The StartDeploy request — A REAL DEPLOY TO A LIVE CLUSTER.
 *
 * The token is a required argument of a type that cannot express "no cluster
 * claim" or "neither release claim", and the caller cannot construct one except
 * from a plan it rendered, because deployTokenFor is the only producer. The
 * server re-plans and starts nothing unless every claim still holds.
 *
 * `approval` is THE APPROVED PLAN, derived from the plan document the operator
 * read — see DeployApproveStep, which is its only producer. The token
 * authorises the TARGET; the approval authorises the CHANGE SET. Without it the
 * deploy would approve whatever forge computes when it runs, which is what
 * approving by digest exists to stop.
 */
export function startDeployRequest(args: {
  projectId: string;
  env: string;
  token: DeployConfirmationToken;
  approval: PlanApproval;
}): MessageInitShape<typeof StartForgeDeployRequestSchema> {
  return {
    projectId: args.projectId,
    env: args.env,
    // THE CLUSTER CLAIM. Required, and it came off the rendered plan.
    expectedDeclaredContext: args.token.expectedDeclaredContext,
    // Exactly one of these is a real claim, enforced by the token's type.
    // The server rejects neither-set as InvalidArgument and both-set as
    // contradictory.
    expectedCurrentRelease: args.token.expectUnbound ? "" : args.token.expectedCurrentRelease,
    expectUnbound: args.token.expectUnbound === true,
    // THE CONTENT CLAIM.
    approveDigest: args.approval.approveDigest,
    releaseVersion: args.approval.releaseVersion,
    acknowledgedFindings: args.approval.acknowledgedFindings,
  };
}

/**
 * StartDeploy's answer. The call returns a handle immediately; the apply runs
 * detached. A synchronous RPC is not available at all here — forge's default
 * rollout budget is five minutes PER RESOURCE, and a killed child would leave a
 * half-converged cluster with nobody able to say what shipped.
 */
export function startDeployResultOf(res: StartForgeDeployResponse): StartDeployResult {
  const guardPlan = classifyForgeResponse<ForgeDeployReport>(res.meta, res.reportJson);
  if ((res.handle ?? "").trim() === "") {
    return { kind: "not-started", outcome: guardPlan };
  }
  return {
    kind: "started",
    handle: res.handle,
    env: res.env,
    startedAt: res.startedAt,
    jobStatus: deployJobDispositionOf(res.jobStatus),
    guardPlan,
  };
}

/** One poll of a running or finished deploy. */
export interface DeployStatus {
  handle: string;
  env: string;
  /** Whether the INVOCATION reached a determinate outcome. Not the verdict. */
  jobStatus: DeployJobDisposition;
  /** The server's sentence for a non-completed terminal state. */
  jobStatusDetail: string;
  startedAt: string;
  finishedAt: string;
  /**
   * Forge's report, once the job produced one. Absent while running, and absent
   * for a job that died before emitting it — which is why jobStatus must be read
   * alongside it rather than inferred from its presence.
   */
  report: ForgeOutcome<ForgeDeployReport> | null;

  /**
   * This job computed a PLAN, not a deploy: `report` holds the change set to
   * approve and nothing was applied.
   *
   * Read from the server rather than sniffed from the document, because a plan
   * and a deploy report look alike and approving one while reading the other is
   * the mistake worth making structurally impossible.
   */
  planOnly: boolean;

  /**
   * FORGE REFUSED THE WRITE after computing the plan. Nothing was deployed.
   *
   * The job itself ran, so this arrives on a successful poll with jobStatus
   * `failed` — not as a thrown error, which would stop a caller reading the
   * outcome of a handle it holds.
   *
   * On a stale plan, `currentPlan` is the plan forge RECOMPUTED. That document
   * is the remedy: show what changed and ask again, never re-send the refused
   * approval.
   */
  refusal: DeployJobRefusal | null;
}

/** Why forge declined the write, from inside a job that really ran. */
export interface DeployJobRefusal {
  /**
   * `plan-stale` — the plan moved between reading and approving.
   * `plan-unacknowledged` — an irreversible change was not accepted by name.
   * `other` — forge refused for a reason this client does not model; the
   * detail carries it. Never collapsed into a success.
   */
  kind: "plan-stale" | "plan-unacknowledged" | "other";
  detail: string;
  /** The recomputed plan, on a stale plan. */
  currentPlan: DeployPlanReport | null;
  /** The codes still needing acceptance, on an unacknowledged one. */
  unacknowledged: string[];
}

/**
 * deployJobRefusalOf decodes the refusal on a status response.
 *
 * An UNRECOGNISED reason becomes `other` and stays a refusal. It is never
 * dropped: a refusal this client cannot name is still a deploy that did not
 * happen, and treating it as absent would render a failed job as a successful
 * one.
 */
function deployJobRefusalOf(
  refusal:
    | {
        reason?: ForgeDeployRefusalReason;
        detail?: string;
        currentPlanJson?: string;
        unacknowledged?: string[];
      }
    | undefined
): DeployJobRefusal | null {
  if (!refusal) return null;

  let kind: DeployJobRefusal["kind"] = "other";
  if (refusal.reason === ForgeDeployRefusalReason.PLAN_STALE) kind = "plan-stale";
  else if (refusal.reason === ForgeDeployRefusalReason.PLAN_UNACKNOWLEDGED) {
    kind = "plan-unacknowledged";
  }

  let currentPlan: DeployPlanReport | null = null;
  const raw = (refusal.currentPlanJson ?? "").trim();
  if (raw !== "") {
    try {
      // The plan arrives as forge's plan object, so it is wrapped in the
      // document shape the rest of the UI reads plans through.
      currentPlan = { deploy_plan: JSON.parse(raw) as DeployPlanReport["deploy_plan"] };
    } catch {
      // A plan we cannot parse is not a plan. Left null, and the refusal still
      // stands — the notice says it could not show what changed, which is
      // honest, rather than claiming nothing did.
      currentPlan = null;
    }
  }

  return {
    kind,
    detail: refusal.detail ?? "",
    currentPlan,
    unacknowledged: refusal.unacknowledged ?? [],
  };
}

/**
 * deployStatusOf reads one poll of a deploy by handle.
 *
 * A handle the daemon does not recognise is NOT an error: the job registry is in
 * memory, so a daemon that restarted mid-apply has lost the handle for a deploy
 * that may well have landed. That arrives as a successful response with
 * jobStatus `unknown`, and it stays `unknown` all the way to the screen.
 */
export function deployStatusOf(res: GetForgeDeployStatusResponse): DeployStatus {
  return {
    handle: res.handle,
    env: res.env,
    jobStatus: deployJobDispositionOf(res.jobStatus),
    jobStatusDetail: res.jobStatusDetail,
    startedAt: res.startedAt,
    finishedAt: res.finishedAt,
    // An empty body while running is expected and is not a malformed document,
    // so it is null rather than an outcome claiming forge said something.
    report:
      (res.reportJson ?? "").trim() === ""
        ? null
        : classifyForgeResponse<ForgeDeployReport>(res.meta, res.reportJson),
    planOnly: res.planOnly === true,
    refusal: deployJobRefusalOf(res.refusal),
  };
}

/**
 * deployJobDispositionOf maps the generated enum onto the UI's idiom.
 *
 * UNSPECIFIED and anything unrecognised become `unknown`, NEVER `completed`. A
 * newer server's additional status decoded as completed would report a deploy of
 * indeterminate outcome as one that finished — the same
 * green-deploy-over-a-broken-environment failure in a different costume. And
 * `unknown` rather than `failed`, because failed licenses a retry and a deploy
 * whose outcome nobody knows must not be retried blind.
 */
function deployJobDispositionOf(status: ForgeDeployJobStatus | undefined): DeployJobDisposition {
  switch (status) {
    case ForgeDeployJobStatus.RUNNING:
      return "running";
    case ForgeDeployJobStatus.COMPLETED:
      return "completed";
    case ForgeDeployJobStatus.FAILED:
      return "failed";
    default:
      return "unknown";
  }
}

/**
 * deployRefusalOf pulls the structured refusal off a connect error.
 *
 * THE DETAIL IS READ, NOT THE MESSAGE. The server's text is "deploy refused,
 * nothing was applied: <detail>", and matching on that string would break the
 * moment the wording improves while silently downgrading a refusal to a generic
 * failure — the one presentation this outcome must never get, because each of
 * the four reasons calls for a different action.
 *
 * A FailedPrecondition whose detail cannot be decoded still returns null and so
 * still surfaces as an error. The server attaches the detail best-effort and
 * fails closed without it, so a missing detail means "refused but unexplained",
 * and inventing an empty refusal panel would claim facts about the cluster that
 * never arrived.
 */
export function deployRefusalOf(error: unknown): DeployRefusal | null {
  if (!(error instanceof ConnectError)) return null;
  if (error.code !== Code.FailedPrecondition) return null;

  for (const detail of error.findDetails(ForgeDeployRefusalSchema)) {
    return {
      reason: deployRefusalReasonOf(detail.reason),
      detail: detail.detail,
      expectedDeclaredContext: detail.expectedDeclaredContext,
      actualDeclaredContext: detail.actualDeclaredContext,
      expectedCurrentRelease: detail.expectedCurrentRelease,
      expectedUnbound: detail.expectedUnbound,
      actualCurrentRelease: detail.actualCurrentRelease,
      actualBound: detail.actualBound,
      guardVerdict: detail.guardVerdict,
      guardReason: detail.guardReason,
      guardFix: detail.guardFix,
      runningHandle: detail.runningHandle,
    };
  }
  return null;
}

/** An unrecognised reason is `unknown` — a refusal this build cannot classify is still a refusal. */
function deployRefusalReasonOf(reason: ForgeDeployRefusalReason | undefined): DeployRefusalReason {
  switch (reason) {
    case ForgeDeployRefusalReason.STALE_DECLARED_CONTEXT:
      return "stale-declared-context";
    case ForgeDeployRefusalReason.STALE_CURRENT_RELEASE:
      return "stale-current-release";
    case ForgeDeployRefusalReason.GUARD_REFUSED:
      return "guard-refused";
    case ForgeDeployRefusalReason.ALREADY_RUNNING:
      return "already-running";
    default:
      return "unknown";
  }
}
