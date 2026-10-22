/**
 * Onboarding checklist persistence contract.
 *
 * The Setup Guide's dismissal is a durable fact, not a UI preference. These
 * tests pin the three properties that keep it from reappearing:
 *
 *   1. Dismissal is mirrored to localStorage synchronously, so it survives a
 *      reload even when the backend write never lands.
 *   2. A backend READ failure must never resurrect a dismissed guide. The old
 *      code funnelled "row missing" and "read failed" into the same `null` and
 *      fell back to the "collapsed" default, which is what made the panel pop
 *      back after a transient RPC error.
 *   3. Dismissal is monotonic across the two stores: if either localStorage or
 *      the backend says dismissed, the guide stays dismissed. Only an explicit
 *      reset revives it.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";

// ─── Persistence layer (the unit under test talks to this) ───────────────────

const readSetting = vi.fn();
const upsertStringSetting = vi.fn(async () => undefined);
const deleteSettingIfExists = vi.fn(async () => undefined);

vi.mock("../../lib/settingsPersistence", () => ({
  readSetting: (key: string) => readSetting(key),
  upsertStringSetting: (key: string, value: string) =>
    upsertStringSetting(key, value),
  deleteSettingIfExists: (key: string) => deleteSettingIfExists(key),
  safeGetSetting: vi.fn(async () => null),
}));

// ─── Everything the module graph drags in ────────────────────────────────────

vi.mock("../../api/client", () => ({
  api: { settings: { getProviders: vi.fn(async () => []) } },
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
vi.mock("../../lib/query-client", () => ({
  queryClient: {
    getQueryCache: () => ({ subscribe: vi.fn(() => () => undefined) }),
  },
}));

import { useOnboardingChecklistStore } from "../onboardingChecklistStore";
import {
  CHECKLIST_SETTINGS_KEYS,
  REQUIRED_ITEMS,
} from "../../components/Onboarding/constants";

const PANEL_STATE_LOCAL_KEY = "reliant.checklist.panelState";

function resetStore() {
  useOnboardingChecklistStore.setState({
    completedItems: new Set(),
    welcomeShown: false,
    panelState: "collapsed",
    isInitialized: false,
    isLoading: false,
  } as any);
}

describe("onboarding checklist — durable dismissal", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    resetStore();
    readSetting.mockResolvedValue({ status: "missing" });
  });

  it("mirrors a dismissal to localStorage as well as the backend", async () => {
    await useOnboardingChecklistStore.getState().setPanelState("dismissed");

    expect(localStorage.getItem(PANEL_STATE_LOCAL_KEY)).toBe("dismissed");
    expect(upsertStringSetting).toHaveBeenCalledWith(
      CHECKLIST_SETTINGS_KEYS.PANEL_STATE,
      "dismissed",
    );
  });

  it("keeps the guide dismissed when the backend read fails", async () => {
    localStorage.setItem(PANEL_STATE_LOCAL_KEY, "dismissed");
    readSetting.mockResolvedValue({ status: "error" });

    await useOnboardingChecklistStore.getState().loadState();

    expect(useOnboardingChecklistStore.getState().panelState).toBe("dismissed");
  });

  it("keeps the guide dismissed when the backend row is missing", async () => {
    // A failed settings sync leaves an empty cache, which reads as "missing"
    // for every key. That must not undo a dismissal recorded on this device.
    localStorage.setItem(PANEL_STATE_LOCAL_KEY, "dismissed");
    readSetting.mockResolvedValue({ status: "missing" });

    await useOnboardingChecklistStore.getState().loadState();

    expect(useOnboardingChecklistStore.getState().panelState).toBe("dismissed");
  });

  it("honors a dismissal recorded on another device", async () => {
    readSetting.mockImplementation(async (key: string) =>
      key === CHECKLIST_SETTINGS_KEYS.PANEL_STATE
        ? { status: "found", value: "dismissed" }
        : { status: "missing" },
    );

    await useOnboardingChecklistStore.getState().loadState();

    expect(useOnboardingChecklistStore.getState().panelState).toBe("dismissed");
    // …and caches it locally so the next boot doesn't depend on the network.
    expect(localStorage.getItem(PANEL_STATE_LOCAL_KEY)).toBe("dismissed");
  });

  it("does not let a stale backend value revive a local dismissal", async () => {
    localStorage.setItem(PANEL_STATE_LOCAL_KEY, "dismissed");
    readSetting.mockImplementation(async (key: string) =>
      key === CHECKLIST_SETTINGS_KEYS.PANEL_STATE
        ? { status: "found", value: "collapsed" }
        : { status: "missing" },
    );

    await useOnboardingChecklistStore.getState().loadState();

    expect(useOnboardingChecklistStore.getState().panelState).toBe("dismissed");
  });

  it("still shows the guide for a genuinely new user", async () => {
    readSetting.mockResolvedValue({ status: "missing" });

    await useOnboardingChecklistStore.getState().loadState();

    expect(useOnboardingChecklistStore.getState().panelState).toBe("collapsed");
  });

  it("restores completed items from localStorage when the backend read fails", async () => {
    localStorage.setItem(
      "reliant.checklist.completedItems",
      JSON.stringify(["add-api-key", "start-chat"]),
    );
    readSetting.mockResolvedValue({ status: "error" });

    await useOnboardingChecklistStore.getState().loadState();

    const { completedItems } = useOnboardingChecklistStore.getState();
    expect(completedItems.has("add-api-key")).toBe(true);
    expect(completedItems.has("start-chat")).toBe(true);
  });

  it("clears the local dismissal record on an explicit reset", async () => {
    await useOnboardingChecklistStore.getState().setPanelState("dismissed");
    expect(localStorage.getItem(PANEL_STATE_LOCAL_KEY)).toBe("dismissed");

    await useOnboardingChecklistStore.getState().revive();

    expect(useOnboardingChecklistStore.getState().panelState).toBe("expanded");
    expect(localStorage.getItem(PANEL_STATE_LOCAL_KEY)).toBe("expanded");
    expect(upsertStringSetting).toHaveBeenCalledWith(
      CHECKLIST_SETTINGS_KEYS.PANEL_STATE,
      "expanded",
    );
  });
});

describe("onboarding checklist — completion", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    resetStore();
    readSetting.mockResolvedValue({ status: "missing" });
  });

  it("reports allRequiredComplete once every required item is done", () => {
    useOnboardingChecklistStore.setState({
      completedItems: new Set(REQUIRED_ITEMS.map((i) => i.id)),
    } as any);

    expect(useOnboardingChecklistStore.getState().allRequiredComplete()).toBe(
      true,
    );
  });

  it("does not report allRequiredComplete while one is outstanding", () => {
    useOnboardingChecklistStore.setState({
      completedItems: new Set(REQUIRED_ITEMS.slice(1).map((i) => i.id)),
    } as any);

    expect(useOnboardingChecklistStore.getState().allRequiredComplete()).toBe(
      false,
    );
  });
});
