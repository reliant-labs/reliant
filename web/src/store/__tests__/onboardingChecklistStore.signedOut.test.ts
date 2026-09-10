/**
 * The onboarding checklist must not reach the network while signed out.
 *
 * THE BUG THIS PINS, and it is the one that actually produced the traffic the
 * owner saw. `useProviderStatuses` has no component callers at all — the
 * GetProviderStatuses calls on the sign-in screen came from HERE:
 *
 *   OnboardingWizard is mounted at the ROUTER ROOT (routes.tsx RootShell), so
 *   it is on screen for every route including /auth. Its `if (!user) return
 *   null` guard runs during RENDER, but effects still fire — so
 *   detectCompletedItems() ran, and subscribeToStoreChanges() armed a
 *   setInterval that re-fetched provider status every 15 seconds, forever,
 *   against a session that did not exist.
 *
 * Both callers swallow the failure ("fail open"), so nothing surfaced and
 * nothing stopped. These tests assert the negative: with no session, the
 * provider fetch is never issued.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";

const readSetting = vi.fn();
const upsertStringSetting = vi.fn(async () => undefined);

vi.mock("../../lib/settingsPersistence", () => ({
  readSetting: (key: string) => readSetting(key),
  upsertStringSetting: (key: string, value: string) =>
    upsertStringSetting(key, value),
  deleteSettingIfExists: vi.fn(async () => undefined),
  safeGetSetting: vi.fn(async () => null),
}));

const getProviders = vi.fn(async () => [] as unknown[]);
vi.mock("../../api/client", () => ({
  api: { settings: { getProviders: () => getProviders() } },
}));
vi.mock("../../api/mcp-grpc", () => ({
  mcpGrpc: { listServers: vi.fn(async () => ({ servers: [] })) },
}));
vi.mock("../../lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
}));
vi.mock("../worktreeStore", () => ({
  useWorktreeStore: {
    getState: () => ({ worktrees: [] }),
    subscribe: vi.fn(() => () => undefined),
  },
}));
vi.mock("../projectStore", () => ({
  useProjectStore: {
    getState: () => ({ currentProject: { id: "project-1" } }),
    subscribe: vi.fn(() => () => undefined),
  },
}));
vi.mock("../globalDataStore", () => ({
  useGlobalDataStore: {
    getState: () => ({ workflows: [], presets: [] }),
    subscribe: vi.fn(() => () => undefined),
  },
}));
vi.mock("../../lib/events", () => ({
  getEventBus: () => ({ on: vi.fn(() => () => undefined) }),
}));
vi.mock("../../hooks/chat-queries", () => ({
  chatKeys: { all: ["chats"] },
  getCachedChatList: () => [],
}));
vi.mock("../tourStore", () => ({
  useTourStore: { getState: () => ({ hasCompletedOnboarding: false }) },
}));

const authState: { session: unknown } = { session: null };
vi.mock("../authStore", () => {
  const useAuthStore = <T,>(selector: (s: { session: unknown }) => T): T =>
    selector(authState);
  useAuthStore.getState = () => authState;
  return { useAuthStore };
});

import { queryClient as realQueryClient } from "../../lib/query-client";
import { useOnboardingChecklistStore } from "../onboardingChecklistStore";

function resetStore() {
  useOnboardingChecklistStore.setState({
    completedItems: new Set(),
    welcomeShown: false,
    panelState: "collapsed",
    isInitialized: false,
    isLoading: false,
  } as never);
}

describe("onboarding checklist — signed out", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.useRealTimers();
    resetStore();
    readSetting.mockResolvedValue({ status: "missing" });
    getProviders.mockResolvedValue([]);
    realQueryClient.clear();
    authState.session = null;
  });

  it("detectCompletedItems issues NO provider fetch with no session", async () => {
    await useOnboardingChecklistStore.getState().detectCompletedItems();

    expect(getProviders).not.toHaveBeenCalled();
    // Nothing was written to the shared cache either — a cached failure would
    // be served to the first authenticated consumer after sign-in.
    expect(realQueryClient.getQueryData(["settings", "providers"])).toBeUndefined();
  });

  it("the 15s provider poll issues NO fetch with no session", async () => {
    vi.useFakeTimers();
    const unsub = useOnboardingChecklistStore.getState().subscribeToStoreChanges();

    // Three full poll intervals — the shape of the "background polling with
    // 401" the owner reported.
    await vi.advanceTimersByTimeAsync(46_000);

    expect(getProviders).not.toHaveBeenCalled();

    unsub();
    vi.useRealTimers();
  });

  it("detectCompletedItems DOES fetch once a session exists", async () => {
    authState.session = { access_token: "token-abc" };
    getProviders.mockResolvedValue([
      { provider: "anthropic", configured: true, hasApiKey: true, displayName: "Anthropic" },
    ]);

    await useOnboardingChecklistStore.getState().detectCompletedItems();

    expect(getProviders).toHaveBeenCalledTimes(1);
    expect(
      useOnboardingChecklistStore.getState().completedItems.has("add-api-key"),
    ).toBe(true);
  });

  it("the poll resumes fetching once a session exists", async () => {
    authState.session = { access_token: "token-abc" };
    vi.useFakeTimers();
    const unsub = useOnboardingChecklistStore.getState().subscribeToStoreChanges();

    await vi.advanceTimersByTimeAsync(16_000);

    expect(getProviders).toHaveBeenCalled();

    unsub();
    vi.useRealTimers();
  });
});
