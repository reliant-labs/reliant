// Copyright (c) 2025 Reliant Labs

/**
 * MERGING DUPLICATE PROJECTS: one repository that became two projects (two
 * machines added it before projects were matched by repository) can be
 * folded into one with ProjectService.MergeProjects. The server decides what
 * may merge and what moves; this module only finds the rows worth offering it
 * on, and says a merge's preview in words.
 *
 * Two rows are one repository when both are the person's OWN projects (a
 * project shared with them is not theirs to merge) and share a non-empty
 * `remoteKey` (Project.remote_key, the server's reduction of the remote to
 * lowercase `host/path`) in one org. A project with no remote, or a host-less
 * one, has no key and is never offered.
 *
 * Pure: no React, no RPCs.
 */

import type { ProjectCatalog, ProjectCatalogRow } from "./catalog";

/** What one merge moves (ProjectMergeCounts), structurally: the generated message satisfies it. */
export interface MergeCounts {
  chats: number;
  worktrees: number;
  checkouts: readonly { daemonId: string; daemonName: string }[];
  triggers: number;
  repos: number;
  otherRows: number;
}

/** How long a project scheduled for deletion can be restored (DeleteProject's window). */
export const MERGE_RESTORE_DAYS = 7;

/**
 * Each of the person's own projects that has a duplicate, keyed by project
 * id, with the other projects of its repository (in catalog order): the
 * projects it can be merged into. A project with no duplicate is absent.
 */
export function mergeTargets(catalog: ProjectCatalog | undefined): Map<string, ProjectCatalogRow[]> {
  const out = new Map<string, ProjectCatalogRow[]>();
  if (!catalog) return out;
  const groups = new Map<string, ProjectCatalogRow[]>();
  for (const row of catalog.rows) {
    if (row.kind !== "project" || row.shared || row.partial) continue;
    const key = row.remoteKey?.trim();
    if (!key) continue;
    // The catalog is one org's; the org is in the key anyway, so a merge is never offered across orgs.
    const group = `${row.project.org_id ?? catalog.orgId}\u0000${key}`;
    const rows = groups.get(group);
    if (rows) rows.push(row);
    else groups.set(group, [row]);
  }
  for (const rows of groups.values()) {
    if (rows.length < 2) continue;
    for (const row of rows) out.set(row.key, rows.filter((other) => other.key !== row.key));
  }
  return out;
}

/** The machines a project is checked out on, by name ("" with none known). */
export function machinesOf(row: Pick<ProjectCatalogRow, "checkouts">): string {
  return listOf(row.checkouts.map((c) => c.daemonName || "an unnamed machine"));
}

/**
 * The menu label for merging `source` into `target`. Two projects of one
 * repository usually share a name too, so when the name does not tell the
 * target from the source or from another target, the machines it is on do.
 */
export function mergeTargetLabel(source: ProjectCatalogRow, target: ProjectCatalogRow, targets: readonly ProjectCatalogRow[]): string {
  const ambiguous = target.name === source.name || targets.some((t) => t.key !== target.key && t.name === target.name);
  if (!ambiguous) return `Merge into ${target.name}…`;
  const machines = machinesOf(target);
  return `Merge into ${target.name} ${machines ? `on ${machines}` : "(on no machine)"}…`;
}

function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`;
}

/** "a", "a and b", "a, b and c". */
export function listOf(items: readonly string[]): string {
  if (items.length <= 1) return items[0] ?? "";
  return `${items.slice(0, -1).join(", ")} and ${items[items.length - 1]}`;
}

/**
 * What a merge moves onto `targetName`, one line each, in words. A zero
 * count is left out, so an empty project's merge has no lines.
 */
export function mergeMoveLines(counts: MergeCounts | undefined, targetName: string): string[] {
  if (!counts) return [];
  const lines: string[] = [];
  if (counts.chats > 0) lines.push(plural(counts.chats, "chat", "chats"));
  // "Workspace" is a git worktree in the product's words (projectCopy.ts).
  if (counts.worktrees > 0) lines.push(plural(counts.worktrees, "workspace", "workspaces"));
  if (counts.checkouts.length > 0) {
    const names = listOf(counts.checkouts.map((c) => c.daemonName || "an unnamed machine"));
    lines.push(
      counts.checkouts.length === 1
        ? `Its checkout on ${names}, which becomes ${targetName}’s at the same path`
        : `Its checkouts on ${names}, which become ${targetName}’s at the same paths`,
    );
  }
  if (counts.triggers > 0) lines.push(plural(counts.triggers, "automation", "automations"));
  if (counts.repos > 0) lines.push(plural(counts.repos, "repository", "repositories"));
  if (counts.otherRows > 0) lines.push(`${plural(counts.otherRows, "other record", "other records")} (settings, configs, catalogs and the like)`);
  return lines;
}
