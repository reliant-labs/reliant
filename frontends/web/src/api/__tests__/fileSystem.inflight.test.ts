// @vitest-environment node
/**
 * The file tree and file content use generated definitions in an unobserved
 * transient query cache (api/fileSystem.ts): sharing lasts only in flight. Two identical reads at once make one
 * request, and the chat is part of "identical", because the server reads the
 * chat's machine: one path under two chats can be two disks.
 *
 * And ReplaceInFiles, which rewrites files under any open editor, says which
 * ones (lib/fileRewrites) and re-runs the mounted content searches.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryObserver, onlineManager } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { FileNodeType, FileViewerKind } from "@/gen/reliant/v1/filesystem_pb";

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

import { getFileContent, getFileTree, getFilePreviewInfo, getFilePreviewBlob, replaceInFiles } from "../fileSystem";

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
  getFilePreviewInfo: vi.fn(),
  getFilePreview: vi.fn(),
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
  onlineManager.setOnline(true);
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

describe("generated transient read contracts", () => {
  it("does not conflate delimiter-containing paths with a different chat", async () => {
    // Former key: project:wt:chat:part:/src:1 for both valid requests.
    const reply = gate<{ files: unknown[] }>();
    server.getFileTree.mockReturnValue(reply.promise);
    mocks.useChatStore.getState.mockReturnValue({ activeChatId: "chat" });
    const first = getFileTree("part:/src", true, "wt", 1);
    await vi.waitFor(() => expect(server.getFileTree).toHaveBeenCalledTimes(1));
    mocks.useChatStore.getState.mockReturnValue({ activeChatId: "chat:part" });
    const second = getFileTree("/src", true, "wt", 1);
    await vi.waitFor(() => expect(server.getFileTree).toHaveBeenCalledTimes(2));
    reply.open({ files: [] });
    await Promise.all([first, second]);
  });

  it("does not let the app cache cancel or retain editor-buffer reads", async () => {
    const reply = gate<{ content: string }>();
    server.getFileContent.mockReturnValue(reply.promise);
    const pending = getFileContent("a.ts", "wt");
    await vi.waitFor(() => expect(server.getFileContent).toHaveBeenCalledTimes(1));
    // App invalidation/removal must not cancel this unobserved editor read.
    const { queryClient } = await import("@/lib/query-client");
    await queryClient.cancelQueries({ queryKey: ["fileSystemService"] });
    reply.open({ content: "still returned" });
    expect(await pending).toBe("still returned");
    expect(queryClient.getQueryCache().findAll({ queryKey: ["fileSystemService"] })).toEqual([]);
  });

  it("retains tree sorting, hidden filtering and unloaded boundary directories", async () => {
    server.getFileTree.mockResolvedValue({ files: [
      { name: "z.ts", path: "z.ts", type: FileNodeType.FILE, size: 9n },
      { name: ".hidden", path: ".hidden", type: FileNodeType.FILE },
      { name: "src", path: "src", type: FileNodeType.DIRECTORY, hasChildren: true },
    ] });
    const rows = await getFileTree("/", false, "wt", 1);
    expect(rows.map((row) => row.name)).toEqual(["src", "z.ts"]);
    expect(rows[0]?.children).toBeUndefined();
    expect(rows[0]?.hasChildren).toBe(true);
    expect(rows[1]?.size).toBe(9);
    expect(server.getFileTree.mock.calls[0][0]).toMatchObject({ showHidden: true, depth: 1 });
  });

  it("uses fresh content/metadata/bytes after every settled read, preserving mapping", async () => {
    mocks.resolveWorktree.mockResolvedValue({ path: "/workspace" });
    server.getFileContent.mockResolvedValueOnce({ content: "old" }).mockResolvedValueOnce({ content: "new" });
    server.getFilePreviewInfo.mockResolvedValue({ info: { path: "image.png", name: "image.png", size: 123n, viewerKind: FileViewerKind.IMAGE, mimeType: "image/png", isBinary: true } });
    server.getFilePreview.mockResolvedValue({ content: new Uint8Array([0, 255, 17]), contentType: "image/png" });
    expect(await getFileContent("/workspace/image.png", "wt")).toBe("old");
    expect(await getFileContent("/workspace/image.png", "wt")).toBe("new");
    const info = await getFilePreviewInfo("/workspace/image.png", "wt");
    expect(info).toMatchObject({ size: 123, viewerKind: "image", mimeType: "image/png", isBinary: true });
    const blob = await getFilePreviewBlob("/workspace/image.png", "wt");
    expect(blob.type).toBe("image/png");
    expect(new Uint8Array(await blob.arrayBuffer())).toEqual(new Uint8Array([0, 255, 17]));
    for (const method of [server.getFileContent, server.getFilePreviewInfo, server.getFilePreview]) {
      expect(method.mock.calls[0][0]).toMatchObject({ projectId: "project-1", path: "image.png", worktreeId: "wt", chatId: "chat-a" });
    }
  });

  it("does not retry failed or canceled responses, and a later read recovers", async () => {
    for (const code of [Code.Unavailable, Code.Canceled]) {
      server.getFileContent.mockRejectedValueOnce(new ConnectError("read failed", code)).mockResolvedValueOnce({ content: "recovered" });
      const before = server.getFileContent.mock.calls.length;
      await expect(getFileContent("a.ts", "wt")).rejects.toMatchObject({ code });
      expect(server.getFileContent).toHaveBeenCalledTimes(before + 1);
      expect(await getFileContent("a.ts", "wt")).toBe("recovered");
    }
  });

  it("still sends explicit filesystem reads while the browser reports offline", async () => {
    onlineManager.setOnline(false);
    server.getFileContent.mockResolvedValue({ content: "local daemon reachable" });
    expect(await getFileContent("a.ts", "wt")).toBe("local daemon reachable");
  });

  it("retains missing-preview error and no-project behavior", async () => {
    server.getFilePreviewInfo.mockResolvedValue({});
    await expect(getFilePreviewInfo("a.ts", "wt")).rejects.toThrow("File preview info not found");
    mocks.useProjectStore.getState.mockReturnValue({ currentProject: null });
    expect(await getFileTree()).toEqual([]);
    await expect(getFileContent("a.ts")).rejects.toThrow();
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
