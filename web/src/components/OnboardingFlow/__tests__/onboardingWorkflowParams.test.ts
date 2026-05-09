import { beforeEach, describe, expect, it, vi } from "vitest";

// Mock localStorage before zustand's persist middleware captures it.
vi.hoisted(() => {
  let store: Record<string, string> = {};
  (globalThis as any).localStorage = {
    getItem: (key: string) => store[key] ?? null,
    setItem: (key: string, value: string) => { store[key] = value; },
    removeItem: (key: string) => { delete store[key]; },
    clear: () => { store = {}; },
    get length() { return Object.keys(store).length; },
    key: (i: number) => Object.keys(store)[i] ?? null,
  };
});

import { useChatParamsStore } from "@/store/chatParamsStore";

describe("chatParamsStore temp params (onboarding workflow params flow)", () => {
  beforeEach(() => {
    // Reset entire store state between tests
    useChatParamsStore.setState({
      chatParams: {},
      chatPresets: {},
      tempNewChatParams: {},
      tempNewChatWorkflow: null,
      tempNewChatPresets: {},
    });
  });

  it("setTempNewChatParams stores workflow params", () => {
    useChatParamsStore.getState().setTempNewChatParams({ mode: "auto", ask: true });
    expect(useChatParamsStore.getState().tempNewChatParams).toEqual({ mode: "auto", ask: true });
  });

  it("setTempNewChatParams replaces previous params entirely", () => {
    useChatParamsStore.getState().setTempNewChatParams({ mode: "plan", ask: false });
    useChatParamsStore.getState().setTempNewChatParams({ mode: "auto" });
    expect(useChatParamsStore.getState().tempNewChatParams).toEqual({ mode: "auto" });
  });

  it("updateTempNewChatParams merges with existing params", () => {
    useChatParamsStore.getState().setTempNewChatParams({ mode: "auto" });
    useChatParamsStore.getState().updateTempNewChatParams({ ask: true });
    expect(useChatParamsStore.getState().tempNewChatParams).toEqual({ mode: "auto", ask: true });
  });

  it("getTempNewChatParam returns a single param value", () => {
    useChatParamsStore.getState().setTempNewChatParams({ mode: "plan", ask: false });
    expect(useChatParamsStore.getState().getTempNewChatParam("mode")).toBe("plan");
    expect(useChatParamsStore.getState().getTempNewChatParam("ask")).toBe(false);
    expect(useChatParamsStore.getState().getTempNewChatParam("nonexistent")).toBeUndefined();
  });

  it("setTempNewChatWorkflow stores workflow id", () => {
    useChatParamsStore.getState().setTempNewChatWorkflow("builtin://forge-one-shot");
    expect(useChatParamsStore.getState().tempNewChatWorkflow).toBe("builtin://forge-one-shot");
  });

  it("setTempNewChatWorkflow accepts null to clear", () => {
    useChatParamsStore.getState().setTempNewChatWorkflow("builtin://agent");
    useChatParamsStore.getState().setTempNewChatWorkflow(null);
    expect(useChatParamsStore.getState().tempNewChatWorkflow).toBeNull();
  });

  it("setTempNewChatPresets stores preset selections", () => {
    useChatParamsStore.getState().setTempNewChatPresets({ default: "ux" });
    expect(useChatParamsStore.getState().tempNewChatPresets).toEqual({ default: "ux" });
  });

  it("clearTempNewChatParams resets all temp state", () => {
    useChatParamsStore.getState().setTempNewChatParams({ mode: "auto", ask: true });
    useChatParamsStore.getState().setTempNewChatWorkflow("builtin://forge-one-shot");
    useChatParamsStore.getState().setTempNewChatPresets({ default: "ux" });

    useChatParamsStore.getState().clearTempNewChatParams();

    expect(useChatParamsStore.getState().tempNewChatParams).toEqual({});
    expect(useChatParamsStore.getState().tempNewChatWorkflow).toBeNull();
    expect(useChatParamsStore.getState().tempNewChatPresets).toEqual({});
  });

  it("transferTempToChat moves temp params to chat-specific storage", () => {
    useChatParamsStore.getState().setTempNewChatParams({ mode: "auto" });
    useChatParamsStore.getState().setTempNewChatWorkflow("builtin://agent");
    useChatParamsStore.getState().setTempNewChatPresets({ default: "documentation" });

    useChatParamsStore.getState().transferTempToChat("chat-123");

    expect(useChatParamsStore.getState().getChatParams("chat-123")).toEqual({ mode: "auto" });
    expect(useChatParamsStore.getState().getChatPresets("chat-123")).toEqual({ default: "documentation" });
    // Temp should be cleared
    expect(useChatParamsStore.getState().tempNewChatParams).toEqual({});
    expect(useChatParamsStore.getState().tempNewChatWorkflow).toBeNull();
    expect(useChatParamsStore.getState().tempNewChatPresets).toEqual({});
  });

  it("transferTempToChat is a no-op when temp state is empty", () => {
    // Pre-populate a chat so we can verify it's untouched
    useChatParamsStore.getState().setChatParams("chat-existing", { mode: "plan" });

    useChatParamsStore.getState().transferTempToChat("chat-new");

    // No entry created for the new chat
    expect(useChatParamsStore.getState().getChatParams("chat-new")).toEqual({});
    expect(useChatParamsStore.getState().getChatPresets("chat-new")).toEqual({});
    // Existing chat untouched
    expect(useChatParamsStore.getState().getChatParams("chat-existing")).toEqual({ mode: "plan" });
  });

  it("transferTempToChat transfers only params when presets are empty", () => {
    useChatParamsStore.getState().setTempNewChatParams({ mode: "auto", ask: true });
    // No presets set

    useChatParamsStore.getState().transferTempToChat("chat-456");

    expect(useChatParamsStore.getState().getChatParams("chat-456")).toEqual({ mode: "auto", ask: true });
    expect(useChatParamsStore.getState().getChatPresets("chat-456")).toEqual({});
  });

  it("transferTempToChat transfers only presets when params are empty", () => {
    useChatParamsStore.getState().setTempNewChatPresets({ default: "ux" });
    // No params set

    useChatParamsStore.getState().transferTempToChat("chat-789");

    expect(useChatParamsStore.getState().getChatParams("chat-789")).toEqual({});
    expect(useChatParamsStore.getState().getChatPresets("chat-789")).toEqual({ default: "ux" });
  });
});

