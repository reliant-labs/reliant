import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { SystemNotificationMessage } from "../SystemNotificationMessage";
import type { Message } from "../../../api/client";
import {
  ContentBlockType,
  DisplayStyle,
  MessageRole,
  StreamingState,
} from "../../../gen/reliant/v1/chat_pb";

/**
 * When a background sub-agent finishes, the drain writes TWO messages: the
 * full <agent_result …> body marked HIDDEN (the LLM needs it, the human does
 * not), and a short SYSTEM-role, INFO-styled notification standing in for it
 * in the transcript. These tests pin the human-visible half of that split.
 */
function notificationMessage(
  text: string,
  displayStyle: DisplayStyle = DisplayStyle.INFO,
): Message {
  return {
    id: "msg-notify",
    chatId: "chat-1",
    seq: BigInt(1),
    thread: "chat-1",
    role: MessageRole.SYSTEM,
    displayStyle,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: [
      { id: "b0", index: 0, type: ContentBlockType.TEXT, content: text },
    ],
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    sequenceNumber: BigInt(1),
  } as Message;
}

describe("SystemNotificationMessage — spawn completion notice", () => {
  it("renders the spawn notification text for a completed sub-agent", () => {
    render(
      <SystemNotificationMessage
        message={notificationMessage('spawn "probe-A" completed')}
      />,
    );

    expect(screen.getByText('spawn "probe-A" completed')).toBeInTheDocument();
  });

  it("renders cancelled and failed notices through the same INFO surface", () => {
    const { rerender } = render(
      <SystemNotificationMessage
        message={notificationMessage('spawn "probe-B" cancelled')}
      />,
    );
    expect(screen.getByText('spawn "probe-B" cancelled')).toBeInTheDocument();

    rerender(
      <SystemNotificationMessage
        message={notificationMessage('spawn "probe-B" failed')}
      />,
    );
    expect(screen.getByText('spawn "probe-B" failed')).toBeInTheDocument();
  });

  it("uses the neutral INFO styling rather than the alarming WARNING styling", () => {
    const { container } = render(
      <SystemNotificationMessage
        message={notificationMessage('spawn "probe-A" completed')}
      />,
    );

    // INFO is a neutral primary-tinted surface; a completed spawn is routine
    // and must not read as a problem.
    const surface = container.querySelector(".border-primary\\/30");
    expect(surface).not.toBeNull();
    expect(container.innerHTML).not.toContain("--warning");
  });

  it("does NOT render the raw agent_result envelope text", () => {
    // The regression this guards: the sub-agent's machine-readable body used
    // to land in the transcript verbatim as an ordinary user message.
    render(
      <SystemNotificationMessage
        message={notificationMessage('spawn "probe-A" completed')}
      />,
    );

    expect(screen.queryByText(/<agent_result/)).not.toBeInTheDocument();
    expect(screen.queryByText(/agent_id:/)).not.toBeInTheDocument();
    expect(screen.queryByText(/<system>/)).not.toBeInTheDocument();
  });
});
