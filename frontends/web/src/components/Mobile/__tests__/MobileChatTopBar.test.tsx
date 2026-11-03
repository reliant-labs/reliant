/**
 * The mobile chat top bar (mobile-chat-redesign §5.1): one status line only
 * when there is status, the context ring only once compaction is near, and a
 * back control that leaves a sub-thread before it leaves the chat.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, ...props }: { children?: React.ReactNode }) => <a {...props}>{children}</a>,
}));

const { MobileChatTopBar, CONTEXT_RING_THRESHOLD } = await import("../MobileChatTopBar");

function renderBar(props: Partial<React.ComponentProps<typeof MobileChatTopBar>> = {}) {
  const onOpenSheet = vi.fn();
  render(<MobileChatTopBar title="Fix flaky login test" onOpenSheet={onOpenSheet} {...props} />);
  return { onOpenSheet };
}

describe("MobileChatTopBar subtitle", () => {
  it("renders no status line for a quiet chat", () => {
    renderBar();
    expect(screen.getByText("Fix flaky login test")).toBeInTheDocument();
    expect(screen.queryByTestId("mobile-chat-status")).not.toBeInTheDocument();
  });

  it("shows a running workflow", () => {
    renderBar({ runningWorkflow: "Get It Right" });
    expect(screen.getByTestId("mobile-chat-status")).toHaveTextContent("Running · Get It Right");
  });

  it("names the thread in view when it is not the main one", () => {
    renderBar({ subThread: { name: "Reviewer", backLabel: "Back to main thread" }, onBackToThread: vi.fn() });
    expect(screen.getByTestId("mobile-chat-status")).toHaveTextContent("Thread: Reviewer");
  });

  it("says when the chat has no machine", () => {
    renderBar({ noMachine: true });
    expect(screen.getByTestId("mobile-chat-status")).toHaveTextContent("No machine");
  });

  it("keeps every status on the one line", () => {
    renderBar({ runningWorkflow: "Agent", noMachine: true });
    expect(screen.getByTestId("mobile-chat-status")).toHaveTextContent("Running · Agent · No machine");
  });
});

describe("MobileChatTopBar title", () => {
  it("opens the Chat sheet", async () => {
    const { onOpenSheet } = renderBar();
    await userEvent.click(screen.getByRole("button", { name: /chat actions/ }));
    expect(onOpenSheet).toHaveBeenCalledTimes(1);
  });
});

describe("MobileChatTopBar context ring", () => {
  const threshold = 200_000;

  it("stays out of the bar below the threshold", () => {
    renderBar({ contextUsage: { threadTokenCount: threshold * CONTEXT_RING_THRESHOLD - 1, compactionThreshold: threshold } });
    expect(screen.queryByRole("button", { name: /^Context/ })).not.toBeInTheDocument();
  });

  it("appears at the threshold and opens the sheet", async () => {
    const { onOpenSheet } = renderBar({
      contextUsage: { threadTokenCount: threshold * CONTEXT_RING_THRESHOLD, compactionThreshold: threshold },
    });
    await userEvent.click(screen.getByRole("button", { name: "Context 70% used" }));
    expect(onOpenSheet).toHaveBeenCalledTimes(1);
  });

  it("stays hidden without usage", () => {
    renderBar({ contextUsage: null });
    expect(screen.queryByRole("button", { name: /^Context/ })).not.toBeInTheDocument();
  });
});

describe("MobileChatTopBar back", () => {
  it("goes back to the chat list from the main view", () => {
    renderBar();
    expect(screen.getByLabelText("Back to chats")).toHaveAttribute("to", "/m/chats");
  });

  it("returns to the parent thread first from a sub-thread", async () => {
    const onBackToThread = vi.fn();
    renderBar({ subThread: { name: "Child", backLabel: "Back to Parent" }, onBackToThread });
    expect(screen.queryByLabelText("Back to chats")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Back to Parent" }));
    expect(onBackToThread).toHaveBeenCalledTimes(1);
  });

  it("drops back-to-chats while the list pane is beside the chat", () => {
    renderBar({ listPaneVisible: true });
    expect(screen.queryByLabelText("Back to chats")).not.toBeInTheDocument();
    // The title is then the bar's first control.
    expect(screen.getByRole("button", { name: /chat actions/ })).toBeInTheDocument();
  });

  it("still returns to the parent thread beside the list pane", async () => {
    const onBackToThread = vi.fn();
    renderBar({
      listPaneVisible: true,
      subThread: { name: "Child", backLabel: "Back to Parent" },
      onBackToThread,
    });
    expect(screen.queryByLabelText("Back to chats")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Back to Parent" }));
    expect(onBackToThread).toHaveBeenCalledTimes(1);
  });
});

describe("MobileChatTopBar workspace", () => {
  it("shows the workspace button only when there is a workspace", async () => {
    const onOpenWorkspace = vi.fn();
    const { unmount } = render(<MobileChatTopBar title="t" onOpenSheet={vi.fn()} />);
    expect(screen.queryByRole("button", { name: "Open workspace" })).not.toBeInTheDocument();
    unmount();
    renderBar({ onOpenWorkspace });
    await userEvent.click(screen.getByRole("button", { name: "Open workspace" }));
    expect(onOpenWorkspace).toHaveBeenCalledTimes(1);
  });
});
