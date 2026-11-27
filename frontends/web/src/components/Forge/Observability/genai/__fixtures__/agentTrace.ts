// Copyright (c) 2026 Reliant Labs

/**
 * A recorded agent run's spans, as forge's pkg/observe/genai helpers emit
 * them (and as OpenInference or OpenLLMetry spans read after ingest
 * normalisation): a workflow, a node, an agent turn with two model calls and
 * two tool calls, one of which failed, an HTTP client span under a model call
 * (not an AI step), and a cancelled model call. Metadata only, with one
 * content attribute the lens must never show.
 */

import type { Span } from "@/services/controlPlane/observability/types";

export const AGENT_TRACE_ID = "0af7651916cd43dd8448eb211c80319c";

export function agentTraceSpans(t0: number): Span[] {
  const span = (
    spanId: string,
    parentSpanId: string,
    name: string,
    start: number,
    duration: number,
    attributes: Record<string, string>,
    extra: Partial<Span> = {},
  ): Span => ({
    spanId,
    parentSpanId,
    name,
    service: "support-agent",
    kind: "internal",
    startMs: t0 + start,
    durationMs: duration,
    status: "ok",
    statusMessage: "",
    attributes,
    resource: { "service.name": "support-agent", "service.version": "v4" },
    events: [],
    source: "",
    operatorData: false,
    ...extra,
  });
  const run = {
    "forge.ai.workflow.name": "support",
    "forge.ai.run.id": "run-7",
    "gen_ai.conversation.id": "conv-42",
    "user.id": "u_8812",
  };
  const chat = (
    model: string,
    inTok: number,
    outTok: number,
    extra: Record<string, string> = {},
  ) => ({
    ...run,
    "gen_ai.operation.name": "chat",
    "gen_ai.provider.name": "anthropic",
    "gen_ai.request.model": model,
    "gen_ai.usage.input_tokens": String(inTok),
    "gen_ai.usage.output_tokens": String(outTok),
    ...extra,
  });
  return [
    span(
      "w1",
      "",
      "invoke_workflow support",
      0,
      9_000,
      { ...run, "gen_ai.operation.name": "invoke_workflow" },
      { kind: "server" },
    ),
    span("n1", "w1", "workflow.node answer", 10, 8_900, {
      ...run,
      "forge.ai.node.id": "answer",
    }),
    span("a1", "n1", "invoke_agent triager", 20, 8_800, {
      ...run,
      "gen_ai.operation.name": "invoke_agent",
      "gen_ai.agent.name": "triager",
    }),
    span(
      "m1",
      "a1",
      "chat claude-sonnet-5",
      30,
      2_000,
      chat("claude-sonnet-5", 1200, 300, {
        "gen_ai.response.model": "claude-sonnet-5-20261001",
        "gen_ai.usage.cache_read.input_tokens": "1000",
        "forge.ai.cost.usd": "0.0123",
        // Content a misconfigured SDK sent: the lens must not show it.
        "gen_ai.input.messages":
          '[{"role":"user","content":"my SSN is 123-45-6789"}]',
      }),
      {
        events: [
          { timeMs: t0 + 530, name: "gen_ai.first_token", attributes: {} },
        ],
      },
    ),
    span(
      "h1",
      "m1",
      "POST https://api.anthropic.com/v1/messages",
      35,
      1_990,
      { "http.request.method": "POST" },
      { kind: "client" },
    ),
    span("t1", "a1", "execute_tool search_docs", 2_100, 400, {
      ...run,
      "gen_ai.operation.name": "execute_tool",
      "gen_ai.tool.name": "search_docs",
    }),
    span(
      "t2",
      "a1",
      "execute_tool run_sql",
      2_600,
      150,
      {
        ...run,
        "gen_ai.operation.name": "execute_tool",
        "gen_ai.tool.name": "run_sql",
        "error.type": "QueryError",
      },
      { status: "error", statusMessage: "relation does not exist" },
    ),
    span(
      "m2",
      "a1",
      "chat claude-sonnet-5",
      2_800,
      3_000,
      chat("claude-sonnet-5", 2000, 500, {
        "forge.ai.cost.usd": "0.02",
        "gen_ai.retries": "1",
      }),
    ),
    span(
      "m3",
      "a1",
      "chat claude-haiku-4-5",
      6_000,
      200,
      chat("claude-haiku-4-5", 50, 0, { "error.type": "canceled" }),
      { status: "error" },
    ),
  ];
}
