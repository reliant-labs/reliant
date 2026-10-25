/**
 * Settings server state read straight off the generated GetPreferences entry
 * (P9 R4 D6, owner decision S option C: the preferences / bookmarks zustand
 * mirrors are gone):
 *
 *  - a write from one reader reaches every other reader without a reload,
 *    and so does a refetch carrying another device's change;
 *  - a switch moves before the server answers, and moves back if it refuses;
 *  - a bookmark write starts from the stored list, and a refused one leaves
 *    the list as it was.
 *
 * Answered at the transport; the app QueryClient, as the imperative readers
 * use it.
 */

import { QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ReactNode } from "react";

import { ConfigScope } from "@/gen/reliant/v1/common_pb";
import { HiddenItemType } from "@/gen/reliant/v1/settings_pb";
import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { useAuthStore } from "@/store/authStore";
import {
  DEFAULT_WORKFLOW,
  useBookmarkActions,
  useBookmarks,
  useDefaultWorkflow,
  useHiddenItems,
  useScopePreferences,
  useToggleHiddenItem,
  useUpdateScopePreferences,
  type Bookmark,
} from "../settings-queries";
import { settingsServiceKeys } from "../settings-service-hooks_gen";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

/** The server's stored state, which the handlers read and write. */
let stored: {
  defaultWorkflow: string;
  defaultMcpScope: ConfigScope;
  hiddenWorkflowSlugs: string[];
  additional: Record<string, string>;
};
let refuseWrites: boolean;
let getCalls: number;
/** Resolve a held UpdatePreferences / SetHiddenItem (so a test can look before the answer). */
let release: (() => void) | null;
let holdWrites: boolean;

const refused = () => new ConnectError("refused", Code.PermissionDenied);

/** The first answer is in the cache (a write before it has nothing to patch). */
const loaded = () =>
  waitFor(() => expect(queryClient.getQueryData(settingsServiceKeys.getPreferences({}))).toBeDefined());

async function held() {
  if (!holdWrites) return;
  await new Promise<void>((resolve) => {
    release = resolve;
  });
}

beforeEach(() => {
  stored = {
    defaultWorkflow: "builtin://agent",
    defaultMcpScope: ConfigScope.PROJECT,
    hiddenWorkflowSlugs: [],
    additional: {},
  };
  refuseWrites = false;
  holdWrites = false;
  release = null;
  getCalls = 0;
  useAuthStore.setState({ session: { access_token: "t" } as never });
  setTransport(
    mockTransport({
      "SettingsService.GetPreferences": () => {
        getCalls++;
        return { ...stored, additional: { ...stored.additional } };
      },
      "SettingsService.UpdatePreferences": async (req) => {
        await held();
        if (refuseWrites) throw refused();
        const r = req as { defaultWorkflow?: string; defaultMcpScope?: ConfigScope; additional: Record<string, string> };
        if (r.defaultWorkflow !== undefined) stored.defaultWorkflow = r.defaultWorkflow;
        if (r.defaultMcpScope !== undefined) stored.defaultMcpScope = r.defaultMcpScope;
        stored.additional = { ...stored.additional, ...r.additional };
        return { success: true };
      },
      "SettingsService.SetHiddenItem": async (req) => {
        await held();
        if (refuseWrites) throw refused();
        const r = req as { itemType: HiddenItemType; slug: string; hidden: boolean };
        const rest = stored.hiddenWorkflowSlugs.filter((s) => s !== r.slug);
        stored.hiddenWorkflowSlugs = r.hidden ? [...rest, r.slug] : rest;
        return { success: true };
      },
    }),
  );
});

afterEach(() => {
  release?.();
  cleanup();
  queryClient.clear();
  setTransport(null);
  useAuthStore.setState({ session: null });
});

describe("default workflow and config scope", () => {
  it("serves the defaults until the answer arrives, and nothing signed out", () => {
    useAuthStore.setState({ session: null });
    const { result } = renderHook(() => useScopePreferences(), { wrapper });
    expect(result.current.data.defaultWorkflow).toBe(DEFAULT_WORKFLOW);
    expect(result.current.data.defaultMcpScope).toBe(ConfigScope.PROJECT);
    expect(getCalls).toBe(0);
  });

  it("a write from one reader reaches another without a reload", async () => {
    const composer = renderHook(() => useDefaultWorkflow(), { wrapper });
    await loaded();
    const picker = renderHook(() => useUpdateScopePreferences(), { wrapper });

    await act(() => picker.result.current.mutateAsync({ defaultWorkflow: "builtin://planner" }));

    // (Query notifications are batched onto a timer, so every read after an
    // act is a waitFor.)
    await waitFor(() => expect(composer.result.current).toBe("builtin://planner"));
    // Success refetched the entry (the generated mutation's invalidation).
    await waitFor(() => expect(getCalls).toBe(2));
    await waitFor(() => expect(queryClient.isFetching()).toBe(0));
    expect(composer.result.current).toBe("builtin://planner");
  });

  it("another device's change reaches a mounted reader on the next refetch", async () => {
    const scope = renderHook(() => useScopePreferences(), { wrapper });
    await waitFor(() => expect(scope.result.current.isPending).toBe(false));
    expect(scope.result.current.data.defaultMcpScope).toBe(ConfigScope.PROJECT);

    stored.defaultMcpScope = ConfigScope.GLOBAL;
    // What the stream's config_health signal does (STREAM_INVALIDATED_SCOPES.settings).
    await act(() => queryClient.invalidateQueries({ queryKey: settingsServiceKeys.all }));

    await waitFor(() => expect(scope.result.current.data.defaultMcpScope).toBe(ConfigScope.GLOBAL));
  });

  it("a refused write puts the old value back", async () => {
    const composer = renderHook(() => useDefaultWorkflow(), { wrapper });
    await loaded();
    const picker = renderHook(() => useUpdateScopePreferences(), { wrapper });
    refuseWrites = true;
    holdWrites = true;

    let write!: Promise<void>;
    act(() => {
      write = picker.result.current.mutateAsync({ defaultWorkflow: "builtin://planner" });
    });
    // Moved before the server answered.
    await waitFor(() => expect(composer.result.current).toBe("builtin://planner"));

    act(() => release?.());
    await act(() => expect(write).rejects.toThrow("refused"));
    await waitFor(() => expect(composer.result.current).toBe("builtin://agent"));
  });
});

