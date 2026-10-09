import type { DiscoveredWorktree } from "../../api/worktree-grpc";

export interface RepoGroup {
  repoId: string;
  repoName: string;
  items: DiscoveredWorktree[];
}

/** Groups checkouts by repo, preserving first-seen repo order. */
export function groupByRepo(worktrees: DiscoveredWorktree[]): RepoGroup[] {
  const groups = new Map<string, RepoGroup>();
  for (const w of worktrees) {
    let group = groups.get(w.repo_id);
    if (!group) {
      group = { repoId: w.repo_id, repoName: w.repo_name || w.repo_id, items: [] };
      groups.set(w.repo_id, group);
    }
    group.items.push(w);
  }
  return Array.from(groups.values());
}

export function adoptConfirmation(
  entry: DiscoveredWorktree,
  workspacesRoot: string,
  name: string
): string {
  if (!entry.moves_on_import) {
    return `Reliant will track ${entry.path} as a workspace. Archiving it later lets Reliant remove the directory, after saving any unpushed work.`;
  }
  const others = entry.branch
    ? ` Reliant also creates checkouts of the project's other repos on branch ${entry.branch}.`
    : " Reliant also creates checkouts of the project's other repos.";
  return `This moves ${entry.path} to ${workspacesRoot}/${name}-…/${entry.repo_name}. Anything running in that directory — a terminal, an agent, a dev server — will lose its working directory.${others}`;
}

export function lockedMoveExplanation(entry: DiscoveredWorktree): string {
  return `Git will not move a locked worktree. Run \`git worktree unlock ${entry.path}\` first, then reopen this dialog.`;
}

export function isAdoptBlocked(entry: DiscoveredWorktree): boolean {
  return entry.locked && entry.moves_on_import;
}

export function shouldShowHint(count: number, dismissedCount: number): boolean {
  return count > 0 && count > dismissedCount;
}

export function hintLabel(count: number): string {
  return `${count} ${count === 1 ? "worktree" : "worktrees"} made outside Reliant`;
}
