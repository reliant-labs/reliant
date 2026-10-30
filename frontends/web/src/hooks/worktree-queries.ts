// Copyright (c) 2025 Reliant Labs

/**
 * Workspaces (`reliant.v1.WorktreeService`) on the generated hooks and keys.
 *
 * The query cache is the one copy of every workspace row: the cache holds what
 * the generated hooks fetch (the proto responses) under `worktreeServiceKeys`,
 * and components read the plain frontend types mapped from it. No zustand
 * store holds a workspace; `worktreeStore` holds only WHICH one is selected
 * (its id), and every reader resolves that id against this cache, so a
 * selection can never freeze at the row the server sent when it was made —
 * a workspace selected straight out of CreateWorktree (CREATING, no path yet)
 * shows its path the moment the settle refetch lands.
 *
 * A row can be cached under four reads — the live list (one infinite entry per
 * project), the main-workspace read, the archived list, and GetWorktree by id.
 * {@link getCachedWorktree} reads the NEWEST of them, so a write that refetches
 * one row by id outranks a list page fetched before it.
 *
 * Stream: `worktree_changes` names one workspace. It fires when that
 * workspace's files change (every file-mutating tool call), when its creation
 * settles, and when it is archived or restored behind the user's back (the
 * worktree tool, the sweeper, archiving its chat). Each event invalidates the
 * reads that name that workspace plus every workspace list — never another
 * workspace's diffs, which are daemon round trips.
 */

import { useCallback, useMemo, useRef, useSyncExternalStore } from "react";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  partialMatchKey,
  useQueryClient,
  type InfiniteData,
  type Query,
  type QueryClient,
} from "@tanstack/react-query";

import { FileChangeStatus } from "../gen/reliant/v1/common_pb";
import {
  GetWorktreeResponseSchema,
  WorktreeStatus,
  type CleanupMetadata as ProtoCleanupMetadata,
  type DiscoveredWorktree as ProtoDiscoveredWorktree,
  type DiscoverWorktreesResponse,
  type GetWorktreeResponse,
  type ListArchivedWorktreesResponse,
  type ListWorktreesResponse,
  type StaleWorktree as ProtoStaleWorktree,
  type Worktree as ProtoWorktree,
} from "../gen/reliant/v1/worktree_pb";
import { logger } from "../lib/logger";
import { queryClient as appQueryClient } from "../lib/query-client";
import { toast } from "../lib/toast-manager";
import { subscribeToRefetch, type RefetchEvent } from "../store/refetchStore";
import { useProjectStore } from "../store/projectStore";
import { useWorktreeStore } from "../store/worktreeStore";
import {
  useDiscoverWorktrees,
  useGetWorktree,
  useListArchivedWorktreesInfinite,
  useListWorktrees,
  useListWorktreesInfinite,
  worktreeServiceKeys,
  worktreeServiceQueries,
} from "./worktree-service-hooks_gen";

export { WorktreeStatus, FileChangeStatus };

// ── Frontend types ──────────────────────────────────────────────────────────

export interface CleanupMetadata {
  directory_deleted: boolean;
  branch_deleted: boolean;
  /** Why the machine kept the files ("dirty", "data", ...); empty once removed. */
  held_reason?: string;
  held_detail?: string;
  /** Local git refs holding work saved before the files were removed. */
  snapshot_refs?: string[];
  /**
   * Regenerable files (node_modules, build output, tool caches) deleted from
   * kept files, and at least how many bytes that freed. Restoring the
   * workspace needs its install/generate step again.
   */
  stripped_paths?: string[];
  stripped_bytes?: number;
}

export interface Worktree {
  id: string;
  name: string;
  path: string;
  branch: string;
  base_branch: string;
  project_id?: string;
  chat_id?: string;
  session_id?: string;
  status: WorktreeStatus;
  is_main: boolean; // TRUE for the main/default project worktree
  created_at: string;
  updated_at: string;
  last_active: string;
  deleted_at?: string | null; // Archive timestamp
  cleanup_metadata?: CleanupMetadata | null; // Tracks what was deleted during archive
  /**
   * Why creation failed, in words the user can act on. Set only on a FAILED
   * worktree whose failure is the user's to fix (a base branch that does not
   * exist, a branch name git refuses); absent when the failure was ours.
   */
  failure_reason?: string;
  /**
   * The machine whose disk holds this workspace's checkout. A chat in it runs
   * there and nowhere else (lib/workspaceMachine.ts). Absent for the main
   * checkout, which every machine with the project has, and for old rows.
   */
  daemon_id?: string;
}

