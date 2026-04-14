import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  filesystemGrpc: {
    getFilePreviewInfo: vi.fn(),
    getFilePreview: vi.fn(),
  },
  useProjectStore: {
    getState: vi.fn(),
  },
  useWorktreeStore: {
    getState: vi.fn(),
  },
  triggerGitStatusRefresh: vi.fn(),
}));

vi.mock("../filesystem-grpc", () => ({
  filesystemGrpc: mocks.filesystemGrpc,
}));

vi.mock("../../store/projectStore", () => ({
  useProjectStore: mocks.useProjectStore,
}));

vi.mock("../../store/worktreeStore", () => ({
  useWorktreeStore: mocks.useWorktreeStore,
}));

vi.mock("../../store/gitStatusStore", () => ({
  triggerGitStatusRefresh: mocks.triggerGitStatusRefresh,
}));

import { getFilePreviewBlob } from "../fileSystem";

describe("fileSystem preview transport", () => {
  beforeEach(() => {
    vi.clearAllMocks();

    mocks.useProjectStore.getState.mockReturnValue({
      currentProject: { id: "project-1" },
    });
    mocks.useWorktreeStore.getState.mockReturnValue({
      worktrees: [{ id: "wt-1", path: "/workspace", name: "main" }],
    });
    mocks.filesystemGrpc.getFilePreview.mockResolvedValue({
      content: new Uint8Array([0x89, 0x50, 0x4e, 0x47]),
      contentType: "image/png",
      filename: "photo.png",
      size: BigInt(4),
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("calls filesystemGrpc.getFilePreview with normalized path and returns a Blob", async () => {
    const blob = await getFilePreviewBlob("/workspace/photo.png", "wt-1");

    expect(mocks.filesystemGrpc.getFilePreview).toHaveBeenCalledWith(
      "project-1",
      "photo.png",
      "wt-1",
    );
    expect(blob).toBeInstanceOf(Blob);
    expect(blob.type).toBe("image/png");
    expect(blob.size).toBe(4);
  });

  it("works without a worktree ID", async () => {
    const blob = await getFilePreviewBlob("photo.png");

    expect(mocks.filesystemGrpc.getFilePreview).toHaveBeenCalledWith(
      "project-1",
      "photo.png",
      undefined,
    );
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
