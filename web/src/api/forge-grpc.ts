// Copyright (c) 2025 Reliant Labs

/**
 * Thin wrapper over `reliant.v1.ForgeService`.
 *
 * Each call returns forge's own report already CLASSIFIED into a ForgeOutcome —
 * see services/forge/topology.ts. Doing that here rather than in a component
 * means the five non-error outcomes (not a forge project, forge too old, cluster
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

import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import { createForgeClient } from "./grpc-client";
import {
  ForgeDeployJobStatus,
  ForgeDeployRefusalReason,
  ForgeDeployRefusalSchema,
  ForgePromoteRefusalReason,
  ForgePromoteRefusalSchema,
  GetForgeAuditRequestSchema,
  GetForgeDeployStatusRequestSchema,
  GetForgeEnvStatusRequestSchema,
  GetForgeTopologyRequestSchema,
  ListForgeSecretsRequestSchema,
  PlanForgeDeployRequestSchema,
  PlanForgePromoteRequestSchema,
  PromoteForgeEnvRequestSchema,
  StartForgeDeployRequestSchema,
  VerifyForgeEnvRequestSchema,
} from "../gen/reliant/v1/forge_pb";
import type {
  DeployConfirmationToken,
  DeployJobDisposition,
  DeployRefusal,
  DeployRefusalReason,
  ForgeDeployReport,
} from "../services/forge/deploy";
import type { ForgePromotePlan, PromoteConfirmationToken, PromoteRefusal } from "../services/forge/promote";
import {
  classifyForgeResponse,
  type ForgeOutcome,
  type ForgeTopologyReport,
  type ForgeVerifyReport,
} from "../services/forge/topology";

export interface GetTopologyArgs {
  projectId: string;
  /**
   * Reconcile every declared environment against its live cluster.
   *
   * Off by default and normally left off: with verify the call reads EVERY
   * env's cluster under a 110s budget and can fail as a whole. The screen loads
   * the ledger first and upgrades one env at a time via verifyEnv, so a single
   * slow cluster cannot keep the rest of the topology off the screen.
   */
  verify?: boolean;
  /** Narrow to these environments. Empty means all declared. */
  envs?: string[];
}

export async function getTopology(
  args: GetTopologyArgs
): Promise<ForgeOutcome<ForgeTopologyReport>> {
  const res = await createForgeClient().getTopology(
    create(GetForgeTopologyRequestSchema, {
      projectId: args.projectId,
      verify: args.verify ?? false,
      envs: args.envs ?? [],
    })
  );
  return classifyForgeResponse<ForgeTopologyReport>(res.meta, res.reportJson);
}

export async function verifyEnv(
  projectId: string,
  env: string
): Promise<ForgeOutcome<ForgeVerifyReport>> {
  const res = await createForgeClient().verifyEnv(
    create(VerifyForgeEnvRequestSchema, { projectId, env })
  );
  return classifyForgeResponse<ForgeVerifyReport>(res.meta, res.reportJson);
}

/**
 * listSecrets returns forge's secret report: NAMES and BOOLEANS only, never a
 * value. The proto message is constrained by a reflection test on the Go side so
 * no field can carry one; nothing here re-widens it.
 */
export async function listSecrets(
  projectId: string,
  env: string
): Promise<ForgeOutcome<Record<string, unknown>>> {
  const res = await createForgeClient().listSecrets(
    create(ListForgeSecretsRequestSchema, { projectId, env })
  );
  return classifyForgeResponse<Record<string, unknown>>(res.meta, res.reportJson);
}

export async function getAudit(projectId: string): Promise<ForgeOutcome<Record<string, unknown>>> {
  const res = await createForgeClient().getAudit(
    create(GetForgeAuditRequestSchema, { projectId })
  );
  return classifyForgeResponse<Record<string, unknown>>(res.meta, res.reportJson);
}

export async function getEnvStatus(
  projectId: string,
  env: string
): Promise<ForgeOutcome<Record<string, unknown>>> {
  const res = await createForgeClient().getEnvStatus(
    create(GetForgeEnvStatusRequestSchema, { projectId, env })
  );
  return classifyForgeResponse<Record<string, unknown>>(res.meta, res.reportJson);
}

// ── Promote: two methods, and the naming is the guard ────────────────────────
//
// PlanPromote and ApplyPromote stay two clearly-named functions here, mirroring
// the two RPCs. They are NOT unified behind one function with a `dryRun` flag,
// and the reason is the same one that shaped the proto: a boolean defaults to
// false, false would be the destructive value, and every call site that forgot
// the argument would write. There is a backend test that fails if anyone adds a
// dry_run field to the request; collapsing them on this side would reintroduce
// exactly what that test defends, one layer up.

/**
 * planPromote previews binding an environment to a release.
 *
 * READ-ONLY, safe and idempotent: forge computes the plan and writes nothing.
 * `dry_run` is true and `applied` false in every document it returns.
 */
export async function planPromote(args: {
  projectId: string;
  env: string;
  release: string;
}): Promise<ForgeOutcome<ForgePromotePlan>> {
  const res = await createForgeClient().planPromote(
    create(PlanForgePromoteRequestSchema, {
      projectId: args.projectId,
      env: args.env,
      release: args.release,
    })
  );
  return classifyForgeResponse<ForgePromotePlan>(res.meta, res.reportJson);
}

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
 * applyPromote WRITES the binding. This is the only function in this module
 * that changes state.
 *
 * The name says apply rather than promote precisely so it cannot be reached by
 * an autocomplete for the generic verb, and the token is a required argument of
 * a type that cannot express "neither claim" — see PromoteConfirmationToken.
 * The caller cannot construct one except from a plan it rendered, because
 * confirmationTokenFor is the only producer.
 *
 * A refusal arrives as a FailedPrecondition carrying a ForgePromoteRefusal
 * detail, and is returned as data. Letting it propagate as an error would leave
 * every call site to re-derive that nothing was written from an error string;
 * lifting it here means the caller handles all three outcomes by construction.
 */
