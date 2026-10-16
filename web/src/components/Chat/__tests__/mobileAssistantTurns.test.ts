import { describe, expect, it } from "vitest";
import { MessageRole } from "../../../gen/reliant/v1/chat_pb";
import type { Message } from "../../../api/client";
import { consolidateMobileAssistantTurns } from "../mobileAssistantTurns";

const row = (id: string, role = MessageRole.ASSISTANT, thread = "main") => ({
  type: "message", message: { id, role, thread, contentBlocks: [{ id: `b-${id}`, index: 0, content: id }], streamingState: 0 } as unknown as Message,
  mobileTurnMessages: undefined as Message[] | undefined,
});
const join = (a: ReturnType<typeof row>, b: ReturnType<typeof row>) => a.message.thread === b.message.thread;
describe("mobile assistant turns", () => {
  it("keeps a stable first identity, ordered blocks, and original action targets", () => {
    const first = row("first"), last = row("last");
    const [turn] = consolidateMobileAssistantTurns([first, last], join);
    expect(turn.message.id).toBe("first");
    expect(turn.mobileTurnMessages?.map(message => message.id)).toEqual(["first", "last"]);
    expect(turn.message.contentBlocks.map(block => [block.content, block.index])).toEqual([["first", 0], ["last", 1]]);
    expect(last.message.contentBlocks[0].index).toBe(0);
  });
  it("never joins across users, thread changes, or non-message rows", () => {
    const rows = [row("a"), row("user", MessageRole.USER), row("b"), row("c", MessageRole.ASSISTANT, "fork")];
    expect(consolidateMobileAssistantTurns(rows, join)).toHaveLength(4);
    const withBoundary = [row("a"), { type: "question" }, row("b")];
    expect(consolidateMobileAssistantTurns(withBoundary, () => true)).toHaveLength(3);
  });
});
