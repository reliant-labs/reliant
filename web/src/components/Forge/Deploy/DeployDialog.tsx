// Copyright (c) 2025 Reliant Labs

/**
 * The deploy flow's container: owns the hooks, DeployFlow owns the pixels.
 *
 * Splitting them mirrors PromoteDialog/PromoteFlow, and here it earns its keep
 * more than anywhere else in this feature: every interesting state — four
 * refusals, a running job, an indeterminate job, a rollout that timed out — is
 * unreachable against a live daemon without actually deploying something to a
 * real cluster. Because this is the only part that talks to react-query, all of
 * them are driven from fixtures in the tests.
 *
 * The dialog is also what enforces that a plan is FRESH. Opening mounts the query
 * with a zero staleTime and zero gcTime, so the next open cannot show a stale plan
 * or — much worse — a stale confirmation token naming a cluster the KCL no longer
 * declares.
 *
 * WATCHING IS SEPARATE FROM STARTING, and that separation is what makes the
 * already-running refusal safe to act on: the handle being followed is a piece of
 * state here, set either by a start we performed or by a refusal naming a deploy
 * someone else started. Either way the job panel polls a handle; nothing about
 * watching can start anything.
 */

import { useCallback, useMemo, useState } from "react";

import { Modal } from "@/components/ui/Modal";
import {
  useForgeDeployPlan,
  useForgeDeployStatus,
  useStartForgeDeploy,
  useStartForgeDeployPlan,
  useVerifyForgeEnv,
} from "@/hooks/forge-queries";
import type { ForgeDeployReport } from "@/services/forge/deploy";
import type { DeployPlanReport, PlanApproval } from "@/services/forge/deployPlan";

import { DeployFlow } from "./DeployFlow";
import { DeployJobPanel } from "./DeployJobPanel";

export interface DeployDialogProps {
  isOpen: boolean;
  onClose: () => void;
  projectId: string | null | undefined;
  env: string;
  projectName?: string;
  /** The checkout to build and render from. Empty is the main checkout. */
  checkoutPath?: string;
}

