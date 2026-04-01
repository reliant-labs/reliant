import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ComponentProps } from "react";
import { FileTree } from "./FileTree";
import type { FileNode } from "./index";

const apiMocks = vi.hoisted(() => ({
  getFileTree: vi.fn(),
  createFile: vi.fn(),
  createFolder: vi.fn(),
  deleteFileOrFolder: vi.fn(),
  copyFile: vi.fn(),
  getFileContent: vi.fn(),
  getFilePreviewInfo: vi.fn(),
}));

const storeMocks = vi.hoisted(() => ({
  addDeletedFile: vi.fn(),
  toastNotify: vi.fn(),
}));
const mockProjectStore = {
  currentProject: {
    id: "proj-1",
    name: "Reliant",
    path: "/workspace",
  },
};
const mockViewerStore = {
  openFileViewer: vi.fn(),
  getActiveViewer: vi.fn(() => null),
};

vi.mock("../../api/fileSystem", () => ({
  getFileTree: apiMocks.getFileTree,
  createFile: apiMocks.createFile,
  createFolder: apiMocks.createFolder,
  deleteFileOrFolder: apiMocks.deleteFileOrFolder,
  copyFile: apiMocks.copyFile,
  getFileContent: apiMocks.getFileContent,
  getFilePreviewInfo: apiMocks.getFilePreviewInfo,
}));

vi.mock("../../store/projectStore", () => ({
  useProjectStore: (selector: (state: typeof mockProjectStore) => unknown) => selector(mockProjectStore),
}));

vi.mock("../../store/viewerStore", () => ({
  useViewerStore: (selector: (state: typeof mockViewerStore) => unknown) => selector(mockViewerStore),
}));

vi.mock("../../store/fileDeletionStore", () => ({
  useFileDeletionStore: (selector: (state: { addDeletedFile: typeof storeMocks.addDeletedFile }) => unknown) =>
    selector({ addDeletedFile: storeMocks.addDeletedFile }),
}));

vi.mock("../../lib/toast-manager", () => ({
  toast: {
    notify: storeMocks.toastNotify,
  },
}));

vi.mock("./FileOperationsModal", () => ({
  FileOperationsModal: () => null,
}));

vi.mock("./FileTreeItem", () => ({
  FileTreeItem: ({ node, onFileOperation }: { node: FileNode; onFileOperation?: (operation: "copy" | "delete", node: FileNode, skipModal?: boolean) => void }) => (
    <button data-testid={`delete-${node.name}`} onClick={() => onFileOperation?.("delete", node, true)}>
      Delete {node.name}
    </button>
  ),
}));

function renderFileTree(overrides: Partial<ComponentProps<typeof FileTree>> = {}) {
  const onRefresh = vi.fn();

  render(
    <FileTree
      searchQuery=""
      onFileSelect={vi.fn()}
      onPathChange={vi.fn()}
      selectedFile={null}
      showHidden={false}
      onRefresh={onRefresh}
      collapseKey={0}
      worktreeId="wt-1"
      {...overrides}
    />
  );

  return { onRefresh };
}

describe("FileTree binary-aware delete snapshots", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.spyOn(window, "focus").mockImplementation(() => undefined);
  });

  it("marks non-text deletes as non-undoable without reading file content", async () => {
    apiMocks.getFileTree.mockResolvedValue([
      {
        name: "archive.zip",
        path: "/workspace/archive.zip",
        type: "file",
      },
    ]);
    apiMocks.getFilePreviewInfo.mockResolvedValue({
      name: "archive.zip",
      path: "/workspace/archive.zip",
      size: 2048,
      modified: "2024-01-01T00:00:00Z",
      viewerKind: "binary",
      mimeType: "application/zip",
      isBinary: true,
      isEditable: false,
    });
    apiMocks.deleteFileOrFolder.mockResolvedValue(undefined);

    const { onRefresh } = renderFileTree();

    fireEvent.click(await screen.findByTestId("delete-archive.zip"));

    await waitFor(() => {
      expect(apiMocks.deleteFileOrFolder).toHaveBeenCalledWith("/workspace/archive.zip", "wt-1");
    });

    expect(apiMocks.getFileContent).not.toHaveBeenCalled();
    expect(storeMocks.addDeletedFile).toHaveBeenCalledWith(
      expect.objectContaining({
        path: "/workspace/archive.zip",
        type: "file",
        content: undefined,
        canUndo: false,
        undoReason: "Undo is unavailable for binary files.",
        worktreeId: "wt-1",
        projectId: "proj-1",
      })
    );
    expect(storeMocks.toastNotify).toHaveBeenCalledWith(
      "File deleted",
      expect.objectContaining({
        description: "archive.zip • Undo is unavailable for binary files.",
        action: undefined,
      })
    );
    expect(onRefresh).toHaveBeenCalled();
  });

  it("captures text content for undoable text deletes", async () => {
    apiMocks.getFileTree.mockResolvedValue([
      {
        name: "example.ts",
        path: "/workspace/example.ts",
        type: "file",
      },
    ]);
    apiMocks.getFilePreviewInfo.mockResolvedValue({
      name: "example.ts",
      path: "/workspace/example.ts",
      size: 128,
      modified: "2024-01-01T00:00:00Z",
      viewerKind: "text",
      mimeType: "text/plain",
      isBinary: false,
      isEditable: true,
    });
    apiMocks.getFileContent.mockResolvedValue("const answer = 42;");
    apiMocks.deleteFileOrFolder.mockResolvedValue(undefined);

    renderFileTree();

    fireEvent.click(await screen.findByTestId("delete-example.ts"));

    await waitFor(() => {
      expect(apiMocks.deleteFileOrFolder).toHaveBeenCalledWith("/workspace/example.ts", "wt-1");
    });

    expect(apiMocks.getFileContent).toHaveBeenCalledWith("/workspace/example.ts", "wt-1");
    expect(storeMocks.addDeletedFile).toHaveBeenCalledWith(
      expect.objectContaining({
        path: "/workspace/example.ts",
        type: "file",
        content: "const answer = 42;",
        canUndo: true,
        undoReason: undefined,
      })
    );
    expect(storeMocks.toastNotify).toHaveBeenCalledWith(
      "File deleted",
      expect.objectContaining({
        description: "example.ts",
        action: expect.objectContaining({ label: "Undo (Cmd+Z)" }),
      })
    );
  });
});
