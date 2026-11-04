import { useCallback, useEffect, useState } from "react";
import { useDiscoveredWorktrees } from "../../hooks/worktree-queries";
import { shouldShowHint } from "./discoverWorktreesUtils";

const dismissKey = (projectId: string) => `reliant.worktreeHint.dismissed.${projectId}`;

function readDismissed(projectId: string): number {
  try {
    const n = Number(localStorage.getItem(dismissKey(projectId)));
    return Number.isFinite(n) ? n : 0;
  } catch {
    return 0;
  }
}

/** Background count of checkouts made outside Reliant; failures are logged, never surfaced. */
export function useExternalWorktreeHint(projectId: string | undefined) {
  // background: a hint must never wake a sleeping machine. A failure (no
  // machine, an older daemon) hides the hint; it is never surfaced.
  const { data } = useDiscoveredWorktrees(projectId, { background: true, staleTime: 60_000 });
  const count = data?.discovered.length ?? 0;

  const [dismissed, setDismissed] = useState(0);
  useEffect(() => {
    setDismissed(projectId ? readDismissed(projectId) : 0);
  }, [projectId]);

  const dismiss = useCallback(() => {
    if (!projectId) return;
    try {
      localStorage.setItem(dismissKey(projectId), String(count));
    } catch {
      /* storage unavailable: dismissal lasts for this session only */
    }
    setDismissed(count);
  }, [projectId, count]);

  return { count, visible: shouldShowHint(count, dismissed), dismiss };
}
