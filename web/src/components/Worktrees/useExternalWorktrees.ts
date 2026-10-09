import { useCallback, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { worktreeGrpc } from "../../api/worktree-grpc";
import { logger } from "../../lib/logger";
import { shouldShowHint } from "./discoverWorktreesUtils";

export const externalWorktreesKey = (projectId: string) =>
  ["external-worktrees", projectId] as const;

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
  const { data: count = 0 } = useQuery({
    queryKey: externalWorktreesKey(projectId ?? ""),
    enabled: !!projectId,
    staleTime: 60_000,
    retry: false,
    queryFn: async () => {
      try {
        // background: a hint must never wake a sleeping machine.
        const res = await worktreeGrpc.discover(projectId!, { background: true });
        return res.discovered.length;
      } catch (err) {
        logger.warn("[WorktreesHint] discover failed; hiding hint", err);
        return 0;
      }
    },
  });

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
