import { describe, expect, it } from "vitest";
import { chatSnapshotAppliedProps, traceIdFromTraceresponse } from "../chatOpenTelemetry";

describe("traceIdFromTraceresponse", () => {
  it("reads the trace id of a W3C traceresponse", () => {
    expect(traceIdFromTraceresponse("00-98028c9f09fd4c11cdcd6d57f26386ba-3cbfd52893356802-01")).toBe(
      "98028c9f09fd4c11cdcd6d57f26386ba",
    );
  });

  it("is empty for a missing or malformed header", () => {
    expect(traceIdFromTraceresponse(null)).toBe("");
    expect(traceIdFromTraceresponse("")).toBe("");
    expect(traceIdFromTraceresponse("00-nothex-3cbfd52893356802-01")).toBe("");
  });
});

describe("chatSnapshotAppliedProps", () => {
  const applied = {
    chatId: "chat-1",
    traceId: "98028c9f09fd4c11cdcd6d57f26386ba",
    openedAtMs: 1000,
    receivedAtMs: 1540.4,
    appliedAtMs: 1610.6,
    initialOpen: true,
    messages: 120,
    otherUpdates: 30,
  };

  it("reports offsets from the stream's open, rounded", () => {
    expect(chatSnapshotAppliedProps(applied, 1700.2)).toEqual({
      chat_id: "chat-1",
      trace_id: "98028c9f09fd4c11cdcd6d57f26386ba",
      initial_open: true,
      received_ms: 540,
      applied_ms: 611,
      visible_ms: 700,
      messages: 120,
      other_updates: 30,
    });
  });

  it("leaves visible_ms out when no frame painted", () => {
    expect(chatSnapshotAppliedProps(applied)).not.toHaveProperty("visible_ms");
  });
});
