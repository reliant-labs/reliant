// Copyright (c) 2026 Reliant Labs

import { beforeEach, describe, expect, it } from "vitest";

import { useChatViewStore } from "../chatViewStore";

const view = (chatId: string) => (useChatViewStore.getState().traceChatId === chatId ? "trace" : "chat");

describe("chatViewStore", () => {
  beforeEach(() => useChatViewStore.setState({ traceChatId: null }));

  it("opens one chat's Trace at a time", () => {
    useChatViewStore.getState().setView("a", "trace");
    expect(view("a")).toBe("trace");
    useChatViewStore.getState().setView("b", "trace");
    expect(view("a")).toBe("chat");
    expect(view("b")).toBe("trace");
  });

  it("closes a chat's Trace, and leaves another chat's alone", () => {
    useChatViewStore.getState().setView("a", "trace");
    useChatViewStore.getState().setView("b", "chat");
    expect(view("a")).toBe("trace");
    useChatViewStore.getState().setView("a", "chat");
    expect(view("a")).toBe("chat");
  });
});
