import { beforeEach, describe, expect, it } from "vitest";
import { useChatParamsStore } from "../chatParamsStore";

const STORAGE_KEY = "reliant-chat-params";

describe("chatParamsStore migration of stored picker keys", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("repairs a stored top-level thinking_level and default.* keys on load", async () => {
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({
        version: 6,
        state: {
          chatParams: {
            "chat-a": { model: { tags: ["flagship"] }, thinking_level: "high" },
            "chat-b": { "default.model": { id: "preset-model" }, "default.mode": "plan" },
          },
          chatPresets: { "chat-b": { default: "my-preset" } },
        },
      }),
    );

    await useChatParamsStore.persist.rehydrate();

    const state = useChatParamsStore.getState();
    expect(state.getChatParams("chat-a")).toEqual({
      model: { tags: ["flagship"], thinking_level: "high" },
    });
    expect(state.getChatParams("chat-b")).toEqual({ model: { id: "preset-model" }, mode: "plan" });
    expect(state.getChatPresets("chat-b")).toEqual({ "": "my-preset" });
  });
});
