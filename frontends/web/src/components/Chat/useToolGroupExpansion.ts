import { useEffect, useRef, useState } from "react";

export interface ToolGroupActivity {
  /** A call in the group is preparing, running, or waiting to start. */
  live: boolean;
  /** A call in the group failed. */
  failed: boolean;
  /** A call in the group is waiting on the user's approval. */
  pendingApproval: boolean;
  /**
   * The surface collapses tool calls whatever their state (a phone, an
   * embed): only an approval, which needs the user, opens the group there.
   */
  collapseAll?: boolean;
}

/**
 * Whether a tool group is open when the user has not chosen: open while its
 * work is live (so the transcript shows what the agent is doing now), while
 * it needs an approval, or when a call in it failed; closed once everything
 * in it finished cleanly, so finished work recedes.
 */
export function toolGroupAutoExpanded({ live, failed, pendingApproval, collapseAll }: ToolGroupActivity): boolean {
  if (pendingApproval) return true;
  if (collapseAll) return false;
  return live || failed;
}

/**
 * A tool group's open state: the automatic rule above until the user toggles
 * it, then theirs. A new approval re-opens the group even after the user
 * closed it, because it cannot be answered from a closed group.
 */
export function useToolGroupExpansion(activity: ToolGroupActivity) {
  const [override, setOverride] = useState<boolean | null>(null);
  const auto = toolGroupAutoExpanded(activity);

  const hadApproval = useRef(activity.pendingApproval);
  useEffect(() => {
    if (activity.pendingApproval && !hadApproval.current) setOverride(null);
    hadApproval.current = activity.pendingApproval;
  }, [activity.pendingApproval]);

  const expanded = override ?? auto;
  return { expanded, toggle: () => setOverride(!expanded) };
}
