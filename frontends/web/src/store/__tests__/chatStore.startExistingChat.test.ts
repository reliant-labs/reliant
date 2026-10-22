import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
} from "../../gen/reliant/v1/chat_pb";
import type { Message } from "../../types/chat";
import type { ChatUpdate } from "../../types/streaming";

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>(
    "../../api/client",
  );
  return {
    ...actual,
    api: {
      ...actual.api,
      chatsV2: { ...actual.api.chatsV2, start: vi.fn() },
    },
  };
});

import { api } from "../../api/client";
import { useChatStore } from "../chatStore";
import { useProjectStore } from "../projectStore";
import {
  clearAllMessagesCache,
  getMessagesFromCache,
  setMessagesInCache,
} from "../../hooks/message-queries";

const start = vi.mocked(api.chatsV2.start);

const inherited = {
  id: "inherited-1",
  chatId: "branch-1",
  role: MessageRole.ASSISTANT,
  contentBlocks: [{ id: "", index: 0, type: ContentBlockType.TEXT, content: "from the parent" }],
  createdAt: "2026-01-01T00:00:00.000Z",
  updatedAt: "2026-01-01T00:00:00.000Z",
  streamingState: StreamingState.COMPLETE,
  seq: 1n,
  thread: "",
  sequenceNumber: 0n,
  attachments: [],
} as unknown as Message;

function realUserUpdate(id: string, text: string): ChatUpdate {
  return {
    update_type: "message",
    message: {
      ...inherited,
      id,
      role: MessageRole.USER,
      contentBlocks: [{ id: `${id}-b0`, index: 0, type: ContentBlockType.TEXT, content: text }],
      seq: 2n,
    },
  } as unknown as ChatUpdate;
}

function branchChat(projectId: string) {
  return { id: "branch-1", projectId, title: "branch" } as never;
}

describe("startExistingChat", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    clearAllMessagesCache();
    // The selected project is deliberately NOT the branch's project.
    useProjectStore.setState({
      currentProject: { id: "selected-project", name: "p", path: "/p" },
    } as never);
  });

  it("keeps the branch's inherited history when it appends the first message", async () => {
    setMessagesInCache("branch-1", [inherited]);
    start.mockResolvedValue(branchChat("branch-project"));

    await useChatStore.getState().startExistingChat("branch-1", "continue here");

    const cached = getMessagesFromCache("branch-1");
    expect(cached.map((m) => m.id)).toContain("inherited-1");
    expect(cached).toHaveLength(2);
    expect(cached[1].contentBlocks[0].content).toBe("continue here");
  });

  // A branch is open and subscribed before its first send, so the server's
  // echo of the persisted message can land on the stream while StartChat is
  // still in flight. The placeholder must already be in the cache for that
  // echo to retire it; written after the response, it lands behind its own
  // echo and the message renders twice until the next snapshot.
  it("does not duplicate the message when its echo arrives before StartChat resolves", async () => {
    setMessagesInCache("branch-1", [inherited]);
    start.mockImplementation(async () => {
      useChatStore
        .getState()
        .processChatStreamUpdates("branch-1", [realUserUpdate("real-1", "continue here")]);
      return branchChat("branch-project");
    });

    await useChatStore.getState().startExistingChat("branch-1", "continue here");

    const cached = getMessagesFromCache("branch-1");
    expect(cached.map((m) => m.id)).toEqual(["inherited-1", "real-1"]);
  });

  it("does not send the selected project; the chat names its own", async () => {
    start.mockResolvedValue(branchChat("branch-project"));

    await useChatStore.getState().startExistingChat("branch-1", "hi");

    const request = start.mock.calls[0][0];
    expect(request.chat_id).toBe("branch-1");
    expect(request.project_id).toBeUndefined();
  });

  it("startChat still replaces the (empty) cache and sends the selected project", async () => {
    start.mockResolvedValue(branchChat("selected-project"));

    await useChatStore.getState().startChat(undefined, "brand new");

    expect(start.mock.calls[0][0].project_id).toBe("selected-project");
    expect(getMessagesFromCache("branch-1")).toHaveLength(1);
  });
});
