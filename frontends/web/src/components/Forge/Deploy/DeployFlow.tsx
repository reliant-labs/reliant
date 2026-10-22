// Copyright (c) 2025 Reliant Labs

/**
 * The deploy flow: plan → confirm → in-flight job, or refused.
 *
 * THE ORDER IS THE GUARD, and it is stricter than promote's because the write is
 * worse. Confirm is rendered only inside the branch where a dry-run plan document
 * exists AND nothing blocks it — not a disabled button that could be re-enabled
 * by a future prop, but an ABSENT one. Every other outcome of the plan call (not a
 * forge project, forge too old, unreachable, malformed) renders the shared state
 * components and offers no confirm at all.
 *
 * BLOCKERS MAKE CONFIRM ABSENT, NOT DISCOURAGED. A blocking preflight finding
 * means forge checked the live target and something this deploy references is not
 * there, so the apply fails; forge's own guard refusing means forge will not
 * deploy at all; a plan with no declared cluster cannot produce a token. None of
 * those is a warning to render beside an enabled button, so in each case the
 * confirm step is not rendered and the reason is.
 *
 * PURE PROPS, like PromoteFlow: the hooks live in the dialog next door, so the
 * whole flow — including the four refusal paths and every job disposition, none
 * of which can be reached against a live daemon without deploying something — is
 * driven from fixtures.
 *
 * Once a deploy is started this switches to the JOB panel rather than closing.
 * Closing on a successful start would be the single most misleading moment
 * available: the RPC returning ok means a deploy is in flight, and nothing
 * whatsoever is yet known about whether it worked.
 */

import { Button } from "@/components/ui/Button";
import type { DeployStatus, StartDeployResult } from "@/api/forge-grpc";
import {
  deployBlockers,
  deployTokenFor,
  isHostedPlan,
  type DeployBlocker,
  type ForgeDeployReport,
} from "@/services/forge/deploy";
import type { DeployPlanReport, PlanApproval } from "@/services/forge/deployPlan";
import { classifyDeployAuthzError } from "@/services/forge/deployAuthz";
import type { ForgeOutcome } from "@/services/forge/topology";

import {
  ForgeMalformed,
  ForgeUnreachable,
  ForgeUnsupported,
  NotForgeProject,
} from "../ForgeStates";
import { ApprovablePlanView } from "./ApprovablePlanView";
import { DeployApproveStep } from "./DeployApproveStep";
import { DeployPlanView } from "./DeployPlanView";
import { DeployRefusalNotice } from "./DeployRefusalNotice";
import { PlanStaleNotice } from "./PlanStaleNotice";

export interface DeployFlowProps {
  /** The plan outcome. Confirm is reachable only from the `report` branch. */
  planOutcome: ForgeOutcome<ForgeDeployReport> | undefined;
  isPlanning: boolean;
  /** A transport/daemon failure — genuinely an error, unlike the outcomes above. */
  planError?: Error | null;
  /** The start's result, once attempted. */
  startResult?: StartDeployResult | null;
  /** A thrown failure from the start. Distinct from a refusal. */
  startError?: Error | null;
  isStarting: boolean;
  onReplan: () => void;

  // ── The approvable half ───────────────────────────────────────────────────
  //
  // Stage one BUILDS: it pushes images and cuts a release, which is why it is
  // a thing the operator asks for rather than something that happens on open.
  // The instant preview above is what the dialog shows for free.
  /** Start working out what this deploy would ship. */
  onBuildAndPlan: () => void;
  isBuildingPlan?: boolean;
  /** The plan job's start result — a refusal lands here. */
  planJobResult?: StartDeployResult | null;
  planJobError?: Error | null;
  /** The plan job's poll, while it is the job being followed. */
  planJobStatus?: DeployStatus | null;
  /** The plan to approve, once the job produced one. */
  approvablePlan?: DeployPlanReport | null;
  /** The irreversible changes accepted so far, by code. */
  acknowledged: ReadonlySet<string>;
  onAcknowledge: (code: string, accepted: boolean) => void;
  /** Deploy the plan on screen, bound to its digest. */
  onApprove: (approval: PlanApproval) => void;
  /** Work the plan out again after it moved underneath an approval. */
  onReplanAfterStale: () => void;
  /** Follow a deploy that was already in flight, from an already-running refusal. */
  onWatchRunning?: (handle: string) => void;
  onClose: () => void;
  projectName?: string;
  /** Rendered in place of the plan once a deploy is being followed. */
  jobPanel?: React.ReactNode;
}

