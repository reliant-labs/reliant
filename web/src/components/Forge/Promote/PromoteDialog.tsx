// Copyright (c) 2025 Reliant Labs

/**
 * The promote flow's container: owns the hooks, PromoteFlow owns the pixels.
 *
 * Splitting them mirrors ForgeTopologyPage/TopologyView, and here it earns its
 * keep twice over — the refusal path and the applied path are both states that
 * are painful to reach against a live daemon, and the whole of PromoteFlow can be
 * driven from fixtures because this is the only part that talks to react-query.
 *
 * The dialog is also what enforces that a plan is FRESH. Opening mounts the
 * query with a zero staleTime, and closing clears the write's result, so the next
 * open cannot show a stale diff or — much worse — a stale confirmation token
 * carried over from a previous session of this dialog.
 */

import { useCallback } from "react";

import { Modal } from "@/components/ui/Modal";
import { useApplyForgePromote, useForgePromotePlan } from "@/hooks/forge-queries";
import type { ForgePromotePlan } from "@/services/forge/promote";

import { PromoteFlow } from "./PromoteFlow";

export interface PromoteDialogProps {
  isOpen: boolean;
  onClose: () => void;
  projectId: string | null | undefined;
  env: string;
  /** The already-built release to preview binding to. */
  release: string;
  projectName?: string;
}

export function PromoteDialog({
  isOpen,
  onClose,
  projectId,
  env,
  release,
  projectName,
}: PromoteDialogProps) {
  // Only enabled while open, so a closed dialog is not holding a plan — and a
  // reopen recomputes rather than resurrecting one.
  const plan = useForgePromotePlan(isOpen ? projectId : null, env, release);
  const apply = useApplyForgePromote(projectId);

  /**
   * The write. It takes the plan document that was rendered and passes it
   * straight through — the token is derived inside the mutation, from this
   * object. Nothing here reconstructs an env or a release, which is what makes
   * the token match the diff by construction.
   */
  const handleConfirm = useCallback(
    (rendered: ForgePromotePlan) => {
      apply.mutate(rendered);
    },
    [apply]
  );

  /**
   * Re-plan after a refusal: drop the stale result, then refetch. Resetting
   * first matters — leaving the refusal in place while a new plan arrives would
   * show a fresh diff under a notice about a binding that is no longer the one
   * being compared.
   */
  const handleReplan = useCallback(() => {
    apply.reset();
    void plan.refetch();
  }, [apply, plan]);

  const handleClose = useCallback(() => {
    apply.reset();
    onClose();
  }, [apply, onClose]);

  return (
    <Modal
      isOpen={isOpen}
      onClose={handleClose}
      size="lg"
      title={`Promote ${env} to ${release}`}
    >
      <PromoteFlow
        planOutcome={plan.data}
        isPlanning={plan.isFetching}
        planError={plan.error as Error | null}
        applyResult={apply.data ?? null}
        applyError={apply.error as Error | null}
        isApplying={apply.isPending}
        onConfirm={handleConfirm}
        onReplan={handleReplan}
        onClose={handleClose}
        projectName={projectName}
      />
    </Modal>
  );
}