// Create-only inputs. base_branches carries per-repo base-branch overrides
// (key = repo_id) for multi-repo projects; the server resolves them into each
// repo's checkout and does not echo them back on the Worktree. copy_files are
// exact paths from the workspace root (".env", "web/node_modules") — never
// searched for.
export type CreateWorktreeInput = Partial<Worktree> & {
  copy_files?: string[];
  force?: boolean;
  source_worktree_id?: string;
  base_branches?: Record<string, string>;
};

export interface DiscoveredWorktree {
  path: string;
  name: string;
  branch: string;
  repo_id: string;
  repo_name: string;
  head: string;
  locked: boolean;
  moves_on_import: boolean;
}

export interface StaleWorktree {
  path: string;
  repo_id: string;
  repo_name: string;
  reason: string;
}

export interface DiscoveredWorktrees {
  discovered: DiscoveredWorktree[];
  stale: StaleWorktree[];
  workspacesRoot: string;
}

// ── Proto mapping ───────────────────────────────────────────────────────────

function cleanupFromProto(proto: ProtoCleanupMetadata): CleanupMetadata {
  return {
    directory_deleted: proto.directoryDeleted,
    branch_deleted: proto.branchDeleted,
    held_reason: proto.heldReason || undefined,
    held_detail: proto.heldDetail || undefined,
    snapshot_refs: proto.snapshotRefs.length > 0 ? [...proto.snapshotRefs] : undefined,
    stripped_paths: proto.strippedPaths.length > 0 ? [...proto.strippedPaths] : undefined,
    stripped_bytes: proto.strippedBytes > 0n ? Number(proto.strippedBytes) : undefined,
  };
}

/** One proto workspace row as the frontend type. */
export function worktreeFromProto(proto: ProtoWorktree): Worktree {
  return {
    id: proto.id,
    name: proto.name,
    path: proto.path,
    branch: proto.branch,
    base_branch: proto.baseBranch,
    project_id: proto.projectId,
    chat_id: proto.chatId || undefined,
    status: proto.status,
    is_main: proto.isMain,
    created_at: proto.createdAt,
    updated_at: proto.updatedAt,
    last_active: proto.lastActive,
    deleted_at: proto.deletedAt || null,
    cleanup_metadata: proto.cleanupMetadata ? cleanupFromProto(proto.cleanupMetadata) : null,
    failure_reason: proto.failureReason || undefined,
    daemon_id: proto.daemonId || undefined,
  };
}

function discoveredFromProto(proto: ProtoDiscoveredWorktree): DiscoveredWorktree {
  return {
    path: proto.path,
    name: proto.name,
    branch: proto.branch,
    repo_id: proto.repoId,
    repo_name: proto.repoName,
    head: proto.head,
    locked: proto.locked,
    moves_on_import: proto.movesOnImport,
  };
}

export function staleFromProto(proto: ProtoStaleWorktree): StaleWorktree {
  return {
    path: proto.path,
    repo_id: proto.repoId,
    repo_name: proto.repoName,
    reason: proto.reason,
  };
}

function discoveredWorktreesFromProto(response: DiscoverWorktreesResponse): DiscoveredWorktrees {
  return {
    discovered: response.discovered.map(discoveredFromProto),
    stale: response.stale.map(staleFromProto),
    workspacesRoot: response.workspacesRoot,
  };
}

// ── Canonical requests ──────────────────────────────────────────────────────
//
// Every reader of one listing must send the identical request, or the reads
// split into separate cache entries that invalidate independently.

export const WORKTREE_PAGE_SIZE = 100;

/** The project's live (unarchived) workspaces, main first, paged. */
export const liveWorktreesRequest = (projectId: string) => ({ projectId, limit: WORKTREE_PAGE_SIZE });

/** Just the project's main workspace (the server creates it when missing). */
export const mainWorktreeRequest = (projectId: string) => ({ projectId, mainOnly: true, limit: 1 });

