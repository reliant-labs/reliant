import { describe, expect, it } from "vitest";
import { isTimelineCatchingUp } from "../timelineCatchUp";

const cached = [{ id: "m1" }, { id: "m2" }];

describe("isTimelineCatchingUp", () => {
  it("is true while the stream has not caught up over cached content", () => {
    expect(
      isTimelineCatchingUp({ chatSyncPending: true, threadHistoryPending: false, messages: cached, connectionStatus: "connected" }),
    ).toBe(true);
  });

  it("is true for a cold open with nothing cached yet", () => {
    expect(
      isTimelineCatchingUp({ chatSyncPending: true, threadHistoryPending: false, messages: [], connectionStatus: "connecting" }),
    ).toBe(true);
  });

  it("is false once caught up, however much is streaming", () => {
    const streaming = [...cached, { id: "streaming-assistant" }];
    expect(
      isTimelineCatchingUp({ chatSyncPending: false, threadHistoryPending: false, messages: streaming, connectionStatus: "connected" }),
    ).toBe(false);
  });

  it("is false while the stream is down, so an offline reader keeps the cached transcript", () => {
    for (const connectionStatus of ["disconnected", "error"]) {
      expect(
        isTimelineCatchingUp({ chatSyncPending: true, threadHistoryPending: false, messages: cached, connectionStatus }),
      ).toBe(false);
    }
  });

  it("is false for a chat showing only this client's own sends (just started)", () => {
    expect(
      isTimelineCatchingUp({
        chatSyncPending: true, threadHistoryPending: false,
        messages: [{ id: "optimistic-user-123" }, { id: "optimistic-failed-abc" }],
        connectionStatus: "connected",
      }),
    ).toBe(false);
  });

  it("is true when local sends sit on top of cached server content", () => {
    expect(
      isTimelineCatchingUp({
        chatSyncPending: true, threadHistoryPending: false,
        messages: [...cached, { id: "optimistic-user-123" }],
        connectionStatus: "connected",
      }),
    ).toBe(true);
  });
});

describe("isTimelineCatchingUp for a side (spawn) thread", () => {
  const caughtUpChat = { chatSyncPending: false, connectionStatus: "connected" };

  it("is true while the thread's history has not loaded, over the previous thread", () => {
    expect(
      isTimelineCatchingUp({ ...caughtUpChat, threadHistoryPending: true, messages: cached }),
    ).toBe(true);
  });

  it("is true with nothing on screen yet", () => {
    expect(
      isTimelineCatchingUp({ ...caughtUpChat, threadHistoryPending: true, messages: [] }),
    ).toBe(true);
  });

  it("is true even when what is on screen is only local sends — they are another thread's", () => {
    expect(
      isTimelineCatchingUp({
        ...caughtUpChat,
        threadHistoryPending: true,
        messages: [{ id: "optimistic-user-1" }],
      }),
    ).toBe(true);
  });

  it("is false once the thread's history has loaded and the chat is caught up", () => {
    expect(
      isTimelineCatchingUp({ ...caughtUpChat, threadHistoryPending: false, messages: cached }),
    ).toBe(false);
  });

  it("is false while the stream is down", () => {
    expect(
      isTimelineCatchingUp({
        chatSyncPending: false,
        threadHistoryPending: true,
        messages: cached,
        connectionStatus: "disconnected",
      }),
    ).toBe(false);
  });
});
