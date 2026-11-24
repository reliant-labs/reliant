// Copyright (c) 2025 Reliant Labs

/**
 * WorktreeService: shop's main checkout and its two branches (the /checkout
 * rate limit, the Stripe webhook retries), and docs' main checkout. Only the
 * reads the app makes on load are answered: the git reads (status, changes,
 * commits) run on a machine and are not part of the pack.
 */
import { Code, ConnectError } from "@connectrpc/connect";

import { WorktreeService } from "@/gen/reliant/v1/worktree_pb";

import { worktrees } from "../../workspace";
import { serve } from "../serve";

export function worktreeHandlers() {
  return serve(WorktreeService, {
    listWorktrees: (req) => {
      const list = worktrees().filter((w) => (!req.projectId || w.projectId === req.projectId) && (!req.mainOnly || w.isMain));
      return { worktrees: list, total: list.length };
    },
    getWorktree: (req) => {
      const worktree = worktrees().find((w) => w.id === req.worktreeId);
      if (!worktree) throw new ConnectError("worktree not found", Code.NotFound);
      return { worktree };
    },
  });
}
