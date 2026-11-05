import { act, screen, waitFor } from "@testing-library/react";
import { renderWithQuery as render } from "../../test/renderWithQuery";
import { FileTree } from "./FileTree";
import type { FileNode } from "./index";

// RightSidebar swaps worktreeId on a mounted FileTree. A slow GetFileTree for
// the previous workspace must not overwrite, or be merged into, the new tree.

const apiMocks = vi.hoisted(() => ({
  getFileTree: vi.fn(),
  createFile: vi.fn(),
  createFolder: vi.fn(),
  deleteFileOrFolder: vi.fn(),
  copyFile: vi.fn(),
  getFileContent: vi.fn(),
  getFilePreviewInfo: vi.fn(),
}));

const mockProjectStore = { currentProject: { id: "proj-1", name: "Reliant", path: "/workspace" } };
const mockViewerStore = { openFileViewer: vi.fn(), getActiveViewer: vi.fn(() => null) };

vi.mock("../../api/fileSystem", () => apiMocks);
vi.mock("../../store/projectStore", () => ({
  useProjectStore: (selector: (state: typeof mockProjectStore) => unknown) => selector(mockProjectStore),
}));
vi.mock("../../store/viewerStore", () => ({
  useViewerStore: (selector: (state: typeof mockViewerStore) => unknown) => selector(mockViewerStore),
}));
vi.mock("../../store/fileDeletionStore", () => ({
  useFileDeletionStore: (selector: (state: { addDeletedFile: () => void }) => unknown) =>
    selector({ addDeletedFile: vi.fn() }),
}));
vi.mock("../../lib/toast-manager", () => ({ toast: { notify: vi.fn() } }));
vi.mock("./FileOperationsModal", () => ({ FileOperationsModal: () => null }));

function MockItem({ node }: { node: FileNode }) {
  return (
    <div>
      <span data-testid={`row-${node.path}`}>{node.name}</span>
      {node.children?.map((c) => <MockItem key={c.path} node={c} />)}
    </div>
  );
}
vi.mock("./FileTreeItem", () => ({ FileTreeItem: MockItem }));

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => { resolve = r; });
  return { promise, resolve };
}

function ui(worktreeId: string) {
  return (
    <FileTree
      searchQuery=""
      onFileSelect={vi.fn()}
      onPathChange={vi.fn()}
      selectedFile={null}
      showHidden={false}
      onRefresh={vi.fn()}
      collapseKey={0}
      worktreeId={worktreeId}
    />
  );
}

describe("FileTree worktree switch", () => {
  beforeEach(() => vi.clearAllMocks());

  it("drops a slow response for the previous worktree that lands after the new one", async () => {
    const oldRoot = deferred<FileNode[]>();
    const newRoot = deferred<FileNode[]>();
    apiMocks.getFileTree.mockImplementation((_path: string, _hidden: boolean, wt: string) =>
      wt === "wt-old" ? oldRoot.promise : newRoot.promise,
    );

    const { rerender } = render(ui("wt-old"));
    rerender(ui("wt-new"));

    await act(async () => {
      newRoot.resolve([
        { name: "src", path: "src", type: "directory", children: [{ name: "new.ts", path: "src/new.ts", type: "file" }] },
      ]);
    });
    await waitFor(() => expect(screen.getByTestId("row-src/new.ts")).toBeInTheDocument());

    // The old workspace answers last.
    await act(async () => {
      oldRoot.resolve([
        { name: "src", path: "src", type: "directory", children: [{ name: "old.ts", path: "src/old.ts", type: "file" }] },
        { name: "OLD.md", path: "OLD.md", type: "file" },
      ]);
    });

    expect(screen.getByTestId("row-src/new.ts")).toBeInTheDocument();
    expect(screen.queryByTestId("row-src/old.ts")).not.toBeInTheDocument();
    expect(screen.queryByTestId("row-OLD.md")).not.toBeInTheDocument();
  });

  it("does not show or merge the old worktree's nodes while the new one loads", async () => {
    const newRoot = deferred<FileNode[]>();
    apiMocks.getFileTree.mockImplementation((_path: string, _hidden: boolean, wt: string) =>
      wt === "wt-old"
        ? Promise.resolve([
            { name: "src", path: "src", type: "directory", children: [{ name: "old.ts", path: "src/old.ts", type: "file" }] },
          ] as FileNode[])
        : newRoot.promise,
    );

    const { rerender } = render(ui("wt-old"));
    await waitFor(() => expect(screen.getByTestId("row-src/old.ts")).toBeInTheDocument());

    rerender(ui("wt-new"));
    expect(screen.queryByTestId("row-src/old.ts")).not.toBeInTheDocument();

    await act(async () => {
      newRoot.resolve([{ name: "src", path: "src", type: "directory", children: [] }]);
    });
    await waitFor(() => expect(screen.getByTestId("row-src")).toBeInTheDocument());
    expect(screen.queryByTestId("row-src/old.ts")).not.toBeInTheDocument();
  });
});
