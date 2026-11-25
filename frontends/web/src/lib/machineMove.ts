// Copyright (c) 2025 Reliant Labs

/**
 * What stays on a machine when a project is set up on another one
 * (PreviewProjectMachineMove), in words.
 *
 * A set-up is a fresh clone: the root and every nested repository with a
 * remote, at what is pushed. So everything else stays where it is: changes
 * not committed, commits not pushed, stashes, other git worktrees, a nested
 * repository with no remote, and the branch workspaces Reliant made there
 * (with their chats). This turns the preview into the lines the set-up
 * surfaces list, and the sentence above them.
 *
 * Pure: no React, no RPCs.
 */

import {
  ProjectMachineMoveCheck,
  type PreviewProjectMachineMoveResponse,
  type ProjectMachineMoveRepo,
} from "@/gen/reliant/v1/project_pb";
import { nestedRepoLabel } from "@/lib/projectPlacement";

/** How many changed file names a line names before "and N more". */
const NAMED_FILES = 3;

export interface StayBehindItem {
  /** Stable within one preview, for React keys and tests. */
  key: string;
  text: string;
}

export interface StayBehind {
  /** The sentence above the list; null renders nothing at all. */
  summary: string | null;
  items: StayBehindItem[];
}

function plural(n: number, one: string, many = `${one}s`): string {
  return `${n} ${n === 1 ? one : many}`;
}

function repoLabel(repo: Pick<ProjectMachineMoveRepo, "name" | "relativePath">): string {
  return nestedRepoLabel({ name: repo.name || "The project", relative_path: repo.relativePath });
}

function namedFiles(repo: ProjectMachineMoveRepo): string {
  const shown = repo.changedFiles.slice(0, NAMED_FILES);
  if (shown.length === 0) return "";
  const rest = repo.changedFileCount - shown.length;
  return ` (${shown.join(", ")}${rest > 0 ? ` and ${rest} more` : ""})`;
}

/** What the machine's git state keeps there: only meaningful once CHECKED. */
function changeItems(repo: ProjectMachineMoveRepo): StayBehindItem[] {
  const label = repoLabel(repo);
  const id = repo.repoId || repo.relativePath || "root";
  const items: StayBehindItem[] = [];
  if (repo.checkError) {
    items.push({ key: `${id}:error`, text: `${label}: couldn't be checked (${repo.checkError.replace(/[.\s]+$/, "")})` });
  }
  if (repo.changedFileCount > 0) {
    items.push({
      key: `${id}:changes`,
      text: `${label}: ${plural(repo.changedFileCount, "uncommitted file")}${namedFiles(repo)}`,
    });
  }
  for (const branch of repo.unpushedBranches) {
    items.push({
      key: `${id}:branch:${branch.name}`,
      text:
        branch.unpushedCommits > 0
          ? `${label}: branch ${branch.name} has ${plural(branch.unpushedCommits, "commit")} not pushed`
          : `${label}: branch ${branch.name} is not pushed`,
    });
  }
  if (repo.stashCount > 0) {
    items.push({ key: `${id}:stash`, text: `${label}: ${plural(repo.stashCount, "stash", "stashes")}` });
  }
  for (const worktree of repo.otherWorktrees) {
    items.push({
      key: `${id}:worktree:${worktree.path}`,
      text: `${label}: git worktree ${worktree.path}${worktree.branch ? ` (${worktree.branch})` : ""}`,
    });
  }
  return items;
}

/**
 * The preview in words. `fallbackName` names the machine when the server
 * did not (an older row with no display name).
 */
export function stayBehind(preview: PreviewProjectMachineMoveResponse, fallbackName: string): StayBehind {
  const machine = preview.daemonName || fallbackName;
  if (preview.check === ProjectMachineMoveCheck.NOT_ON_MACHINE || preview.check === ProjectMachineMoveCheck.UNSPECIFIED) {
    return { summary: null, items: [] };
  }
  const checked = preview.check === ProjectMachineMoveCheck.CHECKED;
  // What Reliant knows without the machine: repositories with no remote, and
  // the branch workspaces it made there.
  const fixed: StayBehindItem[] = [
    ...preview.repos
      .filter((repo) => !repo.clonable)
      .map((repo) => ({
        key: `${repo.repoId || repo.relativePath || "root"}:no-remote`,
        text: `${repoLabel(repo)}: no remote, so it isn't cloned`,
      })),
    ...preview.workspaces.map((workspace) => ({
      key: `workspace:${workspace.worktreeId}`,
      text: `Branch workspace ${workspace.name}${workspace.branch && workspace.branch !== workspace.name ? ` (${workspace.branch})` : ""} stays here with its chats`,
    })),
  ];
  const changes = checked ? preview.repos.filter((repo) => repo.clonable).flatMap(changeItems) : [];
  const items = [...changes, ...fixed];
  switch (preview.check) {
    case ProjectMachineMoveCheck.CHECKED:
      return {
        // "Nothing is left behind" only when nothing is: a repository with no
        // remote and a branch workspace stay too, even when git reports no
        // change in any repository.
        summary:
          items.length === 0
            ? `Nothing is left behind on ${machine}: every repository is committed and pushed.`
            : changes.length === 0
              ? `Every repository's work is committed and pushed, but these stay on ${machine}:`
              : `The clone takes what's pushed. These stay on ${machine}:`,
        items,
      };
    case ProjectMachineMoveCheck.MACHINE_OFFLINE:
      return {
        summary: `${machine} is offline, so its uncommitted changes and unpushed branches can't be checked.`,
        items,
      };
    case ProjectMachineMoveCheck.MACHINE_OUTDATED:
      return { summary: `Update Reliant on ${machine} to check what stays behind.`, items };
    case ProjectMachineMoveCheck.FAILED:
      return {
        summary: preview.checkError
          ? `Couldn't check what stays on ${machine}: ${preview.checkError.replace(/[.\s]+$/, "")}.`
          : `Couldn't check what stays on ${machine}.`,
        items,
      };
    default:
      return { summary: null, items: [] };
  }
}