/** The project's archived workspaces, most recently archived first, paged. */
export const archivedWorktreesRequest = (projectId: string) => ({ projectId, limit: WORKTREE_PAGE_SIZE });

export const getWorktreeRequest = (worktreeId: string) => ({ worktreeId });


// ── Reading the cache ───────────────────────────────────────────────────────

type ListResponse = ListWorktreesResponse | ListArchivedWorktreesResponse;

function isMessage(data: unknown): data is { $typeName: string } {
  return typeof data === "object" && data !== null && "$typeName" in data;
}

/** Every workspace row one cache entry holds, whatever read it came from. */
function rowsIn(data: unknown): ProtoWorktree[] {
  if (typeof data !== "object" || data === null) return [];
  if ("pages" in data && Array.isArray((data as InfiniteData<unknown>).pages)) {
    return (data as InfiniteData<unknown>).pages.flatMap(rowsIn);
  }
  if (!isMessage(data)) return [];
  switch (data.$typeName) {
    case "reliant.v1.GetWorktreeResponse": {
      const row = (data as GetWorktreeResponse).worktree;
      return row ? [row] : [];
    }
    case "reliant.v1.ListWorktreesResponse":
    case "reliant.v1.ListArchivedWorktreesResponse":
      return (data as ListResponse).worktrees;
    default:
      return [];
  }
}

function worktreeQueries(qc: QueryClient): Query[] {
  return qc.getQueryCache().findAll({ queryKey: worktreeServiceKeys.all });
}

/** The newest cached proto row for this id, and when it was fetched. */
function newestCachedRow(qc: QueryClient, worktreeId: string): { row: ProtoWorktree; at: number } | undefined {
  let best: { row: ProtoWorktree; at: number } | undefined;
  for (const query of worktreeQueries(qc)) {
    const row = rowsIn(query.state.data).find((w) => w.id === worktreeId);
    if (row && (!best || query.state.dataUpdatedAt > best.at)) {
      best = { row, at: query.state.dataUpdatedAt };
    }
  }
  return best;
}

/**
 * The workspace with this id as the cache last heard it, from whichever read
 * fetched it most recently; undefined when no read holds it. For imperative
 * code. Components use {@link useWorktreeById}, which also fetches a miss.
 */
export function getCachedWorktree(worktreeId: string, qc: QueryClient = appQueryClient): Worktree | undefined {
  const found = newestCachedRow(qc, worktreeId);
  return found ? worktreeFromProto(found.row) : undefined;
}

/** The project's main workspace as cached, from any read that holds it. */
export function getCachedMainWorktree(projectId: string, qc: QueryClient = appQueryClient): Worktree | undefined {
  let best: { row: ProtoWorktree; at: number } | undefined;
  for (const query of worktreeQueries(qc)) {
    const row = rowsIn(query.state.data).find((w) => w.isMain && w.projectId === projectId);
    if (row && (!best || query.state.dataUpdatedAt > best.at)) best = { row, at: query.state.dataUpdatedAt };
  }
  return best ? worktreeFromProto(best.row) : undefined;
}

/**
 * Every workspace the cache holds — the main ones, whatever list pages were
 * loaded, any fetched by id — newest row per id, optionally for one project.
 * NOT every workspace of the project: a project can hold hundreds, and nothing
 * pages them in unless it lists them. For summaries over what the app already
 * knows (the tray menu, the onboarding checklist) that must never fetch.
 */
export function getCachedWorktrees(projectId?: string, qc: QueryClient = appQueryClient): Worktree[] {
  const newest = new Map<string, { row: ProtoWorktree; at: number }>();
  for (const query of worktreeQueries(qc)) {
    for (const row of rowsIn(query.state.data)) {
      if (projectId && row.projectId !== projectId) continue;
      const held = newest.get(row.id);
      if (!held || query.state.dataUpdatedAt > held.at) newest.set(row.id, { row, at: query.state.dataUpdatedAt });
    }
  }
  return [...newest.values()].map(({ row }) => worktreeFromProto(row));
}

/**
 * Whether a read of the project's workspaces (its main workspace or a list
 * page) has settled, successfully or not — before that, an empty
 * {@link getCachedWorktrees} means "not loaded", not "has none".
 */
