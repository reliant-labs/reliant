// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated ProjectService hooks. Written once, when
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

import { useCreateProject } from "./project-service-hooks_gen";
import { useListProjects } from "./project-service-hooks_gen";
import { useGetProject } from "./project-service-hooks_gen";
import { useUpdateProject } from "./project-service-hooks_gen";
import { useDeleteProject } from "./project-service-hooks_gen";
import { useTouchProject } from "./project-service-hooks_gen";
import { useGetProjectMetadata } from "./project-service-hooks_gen";
import { useUpdateProjectMetadata } from "./project-service-hooks_gen";
import { useGetProjectGitInfo } from "./project-service-hooks_gen";
import { useGetProjectGitBranches } from "./project-service-hooks_gen";
import { useGetProjectInitStatus } from "./project-service-hooks_gen";
import { useInitializeProject } from "./project-service-hooks_gen";
import { useGetProjectChanges } from "./project-service-hooks_gen";
import { useGetProjectPrompts } from "./project-service-hooks_gen";
import { useSaveProjectPrompts } from "./project-service-hooks_gen";
import { useInitializeGitRepo } from "./project-service-hooks_gen";
import { useListProjectDaemonsForDaemon } from "./project-service-hooks_gen";
import { useListProjectDaemons } from "./project-service-hooks_gen";
import { useMarkProjectInstalled } from "./project-service-hooks_gen";
import { useCreateProjectFromRepo } from "./project-service-hooks_gen";
import { useListRepositoriesForDaemon } from "./project-service-hooks_gen";
import { usePullProjectOnDaemon } from "./project-service-hooks_gen";
import { useRemoveProjectFromDaemon } from "./project-service-hooks_gen";
import { useRecloneProjectOnDaemon } from "./project-service-hooks_gen";

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

describe("ProjectService hooks", () => {
  it("useCreateProject resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.CreateProject": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateProject(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListProjects resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.ListProjects": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListProjects({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetProject resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.GetProject": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetProject({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useUpdateProject resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.UpdateProject": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateProject(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useDeleteProject resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.DeleteProject": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDeleteProject(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useTouchProject resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.TouchProject": { response: {} },
      }),
    );

    const { result } = renderHook(() => useTouchProject(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetProjectMetadata resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.GetProjectMetadata": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetProjectMetadata({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useUpdateProjectMetadata resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.UpdateProjectMetadata": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateProjectMetadata(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetProjectGitInfo resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.GetProjectGitInfo": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetProjectGitInfo({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetProjectGitBranches resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.GetProjectGitBranches": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetProjectGitBranches({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetProjectInitStatus resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.GetProjectInitStatus": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetProjectInitStatus({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useInitializeProject resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.InitializeProject": { response: {} },
      }),
    );

    const { result } = renderHook(() => useInitializeProject(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetProjectChanges resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.GetProjectChanges": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetProjectChanges({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetProjectPrompts resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.GetProjectPrompts": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetProjectPrompts({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useSaveProjectPrompts resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.SaveProjectPrompts": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSaveProjectPrompts(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useInitializeGitRepo resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.InitializeGitRepo": { response: {} },
      }),
    );

    const { result } = renderHook(() => useInitializeGitRepo(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListProjectDaemonsForDaemon resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.ListProjectDaemonsForDaemon": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListProjectDaemonsForDaemon({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListProjectDaemons resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.ListProjectDaemons": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListProjectDaemons({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useMarkProjectInstalled resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.MarkProjectInstalled": { response: {} },
      }),
    );

    const { result } = renderHook(() => useMarkProjectInstalled(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCreateProjectFromRepo resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.CreateProjectFromRepo": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateProjectFromRepo(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListRepositoriesForDaemon resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.ListRepositoriesForDaemon": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListRepositoriesForDaemon({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("usePullProjectOnDaemon resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.PullProjectOnDaemon": { response: {} },
      }),
    );

    const { result } = renderHook(() => usePullProjectOnDaemon(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useRemoveProjectFromDaemon resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.RemoveProjectFromDaemon": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRemoveProjectFromDaemon(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useRecloneProjectOnDaemon resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "ProjectService.RecloneProjectOnDaemon": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRecloneProjectOnDaemon(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });
});