describe("chatParamsStore per-chat operations", () => {
  beforeEach(() => {
    useChatParamsStore.setState({
      chatParams: {},
      chatPresets: {},
      tempNewChatParams: {},
      tempNewChatWorkflow: null,
      tempNewChatPresets: {},
    });
  });

  it("setChatParams stores params for a chat", () => {
    useChatParamsStore.getState().setChatParams("chat-1", { mode: "auto" });
    expect(useChatParamsStore.getState().getChatParams("chat-1")).toEqual({ mode: "auto" });
  });

  it("updateChatParams merges into existing params", () => {
    useChatParamsStore.getState().setChatParams("chat-1", { mode: "auto" });
    useChatParamsStore.getState().updateChatParams("chat-1", { ask: true });
    expect(useChatParamsStore.getState().getChatParams("chat-1")).toEqual({ mode: "auto", ask: true });
  });

  it("getChatParam retrieves a single key", () => {
    useChatParamsStore.getState().setChatParams("chat-1", { mode: "plan", ask: false });
    expect(useChatParamsStore.getState().getChatParam("chat-1", "mode")).toBe("plan");
    expect(useChatParamsStore.getState().getChatParam("chat-1", "missing")).toBeUndefined();
  });

  it("getChatParams returns empty object for unknown chat", () => {
    expect(useChatParamsStore.getState().getChatParams("unknown")).toEqual({});
  });

  it("setChatPresets stores presets for a chat", () => {
    useChatParamsStore.getState().setChatPresets("chat-1", { default: "ux" });
    expect(useChatParamsStore.getState().getChatPresets("chat-1")).toEqual({ default: "ux" });
  });

  it("getChatPresets returns empty object for unknown chat", () => {
    expect(useChatParamsStore.getState().getChatPresets("unknown")).toEqual({});
  });

  it("removeChatParams deletes both params and presets for a chat", () => {
    useChatParamsStore.getState().setChatParams("chat-1", { mode: "auto" });
    useChatParamsStore.getState().setChatPresets("chat-1", { default: "ux" });

    useChatParamsStore.getState().removeChatParams("chat-1");

    expect(useChatParamsStore.getState().getChatParams("chat-1")).toEqual({});
    expect(useChatParamsStore.getState().getChatPresets("chat-1")).toEqual({});
  });

  it("removeChatParams does not affect other chats", () => {
    useChatParamsStore.getState().setChatParams("chat-1", { mode: "auto" });
    useChatParamsStore.getState().setChatParams("chat-2", { mode: "plan" });

    useChatParamsStore.getState().removeChatParams("chat-1");

    expect(useChatParamsStore.getState().getChatParams("chat-1")).toEqual({});
    expect(useChatParamsStore.getState().getChatParams("chat-2")).toEqual({ mode: "plan" });
  });
});