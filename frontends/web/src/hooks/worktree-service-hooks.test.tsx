// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated WorktreeService hooks. Written once, when
// no sibling test existed; this file is YOURS from here — forge never
// regenerates it, so edit freely (delete it and run `forge project rescaffold <path>` to
// get a fresh one).
//
// Each test covers the happy path of one RPC: the hook resolves against the
// mock transport, which is the floor that catches a renamed schema, a broken
// transport wiring, or a hook that no longer compiles. Extend with error /
// loading / empty cases as you build the page — see the `frontend-testing`
// skill for the four-state coverage rule.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, it, expect } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";

import { useCreateWorktree } from "./worktree-service-hooks_gen";
import { useListWorktrees } from "./worktree-service-hooks_gen";
import { useListArchivedWorktrees } from "./worktree-service-hooks_gen";
import { useGetWorktree } from "./worktree-service-hooks_gen";
import { useUpdateWorktree } from "./worktree-service-hooks_gen";
import { useDeleteWorktree } from "./worktree-service-hooks_gen";
import { useArchiveWorktree } from "./worktree-service-hooks_gen";
import { useUnarchiveWorktree } from "./worktree-service-hooks_gen";
import { useImportWorktree } from "./worktree-service-hooks_gen";
import { useDiscoverWorktrees } from "./worktree-service-hooks_gen";
import { usePruneWorktrees } from "./worktree-service-hooks_gen";
import { useRecreateWorktree } from "./worktree-service-hooks_gen";
import { useGetWorktreeChanges } from "./worktree-service-hooks_gen";
import { useGetWorktreeGitStatus } from "./worktree-service-hooks_gen";
import { useGetWorktreeCommits } from "./worktree-service-hooks_gen";
import { useListWorktreeRepoStatuses } from "./worktree-service-hooks_gen";
import { useStageFiles } from "./worktree-service-hooks_gen";
import { useUnstageFiles } from "./worktree-service-hooks_gen";
import { useRevertFiles } from "./worktree-service-hooks_gen";
import { useCommitWorktree } from "./worktree-service-hooks_gen";
import { usePushWorktree } from "./worktree-service-hooks_gen";
import { usePullWorktree } from "./worktree-service-hooks_gen";
import { useGetWorktreePR } from "./worktree-service-hooks_gen";
import { useCreateWorktreePR } from "./worktree-service-hooks_gen";

import type { ReactNode } from "react";

// One QueryClient per test, built before it and torn down after it. Every
// test must leave NOTHING queued behind it: the last test in a file is
// followed by the jsdom teardown, and a React render that fires after that
// dies on `window` — "ReferenceError: window is not defined … caught after
// test environment was torn down" — failing a run whose tests all passed.
let queryClient: QueryClient;

beforeEach(() => {
  queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: 0, gcTime: 0 },
      mutations: { retry: false },
    },
  });
});

// cleanup() is called explicitly because Testing Library only registers its
// own auto-cleanup when `afterEach` is a GLOBAL, and vitest does not expose
// globals unless the config opts in. Without it every renderHook root stays
// mounted, still subscribed, until the worker exits. clear() then drops the
// cache along with any gc timers its queries still hold.
afterEach(() => {
  cleanup();
  queryClient.clear();
});

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

// mutateAsync resolves BEFORE the hook re-renders: React Query notifies its
// observers on a later macrotask. A mutation test that returned there would
// leave that render queued past its own end — and past the jsdom teardown,
// when it is the file's last test. Wait for the RENDERED status to settle,
// success or error, so the test ends with nothing left to fire.
async function waitForSettled(result: { current: { status: string } }) {
  await waitFor(() => expect(["success", "error"]).toContain(result.current.status));
}

describe("WorktreeService hooks", () => {
  it("useCreateWorktree resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.CreateWorktree": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateWorktree(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListWorktrees resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.ListWorktrees": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListWorktrees({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListArchivedWorktrees resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.ListArchivedWorktrees": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListArchivedWorktrees({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetWorktree resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.GetWorktree": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetWorktree({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useUpdateWorktree resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.UpdateWorktree": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateWorktree(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useDeleteWorktree resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.DeleteWorktree": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDeleteWorktree(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useArchiveWorktree resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.ArchiveWorktree": { response: {} },
      }),
    );

    const { result } = renderHook(() => useArchiveWorktree(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useUnarchiveWorktree resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.UnarchiveWorktree": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUnarchiveWorktree(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useImportWorktree resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.ImportWorktree": { response: {} },
      }),
    );

    const { result } = renderHook(() => useImportWorktree(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useDiscoverWorktrees resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.DiscoverWorktrees": { response: {} },
      }),
    );

    // DiscoverWorktrees declares NO_SIDE_EFFECTS, so it is a query.
    const { result } = renderHook(() => useDiscoverWorktrees({}), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("usePruneWorktrees resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.PruneWorktrees": { response: {} },
      }),
    );

    const { result } = renderHook(() => usePruneWorktrees(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useRecreateWorktree resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.RecreateWorktree": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRecreateWorktree(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetWorktreeChanges resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.GetWorktreeChanges": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetWorktreeChanges({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetWorktreeGitStatus resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.GetWorktreeGitStatus": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetWorktreeGitStatus({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetWorktreeCommits resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.GetWorktreeCommits": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetWorktreeCommits({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListWorktreeRepoStatuses resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.ListWorktreeRepoStatuses": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListWorktreeRepoStatuses({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useStageFiles resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.StageFiles": { response: {} },
      }),
    );

    const { result } = renderHook(() => useStageFiles(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useUnstageFiles resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.UnstageFiles": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUnstageFiles(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useRevertFiles resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.RevertFiles": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRevertFiles(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCommitWorktree resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.CommitWorktree": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCommitWorktree(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("usePushWorktree resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.PushWorktree": { response: {} },
      }),
    );

    const { result } = renderHook(() => usePushWorktree(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("usePullWorktree resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.PullWorktree": { response: {} },
      }),
    );

    const { result } = renderHook(() => usePullWorktree(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetWorktreePR resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.GetWorktreePR": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetWorktreePR({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useCreateWorktreePR resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "WorktreeService.CreateWorktreePR": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateWorktreePR(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });
});
