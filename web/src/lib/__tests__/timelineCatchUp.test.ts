import { describe, expect, it } from "vitest";
import { isTimelineCatchingUp } from "../timelineCatchUp";

const cached = [{ id: "m1" }, { id: "m2" }];

describe("isTimelineCatchingUp", () => {
  it("is true while the stream has not caught up over cached content", () => {
    expect(
      isTimelineCatchingUp({ syncPending: true, messages: cached, connectionStatus: "connected" }),
    ).toBe(true);
  });

  it("is true for a cold open with nothing cached yet", () => {
    expect(
      isTimelineCatchingUp({ syncPending: true, messages: [], connectionStatus: "connecting" }),
    ).toBe(true);
  });

  it("is false once caught up, however much is streaming", () => {
    const streaming = [...cached, { id: "streaming-assistant" }];
    expect(
      isTimelineCatchingUp({ syncPending: false, messages: streaming, connectionStatus: "connected" }),
    ).toBe(false);
  });

  it("is false while the stream is down, so an offline reader keeps the cached transcript", () => {
    for (const connectionStatus of ["disconnected", "error"]) {
      expect(
        isTimelineCatchingUp({ syncPending: true, messages: cached, connectionStatus }),
      ).toBe(false);
    }
  });

  it("is false for a chat showing only this client's own sends (just started)", () => {
    expect(
      isTimelineCatchingUp({
        syncPending: true,
        messages: [{ id: "optimistic-user-123" }, { id: "optimistic-failed-abc" }],
        connectionStatus: "connected",
      }),
    ).toBe(false);
  });

  it("is true when local sends sit on top of cached server content", () => {
    expect(
      isTimelineCatchingUp({
        syncPending: true,
        messages: [...cached, { id: "optimistic-user-123" }],
        connectionStatus: "connected",
      }),
    ).toBe(true);
  });
});