export async function applyPromote(args: {
  projectId: string;
  env: string;
  release: string;
  token: PromoteConfirmationToken;
}): Promise<ApplyPromoteResult> {
  try {
    const res = await createForgeClient().applyPromote(
      create(PromoteForgeEnvRequestSchema, {
        projectId: args.projectId,
        env: args.env,
        release: args.release,
        // Exactly one of these is set, enforced by the token's type. The server
        // rejects neither-set as InvalidArgument and both-set as contradictory.
        expectedCurrentRelease: args.token.expectUnbound
          ? ""
          : args.token.expectedCurrentRelease,
        expectUnbound: args.token.expectUnbound === true,
      })
    );
    return { kind: "applied", outcome: classifyForgeResponse<ForgePromotePlan>(res.meta, res.reportJson) };
  } catch (error) {
    const refusal = promoteRefusalOf(error);
    if (refusal) return { kind: "refused", refusal };
    throw error;
  }
}

/**
 * promoteRefusalOf pulls the structured refusal off a connect error.
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
function promoteRefusalOf(error: unknown): PromoteRefusal | null {
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

// ── Deploy: three methods, and the naming is the guard ───────────────────────
//
// planDeploy, startDeploy and getDeployStatus mirror the three RPCs exactly.
// There is deliberately NO fourth function that wraps plan-then-start, and no
// `deploy()` of any kind: a single call that both previews and applies is the
// shape this whole path is built to make unavailable, and a backend reflection
// test (TestNoUnguardedDeployRPCExists) fails if a fourth deploy-ish method
// appears on the service. The write is `startDeploy` — named for what it does,
// so it cannot be reached by autocompleting the generic verb.

/**
 * planDeploy previews deploying an environment. APPLIES NOTHING.
 *
 * Forge renders the env, evaluates the declared-cluster guard, runs the
 * preflight against the live target and returns before the first kubectl apply.
 * Read-only and idempotent, so it needs no confirmation token — there is nothing
 * to confirm. Every document it returns has mode "dry_run".
 *
 * It does READ the live cluster (the preflight checks Secret keys and image
 * metadata), so a timeout here means live state is unknown rather than that
 * something is broken.
 */
export async function planDeploy(args: {
  projectId: string;
  env: string;
}): Promise<ForgeOutcome<ForgeDeployReport>> {
  const res = await createForgeClient().planDeploy(
    create(PlanForgeDeployRequestSchema, { projectId: args.projectId, env: args.env })
  );
  return classifyForgeResponse<ForgeDeployReport>(res.meta, res.reportJson);
}

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
      /** The GUARD PLAN this deploy was authorised against. mode "dry_run". */
      guardPlan: ForgeOutcome<ForgeDeployReport>;
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
 * startDeploy STARTS A REAL DEPLOY TO A LIVE CLUSTER. This is the only function
 * in this module that mutates anything outside the repo.
 *
 * The token is a required argument of a type that cannot express "no cluster
 * claim" or "neither release claim", and the caller cannot construct one except
 * from a plan it rendered, because deployTokenFor is the only producer. The
 * server re-plans and starts nothing unless every claim still holds.
 *
 * The call returns a handle immediately; the apply runs detached. A synchronous
 * RPC is not available at all here — forge's default rollout budget is five
 * minutes PER RESOURCE, and a killed child would leave a half-converged cluster
 * with nobody able to say what shipped.
 */
export async function startDeploy(args: {
  projectId: string;
  env: string;
  token: DeployConfirmationToken;
}): Promise<StartDeployResult> {
  try {
    const res = await createForgeClient().startDeploy(
      create(StartForgeDeployRequestSchema, {
        projectId: args.projectId,
        env: args.env,
        // THE CLUSTER CLAIM. Required, and it came off the rendered plan.
        expectedDeclaredContext: args.token.expectedDeclaredContext,
        // Exactly one of these is a real claim, enforced by the token's type.
        // The server rejects neither-set as InvalidArgument and both-set as
        // contradictory.
        expectedCurrentRelease: args.token.expectUnbound
          ? ""
          : args.token.expectedCurrentRelease,
        expectUnbound: args.token.expectUnbound === true,
      })
    );

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
  } catch (error) {
    const refusal = deployRefusalOf(error);
    if (refusal) return { kind: "refused", refusal };
    throw error;
  }
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
}

/**
 * getDeployStatus polls a deploy by handle.
 *
 * A handle the daemon does not recognise is NOT an error: the job registry is in
 * memory, so a daemon that restarted mid-apply has lost the handle for a deploy
 * that may well have landed. That arrives as a successful response with
 * jobStatus `unknown`, and it stays `unknown` all the way to the screen.
 */
export async function getDeployStatus(args: {
  projectId: string;
  handle: string;
}): Promise<DeployStatus> {
  const res = await createForgeClient().getDeployStatus(
    create(GetForgeDeployStatusRequestSchema, {
      projectId: args.projectId,
      handle: args.handle,
    })
  );

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
function deployRefusalOf(error: unknown): DeployRefusal | null {
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

export const forgeGrpc = {
  getTopology,
  verifyEnv,
  listSecrets,
  getAudit,
  getEnvStatus,
  planPromote,
  applyPromote,
  planDeploy,
  startDeploy,
  getDeployStatus,
};
