import { describe, expect, it } from "vitest";
import { ContentBlockType, MessageRole, StreamingState } from "../../../../types/chat";
import type { Message } from "../../../../types/chat";
import { getToolRowSpacing, isToolOnlyAssistantMessage } from "../InterleavedTimeline";

function message(
  id: string,
  role: MessageRole,
  blocks: Message["contentBlocks"],
): Message {
  return {
    id,
    chatId: "chat-1",
    seq: BigInt(id.replace(/\D/g, "") || "1"),
    thread: "chat-1",
    role,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: blocks,
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    sequenceNumber: BigInt(id.replace(/\D/g, "") || "1"),
  } as Message;
}

function toolMessage(id: string): Message {
  return message(id, MessageRole.ASSISTANT, [
    {
      id: `${id}-block`,
      index: 0,
      type: ContentBlockType.TOOL_CALL,
      toolName: "bash",
      toolCallId: `${id}-call`,
      input: "{}",
    },
  ]);
}

function textMessage(id: string): Message {
  return message(id, MessageRole.ASSISTANT, [
    {
      id: `${id}-text`,
      index: 0,
      type: ContentBlockType.TEXT,
      content: "I will do that.",
    },
  ]);
}

describe("InterleavedTimeline tool row spacing", () => {
  it("classifies assistant messages that contain only tool calls", () => {
    expect(isToolOnlyAssistantMessage(toolMessage("m1"))).toBe(true);
    expect(isToolOnlyAssistantMessage(textMessage("m2"))).toBe(false);
    expect(
      isToolOnlyAssistantMessage(
        message("m3", MessageRole.ASSISTANT, [
          { id: "m3-text", index: 0, type: ContentBlockType.TEXT, content: "Plan" },
          {
            id: "m3-tool",
            index: 1,
            type: ContentBlockType.TOOL_CALL,
            toolName: "bash",
            toolCallId: "m3-call",
            input: "{}",
          },
        ]),
      ),
    ).toBe(false);
  });

  it("compacts spacing between adjacent split-turn tool-only messages", () => {
    const items = [
      { type: "message", message: toolMessage("m1") },
      { type: "message", message: toolMessage("m2") },
      { type: "message", message: toolMessage("m3") },
    ];

    expect(getToolRowSpacing(items, 0)).toEqual({ compact: true, compactBefore: false, compactAfter: true });
    expect(getToolRowSpacing(items, 1)).toEqual({ compact: true, compactBefore: true, compactAfter: true });
    expect(getToolRowSpacing(items, 2)).toEqual({ compact: true, compactBefore: true, compactAfter: false });
  });

  it("uses a visible spacer for split-turn rows instead of the zero gap used inside one turn", () => {
    const splitTurnSpacingClass = (compact: boolean) => compact ? "py-0.5" : "py-1";

    expect(splitTurnSpacingClass(getToolRowSpacing([
      { type: "message", message: toolMessage("m1") },
      { type: "message", message: toolMessage("m2") },
    ], 0).compact)).toBe("py-0.5");
  });

  it("keeps normal spacing around text-bearing assistant messages", () => {
    const items = [
      { type: "message", message: toolMessage("m1") },
      { type: "message", message: textMessage("m2") },
      { type: "message", message: toolMessage("m3") },
    ];

    expect(getToolRowSpacing(items, 0)).toEqual({ compact: false, compactBefore: false, compactAfter: false });
    expect(getToolRowSpacing(items, 1)).toEqual({ compact: false, compactBefore: false, compactAfter: false });
    expect(getToolRowSpacing(items, 2)).toEqual({ compact: false, compactBefore: false, compactAfter: false });
  });
});