export function hasLoadedWorktrees(projectId: string, qc: QueryClient = appQueryClient): boolean {
  return worktreeQueries(qc).some((query) => {
    if (query.state.status === "pending") return false;
    if (!isListQuery(query)) return false;
    return requestOf(query)?.projectId === projectId;
  });
}

/** True when a cache event is about a WorktreeService read. */
export function isWorktreeQueryKey(queryKey: readonly unknown[]): boolean {
  return partialMatchKey(queryKey, worktreeServiceKeys.all);
}

/** The selected workspace, resolved against the cache. */
export function getCurrentWorktree(qc: QueryClient = appQueryClient): Worktree | null {
  const id = useWorktreeStore.getState().currentWorktreeId;
  return id ? getCachedWorktree(id, qc) ?? null : null;
}

/**
 * True when the server has said this workspace does not exist (or is not the
 * caller's). A failed fetch is NOT that: a network error leaves it unknown, and
 * acting on absence would discard live state.
 */
export function isWorktreeKnownGone(worktreeId: string, qc: QueryClient = appQueryClient): boolean {
  const error = qc.getQueryState(worktreeServiceKeys.getWorktree(getWorktreeRequest(worktreeId)))?.error;
  return error instanceof ConnectError && (error.code === Code.NotFound || error.code === Code.PermissionDenied);
}

/** Put a row the server just returned where every reader finds it. */
export function cacheWorktreeRow(row: ProtoWorktree, qc: QueryClient = appQueryClient): void {
  qc.setQueryData(
    worktreeServiceKeys.getWorktree(getWorktreeRequest(row.id)),
    create(GetWorktreeResponseSchema, { worktree: row }),
  );
}

// ── Fetching ────────────────────────────────────────────────────────────────

/**
 * Re-read one workspace by id into its GetWorktree entry (shared with every
 * mounted useWorktreeById) and return it. `fresh` skips a still-fresh entry,
 * for a caller that must observe a write that just landed.
 */
export async function fetchWorktree(
  worktreeId: string,
  options?: { fresh?: boolean; qc?: QueryClient },
): Promise<Worktree> {
  const qc = options?.qc ?? appQueryClient;
  const response = await qc.fetchQuery(
    worktreeServiceQueries.getWorktree(getWorktreeRequest(worktreeId), {
      staleTime: options?.fresh ? 0 : undefined,
      // The caller decides what a failure means (resolveWorktree: "unknown");
      // a retry ladder would only hold up app start and workspace switches.
      retry: false,
    }),
  );
  if (!response.worktree) throw new Error("No worktree in response");
  return worktreeFromProto(response.worktree);
}

/**
 * The worktree with this id, fetched when the cache does not hold it; null when
 * the server does not know it (asked once — see {@link isWorktreeKnownGone}) or
 * the fetch failed. For opening a workspace (selecting it, reading its path) —
 * never for grouping chats.
 */
export async function resolveWorktree(worktreeId: string, qc: QueryClient = appQueryClient): Promise<Worktree | null> {
  const held = getCachedWorktree(worktreeId, qc);
  if (held) return held;
  if (isWorktreeKnownGone(worktreeId, qc)) return null;
  try {
    return await fetchWorktree(worktreeId, { qc });
  } catch (error) {
    logger.warn("[Worktrees] Could not resolve worktree by id", { worktreeId, error });
    return null;
  }
}

/** The project's main workspace: cached, or fetched into the main read. */
export async function resolveMainWorktree(projectId: string, qc: QueryClient = appQueryClient): Promise<Worktree | null> {
  const held = getCachedMainWorktree(projectId, qc);
  if (held) return held;
  try {
    const response = await qc.fetchQuery(
      worktreeServiceQueries.listWorktrees(mainWorktreeRequest(projectId), { retry: false }),
    );
    const main = response.worktrees.find((w) => w.isMain);
    return main ? worktreeFromProto(main) : null;
  } catch (error) {
    logger.warn("[Worktrees] Could not load the main workspace", { projectId, error });
    return null;
  }
}

/**
 * Re-read the first page of the project's live list (main first), fresh, into
 * the entry useWorktreeList reads, and return its rows.
 */
