/**
 * "Processing" cycling for minutes with nothing streamed is the same picture
 * as a run that has wedged (chat 66a045ce, 2026-10-10: its replay was failing
 * for three minutes under "Processing •••"). Past a bound with no output the
 * indicator says so, and offers a way to look at the run or stop it.
 */

import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ChatThinkingIndicator } from "../ChatThinkingIndicator";

const activity = vi.hoisted(() => ({ current: null as string | null }));

vi.mock("../../../store/threadActivityStore", () => ({
  useIsThreadActive: () => true,
  useChatCurrentActivity: () => activity.current,
  getActivityDisplayText: (a: string | null) => {
    if (!a) return null;
    return a === "ExecuteTools" ? "Running tools" : "Thinking";
  },
}));

beforeEach(() => {
  vi.useFakeTimers();
  activity.current = null;
});
afterEach(() => {
  vi.useRealTimers();
});

function advance(ms: number) {
  act(() => {
    vi.advanceTimersByTime(ms);
  });
}

describe("ChatThinkingIndicator: a long silence", () => {
  it("reads as still working after a minute with no output, with Run status and Stop", () => {
    const onShowRunStatus = vi.fn();
    const onStop = vi.fn();
    render(<ChatThinkingIndicator chatId="c1" progressKey="k1" onShowRunStatus={onShowRunStatus} onStop={onStop} />);

    advance(59_000);
    expect(screen.queryByText("Still working — no response yet")).toBeNull();

    advance(2_000);
    expect(screen.getByText("Still working — no response yet")).toBeInTheDocument();
    expect(screen.getByText("· 1m 01s")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Run status" }));
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    expect(onShowRunStatus).toHaveBeenCalledOnce();
    expect(onStop).toHaveBeenCalledOnce();
  });

  it("starts the bound again whenever the run shows something", () => {
    const { rerender } = render(<ChatThinkingIndicator chatId="c1" progressKey="k1" />);
    advance(50_000);
    rerender(<ChatThinkingIndicator chatId="c1" progressKey="k2" />);
    advance(50_000);
    expect(screen.queryByText("Still working — no response yet")).toBeNull();
    advance(11_000);
    expect(screen.getByText("Still working — no response yet")).toBeInTheDocument();

    rerender(<ChatThinkingIndicator chatId="c1" progressKey="k3" />);
    expect(screen.queryByText("Still working — no response yet")).toBeNull();
  });

  it("covers a model call that streams nothing, not a tool that says what it is doing", () => {
    activity.current = "CallLLM";
    const { unmount } = render(<ChatThinkingIndicator chatId="c1" progressKey="k1" />);
    advance(61_000);
    expect(screen.getByText("Still working — no response yet")).toBeInTheDocument();
    unmount();

    activity.current = "ExecuteTools";
    render(<ChatThinkingIndicator chatId="c1" progressKey="k1" />);
    advance(300_000);
    expect(screen.getByText("Running tools")).toBeInTheDocument();
    expect(screen.queryByText("Still working — no response yet")).toBeNull();
  });

  it("is not a silence while the run waits for its machine", () => {
    render(<ChatThinkingIndicator chatId="c1" progressKey="k1" waitingOnMachine messageQueuedForMachine />);
    advance(300_000);
    expect(screen.getByText("Queued — will send when your machine connects")).toBeInTheDocument();
    expect(screen.queryByText("Still working — no response yet")).toBeNull();
  });
});
