import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { GenerateVideoToolRenderer, formatElapsed } from "../GenerateVideoToolRenderer";
import type { ToolRenderContext } from "../types";

function ctx(over: Partial<ToolRenderContext>): ToolRenderContext {
  return {
    toolName: "generate_video",
    toolCallId: "c1",
    input: { prompt: "a red ball" },
    isExpanded: true,
    isCompleted: false,
    isExecuting: true,
    isPreparing: false,
    hasFailed: false,
    ...over,
  };
}

describe("GenerateVideoToolRenderer", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("formats elapsed time", () => {
    expect(formatElapsed(7)).toBe("7s");
    expect(formatElapsed(75)).toBe("1m 15s");
  });

  it("shows ticking elapsed time while executing", () => {
    render(<GenerateVideoToolRenderer ctx={ctx({})} />);
    expect(screen.getByTestId("generate-video-progress")).toHaveTextContent("0s elapsed");
    act(() => {
      vi.advanceTimersByTime(65_000);
    });
    expect(screen.getByTestId("generate-video-progress")).toHaveTextContent("1m 05s elapsed");
  });

  it("drops the progress line once finished", () => {
    render(
      <GenerateVideoToolRenderer
        ctx={ctx({ isExecuting: false, isCompleted: true, result: { name: "generate_video", content: "Generated clip" } })}
      />,
    );
    expect(screen.queryByTestId("generate-video-progress")).toBeNull();
    expect(screen.getByText("Generated clip")).toBeInTheDocument();
  });
});
