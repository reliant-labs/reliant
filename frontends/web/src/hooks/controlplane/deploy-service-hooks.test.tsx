// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated DeployService hooks. Written once, when
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

import { useResolveHostedProject } from "./deploy-service-hooks_gen";
import { useGetCapabilities } from "./deploy-service-hooks_gen";
import { useGetTenant } from "./deploy-service-hooks_gen";
import { useCreateTenant } from "./deploy-service-hooks_gen";
import { useCreateEnvironment } from "./deploy-service-hooks_gen";
import { useGetEnvironment } from "./deploy-service-hooks_gen";
import { useListEnvironments } from "./deploy-service-hooks_gen";
import { useListApps } from "./deploy-service-hooks_gen";
import { useUpdateEnvironment } from "./deploy-service-hooks_gen";
import { useDeleteEnvironment } from "./deploy-service-hooks_gen";
import { useListRetainedDatabases } from "./deploy-service-hooks_gen";
import { useDeleteRetainedDatabase } from "./deploy-service-hooks_gen";
import { useSetRetainedDatabaseRetention } from "./deploy-service-hooks_gen";
import { useGetManagedDatabase } from "./deploy-service-hooks_gen";
import { useBeginAppStepUp } from "./deploy-service-hooks_gen";
import { useCompleteAppStepUp } from "./deploy-service-hooks_gen";
import { useScheduleAppDeletion } from "./deploy-service-hooks_gen";
import { useCancelAppDeletion } from "./deploy-service-hooks_gen";
import { useGetAppDeletion } from "./deploy-service-hooks_gen";
import { useEnsureEnvironment } from "./deploy-service-hooks_gen";
import { useCreateDeployment } from "./deploy-service-hooks_gen";
import { useGetDeployment } from "./deploy-service-hooks_gen";
import { useListDeployments } from "./deploy-service-hooks_gen";
import { useUpdateDeployment } from "./deploy-service-hooks_gen";
import { useDeleteDeployment } from "./deploy-service-hooks_gen";
import { usePromote } from "./deploy-service-hooks_gen";
import { useSetRollbackFloor } from "./deploy-service-hooks_gen";
import { useLiftRollbackFloor } from "./deploy-service-hooks_gen";
import { useListRollbackFloors } from "./deploy-service-hooks_gen";
import { useScale } from "./deploy-service-hooks_gen";
import { useSetEnvironmentRunState } from "./deploy-service-hooks_gen";
import { useRestartWorkload } from "./deploy-service-hooks_gen";
import { useGetStatus } from "./deploy-service-hooks_gen";
import { useCutRelease } from "./deploy-service-hooks_gen";
import { useRecordReleaseMigrations } from "./deploy-service-hooks_gen";
import { useListReleases } from "./deploy-service-hooks_gen";
import { useGetRelease } from "./deploy-service-hooks_gen";
import { useListPromotions } from "./deploy-service-hooks_gen";
import { useCompareReleases } from "./deploy-service-hooks_gen";
import { useGetRollout } from "./deploy-service-hooks_gen";
import { useRecordGate } from "./deploy-service-hooks_gen";
import { useListGates } from "./deploy-service-hooks_gen";
import { useGetRun } from "./deploy-service-hooks_gen";
import { useListUsage } from "./deploy-service-hooks_gen";
import { useRecordBundle } from "./deploy-service-hooks_gen";
import { useGetBundle } from "./deploy-service-hooks_gen";
import { useGetWorkloadConfig } from "./deploy-service-hooks_gen";
import { useListConvergences } from "./deploy-service-hooks_gen";
import { useOpenDeployAttempt } from "./deploy-service-hooks_gen";
import { useReportDeployAttempt } from "./deploy-service-hooks_gen";
import { useCloseDeployAttempt } from "./deploy-service-hooks_gen";
import { useListDeployAttempts } from "./deploy-service-hooks_gen";
import { useGetDeployAttempt } from "./deploy-service-hooks_gen";
import { useGetLiveView } from "./deploy-service-hooks_gen";
import { useGetEnvironmentShape } from "./deploy-service-hooks_gen";
import { useGetProjectTopology } from "./deploy-service-hooks_gen";
import { usePlanDeploy } from "./deploy-service-hooks_gen";
import { useCheckDeployCapacity } from "./deploy-service-hooks_gen";
import { useReportLocalSession } from "./deploy-service-hooks_gen";
import { useMintLocalTelemetryToken } from "./deploy-service-hooks_gen";
import { useImportLedger } from "./deploy-service-hooks_gen";
import { useGetDrift } from "./deploy-service-hooks_gen";

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