export async function fetchLiveWorktrees(projectId: string, qc: QueryClient = appQueryClient): Promise<Worktree[]> {
  const query = worktreeServiceQueries.listWorktreesInfinite(liveWorktreesRequest(projectId), { retry: false });
  // A fresh first page replaces the listing: later pages' tokens belong to the old one.
  qc.removeQueries({ queryKey: query.queryKey, exact: true });
  const data = await qc.fetchInfiniteQuery(query);
  return data.pages.flatMap((page) => page.worktrees.map(worktreeFromProto));
}

// ── Invalidation ────────────────────────────────────────────────────────────

/** The request object inside a generated key, when it has one. */
function requestOf(query: Query): Record<string, unknown> | undefined {
  const request = query.queryKey.find((part) => typeof part === "object" && part !== null);
  return request as Record<string, unknown> | undefined;
}

// The reads whose rows a change to any one workspace can move: its status,
// path or archive state is in them. Each scope covers every request of its
// rpc, the infinite pages included.
const LIST_SCOPES = [worktreeServiceKeys.listWorktrees.all, worktreeServiceKeys.listArchivedWorktrees.all];

function isListQuery(query: Query): boolean {
  return LIST_SCOPES.some((scope) => partialMatchKey(query.queryKey, scope));
}

/**
 * Invalidate what one workspace's change can make stale: the reads that name
 * it and every workspace list. Other workspaces' reads — diffs, git status —
 * and discovery are left alone: each is a round trip to a machine. Without an
 * id, the whole service.
 *
 * `lists: "stale"` marks the lists stale without refetching the mounted ones
 * (they refetch on their next mount or window focus) — for an event that
 * names a workspace the lists already hold and is most likely file churn.
 */
export async function invalidateWorktreeQueries(
  qc: QueryClient,
  worktreeId?: string,
  options?: { lists?: "refetch" | "stale" },
): Promise<void> {
  if (!worktreeId) return qc.invalidateQueries({ queryKey: worktreeServiceKeys.all });
  const listsRefetch = (options?.lists ?? "refetch") === "refetch";
  await Promise.all([
    qc.invalidateQueries({
      queryKey: worktreeServiceKeys.all,
      predicate: (query) => (listsRefetch && isListQuery(query)) || requestOf(query)?.worktreeId === worktreeId,
    }),
    listsRefetch
      ? Promise.resolve()
      : qc.invalidateQueries({ queryKey: worktreeServiceKeys.all, predicate: isListQuery, refetchType: "none" }),
  ]);
}

/** Refetch every workspace list (live, main, archived) — after a change made outside this app's writes. */
export function invalidateWorktreeLists(qc: QueryClient): Promise<void> {
  return qc.invalidateQueries({ queryKey: worktreeServiceKeys.all, predicate: isListQuery });
}

/** Re-run discovery (both the foreground and the background hint's reads) for a project. */
export function invalidateDiscoveredWorktrees(qc: QueryClient, projectId: string): Promise<void> {
  return qc.invalidateQueries({
    queryKey: worktreeServiceKeys.discoverWorktrees.all,
    predicate: (query) => requestOf(query)?.projectId === projectId,
  });
}

/** Drop a workspace from every list entry (archived out of the live list, or deleted from all). */
export function removeWorktreeFromLists(
  qc: QueryClient,
  worktreeId: string,
  from: "live" | "all",
): void {
  for (const query of worktreeQueries(qc)) {
    const data = query.state.data;
    if (!rowsIn(data).some((w) => w.id === worktreeId)) continue;
    // A by-id entry is the row itself: the caller refetches or removes it.
    if (hasTypeName(data, "reliant.v1.GetWorktreeResponse")) continue;
    if (from === "live" && hasTypeName(data, "reliant.v1.ListArchivedWorktreesResponse")) continue;
    qc.setQueryData(query.queryKey, (old: unknown) => withoutRow(old, worktreeId));
  }
}

function hasTypeName(data: unknown, typeName: string): boolean {
  if (typeof data === "object" && data !== null && "pages" in data) {
    return (data as InfiniteData<unknown>).pages.some((page) => hasTypeName(page, typeName));
  }
  return isMessage(data) && data.$typeName === typeName;
}

