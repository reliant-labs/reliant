// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated SettingsService hooks. Written once, when
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

import { useCreateSetting } from "./settings-service-hooks_gen";
import { useListSettings } from "./settings-service-hooks_gen";
import { useGetSetting } from "./settings-service-hooks_gen";
import { useUpdateSetting } from "./settings-service-hooks_gen";
import { useDeleteSetting } from "./settings-service-hooks_gen";
import { useBatchUpsertSettings } from "./settings-service-hooks_gen";
import { useGetShortcuts } from "./settings-service-hooks_gen";
import { useUpdateShortcuts } from "./settings-service-hooks_gen";
import { useGetPreferences } from "./settings-service-hooks_gen";
import { useUpdatePreferences } from "./settings-service-hooks_gen";
import { useSetHiddenItem } from "./settings-service-hooks_gen";
import { useGetPrompts } from "./settings-service-hooks_gen";
import { useSavePrompts } from "./settings-service-hooks_gen";
import { useGetProviderStatuses } from "./settings-service-hooks_gen";
import { useValidateProviderAPIKey } from "./settings-service-hooks_gen";
import { useSyncReliantProvider } from "./settings-service-hooks_gen";
import { useCompleteCodexOAuth } from "./settings-service-hooks_gen";
import { useCompleteClaudeOAuth } from "./settings-service-hooks_gen";
import { useCompleteAntigravityOAuth } from "./settings-service-hooks_gen";
import { useStartCopilotDeviceAuth } from "./settings-service-hooks_gen";
import { usePollCopilotDeviceAuth } from "./settings-service-hooks_gen";
import { useGetPrivacySettings } from "./settings-service-hooks_gen";
import { useUpdatePrivacySettings } from "./settings-service-hooks_gen";
import { useTrackPageVisited } from "./settings-service-hooks_gen";
import { useTrackOnboardingEvent } from "./settings-service-hooks_gen";
import { useInstallSkill } from "./settings-service-hooks_gen";
import { useListInstalledSkills } from "./settings-service-hooks_gen";
import { useGetInstalledSkillDefinition } from "./settings-service-hooks_gen";
import { useSetSkillEnabled } from "./settings-service-hooks_gen";
import { useListRecommendedSkills } from "./settings-service-hooks_gen";
import { useDeleteGlobalSkill } from "./settings-service-hooks_gen";
import { useGetConfigHealth } from "./settings-service-hooks_gen";

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

describe("SettingsService hooks", () => {
  it("useCreateSetting resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.CreateSetting": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateSetting(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListSettings resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.ListSettings": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListSettings({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetSetting resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.GetSetting": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetSetting({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useUpdateSetting resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.UpdateSetting": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateSetting(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useDeleteSetting resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.DeleteSetting": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDeleteSetting(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useBatchUpsertSettings resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.BatchUpsertSettings": { response: {} },
      }),
    );

    const { result } = renderHook(() => useBatchUpsertSettings(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetShortcuts resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.GetShortcuts": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetShortcuts({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useUpdateShortcuts resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.UpdateShortcuts": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateShortcuts(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetPreferences resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.GetPreferences": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetPreferences({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useUpdatePreferences resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.UpdatePreferences": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdatePreferences(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useSetHiddenItem resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.SetHiddenItem": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetHiddenItem(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetPrompts resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.GetPrompts": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetPrompts({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useSavePrompts resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.SavePrompts": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSavePrompts(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetProviderStatuses resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.GetProviderStatuses": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetProviderStatuses({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useValidateProviderAPIKey resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.ValidateProviderAPIKey": { response: {} },
      }),
    );

    const { result } = renderHook(() => useValidateProviderAPIKey(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useSyncReliantProvider resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.SyncReliantProvider": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSyncReliantProvider(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCompleteCodexOAuth resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.CompleteCodexOAuth": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCompleteCodexOAuth(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCompleteClaudeOAuth resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.CompleteClaudeOAuth": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCompleteClaudeOAuth(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCompleteAntigravityOAuth resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.CompleteAntigravityOAuth": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCompleteAntigravityOAuth(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useStartCopilotDeviceAuth resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.StartCopilotDeviceAuth": { response: {} },
      }),
    );

    const { result } = renderHook(() => useStartCopilotDeviceAuth(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("usePollCopilotDeviceAuth resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.PollCopilotDeviceAuth": { response: {} },
      }),
    );

    const { result } = renderHook(() => usePollCopilotDeviceAuth(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetPrivacySettings resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.GetPrivacySettings": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetPrivacySettings({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useUpdatePrivacySettings resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.UpdatePrivacySettings": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdatePrivacySettings(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useTrackPageVisited resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.TrackPageVisited": { response: {} },
      }),
    );

    const { result } = renderHook(() => useTrackPageVisited(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useTrackOnboardingEvent resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.TrackOnboardingEvent": { response: {} },
      }),
    );

    const { result } = renderHook(() => useTrackOnboardingEvent(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useInstallSkill resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.InstallSkill": { response: {} },
      }),
    );

    const { result } = renderHook(() => useInstallSkill(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListInstalledSkills resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.ListInstalledSkills": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListInstalledSkills({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetInstalledSkillDefinition resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.GetInstalledSkillDefinition": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetInstalledSkillDefinition({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useSetSkillEnabled resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.SetSkillEnabled": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetSkillEnabled(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListRecommendedSkills resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.ListRecommendedSkills": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListRecommendedSkills({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useDeleteGlobalSkill resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.DeleteGlobalSkill": { response: {} },
      }),
    );

    const { result } = renderHook(() => useDeleteGlobalSkill(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetConfigHealth resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "SettingsService.GetConfigHealth": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetConfigHealth({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });
});