export function DeployFlow({
  planOutcome,
  isPlanning,
  planError,
  startResult,
  startError,
  isStarting,
  onReplan,
  onWatchRunning,
  onClose,
  projectName,
  jobPanel,
  onBuildAndPlan,
  isBuildingPlan,
  planJobResult,
  planJobError,
  planJobStatus,
  approvablePlan,
  acknowledged,
  onAcknowledge,
  onApprove,
  onReplanAfterStale,
}: DeployFlowProps) {
  // A DEPLOY IS IN FLIGHT (or being watched). Terminal for this flow: the plan is
  // no longer the thing on screen, and there is no route back to a confirm.
  if (jobPanel) return <>{jobPanel}</>;

  if (isPlanning && !planOutcome) {
    return (
      <p
        data-testid="deploy-planning"
        className="rounded-lg border border-dashed border-border px-6 py-10 text-center text-sm text-muted-foreground"
      >
        Computing the deploy plan — forge is reading the live target…
      </p>
    );
  }

  if (planError && !planOutcome) {
    // AUTHORIZATION IS NOT UNREACHABILITY. A hosted deploy authenticates
    // before it can plan, so both of these arrive as a failed plan call — but
    // the daemon answered, and telling the user it did not sends them to
    // debug connectivity over a credential or a permission.
    const authz = classifyDeployAuthzError(planError);
    if (authz?.kind === "not-authorized") return <DeployNotAuthorized />;
    if (authz?.kind === "permission-denied") {
      return <DeployPermissionDenied permission={authz.permission} />;
    }
    return (
      <div
        data-testid="deploy-plan-error"
        className="rounded-lg border border-destructive/40 bg-destructive/10 px-6 py-8 text-center text-sm text-destructive-ink"
      >
        Could not reach your daemon to plan this deploy: {planError.message}
      </div>
    );
  }

  if (!planOutcome) return null;

  // Each is a successful RPC with a different meaning, and none of them can
  // support a write — so no confirm step exists in any of these branches.
  if (planOutcome.kind === "not-forge-project") return <NotForgeProject projectName={projectName} />;
  if (planOutcome.kind === "unsupported") return <ForgeUnsupported meta={planOutcome.meta} />;
  if (planOutcome.kind === "unreachable") return <ForgeUnreachable meta={planOutcome.meta} />;
  if (planOutcome.kind === "malformed") return <ForgeMalformed meta={planOutcome.meta} />;

  const plan = planOutcome.report;
  const refused = startResult?.kind === "refused" ? startResult.refusal : null;
  const notStarted = startResult?.kind === "not-started" ? startResult.outcome : null;
  const blockers = deployBlockers(plan);
  // Both gates must pass. The blockers list and the token answer different
  // questions — "will this fail" and "can this claim be made" — and each one on
  // its own would let the other's failure through.
  const confirmable = blockers.length === 0 && deployTokenFor(plan) !== null;

  return (
    <div className="space-y-4">
      {/* The plan, always above anything that could authorise it. */}
      <DeployPlanView plan={plan} />

      {refused ? (
        // Refused: nothing was applied, and the plan above is the one already
        // rejected. No confirm until a fresh plan replaces it.
        <DeployRefusalNotice
          refusal={refused}
          hosted={isHostedPlan(plan)}
          onReplan={onReplan}
          isReplanning={isPlanning}
          onWatchRunning={onWatchRunning}
        />
      ) : (
        <>
          {notStarted && (
            // The RPC succeeded but no job exists — in practice a pinned forge
            // too old for the command. Nothing was started, so this is not a
            // failure to investigate in the cluster.
            <div
              data-testid="deploy-not-started"
              className="space-y-1 rounded-lg border border-dashed border-border px-3 py-2 text-xs text-muted-foreground"
            >
              <p className="text-foreground">
                No deploy was started, and nothing was applied.
              </p>
              <p>
                {notStarted.kind === "unsupported"
                  ? notStarted.meta.unsupportedReason ||
                    "The forge this project is pinned to does not understand this command."
                  : "The daemon returned no job to follow."}
              </p>
            </div>
          )}

          {startError && (
            // A thrown failure, not a refusal. Whether a deploy got underway is
            // genuinely unknown here, so this claims neither.
            <div
              data-testid="deploy-start-error"
              className="space-y-2 rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive-ink"
            >
              <p>The deploy could not be started: {startError.message}</p>
              <p className="text-muted-foreground">
                Whether a deploy got underway is not known from this failure. Re-plan before trying
                again — if one did start, forge will refuse a second as already running.
              </p>
              <Button variant="secondary" size="xs" onClick={onReplan} disabled={isPlanning}>
                Re-plan
              </Button>
            </div>
          )}

          {confirmable ? (
            <ApprovalStage
              env={plan.env}
              onBuildAndPlan={onBuildAndPlan}
              isBuildingPlan={isBuildingPlan}
              planJobResult={planJobResult}
              planJobError={planJobError}
              planJobStatus={planJobStatus}
              approvablePlan={approvablePlan}
              acknowledged={acknowledged}
              onAcknowledge={onAcknowledge}
              onApprove={onApprove}
              onReplanAfterStale={onReplanAfterStale}
              onCancel={onClose}
              isStarting={isStarting}
            />
          ) : (
            // NO CONFIRM. Not a disabled one: there is no route from this plan to
            // a write, so the reasons are rendered where the button would be.
            <BlockedNotice blockers={blockers} onReplan={onReplan} isReplanning={isPlanning} />
          )}
        </>
      )}
    </div>
  );
}

