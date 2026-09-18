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
import type { StartDeployResult } from "@/api/forge-grpc";
import {
  deployBlockers,
  deployTokenFor,
  type DeployBlocker,
  type ForgeDeployReport,
} from "@/services/forge/deploy";
import type { ForgeOutcome } from "@/services/forge/topology";

import {
  ForgeMalformed,
  ForgeUnreachable,
  ForgeUnsupported,
  NotForgeProject,
} from "../ForgeStates";
import { DeployConfirmStep } from "./DeployConfirmStep";
import { DeployPlanView } from "./DeployPlanView";
import { DeployRefusalNotice } from "./DeployRefusalNotice";

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
  onConfirm: (plan: ForgeDeployReport) => void;
  onReplan: () => void;
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
  onConfirm,
  onReplan,
  onWatchRunning,
  onClose,
  projectName,
  jobPanel,
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
    return (
      <div
        data-testid="deploy-plan-error"
        className="rounded-lg border border-destructive/40 bg-destructive/10 px-6 py-8 text-center text-sm text-destructive"
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
              className="space-y-2 rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive"
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
            <DeployConfirmStep
              plan={plan}
              onConfirm={() => onConfirm(plan)}
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
      <h3 className="text-sm font-medium text-destructive" data-testid="deploy-blocked-heading">
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
    case "not-a-preview":
      return `This document is not a read-only preview (mode: ${blocker.mode}), so it cannot authorise a deploy.`;
  }
}
