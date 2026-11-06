/**
 * In the simplified thread a live work fold is the turn's status line
 * ("Working… · Reading ChatInput.tsx"); a second "Thinking •••" under it
 * says the same thing twice. The footer steps aside then — but not when it
 * has something the fold cannot say: a stall, or a wait on the machine.
 */

import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ChatThinkingIndicator } from "../ChatThinkingIndicator";
import { LiveTurnFoldContext } from "../thread-views/liveTurnContext";

vi.mock("../../../store/threadActivityStore", () => ({
  useIsThreadActive: () => true,
  useChatCurrentActivity: () => null,
  getActivityDisplayText: () => null,
}));

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

function renderWithFold(liveFold: boolean, props: Partial<Parameters<typeof ChatThinkingIndicator>[0]> = {}) {
  return render(
    <LiveTurnFoldContext.Provider value={liveFold}>
      <ChatThinkingIndicator chatId="c1" progressKey="k1" {...props} />
    </LiveTurnFoldContext.Provider>,
  );
}

describe("ChatThinkingIndicator beside a live fold", () => {
  it("renders as before with no live fold", () => {
    renderWithFold(false);
    expect(screen.getByTestId("thinking-indicator")).toBeInTheDocument();
  });

  it("stays out of the way while the live fold is the status line", () => {
    renderWithFold(true);
    expect(screen.queryByTestId("thinking-indicator")).toBeNull();
  });

  it("comes back for a stall, with its Stop control", () => {
    renderWithFold(true, { onStop: vi.fn() });
    act(() => {
      vi.advanceTimersByTime(61_000);
    });
    expect(screen.getByText("Still working — no response yet")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Stop" })).toBeInTheDocument();
  });

  it("still says the run waits for its machine", () => {
    renderWithFold(true, { waitingOnMachine: true });
    expect(screen.getByTestId("thinking-indicator")).toBeInTheDocument();
  });
});