function withoutRow(data: unknown, worktreeId: string): unknown {
  if (typeof data !== "object" || data === null) return data;
  if ("pages" in data) {
    const infinite = data as InfiniteData<unknown>;
    return { ...infinite, pages: infinite.pages.map((page) => withoutRow(page, worktreeId)) };
  }
  if (isMessage(data) && "worktrees" in data) {
    const list = data as ListResponse;
    const worktrees = list.worktrees.filter((w) => w.id !== worktreeId);
    if (worktrees.length === list.worktrees.length) return data;
    return { ...list, worktrees, total: Math.max(0, list.total - 1) };
  }
  return data;
}

// ── Stream ──────────────────────────────────────────────────────────────────

// One toast per create: pending, then success or failure, replacing each other
// in place under this id.
const createToastId = (worktreeId: string) => `worktree-create:${worktreeId}`;

// Ids already announced: two refetches can land while a row is still CREATING.
const announcedCreateFailures = new Set<string>();
const announcedCreates = new Set<string>();

// Creates this session is waiting on, so the settle is announced even when
// nothing has the row mounted any more.
const pendingCreates = new Set<string>();

export function announceCreatePending(worktree: Worktree): void {
  pendingCreates.add(worktree.id);
  void toast.loading(`Creating workspace "${worktree.name}"…`, {
    id: createToastId(worktree.id),
  });
}

export function announceCreated(worktree: Worktree): void {
  if (announcedCreates.has(worktree.id)) return;
  announcedCreates.add(worktree.id);
  toast.success(`Workspace "${worktree.name}" created successfully`, {
    id: createToastId(worktree.id),
    duration: 4000,
  });
}

/**
 * Say why a create failed. It settles in the background, so without this the
 * only sign was a "Failed" badge somewhere in the workspace list. The server
 * writes the reason on the row when it is the user's to fix ("base branch 'x'
 * doesn't exist in reliant — check the name ...") and leaves it empty when the
 * failure was ours, which is reported on our side.
 */
function announceCreateFailed(worktree: Worktree): void {
  if (announcedCreateFailures.has(worktree.id)) return;
  announcedCreateFailures.add(worktree.id);
  void toast.error(
    worktree.failure_reason
      ? `Couldn't create workspace "${worktree.name}": ${worktree.failure_reason}`
      : `Couldn't create workspace "${worktree.name}": creating it did not finish. Archive it and try again.`,
    { id: createToastId(worktree.id), duration: 10000 },
  );
}

/** Forget the announcements (sign-out): a new session may see the same ids. */
export function resetWorktreeAnnouncements(): void {
  pendingCreates.clear();
  announcedCreateFailures.clear();
  announcedCreates.clear();
}

/**
 * A `worktree_changes` event: refresh what it can have made stale and, when
 * it settles a create we are waiting on, say how it went.
 *
 * CreateWorktree returns before the workspace exists on disk — the row comes
 * back CREATING with an empty path, and the server flips it to ACTIVE (with
 * the path) or FAILED later, announcing that with this event. The pending row
 * is re-read by id whether or not anything has it mounted, so the toast and
 * the selection's path never wait on a component.
 */
export async function handleWorktreeChanges(event: RefetchEvent, qc: QueryClient = appQueryClient): Promise<void> {
  const id = event.entityId;
  const known = id ? getCachedWorktree(id, qc) : undefined;
  const pending = !!id && (pendingCreates.has(id) || known?.status === WorktreeStatus.CREATING);
  // The event fires on every file-mutating tool call, so for a workspace the
  // lists already hold it is almost always file churn: refresh that
  // workspace's own reads (its row included) and only mark the lists stale.
  // A workspace no list holds yet (one an agent just made) or a create
  // settling moves the lists themselves.
  await invalidateWorktreeQueries(qc, id, { lists: known && !pending ? "stale" : "refetch" });
  if (!id || !pending) return;
  logger.info("[Worktrees] Pending worktree changed, refreshing", { worktreeId: id });
  try {
    const settled = await fetchWorktree(id, { fresh: true, qc });
    if (settled.status === WorktreeStatus.CREATING) return;
    pendingCreates.delete(id);
    if (settled.status === WorktreeStatus.FAILED) announceCreateFailed(settled);
    else if (settled.status === WorktreeStatus.ACTIVE) announceCreated(settled);
  } catch (error) {
    logger.warn("[Worktrees] Could not refresh pending worktree", { worktreeId: id, error });
  }
}