/**
 * THE TWO STAGES, and naming them is half the design.
 *
 * The preview above this is instant and describes the environment as it is. It
 * cannot say what a deploy would ship, because a deploy builds from this
 * checkout and cuts a new release — so the only honest thing to put beside a
 * deploy button is a plan computed by actually doing that work.
 *
 * Hence: "Work out what this would ship" is a thing the operator asks for and
 * waits minutes on, and only once its plan is on screen does a deploy control
 * exist at all. There is no path from the instant preview to a write.
 */
function ApprovalStage({
  env,
  onBuildAndPlan,
  isBuildingPlan,
  planJobResult,
  planJobError,
  planJobStatus,
  approvablePlan,
  acknowledged,
  onAcknowledge,
  onApprove,
  onReplanAfterStale,
  onCancel,
  isStarting,
}: {
  env?: string;
  onBuildAndPlan: () => void;
  isBuildingPlan?: boolean;
  planJobResult?: StartDeployResult | null;
  planJobError?: Error | null;
  planJobStatus?: DeployStatus | null;
  approvablePlan?: DeployPlanReport | null;
  acknowledged: ReadonlySet<string>;
  onAcknowledge: (code: string, accepted: boolean) => void;
  onApprove: (approval: PlanApproval) => void;
  onReplanAfterStale: () => void;
  onCancel: () => void;
  isStarting?: boolean;
}) {
  // The plan job was REFUSED — in practice a deploy of this environment is
  // already in flight. Nothing was built and nothing was deployed.
  if (planJobResult?.kind === "refused") {
    return (
      <div
        data-testid="deploy-plan-refused"
        className="space-y-2 rounded-lg border border-warning/50 bg-warning/10 px-3 py-2 text-xs"
      >
        <p className="text-foreground">{planJobResult.refusal.detail}</p>
        <p className="text-muted-foreground">
          Nothing was built and nothing was deployed.
        </p>
      </div>
    );
  }

  if (planJobError) {
    return (
      <div
        data-testid="deploy-plan-job-error"
        className="space-y-2 rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive-ink"
      >
        <p>The changes could not be worked out: {planJobError.message}</p>
        <p className="text-muted-foreground">
          Nothing was deployed. Your environment is unchanged.
        </p>
        <Button variant="secondary" size="xs" onClick={onBuildAndPlan}>
          Try again
        </Button>
      </div>
    );
  }

  // FORGE REFUSED THE WRITE after recomputing the plan. The stale case is the
  // one that matters most: it re-renders the NEW plan and asks again, and it
  // never offers to re-send what was refused.
  const jobRefusal = planJobStatus?.refusal ?? null;
  if (jobRefusal?.kind === "plan-stale") {
    return (
      <div className="space-y-3">
        <PlanStaleNotice
          approved={approvablePlan?.deploy_plan}
          current={jobRefusal.currentPlan?.deploy_plan}
        />
        {jobRefusal.currentPlan && (
          <>
            <ApprovablePlanView
              report={jobRefusal.currentPlan}
              acknowledged={acknowledged}
              onAcknowledge={onAcknowledge}
            />
            {/* Working it out again, NOT re-approving. The recomputed plan was
                not built here, so its release may not exist to deploy — the
                honest next step is a fresh plan. */}
            <div className="flex items-center justify-end gap-2">
              <Button variant="ghost" size="sm" onClick={onCancel}>
                Cancel
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={onReplanAfterStale}
                loading={isBuildingPlan}
                disabled={isBuildingPlan}
                data-testid="deploy-replan-after-stale"
              >
                {isBuildingPlan ? "Working it out…" : "Work out the changes again"}
              </Button>
            </div>
          </>
        )}
      </div>
    );
  }

  // An irreversible change was not accepted by name. Forge built and cut, and
  // wrote no promotion.
  if (jobRefusal?.kind === "plan-unacknowledged") {
    return (
      <div
        data-testid="deploy-plan-unacknowledged"
        className="space-y-2 rounded-lg border border-destructive/50 bg-destructive/10 px-3 py-2 text-xs"
      >
        <p className="text-foreground">
          This was not deployed, because it destroys something that was not accepted.
        </p>
        <p className="text-muted-foreground">
          Your environment is unchanged. Work the changes out again and accept each
          irreversible one explicitly.
        </p>
        <Button variant="secondary" size="xs" onClick={onReplanAfterStale}>
          Work out the changes again
        </Button>
      </div>
    );
  }

  // Any other refusal from forge. Never collapsed into a success.
  if (jobRefusal) {
    return (
      <div
        data-testid="deploy-plan-other-refusal"
        className="space-y-1 rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs"
      >
        <p className="text-foreground">
          {jobRefusal.detail || "forge declined to deploy this."}
        </p>
        <p className="text-muted-foreground">Nothing was deployed.</p>
      </div>
    );
  }

  // STAGE ONE IN FLIGHT. Minutes, and it says what it is doing — a bare
  // spinner on a multi-minute build reads as a hang.
  if (isBuildingPlan && !approvablePlan) {
    return (
      <p
        data-testid="deploy-building-plan"
        className="rounded-lg border border-dashed border-border px-6 py-10 text-center text-sm text-muted-foreground"
      >
        Working out what this would ship. It builds your code first, so this takes a few
        minutes. Nothing is deployed yet.
      </p>
    );
  }

  // The plan job finished without producing a plan. Not a deploy, and not
  // success: said plainly, with the environment's state stated.
  if (planJobStatus && !approvablePlan && planJobStatus.jobStatus !== "running") {
    return (
      <div
        data-testid="deploy-plan-job-failed"
        className="space-y-2 rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs"
      >
        <p className="text-foreground">
          {planJobStatus.jobStatusDetail || "The changes could not be worked out."}
        </p>
        <Button variant="secondary" size="xs" onClick={onBuildAndPlan}>
          Try again
        </Button>
      </div>
    );
  }

  // THE PLAN IS IN. This is the only state with a deploy control.
  if (approvablePlan) {
    return (
      <div className="space-y-3">
        <ApprovablePlanView
          report={approvablePlan}
          acknowledged={acknowledged}
          onAcknowledge={onAcknowledge}
        />
        <DeployApproveStep
          report={approvablePlan}
          acknowledged={acknowledged}
          onApprove={onApprove}
          onCancel={onCancel}
          isDeploying={isStarting}
          env={env}
        />
      </div>
    );
  }

  // NOTHING HAS BEEN WORKED OUT YET, and there is deliberately no deploy
  // control here. The preview above cannot authorise a deploy.
  return (
    <section className="space-y-2" data-testid="deploy-stage-one">
      <p className="text-xs text-muted-foreground">
        Nothing is deployed yet. Work out the changes first — that builds your code and
        shows exactly what would ship.
      </p>
      <div className="flex items-center justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onCancel}>
          Cancel
        </Button>
        <Button
          variant="secondary"
          size="sm"
          onClick={onBuildAndPlan}
          loading={isBuildingPlan}
          disabled={isBuildingPlan}
          data-testid="deploy-build-and-plan"
        >
          {isBuildingPlan ? "Working it out…" : "Work out what this would ship"}
        </Button>
      </div>
    </section>
  );
}

