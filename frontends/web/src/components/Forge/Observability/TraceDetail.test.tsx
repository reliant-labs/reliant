// Copyright (c) 2026 Reliant Labs

import { useState } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { Span } from "@/services/controlPlane/observability/types";
import { TraceDetail } from "./TraceDetail";

vi.mock("@/hooks/observability-queries", () => ({
  useTrace: () => ({ data: { spans: [span] }, dataUpdatedAt: 0 }),
}));
vi.mock("./AskReliant/AskReliant", () => ({ AskReliantButton: () => null }));
vi.mock("./AskReliant/askReliantContext", () => ({ useObsAskContext: () => ({ environments: [] }) }));
vi.mock("./Metrics/pivots", () => ({ useExploreHref: () => null }));
vi.mock("./serviceSignals", () => ({
  useSignalsIndex: () => null,
  signalState: () => ({ kind: "absent" }),
}));
vi.mock("./LogsView", () => ({ shortId: (id: string) => id }));

const span: Span = {
  spanId: "model-span",
  parentSpanId: "",
  name: "chat",
  service: "agent",
  kind: "client",
  startMs: 1000,
  durationMs: 100,
  status: "error",
  statusMessage: "PRIVATE_STATUS_CONTENT",
  attributes: {
    "gen_ai.operation.name": "chat",
    "gen_ai.request.model": "test-model",
    "gen_ai.prompt": "PRIVATE_PROMPT",
    "gen_ai.completion": "PRIVATE_COMPLETION",
    "gen_ai.tool.call.arguments": "PRIVATE_TOOL_ARGUMENTS",
    "unexpected.sdk.field": "PRIVATE_UNKNOWN_CONTENT",
  },
  resource: { "unexpected.resource.field": "PRIVATE_RESOURCE_CONTENT" },
  events: [{
    name: "PRIVATE_EVENT_NAME",
    timeMs: 1050,
    attributes: { message: "PRIVATE_EVENT_CONTENT" },
  }],
  source: "",
  operatorData: false,
};

function SelectedTrace() {
  const [spanId, setSpanId] = useState<string>();
  return (
    <TraceDetail
      scope={{ orgId: "org", envIds: [], filters: [] }}
      subject="test"
      traceId="trace"
      spanId={spanId}
      range="1h"
      navigate={(next) => setSpanId(next.span)}
    />
  );
}

describe("AI trace detail content safety", () => {
  it("hides raw span content throughout the detail view after selecting an AI step", () => {
    const { container } = render(<SelectedTrace />);
    fireEvent.click(screen.getByTestId("genai-step"));
    expect(screen.getByTestId("span-details")).toBeTruthy();
    expect(container.innerHTML).not.toContain("PRIVATE_");
    expect(screen.getByTestId("genai-steps").textContent).toContain("test-model");

    // The ordinary waterfall remains an explicit way to inspect raw telemetry.
    fireEvent.click(screen.getByTestId("trace-lens-waterfall"));
    expect(screen.getByTestId("span-details").textContent).toContain("PRIVATE_STATUS_CONTENT");
    expect(screen.getByTestId("span-details").textContent).toContain("PRIVATE_RESOURCE_CONTENT");
    fireEvent.click(screen.getByTestId("trace-lens-steps"));
    expect(container.innerHTML).not.toContain("PRIVATE_");
  });
});
