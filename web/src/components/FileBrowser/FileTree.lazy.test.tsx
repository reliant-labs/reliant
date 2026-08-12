import { createRef } from "react";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
// FileTree shows the shared machine-wait state, which reads daemon status
// through React Query — so it needs a provider the way the real app has one.
import { renderWithQuery as render } from "../../test/renderWithQuery";
import { FileTree, type FileTreeHandle } from "./FileTree";
import type { FileNode } from "./index";

// Verifies the VS Code-style lazy loading orchestration in the FileTree
// container: initial depth-2 fetch, chevrons gated on has_children, expand
// triggering a depth-1 fetch that merges children, and refresh refetching only
// the visible directories so a file deleted on disk disappears.

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
  useFileDeletionStore: (selector: (state: { addDeletedFile: () => void }) => unknown) =>
    selector({ addDeletedFile: vi.fn() }),
}));
vi.mock("../../lib/toast-manager", () => ({ toast: { notify: vi.fn() } }));
vi.mock("./FileOperationsModal", () => ({ FileOperationsModal: () => null }));

// A lightweight recursive stand-in for FileTreeItem that surfaces exactly the
// signals this test cares about: a chevron marker when the node has children, a
// loading marker while children fetch, expand/collapse triggers, and the child
// rows for expanded directories.
type ItemProps = {
  node: FileNode;
  expandedPaths?: Set<string>;
  loadingPaths?: Set<string>;
  onExpand?: (path: string) => void;
  onCollapse?: (path: string) => void;
};
function MockItem({ node, expandedPaths, loadingPaths, onExpand, onCollapse }: ItemProps) {
  const isDir = node.type === "directory";
  const hasChildren = node.hasChildren ?? ((node.children?.length ?? 0) > 0);
  const expanded = expandedPaths?.has(node.path) ?? false;
  return (
    <div>
      <span data-testid={`row-${node.path}`}>
        {node.name}
        {isDir && hasChildren ? " [chevron]" : ""}
        {loadingPaths?.has(node.path) ? " [loading]" : ""}
      </span>
      {isDir && (
        <>
          <button data-testid={`expand-${node.path}`} onClick={() => onExpand?.(node.path)}>
            expand
          </button>
          <button data-testid={`collapse-${node.path}`} onClick={() => onCollapse?.(node.path)}>
            collapse
          </button>
        </>
      )}
      {isDir &&
        expanded &&
        node.children?.map((c) => (
          <MockItem
            key={c.path}
            node={c}
            expandedPaths={expandedPaths}
            loadingPaths={loadingPaths}
            onExpand={onExpand}
            onCollapse={onCollapse}
          />
        ))}
    </div>
  );
}
vi.mock("./FileTreeItem", () => ({ FileTreeItem: MockItem }));

// depth-2 snapshot of root: src (dir, expandable) with a loaded child dir "deep"
// left at the depth boundary (children undefined, has_children true) and a file.
function rootDepth2(includeReadme: boolean): FileNode[] {
  const nodes: FileNode[] = [
    {
      name: "src",
      path: "src",
      type: "directory",
      hasChildren: true,
      children: [
        { name: "deep", path: "src/deep", type: "directory", hasChildren: true }, // boundary: not loaded
        { name: "a.ts", path: "src/a.ts", type: "file", size: 10 },
      ],
    },
  ];
  if (includeReadme) nodes.push({ name: "README.md", path: "README.md", type: "file", size: 5 });
  return nodes;
}

function renderTree() {
  const ref = createRef<FileTreeHandle>();
  render(
    <FileTree
      ref={ref}
      searchQuery=""
      onFileSelect={vi.fn()}
      onPathChange={vi.fn()}
      selectedFile={null}
      showHidden={false}
      onRefresh={vi.fn()}
      collapseKey={0}
      worktreeId="wt-1"
    />
  );
  return { ref };
}

