/**
 * The Files tab and the file preview must read from the open chat's machine.
 *
 * The server can only route a request to the chat's machine (its pinned
 * machine, else its workspace's owner) if the request names the chat. These
 * calls never did, so every one of them went to the user's default machine —
 * the wrong disk for any chat on another machine, main checkout included.
 *
 * Answered at the transport, so what is asserted is the request on the wire.
 * Content search names the chat too: hooks/__tests__/file-system-keys.test.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { FileViewerKind } from "@/gen/reliant/v1/filesystem_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { serviceHandlers } from "@/test/serviceTransport";

const mocks = vi.hoisted(() => ({
  useProjectStore: { getState: vi.fn() },
  resolveWorktree: vi.fn(),
  useChatStore: { getState: vi.fn() },
  triggerGitStatusRefresh: vi.fn(),
}));

vi.mock("../../store/projectStore", () => ({ useProjectStore: mocks.useProjectStore }));
vi.mock("../../hooks/worktree-queries", () => ({ resolveWorktree: mocks.resolveWorktree }));
vi.mock("../../store/chatStore", () => ({ useChatStore: mocks.useChatStore }));
vi.mock("../../store/gitStatusStore", () => ({
  triggerGitStatusRefresh: mocks.triggerGitStatusRefresh,
}));

import { getFileContent, getFilePreviewInfo, getFileTree, saveFileContent } from "../fileSystem";

const server = {
  getFileTree: vi.fn(),
  getFileContent: vi.fn(),
  getFilePreviewInfo: vi.fn(),
  saveFileContent: vi.fn(),
};

describe("file APIs name the open chat so the server reads its machine", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.useProjectStore.getState.mockReturnValue({ currentProject: { id: "project-1" } });
    mocks.resolveWorktree.mockResolvedValue(null);
    mocks.useChatStore.getState.mockReturnValue({ activeChatId: "chat-on-b" });
    server.getFileTree.mockResolvedValue({ files: [] });
    server.getFileContent.mockResolvedValue({ content: "hello" });
    server.getFilePreviewInfo.mockResolvedValue({ info: { viewerKind: FileViewerKind.TEXT } });
    server.saveFileContent.mockResolvedValue({});
    setTransport(mockTransport(serviceHandlers("FileSystemService", server)));
  });

  afterEach(() => {
    setTransport(null);
  });

  it("the file tree", async () => {
    await getFileTree("/", false, "wt-main", 2);
    expect(server.getFileTree).toHaveBeenCalledTimes(1);
    expect(server.getFileTree.mock.calls[0][0]).toMatchObject({
      projectId: "project-1", path: "/", worktreeId: "wt-main", chatId: "chat-on-b", depth: 2,
    });
  });

  it("the preview and the file it shows", async () => {
    await getFilePreviewInfo("src/a.ts", "wt-main");
    await getFileContent("src/a.ts", "wt-main");
    expect(server.getFilePreviewInfo.mock.calls[0][0]).toMatchObject({
      projectId: "project-1", path: "src/a.ts", worktreeId: "wt-main", chatId: "chat-on-b",
    });
    expect(server.getFileContent.mock.calls[0][0]).toMatchObject({
      projectId: "project-1", path: "src/a.ts", worktreeId: "wt-main", chatId: "chat-on-b",
    });
  });

  it("a save, which must land on the disk the file was read from", async () => {
    await saveFileContent("src/a.ts", "x", "wt-main");
    expect(server.saveFileContent.mock.calls[0][0]).toMatchObject({
      projectId: "project-1", path: "src/a.ts", content: "x", worktreeId: "wt-main", chatId: "chat-on-b",
    });
  });

  it("names no chat when none is open", async () => {
    mocks.useChatStore.getState.mockReturnValue({ activeChatId: null });
    await getFileTree("/", false, "wt-main", 2);
    expect(server.getFileTree.mock.calls[0][0].chatId).toBeUndefined();
  });
});
