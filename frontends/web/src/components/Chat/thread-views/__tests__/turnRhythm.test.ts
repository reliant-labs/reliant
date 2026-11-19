import { describe, expect, it } from "vitest";
import { ContentBlockType, MessageRole, StreamingState } from "../../../../types/chat";
import type { Message } from "../../../../types/chat";
import { isTurnEnd, turnRhythmClass } from "../InterleavedTimeline";

function message(id: string, role: MessageRole, type: ContentBlockType, extra: Partial<Message> = {}): Message {
  return {
    id,
    chatId: "chat-1",
    seq: BigInt(1),
    thread: "chat-1",
    role,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: [
      type === ContentBlockType.TOOL_CALL
        ? { id: `${id}-b`, index: 0, type, toolName: "shell", toolCallId: `${id}-call`, input: "{}" }
        : { id: `${id}-b`, index: 0, type, content: "Hello." },
    ],
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    sequenceNumber: BigInt(1),
    ...extra,
  } as Message;
}

const user = (id: string) => ({ type: "message", message: message(id, MessageRole.USER, ContentBlockType.TEXT) });
const answer = (id: string) => ({ type: "message", message: message(id, MessageRole.ASSISTANT, ContentBlockType.TEXT) });
const tools = (id: string) => ({ type: "message", message: message(id, MessageRole.ASSISTANT, ContentBlockType.TOOL_CALL) });

// §2.2 of the chat design: 30px before a person's turn, 18.75px between it
// and the answer, 11.25px between the rows of one answer.
describe("turnRhythmClass", () => {
  it("spaces turns apart and keeps a turn's rows together", () => {
    const items = [user("u1"), { type: "work" }, answer("a1"), user("u2"), answer("a2"), { type: "error" }];
    expect(items.map((_, i) => turnRhythmClass(items, i))).toEqual(["", "pt-5", "pt-3", "pt-8", "pt-5", "pt-3"]);
  });

  it("treats another agent's mail as part of the turn, not a person's message", () => {
    const mail = {
      type: "message",
      message: message("m1", MessageRole.USER, ContentBlockType.TEXT, {
        sender: { threadId: "th-review" } as Message["sender"],
      }),
    };
    const items = [user("u1"), answer("a1"), mail];
    expect(turnRhythmClass(items, 2)).toBe("pt-3");
  });

  it("keeps a detailed run of tool-only messages tight", () => {
    const items = [user("u1"), tools("t1"), tools("t2"), answer("a1")];
    expect(items.map((_, i) => turnRhythmClass(items, i))).toEqual(["", "pt-5", "pt-1", "pt-3"]);
  });
});

describe("isTurnEnd", () => {
  const main = { thread: "chat-1" };
  const review = { thread: "th-review" };
  const at = <T extends object>(item: T, workflow = main) => ({ ...item, workflow });

  it("marks only the last row of a turn's own thread", () => {
    const items = [
      at(user("u1")), at(answer("a1")), at({ type: "work" }), at(answer("a2")), at({ type: "error" }),
      at(answer("r1"), review), at(user("u2")), at(answer("a3")),
    ];
    expect(items.map((_, i) => isTurnEnd(items, i))).toEqual([false, false, false, true, true, true, false, true]);
  });
});
