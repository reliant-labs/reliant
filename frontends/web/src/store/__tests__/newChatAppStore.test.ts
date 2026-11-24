// Copyright (c) 2025 Reliant Labs

import { beforeEach, describe, expect, it } from "vitest";

import { isAppName, newChatAppFor, useNewChatAppStore } from "../newChatAppStore";

describe("newChatAppStore", () => {
  beforeEach(() => useNewChatAppStore.getState().clear());

  it("accepts forge's name shape only", () => {
    expect(isAppName("shop")).toBe(true);
    expect(isAppName("shop-front_2")).toBe(true);
    expect(isAppName("2shop")).toBe(false);
    expect(isAppName("shop front")).toBe(false);
    expect(isAppName("a".repeat(129))).toBe(false);
    expect(isAppName(undefined)).toBe(false);
  });

  it("tags only the project it was set for", () => {
    useNewChatAppStore.getState().set({ projectId: "p1", appName: "shop" });
    const { tag } = useNewChatAppStore.getState();
    expect(newChatAppFor(tag, "p1")).toBe("shop");
    expect(newChatAppFor(tag, "p2")).toBeUndefined();
    expect(newChatAppFor(tag, undefined)).toBeUndefined();
  });

  it("drops an invalid name rather than holding it", () => {
    useNewChatAppStore.getState().set({ projectId: "p1", appName: "shop" });
    useNewChatAppStore.getState().set({ projectId: "p1", appName: "not a name" });
    expect(useNewChatAppStore.getState().tag).toBeNull();
  });
});
