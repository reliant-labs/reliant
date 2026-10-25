import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { serviceHandlers } from "@/test/serviceTransport";

const mocks = vi.hoisted(() => ({
  useProjectStore: {
    getState: vi.fn(),
  },
  resolveWorktree: vi.fn(),
  useChatStore: {
    getState: vi.fn(),
  },
  triggerGitStatusRefresh: vi.fn(),
}));

vi.mock("../../store/projectStore", () => ({
  useProjectStore: mocks.useProjectStore,
}));

vi.mock("../../hooks/worktree-queries", () => ({
  resolveWorktree: mocks.resolveWorktree,
}));

vi.mock("../../store/chatStore", () => ({
  useChatStore: mocks.useChatStore,
}));

vi.mock("../../store/gitStatusStore", () => ({
  triggerGitStatusRefresh: mocks.triggerGitStatusRefresh,
}));

import { getFilePreviewBlob } from "../fileSystem";

const getFilePreview = vi.fn();

describe("fileSystem preview transport", () => {
  beforeEach(() => {
    vi.clearAllMocks();

    mocks.useProjectStore.getState.mockReturnValue({
      currentProject: { id: "project-1" },
    });
    const worktrees = [{ id: "wt-1", path: "/workspace", name: "main" }];
    mocks.resolveWorktree.mockImplementation(async (id: string) => worktrees.find((w) => w.id === id) ?? null);
    mocks.useChatStore.getState.mockReturnValue({ activeChatId: "chat-1" });
    getFilePreview.mockResolvedValue({
      content: new Uint8Array([0x89, 0x50, 0x4e, 0x47]),
      contentType: "image/png",
      filename: "photo.png",
      size: BigInt(4),
    });
    setTransport(mockTransport(serviceHandlers("FileSystemService", { getFilePreview })));
  });

  afterEach(() => {
    setTransport(null);
    vi.unstubAllGlobals();
  });

  it("asks for the preview by its worktree-relative path and returns a Blob", async () => {
    const blob = await getFilePreviewBlob("/workspace/photo.png", "wt-1");

    expect(getFilePreview).toHaveBeenCalledTimes(1);
    expect(getFilePreview.mock.calls[0][0]).toMatchObject({
      projectId: "project-1",
      path: "photo.png",
      worktreeId: "wt-1",
      chatId: "chat-1",
    });
    expect(blob).toBeInstanceOf(Blob);
    expect(blob.type).toBe("image/png");
    expect(blob.size).toBe(4);
  });

  it("works without a worktree ID", async () => {
    const blob = await getFilePreviewBlob("photo.png");

    expect(getFilePreview.mock.calls[0][0]).toMatchObject({
      projectId: "project-1",
      path: "photo.png",
      chatId: "chat-1",
    });
    expect(getFilePreview.mock.calls[0][0].worktreeId).toBeUndefined();
    expect(blob).toBeInstanceOf(Blob);
    expect(blob.type).toBe("image/png");
  });

  it("throws when no project is selected", async () => {
    mocks.useProjectStore.getState.mockReturnValue({
      currentProject: null,
    });

    await expect(getFilePreviewBlob("photo.png")).rejects.toThrow(
      "No current project selected",
    );
  });
});
