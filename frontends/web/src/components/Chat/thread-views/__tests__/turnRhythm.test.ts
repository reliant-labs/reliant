import { describe, expect, it } from "vitest";
import { ContentBlockType, DisplayStyle, MessageRole, StreamingState } from "../../../../types/chat";
import type { Message } from "../../../../types/chat";
import { failedSendId } from "../../../../lib/pendingSends";
import { latestTurnEnd, turnEnds, turnRhythm } from "../InterleavedTimeline";

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
describe("turnRhythm", () => {
  it("spaces turns apart and keeps a turn's rows together", () => {
    const items = [user("u1"), { type: "work" }, answer("a1"), user("u2"), answer("a2"), { type: "error" }];
    expect(turnRhythm(items)).toEqual(["", "pt-5", "pt-3", "pt-8", "pt-5", "pt-3"]);
  });

  it("treats another agent's mail as part of the turn, not a person's message", () => {
    const mail = {
      type: "message",
      message: message("m1", MessageRole.USER, ContentBlockType.TEXT, {
        sender: { threadId: "th-review" } as Message["sender"],
      }),
    };
    const items = [user("u1"), answer("a1"), mail];
    expect(turnRhythm(items)[2]).toBe("pt-3");
  });

  it("keeps a detailed run of tool-only messages tight", () => {
    const items = [user("u1"), tools("t1"), tools("t2"), answer("a1")];
    expect(turnRhythm(items)).toEqual(["", "pt-5", "pt-1", "pt-3"]);
  });

  it("keeps each thread's run of tool-only messages its own", () => {
    const review = { thread: "th-review" };
    const items = [user("u1"), tools("t1"), { ...tools("r1"), workflow: review }, tools("t2"), tools("t3")];
    expect(turnRhythm(items)).toEqual(["", "pt-5", "pt-3", "pt-3", "pt-1"]);
  });

  it("never runs a row with no thread into a run", () => {
    const stray = { type: "message", message: message("s1", MessageRole.ASSISTANT, ContentBlockType.TOOL_CALL, { thread: "" }) };
    const items = [user("u1"), tools("t1"), stray, tools("t2")];
    expect(turnRhythm(items)).toEqual(["", "pt-5", "pt-3", "pt-3"]);
  });
});

