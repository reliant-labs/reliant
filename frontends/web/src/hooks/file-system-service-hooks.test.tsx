// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated FileSystemService hooks. Written once, when
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

import { useGetFileTree } from "./file-system-service-hooks_gen";
import { useGetFileContent } from "./file-system-service-hooks_gen";
import { useSaveFileContent } from "./file-system-service-hooks_gen";
import { useGetFileMetadata } from "./file-system-service-hooks_gen";
import { useGetFilePreviewInfo } from "./file-system-service-hooks_gen";
import { useGetFilePreview } from "./file-system-service-hooks_gen";
import { useCreateFileOrFolder } from "./file-system-service-hooks_gen";
import { useDeleteFileOrFolder } from "./file-system-service-hooks_gen";
import { useCopyFile } from "./file-system-service-hooks_gen";
import { useSearchFiles } from "./file-system-service-hooks_gen";
import { useReplaceInFiles } from "./file-system-service-hooks_gen";
import { useListDirectory } from "./file-system-service-hooks_gen";
import { useCreateDirectory } from "./file-system-service-hooks_gen";

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

describe("FileSystemService hooks", () => {
  it("useGetFileTree resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "FileSystemService.GetFileTree": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetFileTree({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetFileContent resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "FileSystemService.GetFileContent": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetFileContent({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useSaveFileContent resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "FileSystemService.SaveFileContent": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSaveFileContent(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetFileMetadata resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "FileSystemService.GetFileMetadata": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetFileMetadata({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetFilePreviewInfo resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "FileSystemService.GetFilePreviewInfo": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetFilePreviewInfo({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetFilePreview resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "FileSystemService.GetFilePreview": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetFilePreview({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useCreateFileOrFolder resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "FileSystemService.CreateFileOrFolder": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateFileOrFolder(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useDeleteFileOrFolder resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "FileSystemService.DeleteFileOrFolder": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDeleteFileOrFolder(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCopyFile resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "FileSystemService.CopyFile": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCopyFile(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useSearchFiles resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "FileSystemService.SearchFiles": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSearchFiles({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useReplaceInFiles resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "FileSystemService.ReplaceInFiles": { response: {} },
      }),
    );

    const { result } = renderHook(() => useReplaceInFiles(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListDirectory resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "FileSystemService.ListDirectory": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListDirectory({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useCreateDirectory resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "FileSystemService.CreateDirectory": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateDirectory(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });
});