const unsubscribeWorktreeChanges = subscribeToRefetch("worktree_changes", (event) => {
  void handleWorktreeChanges(event);
});
// A hot reload re-evaluates this module; drop the old listener so events are
// not handled twice.
import.meta.hot?.dispose(unsubscribeWorktreeChanges);

// ── Hooks ───────────────────────────────────────────────────────────────────

const selectRow = (response: GetWorktreeResponse): Worktree | undefined =>
  response.worktree ? worktreeFromProto(response.worktree) : undefined;

/**
 * One workspace by id, whether or not any list holds it. Seeded from whichever
 * list already holds the row (no request while that is fresh), fetched by id
 * otherwise — once, shared with every other caller asking for the same id. For
 * a component handed a worktree id that needs its path or branch — a file
 * viewer, a browser tab — and must not depend on a list having been loaded.
 */
export function useWorktreeById(worktreeId: string | null | undefined): Worktree | undefined {
  const qc = useQueryClient();
  const id = worktreeId ?? "";
  const { data } = useGetWorktree(getWorktreeRequest(id), {
    enabled: !!worktreeId,
    select: selectRow,
    initialData: () => {
      const found = id ? newestCachedRow(qc, id) : undefined;
      return found ? create(GetWorktreeResponseSchema, { worktree: found.row }) : undefined;
    },
    initialDataUpdatedAt: () => (id ? newestCachedRow(qc, id)?.at : undefined),
  });
  return worktreeId ? data : undefined;
}

/** The selected workspace, or null when none is selected (or it is not loaded yet). */
export function useCurrentWorktree(): Worktree | null {
  const id = useWorktreeStore((s) => s.currentWorktreeId);
  return useWorktreeById(id) ?? null;
}

/** Pass to {@link useCachedWorktrees} for every project's workspaces. */
export const ALL_PROJECTS = Symbol("all-projects");

/**
 * {@link getCachedWorktrees} as a hook: re-renders when a workspace read lands
 * in the cache, never fetches. The array keeps its identity while the rows do
 * not change.
 */
export function useCachedWorktrees(projectId: string | null | undefined | typeof ALL_PROJECTS): Worktree[] {
  const qc = useQueryClient();
  // Bumped on every WorktreeService cache event; a render with no event since
  // the last read reuses it without scanning the cache.
  const version = useRef(0);
  const snapshot = useRef<{ version: number; scope: unknown; key: string; rows: Worktree[] } | null>(null);
  const read = useCallback(() => {
    const held = snapshot.current;
    if (held && held.version === version.current && held.scope === projectId) return held.rows;
    const rows =
      projectId === ALL_PROJECTS ? getCachedWorktrees(undefined, qc) : projectId ? getCachedWorktrees(projectId, qc) : [];
    const key = JSON.stringify(rows);
    const same = held && held.key === key;
    snapshot.current = { version: version.current, scope: projectId, key, rows: same ? held.rows : rows };
    return snapshot.current.rows;
  }, [projectId, qc]);
  const subscribe = useCallback(
    (onChange: () => void) =>
      qc.getQueryCache().subscribe((event) => {
        if (!isWorktreeQueryKey(event.query.queryKey)) return;
        version.current++;
        onChange();
      }),
    [qc],
  );
  return useSyncExternalStore(subscribe, read, read);
}

const selectMain = (response: ListWorktreesResponse): Worktree | undefined => {
  const main = response.worktrees.find((w) => w.isMain);
  return main ? worktreeFromProto(main) : undefined;
};

/** The project's main workspace (one row, however many the project holds). */
export function useMainWorktree(projectId: string | null | undefined): Worktree | undefined {
  const { data } = useListWorktrees(mainWorktreeRequest(projectId ?? ""), {
    enabled: !!projectId,
    select: selectMain,
  });
  return projectId ? data : undefined;
}

/**
 * The active worktree ID for global workspace context (terminal, file browser,
 * search, etc): the selected workspace, else the open project's main one.
 *
 * NOTE: Do NOT use this for chat-specific context (e.g. rendering file links in
 * messages). For that, use the chat's own worktreeId.
 */
