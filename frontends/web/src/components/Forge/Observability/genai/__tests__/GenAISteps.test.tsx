// Copyright (c) 2026 Reliant Labs

/**
 * The GenAI lens in the trace view: a trace with GenAI spans opens on its AI
 * steps, with a way back to the waterfall; any other trace is unchanged.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import {
  createFixtureApi,
  FIXTURE_TRACE_ID,
} from "../../__fixtures__/fixtureApi";
import { renderPanel } from "../../__tests__/obsTestUtils";
import { AGENT_TRACE_ID, agentTraceSpans } from "../__fixtures__/agentTrace";

vi.mock("uplot", async () =>
  (await import("../../__tests__/obsMocks")).obsMocks.uplot(),
);
vi.mock("@tanstack/react-virtual", async () =>
  (await import("../../__tests__/obsMocks")).obsMocks.virtual(),
);
vi.mock("@rrweb/replay", async () => ({
  Replayer: (await import("../../Sessions/__tests__/fakeReplayer"))
    .FakeReplayer,
}));
vi.mock("@rrweb/replay/dist/style.css", () => ({}));

const NOW = Date.parse("2026-11-27T10:30:00Z");

function agentApi() {
  const api = createFixtureApi();
  vi.spyOn(api, "getTrace").mockImplementation(async (req) => ({
    traceId: req.traceId,
    truncated: false,
    spans: agentTraceSpans(NOW - 60_000),
  }));
  return api;
}

describe("GenAI lens", () => {
  it("opens an agent trace on its AI steps, with totals, cost and the failed tool", async () => {
    renderPanel(agentApi(), { view: "traces", trace: AGENT_TRACE_ID });
    const steps = await screen.findByTestId("genai-steps");
    const rows = within(steps).getAllByTestId("genai-step");
    expect(rows.map((r) => r.getAttribute("data-kind"))).toEqual([
      "workflow",
      "node",
      "agent",
      "model",
      "tool",
      "tool",
      "model",
      "model",
    ]);
    expect(rows[3]).toHaveTextContent("claude-sonnet-5-20261001");
    expect(rows[3]).toHaveTextContent("1.5k tok");
    expect(rows[3]).toHaveTextContent("$0.0123");
    expect(within(rows[5]!).getByTestId("genai-step-failed")).toHaveTextContent(
      "QueryError",
    );
    expect(rows[7]).toHaveTextContent("cancelled");
    expect(within(steps).getByTestId("genai-total-cost")).toHaveTextContent(
      "$0.0323",
    );
    expect(within(steps).getByTestId("genai-conversation")).toHaveTextContent(
      "conv-42",
    );
    expect(within(steps).getByTestId("genai-no-content")).toBeInTheDocument();
    // The content attribute a misconfigured SDK sent is not in the lens.
    expect(steps).not.toHaveTextContent("SSN");
  });

  it("selects a step's span, and switches to the waterfall and back", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(agentApi(), {
      view: "traces",
      trace: AGENT_TRACE_ID,
    });
    const rows = await screen.findAllByTestId("genai-step");
    await user.click(rows[4]!);
    expect(harness.current()).toMatchObject({ span: "t1" });
    await user.click(screen.getByTestId("trace-lens-waterfall"));
    expect(screen.queryByTestId("genai-steps")).not.toBeInTheDocument();
    expect(screen.getByTestId("trace-lens-waterfall")).toHaveAttribute(
      "aria-selected",
      "true",
    );
    await user.click(screen.getByTestId("trace-lens-steps"));
    expect(screen.getByTestId("genai-steps")).toBeInTheDocument();
  });

  it("leaves a trace with no GenAI spans as it was", async () => {
    renderPanel(createFixtureApi(), {
      view: "traces",
      trace: FIXTURE_TRACE_ID,
    });
    await screen.findByTestId("trace-title");
    expect(screen.queryByTestId("trace-lens")).not.toBeInTheDocument();
    expect(screen.queryByTestId("genai-steps")).not.toBeInTheDocument();
  });
});
