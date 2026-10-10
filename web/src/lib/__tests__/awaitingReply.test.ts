import { describe, expect, it } from "vitest";

import { MessageRole } from "@/gen/reliant/v1/chat_pb";
import { latestTurnIsUsers } from "../awaitingReply";

const MAIN = "chat-1";

describe("latestTurnIsUsers", () => {
  it("is true for a user message nothing has answered", () => {
    expect(
      latestTurnIsUsers(
        [
          { role: MessageRole.USER, thread: MAIN },
          { role: MessageRole.ASSISTANT, thread: MAIN },
          { role: MessageRole.USER, thread: MAIN },
        ],
        MAIN,
      ),
    ).toBe(true);
  });

  it("is false once the assistant has replied", () => {
    expect(
      latestTurnIsUsers(
        [
          { role: MessageRole.USER, thread: MAIN },
          { role: MessageRole.ASSISTANT, thread: MAIN },
        ],
        MAIN,
      ),
    ).toBe(false);
  });

  it("skips the system note a send writes after the user's message (chat 97654413)", () => {
    // "continue", then the hidden "Some of your params have changed…" note.
    expect(
      latestTurnIsUsers(
        [
          { role: MessageRole.ASSISTANT, thread: MAIN },
          { role: MessageRole.USER, thread: MAIN },
          { role: MessageRole.SYSTEM, thread: MAIN },
        ],
        MAIN,
      ),
    ).toBe(true);
  });

  it("ignores a sub-agent's messages", () => {
    expect(
      latestTurnIsUsers(
        [
          { role: MessageRole.USER, thread: MAIN },
          { role: MessageRole.ASSISTANT, thread: "spawn-1" },
        ],
        MAIN,
      ),
    ).toBe(true);
  });

  it("is false for an empty conversation", () => {
    expect(latestTurnIsUsers([], MAIN)).toBe(false);
  });
});