/**
 * Why this plan cannot be confirmed, in place of the confirm step.
 *
 * Every blocker is listed rather than only the first: an operator who fixes the
 * missing Secret and re-plans should not then discover the guard also refused.
 */
function BlockedNotice({
  blockers,
  onReplan,
  isReplanning,
}: {
  blockers: DeployBlocker[];
  onReplan: () => void;
  isReplanning?: boolean;
}) {
  return (
    <section
      data-testid="deploy-blocked"
      className="space-y-2 rounded-lg border border-solid border-destructive/50 bg-destructive/10 px-4 py-3"
    >
      <h3 className="text-sm font-medium text-destructive-ink" data-testid="deploy-blocked-heading">
        This deploy cannot be confirmed
      </h3>
      <ul className="space-y-1">
        {blockers.map((blocker) => (
          <li
            key={blocker.kind}
            data-testid={`deploy-blocker-${blocker.kind}`}
            className="text-xs text-foreground"
          >
            {describeBlocker(blocker)}
          </li>
        ))}
      </ul>
      <Button
        variant="secondary"
        size="xs"
        onClick={onReplan}
        loading={isReplanning}
        disabled={isReplanning}
        data-testid="deploy-blocked-replan"
      >
        {isReplanning ? "Re-planning…" : "Re-plan"}
      </Button>
    </section>
  );
}

