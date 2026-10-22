import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ThreadTabs } from "../ThreadTabs";
import type { ThreadInfo } from "../useThreads";

const mainThread: ThreadInfo = {
  id: "chat-1",
  name: "Main",
  messageCount: 10,
  isMain: true,
  isActive: false,
  isSpawn: false,
  color: "hsl(var(--primary))",
};

function childThread(overrides: Partial<ThreadInfo>): ThreadInfo {
  return {
    id: "thread-1",
    name: "Thread",
    messageCount: 3,
    isMain: false,
    isActive: false,
    isSpawn: false,
    color: "hsl(120, 65%, 55%)",
    ...overrides,
  };
}

describe("ThreadTabs", () => {
  it("filters spawn-origin threads even when a node thread makes tabs visible", () => {
    render(
      <ThreadTabs
        threads={[
          mainThread,
          childThread({ id: "thread-node", name: "Review Step", messageCount: 4 }),
          childThread({ id: "thread-spawn", name: "Researcher", messageCount: 6, isSpawn: true }),
        ]}
        selectedThreadId={null}
        onSelectThread={vi.fn()}
        chatId="chat-1"
      />,
    );

    expect(screen.getByText("All")).toBeInTheDocument();
    expect(screen.getByText("Main")).toBeInTheDocument();
    expect(screen.getByText("Review Step")).toBeInTheDocument();
    expect(screen.queryByText("Researcher")).not.toBeInTheDocument();
    expect(screen.getByText("14")).toBeInTheDocument();
  });

  it("does not show tabs for only main plus spawn threads", () => {
    const { container } = render(
      <ThreadTabs
        threads={[
          mainThread,
          childThread({ id: "thread-spawn", name: "Researcher", messageCount: 6, isSpawn: true }),
        ]}
        selectedThreadId={null}
        onSelectThread={vi.fn()}
        chatId="chat-1"
      />,
    );

    expect(container).toBeEmptyDOMElement();
  });

  it("selects node threads after spawn threads have been filtered", () => {
    const onSelectThread = vi.fn();
    render(
      <ThreadTabs
        threads={[
          mainThread,
          childThread({ id: "thread-node", name: "Review Step", messageCount: 4 }),
          childThread({ id: "thread-spawn", name: "Researcher", messageCount: 6, isSpawn: true }),
        ]}
        selectedThreadId={null}
        onSelectThread={onSelectThread}
        chatId="chat-1"
      />,
    );

    fireEvent.click(screen.getByText("Review Step"));

    expect(onSelectThread).toHaveBeenCalledWith("thread-node");
  });
});