export function DeployDialog({
  isOpen,
  onClose,
  projectId,
  env,
  projectName,
  checkoutPath,
}: DeployDialogProps) {
  /**
   * The handle being FOLLOWED. Set by a start, or by an already-running refusal
   * naming a deploy that was already in flight. It is deliberately not derived
   * from the start mutation's data alone — watching someone else's deploy is a
   * legitimate state that no start of ours produced.
   */
  const [watchedHandle, setWatchedHandle] = useState<string | null>(null);

  /**
   * The handle of the job WORKING OUT the plan, and the irreversible changes
   * accepted against the plan it produced.
   *
   * The acknowledgements are keyed to nothing, deliberately: they are cleared
   * whenever a new plan arrives (see handleBuildAndPlan and the stale path), so
   * an acceptance can never carry over to a change set it was not given for.
   * That is the whole reason a newly-appeared deletion forces a fresh tick.
   */
  const [planHandle, setPlanHandle] = useState<string | null>(null);
  const [acknowledged, setAcknowledged] = useState<ReadonlySet<string>>(new Set());

  // The instant preview: the guard, the cluster, the current binding. Cheap, so
  // it runs as soon as the dialog opens. Disabled once a job is being followed —
  // re-reading the live target underneath a running apply buys nothing.
  const followed = watchedHandle ?? planHandle;
  const plan = useForgeDeployPlan(isOpen && !followed ? projectId : null, env);

  const buildAndPlan = useStartForgeDeployPlan(projectId);
  const start = useStartForgeDeploy(projectId);
  const status = useForgeDeployStatus(projectId, followed);
  const verify = useVerifyForgeEnv(projectId);

  /**
   * The plan job's result, once it has one. Read through planOnly rather than
   * by sniffing the document, so a deploy report can never be rendered as a
   * plan to approve.
   */
  const approvablePlan = useMemo<DeployPlanReport | null>(() => {
    if (!planHandle || !status.data?.planOnly) return null;
    const report = status.data.report;
    if (!report || report.kind !== "report") return null;
    return report.report as unknown as DeployPlanReport;
  }, [planHandle, status.data]);

  /**
   * STAGE ONE. Builds, pushes and cuts, and writes nothing. Clears any previous
   * acceptance: a tick belongs to one change set.
   */
  const handleBuildAndPlan = useCallback(() => {
    setAcknowledged(new Set());
    start.reset();
    buildAndPlan.mutate(
      { env, checkoutPath },
      {
        onSuccess: (result) => {
          if (result.kind === "started") setPlanHandle(result.handle);
        },
      }
    );
  }, [buildAndPlan, start, env, checkoutPath]);

  const handleAcknowledge = useCallback((code: string, accepted: boolean) => {
    setAcknowledged((previous) => {
      const next = new Set(previous);
      if (accepted) next.add(code);
      else next.delete(code);
      return next;
    });
  }, []);

  /**
   * STAGE TWO: the write, bound to the plan that was read.
   *
   * It takes the approval derived from the rendered plan, plus the preview the
   * target token comes from. Nothing here reconstructs an env, a cluster, a
   * digest or a release — which is what makes "what was approved is what is
   * deployed" true by construction rather than by discipline.
   */
  const handleApprove = useCallback(
    (approval: PlanApproval) => {
      const guardPlan = plan.data?.kind === "report" ? plan.data.report : null;
      if (!guardPlan) return;
      start.mutate(
        { guardPlan: guardPlan as ForgeDeployReport, approval, checkoutPath },
        {
          onSuccess: (result) => {
            if (result.kind === "started") {
              // The deploy supersedes the plan job as the thing being
              // followed.
              setPlanHandle(null);
              setWatchedHandle(result.handle);
            }
          },
        }
      );
    },
    [start, plan.data, checkoutPath]
  );

  /**
   * The plan moved under the approval. Clear the acceptances — they were given
   * for a change set that is no longer the one on offer — and plan again.
   *
   * It never re-sends the refused approval. That would approve a change set
   * nobody has read, which is the accident approving by digest exists to stop.
   */
  const handleReplanAfterStale = useCallback(() => {
    setAcknowledged(new Set());
    setWatchedHandle(null);
    setPlanHandle(null);
    start.reset();
    handleBuildAndPlan();
  }, [start, handleBuildAndPlan]);

  /** Follow a deploy that was already running, rather than racing it with a second. */
  const handleWatchRunning = useCallback((handle: string) => {
    setWatchedHandle(handle);
  }, []);

  /**
   * Re-plan after a refusal: drop the stale result, then refetch. Resetting first
   * matters — leaving the refusal in place while a new plan arrives would show a
   * fresh plan under a notice about a cluster that is no longer the one being
   * compared.
   */
  const handleReplan = useCallback(() => {
    start.reset();
    buildAndPlan.reset();
    setAcknowledged(new Set());
    setPlanHandle(null);
    void plan.refetch();
  }, [start, buildAndPlan, plan]);

  /**
   * Closing forgets the handle but does NOT stop the deploy, and the panel's
   * button says so. A detached apply keeps running; pretending otherwise would be
   * the worse lie.
   */
  const handleClose = useCallback(() => {
    start.reset();
    buildAndPlan.reset();
    setWatchedHandle(null);
    setPlanHandle(null);
    setAcknowledged(new Set());
    onClose();
  }, [start, buildAndPlan, onClose]);

  const jobPanel =
    watchedHandle && status.data ? (
      <DeployJobPanel
        handle={status.data.handle || watchedHandle}
        env={status.data.env || env}
        jobStatus={status.data.jobStatus}
        jobStatusDetail={status.data.jobStatusDetail}
        startedAt={status.data.startedAt}
        finishedAt={status.data.finishedAt}
        report={status.data.report}
        onVerify={() => verify.verify(env)}
        isVerifying={verify.pendingEnv === env}
        onClose={handleClose}
        projectName={projectName}
      />
    ) : watchedHandle ? (
      // A handle with no poll result yet. Explicitly "starting", never a state
      // that could be mistaken for a finished deploy.
      <p
        data-testid="deploy-job-connecting"
        className="rounded-lg border border-dashed border-border px-6 py-10 text-center text-sm text-muted-foreground"
      >
        A deploy is in flight. Reading its status…
      </p>
    ) : undefined;

  return (
    <Modal isOpen={isOpen} onClose={handleClose} size="xl" title={`Deploy ${env}`}>
      <DeployFlow
        planOutcome={plan.data}
        isPlanning={plan.isFetching}
        planError={plan.error as Error | null}
        startResult={start.data ?? null}
        startError={start.error as Error | null}
        isStarting={start.isPending}
        onReplan={handleReplan}
        onWatchRunning={handleWatchRunning}
        onClose={handleClose}
        projectName={projectName}
        jobPanel={jobPanel}
        // The approvable half.
        onBuildAndPlan={handleBuildAndPlan}
        isBuildingPlan={buildAndPlan.isPending || (!!planHandle && !approvablePlan)}
        planJobResult={buildAndPlan.data ?? null}
        planJobError={buildAndPlan.error as Error | null}
        planJobStatus={planHandle ? (status.data ?? null) : null}
        approvablePlan={approvablePlan}
        acknowledged={acknowledged}
        onAcknowledge={handleAcknowledge}
        onApprove={handleApprove}
        onReplanAfterStale={handleReplanAfterStale}
      />
    </Modal>
  );
}
