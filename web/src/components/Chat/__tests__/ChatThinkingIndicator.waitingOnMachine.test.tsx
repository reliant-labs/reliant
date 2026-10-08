/**
 * A run that needs a machine is held until its machine is up
 * (research/QUEUE_UNTIL_DAEMON.md): the chat's activity reads
 * WAITING_FOR_DAEMON while it waits. The transcript's footer says that, rather
 * than cycling "Thinking…" over a run that has not started thinking.
 */

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { ChatThinkingIndicator } from "../ChatThinkingIndicator";

vi.mock("../../../store/threadActivityStore", () => ({
  useIsThreadActive: () => true,
  useChatCurrentActivity: () => "call_llm",
  getActivityDisplayText: (activity: string | null) => (activity ? `Doing ${activity}` : null),
}));

describe("ChatThinkingIndicator", () => {
  it("says the run is waiting for the machine while it is held for one", () => {
    render(<ChatThinkingIndicator chatId="c1" waitingOnMachine />);
    expect(screen.getByText("Waiting for your machine")).toBeInTheDocument();
    expect(screen.queryByText("Doing call_llm")).not.toBeInTheDocument();
  });

  it("shows the run's own activity once it is not waiting", () => {
    render(<ChatThinkingIndicator chatId="c1" />);
    expect(screen.getByText("Doing call_llm")).toBeInTheDocument();
    expect(screen.queryByText("Waiting for your machine")).not.toBeInTheDocument();
  });
});
