/**
 * The file tree and file content stay off the query cache (api/fileSystem.ts):
 * their only sharing is in flight. Two identical reads at once make one
 * request, and the chat is part of "identical", because the server reads the
 * chat's machine: one path under two chats can be two disks.
 *
 * And ReplaceInFiles, which rewrites files under any open editor, says which
 * ones (lib/fileRewrites) and re-runs the mounted content searches.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryObserver } from "@tanstack/react-query";

import { setTransport } from "@/lib/connect";
import { subscribeFileRewrites, type FileRewrite } from "@/lib/fileRewrites";
import { mockTransport } from "@/lib/test-utils";
import { serviceHandlers } from "@/test/serviceTransport";
import { fileSystemServiceQueries } from "@/hooks/file-system-service-hooks_gen";

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

import { getFileContent, getFileTree, replaceInFiles } from "../fileSystem";

/** A promise the test resolves, so two calls can overlap. */
function gate<T>() {
  let open!: (value: T) => void;
  const promise = new Promise<T>((resolve) => {
    open = resolve;
  });
  return { promise, open };
}

const server = {
  getFileTree: vi.fn(),
  getFileContent: vi.fn(),
  replaceInFiles: vi.fn(),
  searchFiles: vi.fn(),
};

beforeEach(() => {
  vi.clearAllMocks();
  mocks.useProjectStore.getState.mockReturnValue({ currentProject: { id: "project-1" } });
  mocks.resolveWorktree.mockResolvedValue(null);
  mocks.useChatStore.getState.mockReturnValue({ activeChatId: "chat-a" });
  setTransport(mockTransport(serviceHandlers("FileSystemService", server)));
});

afterEach(() => {
  setTransport(null);
});

describe("in-flight sharing", () => {
  it("two concurrent getFileTree for one (project, worktree, chat, path, depth) make one call", async () => {
    const reply = gate<{ files: unknown[] }>();
    server.getFileTree.mockReturnValue(reply.promise);

    const first = getFileTree("/src", false, "wt-1", 1);
    const second = getFileTree("/src", true, "wt-1", 1);
    reply.open({ files: [] });
    await Promise.all([first, second]);

    expect(server.getFileTree).toHaveBeenCalledTimes(1);
  });

  it("two chats make two calls", async () => {
    const reply = gate<{ files: unknown[] }>();
    server.getFileTree.mockReturnValue(reply.promise);

    const first = getFileTree("/src", false, "wt-1", 1);
    // Let the first call read its chat before the chat changes.
    await vi.waitFor(() => expect(server.getFileTree).toHaveBeenCalledTimes(1));
    mocks.useChatStore.getState.mockReturnValue({ activeChatId: "chat-b" });
    const second = getFileTree("/src", false, "wt-1", 1);
    reply.open({ files: [] });
    await Promise.all([first, second]);

    expect(server.getFileTree).toHaveBeenCalledTimes(2);
    expect(server.getFileTree.mock.calls.map((c) => c[0].chatId)).toEqual(["chat-a", "chat-b"]);
  });

  it("a different depth is a different request", async () => {
    server.getFileTree.mockResolvedValue({ files: [] });
    await Promise.all([getFileTree("/src", false, "wt-1", 1), getFileTree("/src", false, "wt-1", 2)]);
    expect(server.getFileTree).toHaveBeenCalledTimes(2);
  });

  it("nothing is cached once a read settles", async () => {
    server.getFileTree.mockResolvedValue({ files: [] });
    await getFileTree("/src", false, "wt-1", 1);
    await getFileTree("/src", false, "wt-1", 1);
    expect(server.getFileTree).toHaveBeenCalledTimes(2);
  });

  it("two concurrent reads of one file make one call", async () => {
    const reply = gate<{ content: string }>();
    server.getFileContent.mockReturnValue(reply.promise);

    const first = getFileContent("src/a.ts", "wt-1");
    const second = getFileContent("src/a.ts", "wt-1");
    reply.open({ content: "hello" });

    await expect(Promise.all([first, second])).resolves.toEqual(["hello", "hello"]);
    expect(server.getFileContent).toHaveBeenCalledTimes(1);
  });
});

describe("replaceInFiles", () => {
  it("names the rewritten files, re-runs mounted searches, and refreshes git status", async () => {
    server.replaceInFiles.mockResolvedValue({
      results: [
        { path: "/ws/src/a.ts", replacements: 2, success: true },
        { path: "/ws/src/b.ts", replacements: 0, success: false, error: "denied" },
      ],
      totalReplacements: 2,
      filesModified: 1,
    });
    server.searchFiles.mockResolvedValue({ results: [], totalMatches: 0 });

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const observer = new QueryObserver(
      qc,
      fileSystemServiceQueries.searchFiles({ projectId: "project-1", query: "needle" }),
    );
    const unsubscribeSearch = observer.subscribe(() => {});
    await vi.waitFor(() => expect(server.searchFiles).toHaveBeenCalledTimes(1));

    const rewrites: FileRewrite[] = [];
    const unsubscribe = subscribeFileRewrites((r) => rewrites.push(r));
    const result = await replaceInFiles("needle", "pin", { worktreeId: "wt-1" }, qc);
    unsubscribe();

    expect(result.filesModified).toBe(1);
    expect(server.replaceInFiles.mock.calls[0][0]).toMatchObject({
      projectId: "project-1", searchText: "needle", replaceText: "pin", worktreeId: "wt-1", chatId: "chat-a",
    });
    // Only the files actually rewritten.
    expect(rewrites).toEqual([{ worktreeId: "wt-1", paths: ["/ws/src/a.ts"] }]);
    await vi.waitFor(() => expect(server.searchFiles).toHaveBeenCalledTimes(2));
    expect(mocks.triggerGitStatusRefresh).toHaveBeenCalledWith("wt-1", "project-1");

    unsubscribeSearch();
    qc.clear();
  });
});
