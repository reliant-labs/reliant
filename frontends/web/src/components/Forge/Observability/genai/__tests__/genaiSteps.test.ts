// @vitest-environment node
// Copyright (c) 2026 Reliant Labs

/** The GenAI lens's model: steps, nesting, rollups and what it never reads. */

import { describe, expect, it } from "vitest";

import type { Span } from "@/services/controlPlane/observability/types";

import { agentTraceSpans } from "../__fixtures__/agentTrace";
import {
  buildGenAITrace,
  formatUsd,
  hasGenAI,
  STEP_KEYS,
  type Step,
} from "../genaiSteps";

const T0 = Date.parse("2026-11-27T10:00:00Z");

const outline = (steps: Step[], depth = 0): string[] =>
  steps.flatMap((s) => [
    `${"  ".repeat(depth)}${s.kind}:${s.label}`,
    ...outline(s.children, depth + 1),
  ]);

describe("buildGenAITrace", () => {
  it("nests workflow, node, agent, model and tool steps, skipping the HTTP span under a model call", () => {
    const t = buildGenAITrace(agentTraceSpans(T0));
    expect(outline(t.roots)).toEqual([
      "workflow:support",
      "  node:answer",
      "    agent:triager",
      "      model:claude-sonnet-5-20261001",
      "      tool:search_docs",
      "      tool:run_sql",
      "      model:claude-sonnet-5",
      "      model:claude-haiku-4-5",
    ]);
  });

  it("reads tokens, time to first token, retries and reported cost per model call", () => {
    const t = buildGenAITrace(agentTraceSpans(T0));
    const [m1, , , m2] = t.roots[0]!.children[0]!.children[0]!.children;
    expect(m1).toMatchObject({
      provider: "anthropic",
      ttftMs: 500,
      tokens: { input: 1200, output: 300, cacheRead: 1000 },
      cost: { usd: 0.0123, source: "reported" },
    });
    expect(m2).toMatchObject({
      retries: 1,
      cost: { usd: 0.02, source: "reported" },
    });
  });

  it("rolls totals up, counting a failure but never a cancellation", () => {
    const t = buildGenAITrace(agentTraceSpans(T0));
    expect(t.totals.modelCalls).toBe(3);
    expect(t.totals.toolCalls).toBe(2);
    expect(t.totals.failed).toBe(1);
    expect(t.totals.tokens).toMatchObject({
      input: 3250,
      output: 800,
      cacheRead: 1000,
    });
    expect(t.totals.cost?.usd).toBeCloseTo(0.0323, 6);
    expect(t.totals.cost?.source).toBe("reported");
    const cancelled = t.roots[0]!.children[0]!.children[0]!.children[4]!;
    expect(cancelled).toMatchObject({ failed: false, errorType: "canceled" });
    expect(t.conversationId).toBe("conv-42");
    expect(t.userId).toBe("u_8812");
  });

  it("shows no cost, not $0, where no step reported one", () => {
    const spans = agentTraceSpans(T0).map((s) => {
      const attributes = { ...s.attributes };
      delete attributes[STEP_KEYS.costUsd];
      return { ...s, attributes };
    });
    expect(buildGenAITrace(spans).totals.cost).toBeUndefined();
  });

  it("reads Reliant's older cost name and the older cache-read spelling", () => {
    const [m] = agentTraceSpans(T0).filter((s) => s.spanId === "m2");
    const span: Span = {
      ...m!,
      parentSpanId: "",
      attributes: {
        ...m!.attributes,
        "forge.ai.cost.usd": "",
        "reliant.cost_usd": "0.5",
        "gen_ai.usage.cache_read_input_tokens": "7",
      },
    };
    delete span.attributes["forge.ai.cost.usd"];
    const [step] = buildGenAITrace([span]).roots;
    expect(step).toMatchObject({
      cost: { usd: 0.5 },
      tokens: { cacheRead: 7 },
    });
  });

  it("names where the spans came from, after ingest normalisation", () => {
    const spans = agentTraceSpans(T0).map((s) => ({
      ...s,
      attributes: { ...s.attributes, "forge.ai.convention": "openinference" },
    }));
    expect(buildGenAITrace(spans).conventions).toEqual(["openinference"]);
  });

  it("lifts AI spans out of a non-AI root", () => {
    const spans = agentTraceSpans(T0);
    const http: Span = {
      ...spans[4]!,
      spanId: "root",
      parentSpanId: "",
      name: "POST /answer",
      attributes: {},
    };
    const rooted = spans.map((s) =>
      s.spanId === "w1" ? { ...s, parentSpanId: "root" } : s,
    );
    expect(outline(buildGenAITrace([http, ...rooted]).roots)[0]).toBe(
      "workflow:support",
    );
  });

  it("never carries a content attribute into a step label", () => {
    const t = buildGenAITrace(agentTraceSpans(T0));
    expect(JSON.stringify(outline(t.roots))).not.toContain("SSN");
    expect(Object.values(STEP_KEYS)).not.toContain("gen_ai.input.messages");
  });
});

describe("hasGenAI", () => {
  it("is true only when a span carries gen_ai.operation.name", () => {
    const spans = agentTraceSpans(T0);
    expect(hasGenAI(spans)).toBe(true);
    expect(
      hasGenAI(spans.filter((s) => !s.attributes["gen_ai.operation.name"])),
    ).toBe(false);
  });
});

describe("formatUsd", () => {
  it("keeps four decimals under a dollar", () => {
    expect(formatUsd(0.0123)).toBe("$0.0123");
    expect(formatUsd(12.345)).toBe("$12.35");
    expect(formatUsd(0.00001)).toBe("<$0.0001");
    expect(formatUsd(0)).toBe("$0");
  });
});
