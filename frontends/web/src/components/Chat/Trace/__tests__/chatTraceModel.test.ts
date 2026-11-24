// Copyright (c) 2026 Reliant Labs

/**
 * The Trace tab's pure half: a chat span becomes the waterfall's Span (with
 * owner-only attributes dropped for a viewer even if the server sent them),
 * every PARTIAL reason has words, and cost keeps reported and estimated apart
 * with the unpriced share counted.
 */

import { describe, expect, it } from "vitest";

import { ChatTraceCompleteness, ChatTracePartialReason } from "@/gen/reliant/v1/chat_trace_pb";

import {
  costSummary,
  formatTokenCount,
  formatUsdNanos,
  isEmptyTrace,
  isOwnerOnlyAttribute,
  partialReasonText,
  partialReasons,
  spanStatus,
  toWaterfallSpan,
} from "../chatTraceModel";
import { T0, emptyTrace, ownerTrace } from "./fixtures";

describe("toWaterfallSpan", () => {
  it("maps a chat span onto the waterfall's span", () => {
    const chat = ownerTrace().spans.find((span) => span.spanId === "s-chat")!;
    const span = toWaterfallSpan(chat, false);
    expect(span).toMatchObject({
      spanId: "s-chat",
      parentSpanId: "s-turn",
      name: "chat claude-sonnet-5",
      service: "reliant-worker",
      kind: "client",
      startMs: T0 + 300,
      durationMs: 4000,
      status: "ok",
    });
    expect(span.attributes["forge.ai.cost.usd"]).toBe("0.0123");
    expect(span.events).toEqual([
      { timeMs: T0 + 900, name: "gen_ai.first_token", attributes: { "gen_ai.server.time_to_first_token": "0.6" } },
    ]);
    expect(span.links).toEqual([]);
  });

  it("drops cost, reliant.* attributes, event attributes and links for a viewer, whatever the server sent", () => {
    const chat = ownerTrace().spans.find((span) => span.spanId === "s-chat")!;
    const span = toWaterfallSpan(chat, true);
    expect(Object.keys(span.attributes).sort()).toEqual([
      "gen_ai.request.model",
      "gen_ai.usage.input_tokens",
      "gen_ai.usage.output_tokens",
    ]);
    expect(span.events[0].attributes).toEqual({});
    expect(span.links).toBeUndefined();
  });

  it("reads every spelling of OTel status", () => {
    expect(spanStatus("Error")).toBe("error");
    expect(spanStatus("STATUS_CODE_ERROR")).toBe("error");
    expect(spanStatus("Ok")).toBe("ok");
    expect(spanStatus("STATUS_CODE_OK")).toBe("ok");
    expect(spanStatus("Unset")).toBe("unset");
    expect(spanStatus("")).toBe("unset");
  });

  it("knows which attributes are the owner's", () => {
    expect(isOwnerOnlyAttribute("forge.ai.cost.usd")).toBe(true);
    expect(isOwnerOnlyAttribute("reliant.cost_usd")).toBe(true);
    expect(isOwnerOnlyAttribute("reliant.anything")).toBe(true);
    expect(isOwnerOnlyAttribute("gen_ai.usage.input_tokens")).toBe(false);
    expect(isOwnerOnlyAttribute("gen_ai.tool.name")).toBe(false);
  });
});

describe("partial reasons", () => {
  it("names every reason in plain words", () => {
    const reasons = [
      ChatTracePartialReason.TRACE_EXPIRED,
      ChatTracePartialReason.SPANS_UNAVAILABLE,
      ChatTracePartialReason.SPANS_TRUNCATED,
      ChatTracePartialReason.SPANS_MISSING,
      ChatTracePartialReason.CALLS_EXPIRED,
      ChatTracePartialReason.CALLS_TRUNCATED,
    ];
    const texts = reasons.map(partialReasonText);
    expect(new Set(texts).size).toBe(reasons.length);
    for (const text of texts) {
      expect(text).not.toMatch(/_/);
      expect(text).not.toMatch(/span/i);
    }
    expect(partialReasonText(ChatTracePartialReason.TRACE_EXPIRED)).toMatch(/7 days/);
    expect(partialReasonText(ChatTracePartialReason.CALLS_EXPIRED)).toMatch(/90 days/);
  });

  it("is empty for a complete answer and never empty for a partial one", () => {
    expect(partialReasons({ completeness: ChatTraceCompleteness.COMPLETE, partialReasons: [] })).toEqual([]);
    expect(partialReasons({ completeness: ChatTraceCompleteness.PARTIAL, partialReasons: [] })).toEqual([
      "Part of this chat's trace is missing.",
    ]);
    expect(
      partialReasons({
        completeness: ChatTraceCompleteness.PARTIAL,
        partialReasons: [ChatTracePartialReason.TRACE_EXPIRED, ChatTracePartialReason.TRACE_EXPIRED],
      }),
    ).toHaveLength(1);
  });

  it("knows an empty answer", () => {
    expect(isEmptyTrace(emptyTrace())).toBe(true);
    expect(isEmptyTrace(ownerTrace())).toBe(false);
  });
});

describe("money and counts", () => {
  it("formats billionths of a dollar without reading a small cost as zero", () => {
    expect(formatUsdNanos(0n)).toBe("$0.00");
    expect(formatUsdNanos(50n)).toBe("<$0.0001");
    expect(formatUsdNanos(12_300_000n)).toBe("$0.0123");
    expect(formatUsdNanos(4_500_000_000n)).toBe("$4.50");
  });

  it("keeps reported and estimated apart and counts the unpriced share", () => {
    const cost = costSummary(ownerTrace().totals);
    expect(cost.reported).toEqual({ usd: "$0.0123", calls: 1 });
    expect(cost.estimated).toEqual({ usd: "$0.0020", calls: 1 });
    expect(cost.unpricedCalls).toBe(1);
    expect(cost.unknownCalls).toBe(0);
    expect(cost.unpricedShare).toBeCloseTo(1 / 3);
  });

  it("has no reported or estimated figure when no call was priced that way", () => {
    const cost = costSummary(undefined);
    expect(cost).toMatchObject({ calls: 0, reported: null, estimated: null, unpricedShare: 0 });
  });

  it("counts tokens", () => {
    expect(formatTokenCount(1260n)).toBe((1260).toLocaleString());
    expect(formatTokenCount(12_500)).toBe("12.5k");
    expect(formatTokenCount(3_000_000n)).toBe("3M");
  });
});
