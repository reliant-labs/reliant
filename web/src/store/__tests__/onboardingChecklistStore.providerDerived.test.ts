/**
 * "Add an API key" completion is derived from the provider-status query
 * cache (settingsKeys.providers()), not from the "api-key:saved" event.
 *
 * THE BUG THIS PINS: a real prod incident where a user redeemed an AI
 * coupon, control-plane credited their wallet, but the checklist item
 * stayed unchecked — because the coupon-redemption path was one of many UI
 * flows that grant a provider, and it was the one that forgot to emit
 * "api-key:saved". The old design made every flow individually
 * responsible for remembering to announce itself; a forgotten emit meant
 * the checklist lied forever, since nothing else ever re-checked.
 *
 * These tests use a REAL QueryClient (not mocked) so the cache-subscribe
 * wiring in subscribeToStoreChanges is exercised end-to-end, and prove the
 * property with NO "api-key:saved" emission anywhere in the test — only a
 * write to the query cache, exactly as a `useProviderStatuses()` refetch
 * from any other part of the app would produce.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";

// ─── Persistence layer (the unit under test talks to this) ───────────────────

const readSetting = vi.fn();
const upsertStringSetting = vi.fn(async () => undefined);

vi.mock("../../lib/settingsPersistence", () => ({
  readSetting: (key: string) => readSetting(key),
  upsertStringSetting: (key: string, value: string) =>
    upsertStringSetting(key, value),
  deleteSettingIfExists: vi.fn(async () => undefined),
  safeGetSetting: vi.fn(async () => null),
}));

// ─── Everything else the module graph drags in ───────────────────────────────

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
// Never lets "api-key:saved" fire — `on` is captured but no test calls it.
// If a regression made completion depend on this event, these tests would
// hang or fail rather than pass, since nothing here ever emits it.
vi.mock("../../lib/events", () => ({
  getEventBus: () => ({ on: vi.fn(() => () => undefined) }),
}));
vi.mock("../../hooks/chat-queries", () => ({
  chatKeys: { all: ["chats"] },
  getCachedChatList: () => [],
}));

// Deliberately NOT mocked: the store imports the real singleton from
// "../../lib/query-client", and this is the point of the test — writing to
// that same real QueryClient (as any `useProviderStatuses()` refetch
// elsewhere in the app would) must be what the store's subscription reacts
// to, with no event involved.
import { queryClient as realQueryClient } from "../../lib/query-client";

import { useOnboardingChecklistStore } from "../onboardingChecklistStore";
import { settingsKeys } from "../../hooks/settings-queries";

function resetStore() {
  useOnboardingChecklistStore.setState({
    completedItems: new Set(),
    welcomeShown: false,
    panelState: "collapsed",
    isInitialized: false,
    isLoading: false,
  } as any);
}

describe("onboarding checklist — add-api-key derives from provider-status cache", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    resetStore();
    readSetting.mockResolvedValue({ status: "missing" });
    getProviders.mockResolvedValue([]);
    realQueryClient.clear();
  });

  it("completes when a query-cache refetch reports a configured provider — with NO event ever emitted", async () => {
    const unsub = useOnboardingChecklistStore.getState().subscribeToStoreChanges();

    expect(useOnboardingChecklistStore.getState().completedItems.has("add-api-key")).toBe(
      false,
    );

    // Simulate what RedeemCouponForm (or any other flow) produces WITHOUT
    // emitting "api-key:saved": a write to the shared providers query cache,
    // exactly as a background refetch of useProviderStatuses() would do.
    realQueryClient.setQueryData(settingsKeys.providers(), [
      { provider: "reliant", configured: true, hasApiKey: false, displayName: "Reliant" },
    ]);

    await vi.waitFor(() => {
      expect(
        useOnboardingChecklistStore.getState().completedItems.has("add-api-key"),
      ).toBe(true);
    });

    unsub();
  });

  it("does not complete while the cached provider list has no configured provider", async () => {
    const unsub = useOnboardingChecklistStore.getState().subscribeToStoreChanges();

    realQueryClient.setQueryData(settingsKeys.providers(), [
      { provider: "anthropic", configured: false, hasApiKey: false, displayName: "Anthropic" },
    ]);

    // Give any stray async work a tick to (not) run.
    await new Promise((r) => setTimeout(r, 0));
    expect(useOnboardingChecklistStore.getState().completedItems.has("add-api-key")).toBe(
      false,
    );

    unsub();
  });

  it("detectCompletedItems (the init-time check) also reads through the shared query cache", async () => {
    getProviders.mockResolvedValue([
      { provider: "anthropic", configured: true, hasApiKey: true, displayName: "Anthropic" },
    ]);

    await useOnboardingChecklistStore.getState().detectCompletedItems();

    expect(useOnboardingChecklistStore.getState().completedItems.has("add-api-key")).toBe(
      true,
    );
    // The fetch landed in the shared cache other consumers (useProviderStatuses) read.
    expect(realQueryClient.getQueryData(settingsKeys.providers())).toBeDefined();
  });

  it("THE OLD BUG, characterized: with the event-only latch this would have stayed unchecked forever", async () => {
    // This test documents what broke: a flow that grants a provider but
    // never calls getEventBus().emit("api-key:saved", ...). The mocked event
    // bus above never fires "on"'s handler for anything, by construction —
    // so if `add-api-key` completes here, it did NOT come from the event.
    const unsub = useOnboardingChecklistStore.getState().subscribeToStoreChanges();

    realQueryClient.setQueryData(settingsKeys.providers(), [
      { provider: "reliant", configured: true, hasApiKey: false, displayName: "Reliant" },
    ]);

    await vi.waitFor(() => {
      expect(
        useOnboardingChecklistStore.getState().completedItems.has("add-api-key"),
      ).toBe(true);
    });

    unsub();
  });
});
