import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>(
    "../../api/client",
  );
  return {
    ...actual,
    api: {
      ...actual.api,
      chatsV2: { ...actual.api.chatsV2, pause: vi.fn(), sendMessage: vi.fn() },
    },
  };
});

import { api } from "../../api/client";
import { useChatStore } from "../chatStore";
import { seedChatDetail } from "../../hooks/chat-queries";
import { clearAllMessagesCache } from "../../hooks/message-queries";

const pause = vi.mocked(api.chatsV2.pause);
const sendMessage = vi.mocked(api.chatsV2.sendMessage);

const CHAT = "c-pause-send";

// ESC fires pauseChat without awaiting it, so a message typed right after
// sends while the pause RPC is still in flight. Unordered, the send can reach
// the server first, be routed as a wake to a "running" run, and then the pause
// lands and strands the message (chat 264b5697). The client must not let a
// send overtake the pause the user issued before it.
describe("sendMessage after a pause", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    clearAllMessagesCache();
    seedChatDetail({ id: CHAT, projectId: "p" } as never);
    sendMessage.mockResolvedValue({ chatId: CHAT } as never);
  });

  it("does not send until the in-flight pause has resolved", async () => {
    let finishPause!: () => void;
    pause.mockReturnValue(new Promise<void>((resolve) => (finishPause = resolve)) as never);

    const paused = useChatStore.getState().pauseChat(CHAT);
    const sent = useChatStore.getState().sendMessage(CHAT, "after esc");

    await Promise.resolve();
    await Promise.resolve();
    expect(sendMessage).not.toHaveBeenCalled();

    finishPause();
    await paused;
    await sent;
    expect(sendMessage).toHaveBeenCalledTimes(1);
  });

  it("still sends when the pause fails", async () => {
    pause.mockRejectedValue(new Error("boom"));
    vi.spyOn(useChatStore.getState(), "refreshChat").mockResolvedValue(undefined as never);

    const paused = useChatStore.getState().pauseChat(CHAT);
    await useChatStore.getState().sendMessage(CHAT, "after esc");
    await paused;

    expect(sendMessage).toHaveBeenCalledTimes(1);
  });
});