describe("FileTree lazy loading", () => {
  beforeEach(() => vi.clearAllMocks());

  it("fetches depth 2 initially, lazily loads on expand, and refetches on refresh", async () => {
    let readmePresent = true;
    let deepFilePresent = true;
    apiMocks.getFileTree.mockImplementation(
      async (path: string) => {
        if (path === "/") return rootDepth2(readmePresent);
        if (path === "src") {
          // src's immediate children (depth 1): the boundary dir + a file.
          return [
            { name: "deep", path: "src/deep", type: "directory", hasChildren: true },
            { name: "a.ts", path: "src/a.ts", type: "file", size: 10 },
          ] as FileNode[];
        }
        if (path === "src/deep") {
          return deepFilePresent
            ? ([{ name: "deep.ts", path: "src/deep/deep.ts", type: "file", size: 3 }] as FileNode[])
            : [];
        }
        return [];
      }
    );

    const { ref } = renderTree();

    // (1) Initial load requests two levels below root.
    await waitFor(() => expect(screen.getByTestId("row-src")).toBeInTheDocument());
    expect(apiMocks.getFileTree).toHaveBeenCalledWith("/", false, "wt-1", 2);

    // (2) has_children drives the chevron: src and README differ.
    expect(screen.getByTestId("row-src")).toHaveTextContent("[chevron]");
    expect(screen.getByTestId("row-README.md")).not.toHaveTextContent("[chevron]");

    // (3) Expanding src reveals its preloaded children WITHOUT another fetch
    // (depth-2 already loaded them). "deep" sits at the boundary with a chevron.
    fireEvent.click(screen.getByTestId("expand-src"));
    await waitFor(() => expect(screen.getByTestId("row-src/deep")).toBeInTheDocument());
    expect(screen.getByTestId("row-src/a.ts")).toBeInTheDocument();
    expect(screen.getByTestId("row-src/deep")).toHaveTextContent("[chevron]");
    expect(apiMocks.getFileTree).toHaveBeenCalledTimes(1); // no fetch for src

    // (4) Expanding the boundary dir lazily fetches its children at depth 1.
    fireEvent.click(screen.getByTestId("expand-src/deep"));
    await waitFor(() => expect(screen.getByTestId("row-src/deep/deep.ts")).toBeInTheDocument());
    expect(apiMocks.getFileTree).toHaveBeenCalledWith("src/deep", false, "wt-1", 1);

    // (5) Files deleted on disk (README.md at root, deep.ts inside the expanded
    // dir) disappear after a refresh, which refetches only the visible
    // directories: root at depth 2 plus every expanded directory at depth 1.
    readmePresent = false;
    deepFilePresent = false;
    apiMocks.getFileTree.mockClear();

    await act(async () => {
      await ref.current!.refresh();
    });

    await waitFor(() => expect(screen.queryByTestId("row-README.md")).not.toBeInTheDocument());
    expect(screen.queryByTestId("row-src/deep/deep.ts")).not.toBeInTheDocument();
    // Surviving nodes remain; the expanded subtree did not collapse.
    expect(screen.getByTestId("row-src")).toBeInTheDocument();
    expect(screen.getByTestId("row-src/a.ts")).toBeInTheDocument();

    // Refresh refetched root (depth 2) and each expanded directory (depth 1),
    // not the whole recursive tree.
    expect(apiMocks.getFileTree).toHaveBeenCalledWith("/", false, "wt-1", 2);
    expect(apiMocks.getFileTree).toHaveBeenCalledWith("src", false, "wt-1", 1);
    expect(apiMocks.getFileTree).toHaveBeenCalledWith("src/deep", false, "wt-1", 1);
  });

  it("collapsing a directory hides its children", async () => {
    apiMocks.getFileTree.mockImplementation(async (path: string) =>
      path === "/" ? rootDepth2(true) : []
    );

    renderTree();
    await waitFor(() => expect(screen.getByTestId("row-src")).toBeInTheDocument());

    fireEvent.click(screen.getByTestId("expand-src"));
    await waitFor(() => expect(screen.getByTestId("row-src/a.ts")).toBeInTheDocument());

    fireEvent.click(screen.getByTestId("collapse-src"));
    await waitFor(() => expect(screen.queryByTestId("row-src/a.ts")).not.toBeInTheDocument());
  });
});
