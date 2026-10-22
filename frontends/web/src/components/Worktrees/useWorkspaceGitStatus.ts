import { useQuery } from "@tanstack/react-query";
import { worktreeGrpc } from "../../api/worktree-grpc";

/**
 * A workspace's git status, cached per workspace. The status is computed by
 * the daemon against the worktree on disk, so this fails whenever no machine
 * is attached — callers render that as "unavailable", never as an error
 * state, and it is not retried.
 */
export function useWorkspaceGitStatus(worktreeId: string | undefined, enabled = true) {
  return useQuery({
    queryKey: ["worktree", "git-status", worktreeId],
    queryFn: () => worktreeGrpc.getGitStatus(worktreeId!),
    enabled: enabled && !!worktreeId,
    staleTime: 30_000,
    retry: false,
    refetchOnWindowFocus: false,
  });
}
