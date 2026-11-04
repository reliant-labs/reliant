/**
 * A stand-in for hooks/worktree-queries' readers, answering from a fixed set
 * of workspaces — for a component test that is about something else (a chat
 * header, a file link, a picker's layout) and used to stub the old
 * `useWorktreeStore` selector with `{ worktrees, currentWorktree }`.
 *
 *   vi.mock("../../../hooks/worktree-queries", async (importOriginal) => ({
 *     ...(await importOriginal<typeof import("../../../hooks/worktree-queries")>()),
 *     ...(await import("../../../test/worktreeQueriesMock")).worktreeQueriesMock(() => ({
 *       worktrees: [{ id: "w-main", is_main: true }],
 *       currentWorktree: { id: "w-main" },
 *     })),
 *   }));
 *
 * A test about workspaces themselves should seed the real cache instead
 * (test/worktreeCache.ts) or answer at the transport (test/worktreeServer.ts).
 */

import type { Worktree } from "@/hooks/worktree-queries";

type Row = Partial<Worktree> & { id: string };

export interface WorktreeFixture {
  worktrees?: Row[];
  currentWorktree?: Row | null;
}

export function worktreeQueriesMock(fixture: () => WorktreeFixture) {
  const rows = () => (fixture().worktrees ?? []) as Worktree[];
  const current = () => (fixture().currentWorktree ?? null) as Worktree | null;
  const byId = (id: string | null | undefined) => (id ? rows().find((w) => w.id === id) : undefined);
  const main = () => rows().find((w) => w.is_main);
  return {
    useCurrentWorktree: () => current(),
    useWorktreeById: (id: string | null | undefined) => byId(id) ?? (current()?.id === id ? current() ?? undefined : undefined),
    useMainWorktree: () => main(),
    useActiveWorktreeId: () => current()?.id ?? main()?.id,
    useCachedWorktrees: () => rows(),
    useWorktreeList: () => ({
      worktrees: rows(),
      hasMore: false,
      hasLoaded: true,
      isLoading: false,
      isFetching: false,
      isLoadingMore: false,
      error: null,
      loadMore: () => {},
      refetch: async () => undefined,
    }),
    getCurrentWorktree: () => current(),
    getCachedWorktree: (id: string) => byId(id),
    getCachedMainWorktree: () => main(),
    getCachedWorktrees: () => rows(),
    resolveWorktree: async (id: string) => byId(id) ?? null,
    resolveMainWorktree: async () => main() ?? null,
  };
}
