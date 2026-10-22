import { beforeEach, describe, expect, it, vi } from "vitest";

const fileSystemMocks = vi.hoisted(() => ({
  getFileTree: vi.fn(),
  getFileContent: vi.fn(),
  getFilePreviewInfo: vi.fn(),
}));

vi.mock("../../api/fileSystem", () => ({
  getFileTree: fileSystemMocks.getFileTree,
  getFileContent: fileSystemMocks.getFileContent,
  getFilePreviewInfo: fileSystemMocks.getFilePreviewInfo,
}));

vi.mock("../logger", () => ({
  logger: {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  },
}));

import { monacoFileSync } from "../monacoFileSync";

function createMonacoMock() {
  return {
    languages: {
      typescript: {
        typescriptDefaults: {
          addExtraLib: vi.fn(),
        },
        javascriptDefaults: {
          addExtraLib: vi.fn(),
        },
      },
    },
  } as any;
}

describe("monacoFileSync", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    monacoFileSync.dispose();
  });

  it("skips non-text files before requesting content", async () => {
    const monaco = createMonacoMock();
    monacoFileSync.initialize(monaco, { worktreeId: "wt-1" });

    fileSystemMocks.getFilePreviewInfo.mockResolvedValue({
      name: "photo.png",
      path: "/workspace/photo.png",
      size: 512,
      modified: "2024-01-01T00:00:00Z",
      viewerKind: "image",
      mimeType: "image/png",
      isBinary: true,
      isEditable: false,
    });

    await monacoFileSync.syncFile("/workspace/photo.png");

    expect(fileSystemMocks.getFilePreviewInfo).toHaveBeenCalledWith("/workspace/photo.png", "wt-1");
    expect(fileSystemMocks.getFileContent).not.toHaveBeenCalled();
    expect(monaco.languages.typescript.typescriptDefaults.addExtraLib).not.toHaveBeenCalled();
    expect(monacoFileSync.isFileSynced("/workspace/photo.png")).toBe(false);
  });

  it("syncs text files into Monaco extra libs", async () => {
    const monaco = createMonacoMock();
    monacoFileSync.initialize(monaco, { worktreeId: "wt-1" });

    fileSystemMocks.getFilePreviewInfo.mockResolvedValue({
      name: "example.ts",
      path: "/workspace/example.ts",
      size: 128,
      modified: "2024-01-01T00:00:00Z",
      viewerKind: "text",
      mimeType: "text/plain",
      isBinary: false,
      isEditable: true,
    });
    fileSystemMocks.getFileContent.mockResolvedValue("export const answer = 42;");

    await monacoFileSync.syncFile("/workspace/example.ts");

    expect(fileSystemMocks.getFileContent).toHaveBeenCalledWith("/workspace/example.ts", "wt-1");
    expect(monaco.languages.typescript.typescriptDefaults.addExtraLib).toHaveBeenCalledWith(
      "export const answer = 42;",
      "file:///workspace/example.ts"
    );
    expect(monacoFileSync.isFileSynced("/workspace/example.ts")).toBe(true);
  });
});
