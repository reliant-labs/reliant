/**
 * SettingsService: one cache entry per read, whoever reads it.
 *
 *  - useUpdatePreferences (the generated mutation) refetches every mounted
 *    usePreferences;
 *  - an imperative reader (readBrowserPreferences) shares the hook's entry;
 *  - a provider-credential write (an OAuth completion, a key added from the
 *    single-credential view) refetches useProviderStatuses;
 *  - settingsServiceKeys.all reaches config health.
 */

import { QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ReactNode } from "react";

import { settingsGrpc } from "@/api/settings-grpc";
import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { useAuthStore } from "@/store/authStore";
import { addProviderApiKey } from "../provider-account-queries";
import { readBrowserPreferences, usePreferences, useProviderStatuses, useUpdatePreferences } from "../settings-queries";
import { settingsServiceKeys, useGetConfigHealth } from "../settings-service-hooks_gen";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

let archiveMode: string;
let configured: boolean;
const calls: Record<string, number> = {};
const count = (rpc: string) => {
  calls[rpc] = (calls[rpc] ?? 0) + 1;
};

beforeEach(() => {
  archiveMode = "ask_me";
  configured = false;
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
    expect(a.openLinksInApp).toBe(true);
    expect(b.openLinksInApp).toBe(true);
    expect(queryClient.getQueryData(settingsServiceKeys.getPreferences({}))).toBeDefined();
  });

  it("an OAuth completion refetches the provider statuses", async () => {
    const statuses = renderHook(() => useProviderStatuses(), { wrapper });
    await waitFor(() => expect(statuses.result.current.data?.providers[0]?.configured).toBe(false));

    await settingsGrpc.completeClaudeOAuth("code", "verifier", "http://cb", "state");

    await waitFor(() => expect(statuses.result.current.data?.providers[0]?.configured).toBe(true));
  });

  it("a key added from the single-credential view refetches the provider statuses", async () => {
    const statuses = renderHook(() => useProviderStatuses(), { wrapper });
    await waitFor(() => expect(calls.GetProviderStatuses).toBe(1));

    await addProviderApiKey("anthropic", "sk-ant");

    await waitFor(() => expect(statuses.result.current.data?.providers[0]?.configured).toBe(true));
  });

  it("settingsServiceKeys.all reaches config health", async () => {
    renderHook(() => useGetConfigHealth({ projectId: "p1" }), { wrapper });
    await waitFor(() => expect(calls.GetConfigHealth).toBe(1));

    await queryClient.invalidateQueries({ queryKey: settingsServiceKeys.all });
    await waitFor(() => expect(calls.GetConfigHealth).toBe(2));
  });
});
