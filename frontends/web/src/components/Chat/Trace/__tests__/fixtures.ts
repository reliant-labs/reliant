// Copyright (c) 2026 Reliant Labs

/**
 * ChatTraceService answers for the Trace tab's tests, at fixed times (no
 * wall clock): one run with a workflow node, an agent turn, a model call and
 * a tool call; two model calls, one reported and one estimated; and the
 * viewer shape the server answers for the same chat.
 */

import { create } from "@bufbuild/protobuf";
import { timestampFromMs, type Timestamp } from "@bufbuild/protobuf/wkt";

import {
  ChatTraceCompleteness,
  GetChatTraceResponseSchema,
  type ChatTracePartialReason,
  type GetChatTraceResponse,
} from "@/gen/reliant/v1/chat_trace_pb";

export const T0 = Date.UTC(2026, 10, 24, 12, 0, 0);
const at = (offsetMs: number) => timestampFromMs(T0 + offsetMs);
const ns = (ms: number) => BigInt(ms) * 1_000_000n;

interface SpanInit {
  spanId: string;
  parentSpanId: string;
  name: string;
  kind: string;
  serviceName: string;
  startTime: Timestamp;
  durationNs: bigint;
  statusCode: string;
  attributes?: Record<string, string>;
  events?: { name: string; time: Timestamp; attributes?: Record<string, string> }[];
}

const ownerSpans: SpanInit[] = [
  { spanId: "s-run", parentSpanId: "", name: "RunWorkflow:DynamicWorkflow", kind: "Internal", serviceName: "reliant-worker", startTime: at(0), durationNs: ns(9000), statusCode: "Ok" },
  { spanId: "s-node", parentSpanId: "s-run", name: "workflow.node agent", kind: "Internal", serviceName: "reliant-worker", startTime: at(100), durationNs: ns(8800), statusCode: "Unset" },
  { spanId: "s-turn", parentSpanId: "s-node", name: "invoke_agent main", kind: "Internal", serviceName: "reliant-worker", startTime: at(200), durationNs: ns(8000), statusCode: "Unset" },
  {
    spanId: "s-chat",
    parentSpanId: "s-turn",
    name: "chat claude-sonnet-5",
    kind: "Client",
    serviceName: "reliant-worker",
    startTime: at(300),
    durationNs: ns(4000),
    statusCode: "Ok",
    attributes: {
      "gen_ai.request.model": "claude-sonnet-5",
      "gen_ai.usage.input_tokens": "1200",
      "gen_ai.usage.output_tokens": "300",
      "forge.ai.cost.usd": "0.0123",
      "reliant.cost_usd": "0.0123",
    },
    events: [{ name: "gen_ai.first_token", time: at(900), attributes: { "gen_ai.server.time_to_first_token": "0.6" } }],
  },
  {
    spanId: "s-tool",
    parentSpanId: "s-turn",
    name: "execute_tool shell",
    kind: "Internal",
    serviceName: "reliant-worker",
    startTime: at(4500),
    durationNs: ns(2500),
    statusCode: "Error",
    attributes: { "gen_ai.tool.name": "shell" },
  },
];

export function ownerTrace(overrides: { spans?: SpanInit[] } = {}): GetChatTraceResponse {
  return create(GetChatTraceResponseSchema, {
    chatId: "chat-1",
    completeness: ChatTraceCompleteness.COMPLETE,
    viewer: false,
    spans: ownerSpans.map((span) => ({ traceId: "t-1", ...span })),
    modelCalls: [
      {
        id: "call-1",
        attempt: 1,
        time: at(300),
        purpose: "chat",
        provider: "anthropic",
        model: "claude-sonnet-5",
        outcome: "success",
        inputTokens: 1200n,
        outputTokens: 300n,
        cacheReadTokens: 800n,
        latencyMs: 4000n,
        ttftMs: 600n,
        authMode: "managed",
        costUsdNanos: 12_300_000n,
        costBasis: "reported",
      },
      {
        id: "call-2",
        attempt: 1,
        time: at(7000),
        purpose: "title",
        provider: "openai",
        model: "gpt-5-mini",
        outcome: "error",
        errorClass: "rate_limit",
        inputTokens: 50n,
        outputTokens: 0n,
        latencyMs: 200n,
        authMode: "byok",
        costUsdNanos: 2_000_000n,
        costBasis: "estimated",
      },
      {
        id: "call-3",
        attempt: 1,
        time: at(8000),
        purpose: "chat",
        provider: "anthropic",
        model: "claude-opus-5-5",
        outcome: "success",
        inputTokens: 10n,
        outputTokens: 10n,
        latencyMs: 100n,
        authMode: "subscription",
        costBasis: "unpriced",
      },
    ],
    totals: {
      calls: 3n,
      errorCalls: 1n,
      inputTokens: 1260n,
      outputTokens: 310n,
      cacheReadTokens: 800n,
      latencyMsTotal: 4300n,
      reportedCostUsdNanos: 12_300_000n,
      reportedCalls: 1n,
      estimatedCostUsdNanos: 2_000_000n,
      estimatedCalls: 1n,
      unpricedCalls: 1n,
      unknownCostCalls: 0n,
    },
    asOf: at(60_000),
    ...overrides,
  });
}

/** The same chat as the server answers a viewer: no ids, cost or `reliant.*`. */
export function viewerTrace(): GetChatTraceResponse {
  const owner = ownerTrace();
  return create(GetChatTraceResponseSchema, {
    chatId: "chat-1",
    completeness: ChatTraceCompleteness.COMPLETE,
    viewer: true,
    spans: ownerSpans.map((span) => ({
      ...span,
      attributes: Object.fromEntries(
        Object.entries(span.attributes ?? {}).filter(([key]) => key.startsWith("gen_ai.")),
      ),
      events: (span.events ?? []).map((event) => ({ name: event.name, time: event.time })),
      traceId: "",
    })),
    modelCalls: owner.modelCalls.map((call) => ({
      attempt: call.attempt,
      time: call.time,
      purpose: call.purpose,
      provider: call.provider,
      model: call.model,
      outcome: call.outcome,
      errorClass: call.errorClass,
      inputTokens: call.inputTokens,
      outputTokens: call.outputTokens,
      cacheReadTokens: call.cacheReadTokens,
      latencyMs: call.latencyMs,
      ttftMs: call.ttftMs,
    })),
    totals: { calls: 3n, errorCalls: 1n, inputTokens: 1260n, outputTokens: 310n, cacheReadTokens: 800n, latencyMsTotal: 4300n },
    asOf: at(60_000),
  });
}

export function partialTrace(reasons: ChatTracePartialReason[], base = ownerTrace()): GetChatTraceResponse {
  return { ...base, completeness: ChatTraceCompleteness.PARTIAL, partialReasons: reasons };
}

export function emptyTrace(): GetChatTraceResponse {
  return create(GetChatTraceResponseSchema, {
    chatId: "chat-1",
    completeness: ChatTraceCompleteness.COMPLETE,
    totals: {},
    asOf: at(60_000),
  });
}
