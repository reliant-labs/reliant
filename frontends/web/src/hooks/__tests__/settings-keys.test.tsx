/**
 * SettingsService: one cache entry per read, whoever reads it.
 *
 *  - useUpdatePreferences (the generated mutation) refetches every mounted
 *    usePreferences;
 *  - an imperative reader (readBrowserPreferences) shares the hook's entry;
 *  - a provider-credential write (an OAuth completion, a key added from the
 *    single-credential view) refetches useProviderStatuses;
 *  - a settings or shortcuts write from outside React refetches the mounted
 *    read of what it changed;
 *  - settingsServiceKeys.all reaches config health.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ReactNode } from "react";

import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { useAuthStore } from "@/store/authStore";
import { addProviderApiKey } from "../provider-account-queries";
import {
  batchUpsertSettings,
  completeAntigravityOAuth,
  completeClaudeOAuth,
  completeCodexOAuth,
  deleteSetting,
  fetchProviderStatuses,
  fetchSettings,
  pollCopilotDeviceAuth,
  readBrowserPreferences,
  syncReliantProvider,
  updateShortcuts,
  usePreferences,
  useProviderStatuses,
  useUpdatePreferences,
} from "../settings-queries";
import {
  settingsServiceKeys,
  useGetConfigHealth,
  useGetShortcuts,
  useListSettings,
} from "../settings-service-hooks_gen";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

let archiveMode: string;
let configured: boolean;
let stored: Array<{ key: string; value: string }>;
let shortcuts: string;
const calls: Record<string, number> = {};
const count = (rpc: string) => {
  calls[rpc] = (calls[rpc] ?? 0) + 1;
};

beforeEach(() => {
  archiveMode = "ask_me";
  configured = false;
  stored = [];
  shortcuts = "{}";
  for (const k of Object.keys(calls)) delete calls[k];
  useAuthStore.setState({ session: { access_token: "t" } as never });
  setTransport(
    mockTransport({
      "SettingsService.GetPreferences": () => {
        count("GetPreferences");
        return { worktreeArchiveMode: archiveMode, additional: {} };
      },
      "SettingsService.UpdatePreferences": (req) => {
        count("UpdatePreferences");
        const r = req as { worktreeArchiveMode?: string };
        if (r.worktreeArchiveMode) archiveMode = r.worktreeArchiveMode;
        return { success: true };
      },
      "SettingsService.GetProviderStatuses": () => {
        count("GetProviderStatuses");
        return { providers: [{ provider: "claude", configured, displayName: "Claude" }] };
      },
      "SettingsService.CompleteClaudeOAuth": () => {
        configured = true;
        return { success: true, created: true };
      },
      "ProviderAccountService.AddProviderAPIKey": () => {
        configured = true;
        return { created: true };
      },
      "ProviderAccountService.ListProviderAccounts": () => ({ accounts: [] }),
      "SettingsService.ListSettings": () => {
        count("ListSettings");
        return { settings: stored, total: stored.length };
      },
      "SettingsService.BatchUpsertSettings": (req) => {
        const r = req as { settings: Array<{ key: string; value: string }> };
        stored = [...stored, ...r.settings];
        return { settings: r.settings };
      },
      "SettingsService.GetShortcuts": () => {
        count("GetShortcuts");
        return { shortcuts };
      },
      "SettingsService.UpdateShortcuts": (req) => {
        shortcuts = (req as { shortcuts: string }).shortcuts;
        return { success: true };
      },
      "SettingsService.GetConfigHealth": () => {
        count("GetConfigHealth");
        return { errors: [], errorCount: 0, warningCount: 0 };
      },
    }),
  );
});

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
  useAuthStore.setState({ session: null });
});

describe("SettingsService keys", () => {
  it("useUpdatePreferences refetches the mounted preferences", async () => {
    const prefs = renderHook(() => usePreferences(), { wrapper });
    await waitFor(() => expect(calls.GetPreferences).toBe(1));

    const update = renderHook(() => useUpdatePreferences(), { wrapper });
    await act(() => update.result.current.mutateAsync({
        worktree: { archiveMode: "always_keep", defaultDeleteBranch: false, branchCopyUncommittedFilesDefault: false },
      }),
    );

    await waitFor(() => expect(calls.GetPreferences).toBe(2));
    expect(prefs.result.current.data.worktree.archiveMode).toBe("always_keep");
  });

  it("an imperative read shares the hook's cache entry", async () => {
    const [a, b] = await Promise.all([readBrowserPreferences(), readBrowserPreferences()]);
    expect(calls.GetPreferences).toBe(1);
    expect(a.defaultPage).toBe("");
    expect(b.defaultPage).toBe("");
    expect(queryClient.getQueryData(settingsServiceKeys.getPreferences({}))).toBeDefined();
  });

  it("an OAuth completion refetches the provider statuses", async () => {
    const statuses = renderHook(() => useProviderStatuses(), { wrapper });
    await waitFor(() => expect(statuses.result.current.data?.providers[0]?.configured).toBe(false));

    await completeClaudeOAuth({ code: "code", codeVerifier: "verifier", redirectUri: "http://cb", state: "state" });

    await waitFor(() => expect(statuses.result.current.data?.providers[0]?.configured).toBe(true));
  });

  it("a key added from the single-credential view refetches the provider statuses", async () => {
    const statuses = renderHook(() => useProviderStatuses(), { wrapper });
    await waitFor(() => expect(calls.GetProviderStatuses).toBe(1));

    await addProviderApiKey("anthropic", "sk-ant");

    await waitFor(() => expect(statuses.result.current.data?.providers[0]?.configured).toBe(true));
  });

  it("an imperative provider-status read fails once, without the retry ladder", async () => {
    setTransport(
      mockTransport({
        "SettingsService.GetProviderStatuses": () => {
          count("GetProviderStatuses");
          // Unavailable is retryable under the app's default predicate, so
          // only the read's own retry: false keeps this to one request.
          throw new ConnectError("down", Code.Unavailable);
        },
      }),
    );

    await expect(fetchProviderStatuses()).rejects.toThrow("down");
    expect(calls.GetProviderStatuses).toBe(1);
  });

  it("an imperative settings read shares the hook's entry, and a batch upsert refetches it", async () => {
    const list = renderHook(() => useListSettings({}), { wrapper });
    await waitFor(() => expect(calls.ListSettings).toBe(1));

    const fetched = await fetchSettings();
    expect(fetched.settings).toEqual([]);
    expect(calls.ListSettings).toBe(2);

    await batchUpsertSettings([{ key: "appearance.theme", value: "dark" }]);
    await waitFor(() => expect(list.result.current.data?.settings.map((s) => s.key)).toEqual(["appearance.theme"]));
  });

  it("a shortcuts write refetches the mounted shortcuts", async () => {
    const read = renderHook(() => useGetShortcuts({}), { wrapper });
    await waitFor(() => expect(read.result.current.data?.shortcuts).toBe("{}"));

    await updateShortcuts('{"a":{"currentBinding":"mod+k"}}');
    await waitFor(() => expect(read.result.current.data?.shortcuts).toBe('{"a":{"currentBinding":"mod+k"}}'));
  });

  it("deleting a setting refreshes the existing request-keyed list and returns the response", async () => {
    stored = [{ key: "theme", value: "dark" }];
    setTransport(mockTransport({
      "SettingsService.ListSettings": () => ({ settings: stored }),
      "SettingsService.DeleteSetting": () => {
        stored = [];
        return { success: true };
      },
    }));
    const list = renderHook(() => useListSettings({ projectId: "p1" }), { wrapper });
    await waitFor(() => expect(list.result.current.data?.settings).toHaveLength(1));

    const response = await deleteSetting("theme", "p1");

    expect(response.success).toBe(true);
    await waitFor(() => expect(list.result.current.data?.settings).toEqual([]));
  });

  it("imperative settings writes now share their generated hook's service-wide invalidation", async () => {
    const key = settingsServiceKeys.getConfigHealth({ projectId: "p1" });
    queryClient.setQueryData(key, { errors: [] });

    await updateShortcuts("changed");

    // No new key layout or parallel cache: exactly the hook's existing scope.
    expect(queryClient.getQueryState(key)?.isInvalidated).toBe(true);
    expect(queryClient.getMutationCache().getAll()).toEqual([]);
  });

  it("an empty batch makes no request and does not invalidate cached reads", async () => {
    const key = settingsServiceKeys.listSettings({ projectId: "p1" });
    queryClient.setQueryData(key, { settings: [] });
    expect(await batchUpsertSettings([], "p1")).toEqual([]);
    expect(queryClient.getQueryState(key)?.isInvalidated).toBe(false);
  });

  it("a failed ordinary write rejects without invalidating the read or retrying", async () => {
    const key = settingsServiceKeys.getShortcuts({});
    queryClient.setQueryData(key, { shortcuts: "{}" });
    setTransport(mockTransport({
      "SettingsService.UpdateShortcuts": () => {
        count("UpdateShortcuts");
        throw new ConnectError("denied", Code.Unavailable);
      },
    }));

    await expect(updateShortcuts("changed")).rejects.toThrow("denied");

    expect(calls.UpdateShortcuts).toBe(1);
    expect(queryClient.getQueryState(key)?.isInvalidated).toBe(false);
    expect(queryClient.getMutationCache().getAll()).toEqual([]);
  });

  it.each([
    ["SyncReliantProvider", () => syncReliantProvider()],
    ["CompleteClaudeOAuth", () => completeClaudeOAuth({})],
    ["CompleteCodexOAuth", () => completeCodexOAuth({})],
    ["CompleteAntigravityOAuth", () => completeAntigravityOAuth({})],
    ["PollCopilotDeviceAuth", () => pollCopilotDeviceAuth({})],
  ] as const)("%s still refreshes provider statuses on failure without retrying", async (rpc, call) => {
    const key = settingsServiceKeys.getProviderStatuses({});
    queryClient.setQueryData(key, { providers: [] });
    setTransport(mockTransport({
      [`SettingsService.${rpc}`]: () => {
        count(rpc);
        throw new ConnectError("credential saved before failure", Code.Unavailable);
      },
    }));

    await expect(call()).rejects.toThrow("credential saved before failure");

    expect(calls[rpc]).toBe(1);
    expect(queryClient.getQueryState(key)?.isInvalidated).toBe(true);
    expect(queryClient.getMutationCache().getAll()).toEqual([]);
  });

  it("settingsServiceKeys.all reaches config health", async () => {
    renderHook(() => useGetConfigHealth({ projectId: "p1" }), { wrapper });
    await waitFor(() => expect(calls.GetConfigHealth).toBe(1));

    await queryClient.invalidateQueries({ queryKey: settingsServiceKeys.all });
    await waitFor(() => expect(calls.GetConfigHealth).toBe(2));
  });
});
