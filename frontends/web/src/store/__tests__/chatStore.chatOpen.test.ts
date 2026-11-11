import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { useChatStore } from "../chatStore";
import { clearAllMessagesCache, setMessagesInCache } from "../../hooks/message-queries";
import { resetChatOpen } from "../../lib/chatOpenInteraction";
import { installRecordingTracer } from "../../test/recordingTracer";
import type { Chat, Message } from "../../api/client";

// selectChat is where a chat open starts: the click. A real switch starts a
// chat.open interaction; re-selecting the open chat does not; the next
// switch supersedes an open still in flight.

let tracer: ReturnType<typeof installRecordingTracer>;
const opens = () => tracer.spans.filter((s) => s.name === "chat.open");

beforeAll(() => {
  tracer = installRecordingTracer();
});
afterAll(() => tracer.uninstall());

beforeEach(() => {
  clearAllMessagesCache();
  useChatStore.setState({ activeChatId: null } as never);
});
afterEach(() => {
  resetChatOpen();
  tracer.spans.length = 0;
});

describe("selectChat starts the chat.open interaction", () => {
  it("starts one for a switch, saying whether the chat was cached", () => {
    setMessagesInCache("c-cached", [{ id: "m-1" }, { id: "m-2" }] as Message[]);

    useChatStore.getState().selectChat({ id: "c-new" } as Chat);
    useChatStore.getState().selectChat({ id: "c-cached" } as Chat);

    const [first, second] = opens();
    expect(first!.attributes).toMatchObject({
      "chat.id": "c-new",
      "chat.cached": false,
      "chat.cached_messages": 0,
    });
    // The second click left the first open before it was usable.
    expect(first!.attributes["interaction.outcome"]).toBe("superseded");
    expect(second!.attributes).toMatchObject({
      "chat.id": "c-cached",
      "chat.cached": true,
      "chat.cached_messages": 2,
    });
    expect(second!.ended).toBe(false);
  });

  it("ends an open in flight as superseded when the chat is left for no other", () => {
    useChatStore.getState().selectChat({ id: "c1" } as Chat);

    useChatStore.getState().clearCurrentChat();

    expect(opens()[0]!.ended).toBe(true);
    expect(opens()[0]!.attributes["interaction.outcome"]).toBe("superseded");
  });

  it("says where the open came from: a switch, or the workspace restoring its chat", () => {
    useChatStore.getState().selectChat({ id: "c1" } as Chat);
    useChatStore.getState().selectChat({ id: "c2" } as Chat, { openEntry: "restore" });

    expect(opens().map((o) => o.attributes["chat.open.entry"])).toEqual(["switch", "restore"]);
  });

  it("does not start one when the chat is already open", () => {
    useChatStore.getState().selectChat({ id: "c1" } as Chat);
    useChatStore.getState().selectChat({ id: "c1" } as Chat);

    expect(opens()).toHaveLength(1);
    expect(opens()[0]!.ended).toBe(false);
  });
});
