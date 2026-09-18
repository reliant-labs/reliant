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

import { useCallback, useState } from "react";

import { Modal } from "@/components/ui/Modal";
import {
  useForgeDeployPlan,
  useForgeDeployStatus,
  useStartForgeDeploy,
  useVerifyForgeEnv,
} from "@/hooks/forge-queries";
import type { ForgeDeployReport } from "@/services/forge/deploy";

import { DeployFlow } from "./DeployFlow";
import { DeployJobPanel } from "./DeployJobPanel";

export interface DeployDialogProps {
  isOpen: boolean;
  onClose: () => void;
  projectId: string | null | undefined;
  env: string;
  projectName?: string;
}

export function DeployDialog({
  isOpen,
  onClose,
  projectId,
  env,
  projectName,
}: DeployDialogProps) {
  /**
   * The handle being FOLLOWED. Set by a start, or by an already-running refusal
   * naming a deploy that was already in flight. It is deliberately not derived
   * from the start mutation's data alone — watching someone else's deploy is a
   * legitimate state that no start of ours produced.
   */
  const [watchedHandle, setWatchedHandle] = useState<string | null>(null);

  // Only enabled while open and while no job is being followed: once a deploy is
  // in flight the plan is not the thing on screen, and re-reading the live target
  // underneath a running apply buys nothing.
  const plan = useForgeDeployPlan(isOpen && !watchedHandle ? projectId : null, env);
  const start = useStartForgeDeploy(projectId);
  const status = useForgeDeployStatus(projectId, watchedHandle);
  const verify = useVerifyForgeEnv(projectId);

  /**
   * The write. It takes the plan document that was rendered and passes it
   * straight through — the token, including the declared context, is derived
   * inside the mutation from this object. Nothing here reconstructs an env or a
   * cluster name, which is what makes the token match the plan by construction.
   */
  const handleConfirm = useCallback(
    (rendered: ForgeDeployReport) => {
      start.mutate(rendered, {
        onSuccess: (result) => {
          if (result.kind === "started") setWatchedHandle(result.handle);
        },
      });
    },
    [start]
  );

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
    void plan.refetch();
  }, [start, plan]);

  /**
   * Closing forgets the handle but does NOT stop the deploy, and the panel's
   * button says so. A detached apply keeps running; pretending otherwise would be
   * the worse lie.
   */
  const handleClose = useCallback(() => {
    start.reset();
    setWatchedHandle(null);
    onClose();
  }, [start, onClose]);

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
        onConfirm={handleConfirm}
        onReplan={handleReplan}
        onWatchRunning={handleWatchRunning}
        onClose={handleClose}
        projectName={projectName}
        jobPanel={jobPanel}
      />
    </Modal>
  );
}
