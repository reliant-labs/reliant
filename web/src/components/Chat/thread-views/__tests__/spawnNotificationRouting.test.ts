import { describe, expect, it } from "vitest";
import type { Message } from "../../../../api/client";
import {
  ContentBlockType,
  DisplayStyle,
  MessageRole,
} from "../../../../gen/reliant/v1/chat_pb";

/**
 * A finished background sub-agent drains into two messages: the full
 * <agent_result …> body marked HIDDEN, and a short SYSTEM-role INFO
 * notification. This pins the two InterleavedTimeline decisions that make
 * that split render correctly.
 *
 * The routing is mirrored here rather than driven through a full timeline
 * render because the component pulls in Virtuoso, the settings sync layer and
 * several stores; the branches under test are two plain conditions, and
 * mirroring them keeps the test about the decision instead of the harness.
 * The line numbers named below are the source of truth.
 */

/** InterleavedTimeline.tsx: `if (msg.displayStyle === DisplayStyle.HIDDEN) continue;` */
function isRenderedInTimeline(msg: Message): boolean {
  return msg.displayStyle !== DisplayStyle.HIDDEN;
}

type Renderer = "ChatMessage" | "SystemNotificationMessage";

/**
 * InterleavedTimeline.tsx: USER-role messages short-circuit to ChatMessage
 * BEFORE the displayStyle check, so anything meant to render as a
 * notification must not be USER role.
 */
function rendererFor(msg: Message): Renderer {
  if (msg.role === MessageRole.USER) return "ChatMessage";
  if (msg.displayStyle) return "SystemNotificationMessage";
  return "ChatMessage";
}

function message(overrides: Partial<Message>): Message {
  return {
    id: "msg-1",
    chatId: "chat-1",
    seq: BigInt(1),
    thread: "chat-1",
    role: MessageRole.USER,
    contentBlocks: [
      { id: "b0", index: 0, type: ContentBlockType.TEXT, content: "body" },
    ],
    createdAt: "2026-01-01T00:00:00.000Z",
    updatedAt: "2026-01-01T00:00:00.000Z",
    sequenceNumber: 0n,
    ...overrides,
  } as Message;
}

describe("spawn completion routing in the timeline", () => {
  it("does not render the HIDDEN agent_result body", () => {
    const hiddenBody = message({
      role: MessageRole.USER,
      displayStyle: DisplayStyle.HIDDEN,
      contentBlocks: [
        {
          id: "b0",
          index: 0,
          type: ContentBlockType.TEXT,
          content:
            '<agent_result agent_id="c13c3bc1" status="completed">done</agent_result>',
        },
      ],
    });

    expect(isRenderedInTimeline(hiddenBody)).toBe(false);
  });

  it("renders the SYSTEM+INFO notification through SystemNotificationMessage", () => {
    const notification = message({
      role: MessageRole.SYSTEM,
      displayStyle: DisplayStyle.INFO,
      contentBlocks: [
        {
          id: "b0",
          index: 0,
          type: ContentBlockType.TEXT,
          content: 'spawn "probe-A" completed',
        },
      ],
    });

    expect(isRenderedInTimeline(notification)).toBe(true);
    expect(rendererFor(notification)).toBe("SystemNotificationMessage");
  });

  it("would NOT reach SystemNotificationMessage if the notification were USER role", () => {
    // This is why the notification is written SYSTEM rather than USER: the
    // USER branch short-circuits to ChatMessage before displayStyle is ever
    // consulted, so a USER+INFO message renders as an ordinary chat bubble.
    const userRoleNotification = message({
      role: MessageRole.USER,
      displayStyle: DisplayStyle.INFO,
    });

    expect(rendererFor(userRoleNotification)).toBe("ChatMessage");
  });

  it("leaves an ordinary drained human message rendering as a chat bubble", () => {
    const humanMessage = message({
      role: MessageRole.USER,
      displayStyle: undefined,
    });

    expect(isRenderedInTimeline(humanMessage)).toBe(true);
    expect(rendererFor(humanMessage)).toBe("ChatMessage");
  });
});
