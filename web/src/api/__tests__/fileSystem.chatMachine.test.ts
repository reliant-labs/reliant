/**
 * The Files tab and the file preview must read from the open chat's machine.
 *
 * The server can only route a request to the chat's machine (its pinned
 * machine, else its workspace's owner) if the request names the chat. These
 * calls never did, so every one of them went to the user's default machine —
 * the wrong disk for any chat on another machine, main checkout included.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  filesystemGrpc: {
    getFileTree: vi.fn(),
    getFileContent: vi.fn(),
    getFilePreviewInfo: vi.fn(),
    saveFileContent: vi.fn(),
    searchFiles: vi.fn(),
  },
  useProjectStore: { getState: vi.fn() },
  useWorktreeStore: { getState: vi.fn() },
  useChatStore: { getState: vi.fn() },
  triggerGitStatusRefresh: vi.fn(),
}));

vi.mock("../filesystem-grpc", () => ({ filesystemGrpc: mocks.filesystemGrpc }));
vi.mock("../../store/projectStore", () => ({ useProjectStore: mocks.useProjectStore }));
vi.mock("../../store/worktreeStore", () => ({ useWorktreeStore: mocks.useWorktreeStore }));
vi.mock("../../store/chatStore", () => ({ useChatStore: mocks.useChatStore }));
vi.mock("../../store/gitStatusStore", () => ({
  triggerGitStatusRefresh: mocks.triggerGitStatusRefresh,
}));

import { getFileContent, getFilePreviewInfo, getFileTree, saveFileContent, searchFiles } from "../fileSystem";

describe("file APIs name the open chat so the server reads its machine", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.useProjectStore.getState.mockReturnValue({ currentProject: { id: "project-1" } });
    mocks.useWorktreeStore.getState.mockReturnValue({ worktrees: [] });
    mocks.useChatStore.getState.mockReturnValue({ activeChatId: "chat-on-b" });
    mocks.filesystemGrpc.getFileTree.mockResolvedValue([]);
    mocks.filesystemGrpc.getFileContent.mockResolvedValue("hello");
    mocks.filesystemGrpc.getFilePreviewInfo.mockResolvedValue({ viewerKind: "text" });
    mocks.filesystemGrpc.saveFileContent.mockResolvedValue(undefined);
    mocks.filesystemGrpc.searchFiles.mockResolvedValue({ results: [], totalMatches: 0, truncated: false });
  });

  it("the file tree", async () => {
    await getFileTree("/", false, "wt-main", 2);
    expect(mocks.filesystemGrpc.getFileTree).toHaveBeenCalledWith(
      "project-1", "/", false, "wt-main", "chat-on-b", 2,
    );
  });

  it("the preview and the file it shows", async () => {
    await getFilePreviewInfo("src/a.ts", "wt-main");
    await getFileContent("src/a.ts", "wt-main");
    expect(mocks.filesystemGrpc.getFilePreviewInfo).toHaveBeenCalledWith(
      "project-1", "src/a.ts", "wt-main", "chat-on-b",
    );
    expect(mocks.filesystemGrpc.getFileContent).toHaveBeenCalledWith(
      "project-1", "src/a.ts", "wt-main", "chat-on-b",
    );
  });

  it("a save, which must land on the disk the file was read from", async () => {
    await saveFileContent("src/a.ts", "x", "wt-main");
    expect(mocks.filesystemGrpc.saveFileContent).toHaveBeenCalledWith(
      "project-1", "src/a.ts", "x", "wt-main", "chat-on-b",
    );
  });

  it("a search", async () => {
    await searchFiles("needle", { worktreeId: "wt-main" });
    expect(mocks.filesystemGrpc.searchFiles).toHaveBeenCalledWith(
      "project-1", "needle", { worktreeId: "wt-main", chatId: "chat-on-b" },
    );
  });

  it("names no chat when none is open", async () => {
    mocks.useChatStore.getState.mockReturnValue({ activeChatId: null });
    await getFileTree("/", false, "wt-main", 2);
    expect(mocks.filesystemGrpc.getFileTree).toHaveBeenCalledWith(
      "project-1", "/", false, "wt-main", undefined, 2,
    );
  });
});