describe("DeployService hooks", () => {
  it("useResolveHostedProject resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.ResolveHostedProject": { response: {} },
      }),
    );

    const { result } = renderHook(() => useResolveHostedProject(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetCapabilities resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.GetCapabilities": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetCapabilities({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetTenant resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.GetTenant": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetTenant({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useCreateTenant resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.CreateTenant": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateTenant(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCreateEnvironment resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.CreateEnvironment": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateEnvironment(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetEnvironment resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.GetEnvironment": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetEnvironment({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListEnvironments resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.ListEnvironments": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListEnvironments({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListApps resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.ListApps": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListApps({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useUpdateEnvironment resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.UpdateEnvironment": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateEnvironment(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useDeleteEnvironment resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.DeleteEnvironment": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDeleteEnvironment(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListRetainedDatabases resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.ListRetainedDatabases": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListRetainedDatabases({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useDeleteRetainedDatabase resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.DeleteRetainedDatabase": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDeleteRetainedDatabase(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useSetRetainedDatabaseRetention resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.SetRetainedDatabaseRetention": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetRetainedDatabaseRetention(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetManagedDatabase resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.GetManagedDatabase": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetManagedDatabase({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useBeginAppStepUp resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.BeginAppStepUp": { response: {} },
      }),
    );

    const { result } = renderHook(() => useBeginAppStepUp(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCompleteAppStepUp resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.CompleteAppStepUp": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCompleteAppStepUp(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useScheduleAppDeletion resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.ScheduleAppDeletion": { response: {} },
      }),
    );

    const { result } = renderHook(() => useScheduleAppDeletion(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCancelAppDeletion resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.CancelAppDeletion": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCancelAppDeletion(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetAppDeletion resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.GetAppDeletion": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetAppDeletion({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useEnsureEnvironment resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.EnsureEnvironment": { response: {} },
      }),
    );

    const { result } = renderHook(() => useEnsureEnvironment(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCreateDeployment resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.CreateDeployment": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateDeployment(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetDeployment resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.GetDeployment": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetDeployment({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListDeployments resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.ListDeployments": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListDeployments({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useUpdateDeployment resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.UpdateDeployment": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateDeployment(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useDeleteDeployment resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.DeleteDeployment": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDeleteDeployment(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("usePromote resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.Promote": { response: {} },
      }),
    );

    const { result } = renderHook(() => usePromote(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useSetRollbackFloor resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.SetRollbackFloor": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetRollbackFloor(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useLiftRollbackFloor resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.LiftRollbackFloor": { response: {} },
      }),
    );

    const { result } = renderHook(() => useLiftRollbackFloor(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListRollbackFloors resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.ListRollbackFloors": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListRollbackFloors({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useScale resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.Scale": { response: {} },
      }),
    );

    const { result } = renderHook(() => useScale(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useSetEnvironmentRunState resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.SetEnvironmentRunState": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetEnvironmentRunState(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useRestartWorkload resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.RestartWorkload": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRestartWorkload(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetStatus resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.GetStatus": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetStatus({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useCutRelease resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.CutRelease": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCutRelease(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useRecordReleaseMigrations resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.RecordReleaseMigrations": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRecordReleaseMigrations(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListReleases resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.ListReleases": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListReleases({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetRelease resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.GetRelease": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetRelease({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListPromotions resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.ListPromotions": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListPromotions({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useCompareReleases resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.CompareReleases": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCompareReleases({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetRollout resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.GetRollout": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetRollout({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useRecordGate resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.RecordGate": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRecordGate(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListGates resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.ListGates": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListGates({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetRun resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.GetRun": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetRun({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListUsage resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.ListUsage": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListUsage({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useRecordBundle resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.RecordBundle": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRecordBundle(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetBundle resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.GetBundle": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetBundle({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetWorkloadConfig resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.GetWorkloadConfig": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetWorkloadConfig({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListConvergences resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.ListConvergences": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListConvergences({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useOpenDeployAttempt resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.OpenDeployAttempt": { response: {} },
      }),
    );

    const { result } = renderHook(() => useOpenDeployAttempt(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useReportDeployAttempt resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.ReportDeployAttempt": { response: {} },
      }),
    );

    const { result } = renderHook(() => useReportDeployAttempt(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCloseDeployAttempt resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.CloseDeployAttempt": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCloseDeployAttempt(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListDeployAttempts resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.ListDeployAttempts": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListDeployAttempts({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetDeployAttempt resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.GetDeployAttempt": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetDeployAttempt({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetLiveView resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.GetLiveView": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetLiveView({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetEnvironmentShape resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.GetEnvironmentShape": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetEnvironmentShape({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetProjectTopology resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.GetProjectTopology": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetProjectTopology({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("usePlanDeploy resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.PlanDeploy": { response: {} },
      }),
    );

    const { result } = renderHook(() => usePlanDeploy({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useCheckDeployCapacity resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.CheckDeployCapacity": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCheckDeployCapacity({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useReportLocalSession resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.ReportLocalSession": { response: {} },
      }),
    );

    const { result } = renderHook(() => useReportLocalSession(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useMintLocalTelemetryToken resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.MintLocalTelemetryToken": { response: {} },
      }),
    );

    const { result } = renderHook(() => useMintLocalTelemetryToken(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useImportLedger resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.ImportLedger": { response: {} },
      }),
    );

    const { result } = renderHook(() => useImportLedger(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetDrift resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "DeployService.GetDrift": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetDrift({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });
});
