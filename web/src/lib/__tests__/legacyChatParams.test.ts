import { beforeEach, describe, expect, it } from "vitest";
import { clearLegacyChatParams, peekLegacyChatParams } from "../legacyChatParams";

const KEY = "reliant-chat-params";

function store(version: number, chatParams: Record<string, unknown>, chatPresets: Record<string, unknown> = {}) {
  localStorage.setItem(KEY, JSON.stringify({ version, state: { chatParams, chatPresets } }));
}

describe("peekLegacyChatParams", () => {
  beforeEach(() => localStorage.clear());

  it("is undefined with nothing stored, or nothing for the chat", () => {
    expect(peekLegacyChatParams("c1")).toBeUndefined();
    store(7, { other: { mode: "plan" } });
    expect(peekLegacyChatParams("c1")).toBeUndefined();
    store(7, { c1: {} });
    expect(peekLegacyChatParams("c1")).toBeUndefined();
  });

  it("returns the chat's params, with a group's keys left flat", () => {
    store(7, { c1: { model: { id: "a" }, "Agent A.model": { id: "b" } } });
    expect(peekLegacyChatParams("c1")).toEqual({ model: { id: "a" }, "Agent A.model": { id: "b" } });
  });

  it("repairs the keys older builds poisoned, so they never seed a refused selection", () => {
    store(6, { c1: { model: { tags: ["flagship"] }, thinking_level: "high", "default.mode": "plan" } });
    expect(peekLegacyChatParams("c1")).toEqual({ model: { tags: ["flagship"], thinking_level: "high" }, mode: "plan" });
  });

  it("ignores state older than v5, which migration discarded, and metadata that leaked into params", () => {
    store(4, { c1: { mode: "plan" } });
    expect(peekLegacyChatParams("c1")).toBeUndefined();
    store(7, { c1: { mode: "plan", __selectedWorkflow: "x" } });
    expect(peekLegacyChatParams("c1")).toEqual({ mode: "plan" });
  });

  it("survives a corrupt blob", () => {
    localStorage.setItem(KEY, "{not json");
    expect(peekLegacyChatParams("c1")).toBeUndefined();
    expect(() => clearLegacyChatParams("c1")).not.toThrow();
  });
});

describe("clearLegacyChatParams", () => {
  beforeEach(() => localStorage.clear());

  it("forgets one chat and keeps the others", () => {
    store(7, { c1: { mode: "plan" }, c2: { mode: "auto" } }, { c1: { "": "general" } });
    clearLegacyChatParams("c1");
    const left = JSON.parse(localStorage.getItem(KEY)!);
    expect(left.state.chatParams).toEqual({ c2: { mode: "auto" } });
    expect(left.state.chatPresets).toEqual({});
  });

  it("removes the key when the last chat goes", () => {
    store(7, { c1: { mode: "plan" } });
    clearLegacyChatParams("c1");
    expect(localStorage.getItem(KEY)).toBeNull();
  });
});