describe("hidden workflows", () => {
  it("a toggle hides at once and the predicate changes identity", async () => {
    const hidden = renderHook(() => useHiddenItems(), { wrapper });
    await loaded();
    const before = hidden.result.current.isWorkflowHidden;
    const toggle = renderHook(() => useToggleHiddenItem(), { wrapper });
    holdWrites = true;

    let write!: Promise<void>;
    act(() => {
      write = toggle.result.current.toggleWorkflowVisibility("builtin://review");
    });
    await waitFor(() => expect(hidden.result.current.isWorkflowHidden("builtin://review")).toBe(true));
    expect(hidden.result.current.isWorkflowHidden).not.toBe(before);

    act(() => release?.());
    await act(() => write);
    expect(stored.hiddenWorkflowSlugs).toEqual(["builtin://review"]);
  });

  it("a refused toggle shows the workflow again and rejects", async () => {
    const hidden = renderHook(() => useHiddenItems(), { wrapper });
    await loaded();
    const toggle = renderHook(() => useToggleHiddenItem(), { wrapper });

    refuseWrites = true;
    holdWrites = true;
    let write!: Promise<void>;
    act(() => {
      write = toggle.result.current.toggleWorkflowVisibility("builtin://review");
    });
    await waitFor(() => expect(hidden.result.current.isWorkflowHidden("builtin://review")).toBe(true));

    act(() => release?.());
    await act(() => expect(write).rejects.toThrow("refused"));
    await waitFor(() => expect(hidden.result.current.isWorkflowHidden("builtin://review")).toBe(false));
  });
});

describe("bookmarks", () => {
  const github: Bookmark = { id: "b1", title: "GitHub", url: "https://github.com", createdAt: "2026-01-01T00:00:00Z" };

  it("reads the stored list from additional.browserBookmarks", async () => {
    stored.additional.browserBookmarks = JSON.stringify([github]);
    const { result } = renderHook(() => useBookmarks(), { wrapper });
    await waitFor(() => expect(result.current).toEqual([github]));
  });

  it("an add with nothing cached starts from the stored list, never an empty one", async () => {
    stored.additional.browserBookmarks = JSON.stringify([github]);
    const actions = renderHook(() => useBookmarkActions(), { wrapper });

    await act(() => actions.result.current.addBookmark({ title: "Docs", url: "https://docs.example.com" }));

    const saved = JSON.parse(stored.additional.browserBookmarks) as Bookmark[];
    expect(saved.map((b) => b.url)).toEqual(["https://github.com", "https://docs.example.com"]);
  });

  it("a duplicate URL is not added", async () => {
    stored.additional.browserBookmarks = JSON.stringify([github]);
    const actions = renderHook(() => useBookmarkActions(), { wrapper });
    await act(() => actions.result.current.addBookmark({ title: "Again", url: "https://github.com" }));
    expect(JSON.parse(stored.additional.browserBookmarks)).toEqual([github]);
  });

  it("bookmarks survive a refused write", async () => {
    stored.additional.browserBookmarks = JSON.stringify([github]);
    const list = renderHook(() => useBookmarks(), { wrapper });
    await waitFor(() => expect(list.result.current).toEqual([github]));
    await loaded();
    const actions = renderHook(() => useBookmarkActions(), { wrapper });
    refuseWrites = true;
    holdWrites = true;

    let write!: Promise<void>;
    act(() => {
      write = actions.result.current.removeBookmark("b1");
    });
    await waitFor(() => expect(list.result.current).toEqual([]));

    act(() => release?.());
    await act(() => expect(write).rejects.toThrow("refused"));
    await waitFor(() => expect(list.result.current).toEqual([github]));
    expect(JSON.parse(stored.additional.browserBookmarks)).toEqual([github]);
  });
});
