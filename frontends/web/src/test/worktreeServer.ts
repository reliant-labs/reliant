/**
 * An in-memory WorktreeService for tests: `mockTransport` handlers backed by a
 * list of rows, so a test drives the real generated hooks and the lifecycle
 * actions (hooks/worktree-actions.ts) through the transport instead of mocking
 * a client.
 *
 *   const server = worktreeServer([buildWorktree({ id: "wt-main", is_main: true })]);
 *   setTransport(mockTransport(server.handlers));
 *   ...
 *   expect(server.calls("ListWorktrees")).toBe(1);
 *
 * Paging follows the real server: `limit` rows per page (fewer when
 * `options.pageSize` caps it), main first, and an opaque next_page_token (the
 * next offset). Archive sets deleted_at; delete
 * archives a live row and removes an archived one; unarchive/recreate clear
 * deleted_at. `fail(rpc, error)` makes one rpc reject until `fail(rpc, null)`.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import { WorktreeStatus, type Worktree as ProtoWorktree } from "@/gen/reliant/v1/worktree_pb";
import type { Worktree } from "@/hooks/worktree-queries";
import type { MockHandlers } from "@/lib/test-utils";
import { buildWorktree, worktreeProto } from "./worktreeCache";

type Req = Record<string, unknown>;

export interface WorktreeServer {
  handlers: MockHandlers;
  rows: Worktree[];
  calls: (rpc: string) => number;
  requests: (rpc: string) => Req[];
  fail: (rpc: string, error: Error | null) => void;
  update: (id: string, patch: Partial<Worktree>) => void;
}

const now = "2026-01-01T00:00:00.000Z";

export function worktreeServer(
  initial: Partial<Worktree>[] = [],
  // pageSize caps a page below the request's `limit`, as a real server may:
  // a paging test then needs a handful of rows, not a hundred rendered ones.
  options?: { createStatus?: WorktreeStatus; pageSize?: number },
): WorktreeServer {
  const rows: Worktree[] = initial.map((w) => buildWorktree(w));
  const counts: Record<string, Req[]> = {};
  const failures = new Map<string, Error>();
  let nextId = 1;

  const record = (rpc: string, req: Req) => {
    (counts[rpc] ??= []).push(req);
    const failure = failures.get(rpc);
    if (failure) throw failure;
  };
  const find = (id: unknown) => rows.find((w) => w.id === id);
  const notFound = (id: unknown) => new ConnectError(`worktree ${String(id)} not found`, Code.NotFound);
  const page = (list: Worktree[], req: Req) => {
    const limit = Math.min(Number(req.limit) || 100, options?.pageSize ?? Infinity);
    const offset = Number(req.pageToken || 0);
    const slice = list.slice(offset, offset + limit);
    const next = offset + limit < list.length ? String(offset + limit) : "";
    return { worktrees: slice.map(worktreeProto), total: list.length, nextPageToken: next };
  };
  const live = (projectId: unknown) =>
    rows
      .filter((w) => w.project_id === projectId && !w.deleted_at)
      .sort((a, b) => Number(b.is_main) - Number(a.is_main));

  const handlers: MockHandlers = {
    "WorktreeService.ListWorktrees": (req) => {
      const r = req as Req;
      record("ListWorktrees", r);
      const list = live(r.projectId);
      return r.mainOnly ? { worktrees: list.filter((w) => w.is_main).slice(0, 1).map(worktreeProto), total: 1 } : page(list, r);
    },
    "WorktreeService.ListArchivedWorktrees": (req) => {
      const r = req as Req;
      record("ListArchivedWorktrees", r);
      return page(rows.filter((w) => w.project_id === r.projectId && w.deleted_at), r);
    },
    "WorktreeService.GetWorktree": (req) => {
      const r = req as Req;
      record("GetWorktree", r);
      const row = find(r.worktreeId);
      if (!row) throw notFound(r.worktreeId);
      return { worktree: worktreeProto(row) };
    },
    "WorktreeService.CreateWorktree": (req) => {
      const r = req as Req;
      record("CreateWorktree", r);
      const row = buildWorktree({
        id: `wt-new-${nextId++}`,
        name: String(r.name),
        branch: String(r.branch),
        base_branch: String(r.baseBranch ?? ""),
        project_id: String(r.projectId),
        status: options?.createStatus ?? WorktreeStatus.CREATING,
        path: options?.createStatus === WorktreeStatus.ACTIVE ? `/tmp/${String(r.name)}` : "",
        created_at: now,
      });
      rows.push(row);
      return { worktree: worktreeProto(row) };
    },
    "WorktreeService.ImportWorktree": (req) => {
      const r = req as Req;
      record("ImportWorktree", r);
      const row = buildWorktree({
        id: `wt-imported-${nextId++}`,
        name: String(r.name || r.path),
        path: String(r.path),
        project_id: String(r.projectId),
      });
      rows.push(row);
      return { worktree: worktreeProto(row) };
    },
    "WorktreeService.ArchiveWorktree": (req) => {
      const r = req as Req;
      record("ArchiveWorktree", r);
      const row = find(r.worktreeId);
      if (!row) throw notFound(r.worktreeId);
      row.deleted_at = now;
      return { message: "archived", deletedBranch: false };
    },
    "WorktreeService.DeleteWorktree": (req) => {
      const r = req as Req;
      record("DeleteWorktree", r);
      const row = find(r.worktreeId);
      if (!row) throw notFound(r.worktreeId);
      if (row.deleted_at) {
        rows.splice(rows.indexOf(row), 1);
        return { message: "deleted", isPermanentDelete: true };
      }
      row.deleted_at = now;
      return { message: "archived", isPermanentDelete: false };
    },
    "WorktreeService.UnarchiveWorktree": (req) => {
      const r = req as Req;
      record("UnarchiveWorktree", r);
      const row = find(r.worktreeId);
      if (!row) throw notFound(r.worktreeId);
      row.deleted_at = null;
      return { message: "restored" };
    },
    "WorktreeService.RecreateWorktree": (req) => {
      const r = req as Req;
      record("RecreateWorktree", r);
      const row = find(r.worktreeId);
      if (!row) throw notFound(r.worktreeId);
      row.deleted_at = null;
      row.cleanup_metadata = null;
      return { message: "recreated", path: row.path, branch: row.branch, snapshotWarning: "", snapshotRefs: [] };
    },
    "WorktreeService.UpdateWorktree": (req) => {
      const r = req as Req;
      record("UpdateWorktree", r);
      const row = find(r.worktreeId);
      if (!row) throw notFound(r.worktreeId);
      if (r.status !== undefined) row.status = r.status as WorktreeStatus;
      return { worktree: worktreeProto(row) };
    },
    "WorktreeService.PruneWorktrees": (req) => {
      record("PruneWorktrees", req as Req);
      return { pruned: [] };
    },
    "WorktreeService.DiscoverWorktrees": (req) => {
      record("DiscoverWorktrees", req as Req);
      return { discovered: [], stale: [], workspacesRoot: "/tmp/workspaces" };
    },
  };

  return {
    handlers,
    rows,
    calls: (rpc) => counts[rpc]?.length ?? 0,
    requests: (rpc) => counts[rpc] ?? [],
    fail: (rpc, error) => {
      if (error) failures.set(rpc, error);
      else failures.delete(rpc);
    },
    update: (id, patch) => {
      const row = find(id);
      if (row) Object.assign(row, patch);
    },
  };
}

export type { ProtoWorktree };