export function useActiveWorktreeId(): string | undefined {
  const currentWorktreeId = useWorktreeStore((s) => s.currentWorktreeId);
  const projectId = useProjectStore((s) => s.currentProject?.id);
  const main = useMainWorktree(currentWorktreeId ? undefined : projectId);
  return currentWorktreeId || main?.id;
}

const flattenRows = (data: InfiniteData<ListResponse, string>): Worktree[] =>
  data.pages.flatMap((page) => page.worktrees.map(worktreeFromProto));

/**
 * The project's live workspaces, loaded on demand and paged. For consumers that
 * show workspaces without chats — a worktree picker for a new chat, the
 * Workspaces page, the run form. The sidebar and mobile chat list do NOT use
 * this: they group from the chat list, so app start never pages workspaces.
 *
 * Mounting loads the first page (main first); `loadMore` fetches the next while
 * `hasMore`. The selected workspace is always in the list when it belongs to
 * the project: it may sit beyond the loaded pages, and a picker that dropped it
 * would unselect what the user is working in.
 */
export function useWorktreeList(projectId: string | null | undefined, options?: { enabled?: boolean }) {
  const query = useListWorktreesInfinite(liveWorktreesRequest(projectId ?? ""), {
    enabled: !!projectId && (options?.enabled ?? true),
    select: flattenRows,
  });
  const current = useCurrentWorktree();
  const loaded = query.data;
  const worktrees = useMemo(() => {
    const rows = projectId ? loaded ?? [] : [];
    if (
      current &&
      current.project_id === projectId &&
      !current.deleted_at &&
      !rows.some((w) => w.id === current.id)
    ) {
      return [...rows, current];
    }
    return rows;
  }, [loaded, current, projectId]);
  const { fetchNextPage, hasNextPage, isFetchingNextPage } = query;
  const loadMore = useCallback(() => {
    if (hasNextPage && !isFetchingNextPage) void fetchNextPage();
  }, [fetchNextPage, hasNextPage, isFetchingNextPage]);

  return {
    worktrees,
    hasMore: !!hasNextPage,
    hasLoaded: query.data !== undefined,
    isLoading: query.isLoading,
    isFetching: query.isFetching,
    isLoadingMore: isFetchingNextPage,
    error: query.error,
    loadMore,
    refetch: query.refetch,
  };
}

/** The project's archived workspaces, most recently archived first. Only the archived view asks. */
export function useArchivedWorktreeList(projectId: string | null | undefined, options?: { enabled?: boolean }) {
  const query = useListArchivedWorktreesInfinite(archivedWorktreesRequest(projectId ?? ""), {
    enabled: !!projectId && (options?.enabled ?? true),
    select: flattenRows,
  });
  const { fetchNextPage, hasNextPage, isFetchingNextPage } = query;
  const loadMore = useCallback(() => {
    if (hasNextPage && !isFetchingNextPage) void fetchNextPage();
  }, [fetchNextPage, hasNextPage, isFetchingNextPage]);
  return {
    worktrees: projectId ? query.data ?? [] : [],
    hasMore: !!hasNextPage,
    hasLoaded: query.data !== undefined,
    isLoading: query.isLoading,
    isFetching: query.isFetching,
    isLoadingMore: isFetchingNextPage,
    error: query.error,
    loadMore,
    refetch: query.refetch,
  };
}

/**
 * Git checkouts in the project's repos that Reliant does not track, and git
 * records whose directories are gone. `background` is for a caller nobody asked
 * for (the sidebar hint): it never wakes a sleeping machine, and the server
 * answers Unavailable instead.
 */
export function useDiscoveredWorktrees(
  projectId: string | null | undefined,
  options?: { background?: boolean; enabled?: boolean; staleTime?: number; daemonId?: string },
) {
  return useDiscoverWorktrees(
    // daemonId: the machine to look on (each machine has its own checkouts);
    // unset asks the project's default machine.
    { projectId: projectId ?? "", background: options?.background ?? false, daemonId: options?.daemonId },
    {
      enabled: !!projectId && (options?.enabled ?? true),
      select: discoveredWorktreesFromProto,
      staleTime: options?.staleTime,
      retry: options?.background ? false : undefined,
    },
  );
}