describe("turnEnds", () => {
  const main = { thread: "chat-1" };
  const review = { thread: "th-review" };
  const at = <T extends object>(item: T, workflow = main) => ({ ...item, workflow });

  it("marks only the last row of a turn's own thread", () => {
    const items = [
      at(user("u1")), at(answer("a1")), at({ type: "work" }), at(answer("a2")), at({ type: "error" }),
      at(answer("r1"), review), at(user("u2")), at(answer("a3")),
    ];
    expect(turnEnds(items)).toEqual([false, false, false, true, true, true, false, true]);
  });

  // A row with no workflow, or no thread at all, is neutral: it neither joins
  // a turn nor ends one, and it is its own end.
  describe("a row with no thread", () => {
    const strayWork = { type: "work" };
    const strayMessage = {
      type: "message",
      message: message("s1", MessageRole.ASSISTANT, ContentBlockType.TEXT, { thread: "" }),
    };

    it("does not end the answer it sits inside", () => {
      const items = [at(user("u1")), at(answer("a1")), strayWork, strayMessage, at(answer("a2"))];
      expect(turnEnds(items)).toEqual([false, false, true, true, true]);
    });

    it("does not continue the answer it follows", () => {
      const items = [at(user("u1")), at(answer("a1")), strayWork, strayMessage, at(user("u2"))];
      expect(turnEnds(items)).toEqual([false, true, true, true, true]);
    });

    it("does not answer a prompt", () => {
      const items = [at(user("u1")), strayWork, strayMessage];
      expect(turnEnds(items)).toEqual([true, true, true]);
    });
  });

  it("gives a main-thread answer its actions row through other threads' rows", () => {
    const items = [
      at(user("u1")),
      at({ type: "thread-start" }, review),
      at(answer("a1")),
      at(tools("r1"), review),
      at({ type: "work" }, review),
      at(tools("a2")),
      at(answer("r2"), review),
      { type: "error" },
      { type: "info" },
    ];
    expect(turnEnds(items)).toEqual([false, false, false, false, false, true, true, true, true]);
  });

  it("keeps a thread's answer to one prompt apart from its answer to the next", () => {
    const items = [at(user("u1")), at(answer("a1")), at(answer("r1"), review), at(user("u2")), at(answer("a2"))];
    expect(turnEnds(items)).toEqual([false, true, true, false, true]);
  });

  describe("a person's message", () => {
    it("leaves the actions to the answer that follows it", () => {
      expect(turnEnds([at(user("u1")), at(answer("a1"))])).toEqual([false, true]);
    });

    it("leaves them to another thread's answer too", () => {
      expect(turnEnds([at(user("u1")), at(answer("r1"), review)])).toEqual([false, true]);
    });

    it("keeps them while it is the latest prompt and nothing has answered it", () => {
      const items = [at(user("u1")), at(answer("a1")), at(user("u2")), at({ type: "thread-start" }, review), { type: "info" }];
      expect(turnEnds(items)).toEqual([false, true, true, true, true]);
    });

    it("keeps them when its send failed, even after a retry is answered", () => {
      const failed = { type: "message", message: message(failedSendId("c1"), MessageRole.USER, ContentBlockType.TEXT) };
      const items = [at(failed), { type: "error" }, at(user("u2")), at(answer("a2"))];
      expect(turnEnds(items)).toEqual([true, true, false, true]);
    });
  });

  // A row that draws no actions row cannot take them off the answer it follows.
  describe("a row after the answer that draws no actions row", () => {
    const notice = at({
      type: "message",
      message: message("n1", MessageRole.SYSTEM, ContentBlockType.TEXT, { displayStyle: DisplayStyle.INFO }),
    });
    const mail = at({
      type: "message",
      message: message("m1", MessageRole.USER, ContentBlockType.TEXT, {
        sender: { threadId: "th-review" } as Message["sender"],
      }),
    });
    const surfaced = at({ ...tools("a1"), slice: { role: "surfaced" as const } });
    const earlierProse = at({ ...answer("a1"), slice: { role: "prose" as const } });
    const lastProse = at({ ...answer("a1"), slice: { role: "prose" as const, chrome: true } });

    it.each([
      ["a fold of work", at({ type: "work" })],
      ["a notice", notice],
      ["another agent's mail", mail],
      ["a surfaced call", surfaced],
    ])("leaves them under the answer: %s", (_label, after) => {
      const items = [at(user("u1")), at(answer("a1")), after];
      expect(turnEnds(items).slice(0, 2)).toEqual([false, true]);
      expect(latestTurnEnd(items, turnEnds(items))).toBe(1);
    });

    it("gives them to the message's last prose slice, never an earlier one", () => {
      const items = [at(user("u1")), earlierProse, at({ type: "work" }), lastProse, at({ type: "work" })];
      expect(turnEnds(items)).toEqual([false, false, false, true, true]);
      expect(latestTurnEnd(items, turnEnds(items))).toBe(3);
    });
  });

  describe("while the run is live", () => {
    it("ends nothing in the part still being written", () => {
      const items = [at(user("u1")), at(answer("a1")), at({ type: "work" })];
      expect(turnEnds(items, { live: true })).toEqual([false, false, false]);
      expect(latestTurnEnd(items, turnEnds(items, { live: true }), { live: true })).toBe(-1);
    });

    it("still ends another thread's finished part, without holding it on screen", () => {
      const items = [at(user("u1")), at(answer("a1")), at(answer("r1"), review)];
      const ends = turnEnds(items, { live: true });
      expect(ends).toEqual([false, true, false]);
      expect(latestTurnEnd(items, ends, { live: true })).toBe(-1);
    });

    it("keeps a just-sent prompt's actions on screen", () => {
      const items = [at(user("u1")), at(answer("a1")), at(user("u2"))];
      const ends = turnEnds(items, { live: true });
      expect(ends).toEqual([false, true, true]);
      expect(latestTurnEnd(items, ends, { live: true })).toBe(2);
    });
  });

  it("holds no earlier turn's actions on screen when the latest has none", () => {
    const items = [at(user("u1")), at(answer("a1")), at(user("u2")), at({ type: "work" })];
    expect(latestTurnEnd(items, turnEnds(items))).toBe(-1);
  });
});