/**
 * FORGE ISN'T AUTHORIZED. There is no control-plane credential on this machine.
 *
 * THE FIX IS SIGNING IN TO RELIANT, not `forge login`. Signing in deposits the
 * user's token into forge's own credential store (internal/cliauth's
 * DepositForForge, over forge/pkg/cloudcred), which is what makes "logged in to
 * Reliant" mean "forge is logged in". Telling the user to run `forge login`
 * instead would be a dead end in the case that matters most: a managed daemon
 * on a remote pod, where the loopback browser flow that command needs cannot
 * happen at all.
 *
 * No re-plan button. Re-planning with the same missing credential produces the
 * same failure; the action is out here, not in the flow.
 */
function DeployNotAuthorized() {
  return (
    <section
      data-testid="deploy-not-authorized"
      className="space-y-2 rounded-lg border border-solid border-warning/50 bg-warning/10 px-4 py-3"
    >
      <h3 className="text-sm font-medium text-warning-ink">
        Forge isn&apos;t signed in to Reliant cloud
      </h3>
      <p className="text-xs text-foreground">
        This deploy targets hosted infrastructure, and forge has no credential
        for it on this machine. Sign in to Reliant and forge picks up the same
        credential automatically — you do not need a second login.
      </p>
      <p className="text-2xs text-muted-foreground">
        If you are already signed in, your session may have expired. Signing in
        again refreshes it.
      </p>
    </section>
  );
}

/**
 * YOUR PERMISSIONS DON'T INCLUDE IT. Signed in, but not allowed.
 *
 * NAMES THE PERMISSION AND WHO GRANTS IT. This is the one refusal the user
 * genuinely cannot resolve alone, so the panel's whole job is to make the ask
 * precise: which permission, and that an organization admin holding token:write
 * is who can grant it. "Permission denied" with no route forward is where these
 * conversations stall.
 *
 * No re-plan button, deliberately: authority does not change by re-planning,
 * and offering it invites a loop that always ends here.
 */
function DeployPermissionDenied({ permission }: { permission: string }) {
  return (
    <section
      data-testid="deploy-permission-denied"
      className="space-y-2 rounded-lg border border-solid border-warning/50 bg-warning/10 px-4 py-3"
    >
      <h3 className="text-sm font-medium text-warning-ink">
        Your permissions don&apos;t include{" "}
        {permission ? (
          <span className="font-mono">{permission}</span>
        ) : (
          "what this deploy needs"
        )}
      </h3>
      <p className="text-xs text-foreground">
        You are signed in, but your organization permissions do not cover this
        deploy. Nothing was changed.
      </p>
      <p className="text-xs text-foreground">
        An organization admin can grant{" "}
        {permission ? (
          <span className="font-mono">{permission}</span>
        ) : (
          "it"
        )}{" "}
        in the organization&apos;s member permissions.
      </p>
    </section>
  );
}

/**
 * describeBlocker states the consequence, not the category.
 *
 * "Would fail" for a blocking preflight finding rather than "has warnings",
 * because the preflight read the live target and the apply genuinely does not
 * succeed — softening that is how a known failure gets attempted anyway.
 */
function describeBlocker(blocker: DeployBlocker): string {
  switch (blocker.kind) {
    case "preflight-blocking":
      return (
        `${blocker.findings.length} blocking preflight finding${blocker.findings.length === 1 ? "" : "s"}: ` +
        "something this deploy references is not on the live target, so the apply would fail. See the preflight section above."
      );
    case "guard-refused":
      return (
        "Forge's own declared-cluster guard refused this environment, so forge will not deploy it at all." +
        (blocker.fix ? ` ${blocker.fix}` : "")
      );
    case "no-declared-cluster":
      return "This environment declares no cluster, so there is no target to authorise a deploy against.";
    case "no-declared-endpoint":
      // The hosted twin of no-declared-cluster, and the one blocker whose cause
      // is entirely on our side of the line — so it says what is true without
      // naming the endpoint the customer never configured.
      return "This environment isn't set up to be deployed to yet, so there is nothing to deploy against.";
    case "not-a-preview":
      return `This document is not a read-only preview (mode: ${blocker.mode}), so it cannot authorise a deploy.`;
  }
}
