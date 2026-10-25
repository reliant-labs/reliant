/**
 * Put workspaces where the app reads them in a test: the query cache, under
 * the generated WorktreeService keys (hooks/worktree-queries.ts). No store
 * holds workspace rows any more, so a test that used to
 * `useWorktreeStore.setState({ worktrees, currentWorktree })` seeds the rows
 * here and selects one with `selectWorktreeForTest`.
 *
 * Every helper defaults to the app's singleton QueryClient (lib/query-client),
 * the one the imperative readers (getCurrentWorktree, getCachedWorktree,
 * resolveWorktree) use. Render with that same client —
 * `<QueryClientProvider client={queryClient}>` — and `queryClient.clear()` in
 * afterEach.
 */

import { create } from "@bufbuild/protobuf";
import type { InfiniteData, QueryClient } from "@tanstack/react-query";

import {
  CleanupMetadataSchema,
  GetWorktreeResponseSchema,
  ListArchivedWorktreesResponseSchema,
  ListWorktreesResponseSchema,
  WorktreeSchema,
  WorktreeStatus,
  type ListArchivedWorktreesResponse,
  type ListWorktreesResponse,
  type Worktree as ProtoWorktree,
} from "@/gen/reliant/v1/worktree_pb";
import {
  archivedWorktreesRequest,
  getWorktreeRequest,
  liveWorktreesRequest,
  mainWorktreeRequest,
  type Worktree,
} from "@/hooks/worktree-queries";
import { worktreeServiceKeys } from "@/hooks/worktree-service-hooks_gen";
import { queryClient as appQueryClient } from "@/lib/query-client";
import { useWorktreeStore } from "@/store/worktreeStore";

const now = "2026-01-01T00:00:00.000Z";

/** A frontend workspace with every required field filled; override what the test cares about. */
export function buildWorktree(overrides: Partial<Worktree> = {}): Worktree {
  return {
    id: "wt-1",
    name: "Workspace",
    path: "/tmp/project/wt-1",
    branch: "feature",
    base_branch: "main",
    project_id: "project-1",
    status: WorktreeStatus.ACTIVE,
    is_main: false,
    created_at: now,
    updated_at: now,
    last_active: now,
    deleted_at: null,
    cleanup_metadata: null,
    ...overrides,
  };
}

/** The proto row the server would send for this frontend workspace. */
export function worktreeProto(view: Partial<Worktree>): ProtoWorktree {
  const w = buildWorktree(view);
  return create(WorktreeSchema, {
    id: w.id,
    name: w.name,
    path: w.path,
    branch: w.branch,
    baseBranch: w.base_branch,
    projectId: w.project_id ?? "",
    chatId: w.chat_id,
    status: w.status,
    isMain: w.is_main,
    createdAt: w.created_at,
    updatedAt: w.updated_at,
    lastActive: w.last_active,
    deletedAt: w.deleted_at ?? undefined,
    failureReason: w.failure_reason ?? "",
    cleanupMetadata: w.cleanup_metadata
      ? create(CleanupMetadataSchema, {
          directoryDeleted: w.cleanup_metadata.directory_deleted,
          branchDeleted: w.cleanup_metadata.branch_deleted,
          heldReason: w.cleanup_metadata.held_reason ?? "",
          heldDetail: w.cleanup_metadata.held_detail ?? "",
          snapshotRefs: w.cleanup_metadata.snapshot_refs ?? [],
          strippedPaths: w.cleanup_metadata.stripped_paths ?? [],
          strippedBytes: BigInt(w.cleanup_metadata.stripped_bytes ?? 0),
        })
      : undefined,
  });
}

/** Seed one workspace's GetWorktree entry (what useWorktreeById reads). */
export function seedWorktree(view: Partial<Worktree>, qc: QueryClient = appQueryClient): Worktree {
  const w = buildWorktree(view);
  qc.setQueryData(
    worktreeServiceKeys.getWorktree(getWorktreeRequest(w.id)),
    create(GetWorktreeResponseSchema, { worktree: worktreeProto(w) }),
  );
  return w;
}

