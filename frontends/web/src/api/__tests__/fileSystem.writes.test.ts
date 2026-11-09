// @vitest-environment node
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FileNodeType } from "@/gen/reliant/v1/filesystem_pb";
import { fileSystemServiceKeys } from "@/hooks/file-system-service-hooks_gen";
import { subscribeFileRewrites } from "@/lib/fileRewrites";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { serviceHandlers } from "@/test/serviceTransport";

const mocks = vi.hoisted(() => ({
  useProjectStore: { getState: vi.fn() }, useChatStore: { getState: vi.fn() },
  resolveWorktree: vi.fn(), refresh: vi.fn(),
}));
vi.mock("../../store/projectStore", () => ({ useProjectStore: mocks.useProjectStore }));
vi.mock("../../store/chatStore", () => ({ useChatStore: mocks.useChatStore }));
vi.mock("../../hooks/worktree-queries", () => ({ resolveWorktree: mocks.resolveWorktree }));
vi.mock("../../store/gitStatusStore", () => ({ triggerGitStatusRefresh: mocks.refresh }));

import { saveFileContent, createFile, createFolder, deleteFileOrFolder, copyFile, moveFile, replaceInFiles } from "../fileSystem";

const server = {
  saveFileContent: vi.fn(), createFileOrFolder: vi.fn(), deleteFileOrFolder: vi.fn(),
  copyFile: vi.fn(), replaceInFiles: vi.fn(),
};
beforeEach(() => {
  vi.resetAllMocks();
  mocks.useProjectStore.getState.mockReturnValue({ currentProject: { id: "project-a" } });
  mocks.useChatStore.getState.mockReturnValue({ activeChatId: "chat-a" });
  mocks.resolveWorktree.mockResolvedValue({ path: "/ws" });
  for (const method of Object.values(server)) method.mockResolvedValue({});
  setTransport(mockTransport(serviceHandlers("FileSystemService", server)));
});
afterEach(() => setTransport(null));

const cases = [
  { name: "save", method: "saveFileContent", call: () => saveFileContent("/ws/a.ts", "new bytes", "wt-a"), request: { path: "a.ts", content: "new bytes" } },
  { name: "create file", method: "createFileOrFolder", call: () => createFile("nested/a.ts", "initial", "wt-a"), request: { path: "nested/a.ts", content: "initial", type: FileNodeType.FILE } },
  { name: "create folder", method: "createFileOrFolder", call: () => createFolder("nested/dir", "wt-a"), request: { path: "nested/dir", content: "", type: FileNodeType.DIRECTORY } },
  { name: "delete", method: "deleteFileOrFolder", call: () => deleteFileOrFolder("nested/a.ts", "wt-a"), request: { path: "nested/a.ts" } },
  { name: "copy", method: "copyFile", call: () => copyFile("nested/a.ts", "nested/b.ts", "wt-a"), request: { sourcePath: "nested/a.ts", destinationPath: "nested/b.ts" } },
] as const;

