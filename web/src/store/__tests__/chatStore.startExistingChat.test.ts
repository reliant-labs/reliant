import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
} from "../../gen/reliant/v1/chat_pb";
import type { Message } from "../../types/chat";

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