/**
 * Seed a project's live list (one page, as useWorktreeList reads it) and, when
 * one of the rows is main, its main-workspace read. Archived rows are seeded
 * into the archived list instead.
 */
export function seedWorktreeList(
  projectId: string,
  views: Partial<Worktree>[],
  qc: QueryClient = appQueryClient,
  options?: { nextPageToken?: string },
): Worktree[] {
  const rows = views.map((v) => buildWorktree({ project_id: projectId, ...v }));
  const live = rows.filter((w) => !w.deleted_at);
  const archived = rows.filter((w) => w.deleted_at);
  const page = create(ListWorktreesResponseSchema, {
    worktrees: live.map(worktreeProto),
    total: live.length,
    nextPageToken: options?.nextPageToken ?? "",
  });
  qc.setQueryData<InfiniteData<ListWorktreesResponse, string>>(
    worktreeServiceKeys.listWorktreesInfinite(liveWorktreesRequest(projectId)),
    { pages: [page], pageParams: [""] },
  );
  const main = live.find((w) => w.is_main);
  if (main) {
    qc.setQueryData(
      worktreeServiceKeys.listWorktrees(mainWorktreeRequest(projectId)),
      create(ListWorktreesResponseSchema, { worktrees: [worktreeProto(main)], total: 1 }),
    );
  }
  if (archived.length > 0) {
    qc.setQueryData<InfiniteData<ListArchivedWorktreesResponse, string>>(
      worktreeServiceKeys.listArchivedWorktreesInfinite(archivedWorktreesRequest(projectId)),
      {
        pages: [create(ListArchivedWorktreesResponseSchema, { worktrees: archived.map(worktreeProto), total: archived.length })],
        pageParams: [""],
      },
    );
  }
  return rows;
}

/** Seed a workspace and select it, as the app does after resolving one. */
export function selectWorktreeForTest(view: Partial<Worktree> | null, qc: QueryClient = appQueryClient): Worktree | null {
  if (!view) {
    useWorktreeStore.setState({ currentWorktreeId: null, currentWorktreeProjectId: null });
    return null;
  }
  const w = seedWorktree(view, qc);
  useWorktreeStore.setState({ currentWorktreeId: w.id, currentWorktreeProjectId: w.project_id ?? null });
  return w;
}

/**
 * What tests used to write with
 * `useWorktreeStore.setState({ worktrees, currentWorktree, deletingId })`,
 * in the shape the app reads now: every row gets its GetWorktree entry, each
 * project's live (and archived) list holds its rows, main rows answer the
 * main-workspace read, and `currentWorktree` becomes the selected id. Keys a
 * test passes that no longer exist (isLoading, hasLoaded, ...) are ignored.
 */
export function setWorktreesForTest(
  state: {
    worktrees?: Partial<Worktree>[];
    archivedWorktrees?: Partial<Worktree>[];
    currentWorktree?: Partial<Worktree> | null;
    deletingId?: string | null;
    [ignored: string]: unknown;
  },
  qc: QueryClient = appQueryClient,
): void {
  const rows = [...(state.worktrees ?? []), ...(state.archivedWorktrees ?? [])].map((w) => buildWorktree(w));
  const byProject = new Map<string, Worktree[]>();
  for (const row of rows) {
    seedWorktree(row, qc);
    const projectId = row.project_id ?? "";
    byProject.set(projectId, [...(byProject.get(projectId) ?? []), row]);
  }
  for (const [projectId, projectRows] of byProject) seedWorktreeList(projectId, projectRows, qc);
  if (state.currentWorktree !== undefined) selectWorktreeForTest(state.currentWorktree, qc);
  if (state.deletingId !== undefined) useWorktreeStore.setState({ deletingId: state.deletingId });
}