describe("owned filesystem write policy (not yet migrated to mutation helpers)", () => {
  it.each(cases)("$name preserves wire scope and refreshes only once after success", async ({ method, call, request }) => {
    await call();
    expect(server[method]).toHaveBeenCalledTimes(1);
    expect(server[method].mock.calls[0]?.[0]).toMatchObject({ projectId: "project-a", worktreeId: "wt-a", chatId: "chat-a", ...request });
    expect(mocks.refresh).toHaveBeenCalledExactlyOnceWith("wt-a", "project-a");
  });

  it.each(cases)("$name neither retries nor refreshes failed/canceled writes", async ({ method, call }) => {
    for (const code of [Code.Unavailable, Code.Canceled]) {
      server[method].mockRejectedValueOnce(new ConnectError("write refused", code));
      const before = server[method].mock.calls.length;
      await expect(call()).rejects.toMatchObject({ code });
      expect(server[method]).toHaveBeenCalledTimes(before + 1);
      expect(mocks.refresh).not.toHaveBeenCalled();
    }
  });

  it("move captures one project/chat scope even if UI switches during copy", async () => {
    let copied!: () => void;
    server.copyFile.mockReturnValue(new Promise((resolve) => { copied = () => resolve({}); }));
    const moving = moveFile("nested/a.ts", "nested/b.ts", "wt-a");
    await vi.waitFor(() => expect(server.copyFile).toHaveBeenCalledTimes(1));
    expect(server.deleteFileOrFolder).not.toHaveBeenCalled();
    mocks.useProjectStore.getState.mockReturnValue({ currentProject: { id: "project-b" } });
    mocks.useChatStore.getState.mockReturnValue({ activeChatId: "chat-b" });
    copied();
    await moving;
    expect(server.deleteFileOrFolder.mock.calls[0]?.[0]).toMatchObject({ projectId: "project-a", chatId: "chat-a", worktreeId: "wt-a", path: "nested/a.ts" });
    expect(mocks.refresh).toHaveBeenCalledExactlyOnceWith("wt-a", "project-a");
  });

  it("move never deletes on failed copy and never retries/refreshes a failed delete", async () => {
    server.copyFile.mockRejectedValueOnce(new ConnectError("copy canceled", Code.Canceled));
    await expect(moveFile("a", "b", "wt-a")).rejects.toMatchObject({ code: Code.Canceled });
    expect(server.deleteFileOrFolder).not.toHaveBeenCalled();
    server.copyFile.mockResolvedValue({});
    server.deleteFileOrFolder.mockRejectedValueOnce(new ConnectError("delete failed", Code.Unavailable));
    await expect(moveFile("a", "b", "wt-a")).rejects.toMatchObject({ code: Code.Unavailable });
    expect(server.deleteFileOrFolder).toHaveBeenCalledTimes(1);
    expect(mocks.refresh).not.toHaveBeenCalled();
  });

  it("replace keeps exact search invalidation, rewrite mapping and success notification", async () => {
    const qc = new QueryClient();
    const search = fileSystemServiceKeys.searchFiles({ projectId: "project-a", query: "old" });
    const content = fileSystemServiceKeys.getFileContent({ projectId: "project-a", path: "a.ts" });
    qc.setQueryData(search, {}); qc.setQueryData(content, {});
    const rewritten = vi.fn(); const off = subscribeFileRewrites(rewritten);
    server.replaceInFiles.mockResolvedValue({ results: [{ path: "a.ts", replacements: 2, success: true }, { path: "b.ts", success: false, error: "denied" }], totalReplacements: 2, filesModified: 1 });
    try {
      const result = await replaceInFiles("old", "new", { worktreeId: "wt-a", path: "nested", filePaths: ["a.ts"], caseSensitive: true }, qc);
      expect(server.replaceInFiles.mock.calls[0]?.[0]).toMatchObject({ projectId: "project-a", chatId: "chat-a", worktreeId: "wt-a", searchText: "old", replaceText: "new", path: "nested", filePaths: ["a.ts"], caseSensitive: true });
      expect(result.results[0]?.error).toBeUndefined();
      expect(rewritten).toHaveBeenCalledExactlyOnceWith({ worktreeId: "wt-a", paths: ["a.ts"] });
      expect(qc.getQueryState(search)?.isInvalidated).toBe(true);
      expect(qc.getQueryState(content)?.isInvalidated).toBe(false);
      expect(qc.getMutationCache().getAll()).toEqual([]);
      expect(mocks.refresh).toHaveBeenCalledExactlyOnceWith("wt-a", "project-a");
    } finally { off(); qc.clear(); }
  });

  it("replace failure/cancellation leaves caches and editor rewrite notifications alone", async () => {
    const qc = new QueryClient(); const key = fileSystemServiceKeys.searchFiles({ projectId: "project-a", query: "old" });
    qc.setQueryData(key, {}); const rewritten = vi.fn(); const off = subscribeFileRewrites(rewritten);
    try {
      for (const code of [Code.Unavailable, Code.Canceled]) {
        server.replaceInFiles.mockRejectedValueOnce(new ConnectError("replace refused", code));
        await expect(replaceInFiles("old", "new", { worktreeId: "wt-a" }, qc)).rejects.toMatchObject({ code });
      }
      expect(server.replaceInFiles).toHaveBeenCalledTimes(2);
      expect(qc.getQueryState(key)?.isInvalidated).toBe(false);
      expect(rewritten).not.toHaveBeenCalled(); expect(mocks.refresh).not.toHaveBeenCalled();
    } finally { off(); qc.clear(); }
  });

  it("save remains a no-op without a project; other writes reject before dispatch", async () => {
    mocks.useProjectStore.getState.mockReturnValue({ currentProject: null });
    await saveFileContent("a", "bytes");
    await expect(createFile("a")).rejects.toThrow("No current project selected");
    for (const method of Object.values(server)) expect(method).not.toHaveBeenCalled();
  });
});
