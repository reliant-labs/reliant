import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  filesystemGrpc: {
    getFilePreviewInfo: vi.fn(),
  },
  useProjectStore: {
    getState: vi.fn(),
  },
  useWorktreeStore: {
    getState: vi.fn(),
  },
  supabase: {
    auth: {
      getSession: vi.fn(),
    },
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

vi.mock("../../lib/supabase", () => ({
  supabase: mocks.supabase,
}));

import { getFilePreviewBlob } from "../fileSystem";

describe("fileSystem preview transport", () => {
  const fetchSpy = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    vi.stubGlobal("fetch", fetchSpy);

    window.RELIANT_CONFIG = {
      isElectron: true,
      backendUrl: "https://localhost:8132",
      grpcUrl: "https://localhost:9142",
    };

    mocks.useProjectStore.getState.mockReturnValue({
      currentProject: { id: "project-1" },
    });
    mocks.useWorktreeStore.getState.mockReturnValue({
      worktrees: [{ id: "wt-1", path: "/workspace", name: "main" }],
    });
    mocks.filesystemGrpc.getFilePreviewInfo.mockResolvedValue({
      name: "photo.png",
      path: "photo.png",
      size: 123,
      modified: "2024-01-01T00:00:00Z",
      viewerKind: "image",
      mimeType: "image/png",
      isBinary: true,
      isEditable: false,
    });
    mocks.supabase.auth.getSession.mockResolvedValue({
      data: { session: null },
    });
    fetchSpy.mockResolvedValue({
      ok: true,
      blob: vi.fn().mockResolvedValue(new Blob(["img"], { type: "image/png" })),
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    delete window.RELIANT_CONFIG;
  });

  it("uses the backend API origin instead of the gRPC origin for preview blobs", async () => {
    await getFilePreviewBlob("/workspace/photo.png", "wt-1");

    expect(mocks.filesystemGrpc.getFilePreviewInfo).toHaveBeenCalledWith(
      "project-1",
      "photo.png",
      "wt-1",
    );
    expect(fetchSpy).toHaveBeenCalledWith(
      "https://localhost:8132/api/v2/files/preview?project_id=project-1&path=photo.png&worktree_id=wt-1",
      { headers: {} },
    );
  });

  it("falls back to the browser origin when backend config is unavailable", async () => {
    delete window.RELIANT_CONFIG;

    await getFilePreviewBlob("/workspace/photo.png", "wt-1");

    expect(fetchSpy).toHaveBeenCalledWith(
      `${window.location.origin}/api/v2/files/preview?project_id=project-1&path=photo.png&worktree_id=wt-1`,
      { headers: {} },
    );
  });
});
