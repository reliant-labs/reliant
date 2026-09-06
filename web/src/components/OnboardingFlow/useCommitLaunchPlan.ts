/**
 * The terminal steps' handle on the commit point.
 *
 * All three of them (ProjectChoiceStep, ProjectPickerStep, GitHubConnectStep)
 * finish the same way — confirm onboarding server-side, then commit the plan —
 * and each used to re-derive its own version of "and now show a gate". One hook
 * means one place decides when the commit fires, which is the property the
 * design rests on.
 *
 * ── The seam the billing step will call ──
 *
 * A consolidated billing step lands the user on the commit AFTER payment is
 * server-confirmed. It calls exactly this: `commit(plan)`, once, from its own
 * confirmation handler. Nothing about that call is onboarding-step-specific —
 * the key comes from the plan, so the billing step and a terminal step
 * committing the same plan are the SAME commit, not two.
 *
 * When the commit moves server-side (a `CommitLaunchPlan` RPC plus
 * `GetCommitStatus` polling), this hook's body changes and its signature does
 * not.
 */
import { useCallback, useState } from "react";
import {
  commitLaunchPlan,
  ensureCommitKey,
  retryCommit,
  type CommitResult,
} from "./commitLaunchPlan";
import type { LaunchPlan } from "./types";

export interface UseCommitLaunchPlan {
  /** The settled commit, or null before one has been asked for. */
  commit: CommitResult | null;
  running: boolean;
  /** Fire the commit. Idempotent on the plan's `commitKey`. */
  runCommit: (plan: Partial<LaunchPlan>) => Promise<CommitResult>;
  /** Discard a failed commit and run it again. User-initiated only. */
  retry: (plan: Partial<LaunchPlan>) => Promise<CommitResult>;
}

export function useCommitLaunchPlan(
  updatePlan: (updates: Partial<LaunchPlan>) => Promise<void> | void,
): UseCommitLaunchPlan {
  const [commit, setCommit] = useState<CommitResult | null>(null);
  const [running, setRunning] = useState(false);

  const runCommit = useCallback(
    async (plan: Partial<LaunchPlan>) => {
      setRunning(true);
      try {
        // Mint the key into the URL before committing, not after: a reload
        // between the two would otherwise come back keyless and commit again.
        const commitKey = await ensureCommitKey(plan, updatePlan);
        const result = await commitLaunchPlan({ ...plan, commitKey });
        setCommit(result);
        return result;
      } finally {
        setRunning(false);
      }
    },
    [updatePlan],
  );

  const retry = useCallback(
    async (plan: Partial<LaunchPlan>) => {
      if (plan.commitKey) retryCommit(plan.commitKey);
      return runCommit(plan);
    },
    [runCommit],
  );

  return { commit, running, runCommit, retry };
}
