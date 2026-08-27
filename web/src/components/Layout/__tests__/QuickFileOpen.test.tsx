/**
 * The Cmd+P picker must never ask for an unbounded file tree.
 *
 * It used to call `getFileTree("/", false, worktreeId)` and lean on that
 * function's old default depth of 0, which the backend then read as "recurse
 * forever". Opening the picker on a ~100k-file project therefore made the
 * daemon walk and stat the entire repo for one keystroke, exhausting the host's
 * file table (ENFILE) and taking the app down with it.
 *
 * Depth 0 no longer means unlimited, and no depth value does: the server bounds
 * every walk with gitignore exclusion, a canonical skip set, and a 50k node
 * budget. So the picker now asks for the MAX sentinel rather than a fixed depth
 * (a fixed depth just makes deep source files unfindable). These tests pin the
 * explicit sentinel, the renderer-side candidate cap, and the UI notice that
 * keeps a partial index from looking like a bug.
 */
import { render, screen, waitFor } from "@testing-library/react";
import type { FileNode } from "../../FileBrowser";
import { QuickFileOpen, collectFiles } from "../QuickFileOpen";

const apiMocks = vi.hoisted(() => ({ getFileTree: vi.fn() }));

vi.mock("../../../api/fileSystem", () => ({
  getFileTree: apiMocks.getFileTree,
  FILE_TREE_DEPTH_MAX: -1,
  FILE_TREE_DEPTH_SERVER_DEFAULT: 0,
  FILE_TREE_DEPTH_DEFAULT: 1,
}));

const mockProjectStore = { currentProject: { id: "proj-1", name: "Reliant", path: "/workspace" } };
vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (selector: (state: typeof mockProjectStore) => unknown) => selector(mockProjectStore),
}));
vi.mock("../../../store/worktreeStore", () => ({ useActiveWorktreeId: () => "wt-1" }));
vi.mock("../../../store/viewerStore", () => ({
  useViewerStore: { getState: () => ({ openFileViewer: vi.fn() }) },
}));
vi.mock("../../../hooks/useFocusManager", () => ({ focusChatInput: vi.fn() }));

function file(path: string): FileNode {
  return { name: path.split("/").pop()!, path, type: "file" };
}

function dir(path: string, children?: FileNode[], hasChildren?: boolean): FileNode {
  return { name: path.split("/").pop()!, path, type: "directory", children, hasChildren };
}

describe("QuickFileOpen bounded fetch", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("requests an explicit, bounded depth instead of the whole project", async () => {
    apiMocks.getFileTree.mockResolvedValue([file("README.md")]);

    render(<QuickFileOpen isOpen />);

    await waitFor(() => expect(apiMocks.getFileTree).toHaveBeenCalled());
    // Depth is the 4th argument and must be passed explicitly. It is the
    // budget-bounded MAX sentinel (-1), never omitted. A fixed positive depth
    // would silently make deep source files unfindable; the real bound lives on
    // the server (gitignore + skip set + node budget).
    const [path, showHidden, worktreeId, depth] = apiMocks.getFileTree.mock.calls[0];
    expect(path).toBe("/");
    expect(showHidden).toBe(false);
    expect(worktreeId).toBe("wt-1");
    expect(depth).toBe(-1);
  });

  it("caps the candidate set and tells the user the index is partial", async () => {
    apiMocks.getFileTree.mockResolvedValue(
      Array.from({ length: 20001 }, (_, i) => file(`src/file-${i}.ts`))
    );

    render(<QuickFileOpen isOpen />);

    const notice = await screen.findByTestId("quick-open-truncated");
    expect(notice).toHaveTextContent("20,000 files");
    // Only the first page of matches is rendered regardless.
    expect(screen.getAllByText(/^file-\d+\.ts$/)).toHaveLength(20);
  });

  it("tells the user when the walk stopped at the depth boundary", async () => {
    apiMocks.getFileTree.mockResolvedValue([
      file("README.md"),
      // A directory the depth-limited request never descended into: the backend
      // sets has_children but sends no children.
      dir("assets", undefined, true),
    ]);

    render(<QuickFileOpen isOpen />);

    expect(await screen.findByTestId("quick-open-truncated")).toBeInTheDocument();
  });

  it("shows no notice when the whole tree came back", async () => {
    apiMocks.getFileTree.mockResolvedValue([
      file("README.md"),
      dir("src", [file("src/main.ts")], true),
      dir("empty", [], false),
    ]);

    render(<QuickFileOpen isOpen />);

    await screen.findByText("main.ts");
    expect(screen.queryByTestId("quick-open-truncated")).not.toBeInTheDocument();
  });
});

describe("collectFiles", () => {
  it("flattens files in order and skips directory nodes", () => {
    const { files, truncated } = collectFiles([
      dir("src", [file("src/a.ts"), dir("src/deep", [file("src/deep/b.ts")], true)], true),
      file("z.md"),
    ]);

    expect(files.map((f) => f.path)).toEqual(["src/a.ts", "src/deep/b.ts", "z.md"]);
    expect(truncated).toBe(false);
  });

  it("stops at the limit and reports truncation", () => {
    const { files, truncated } = collectFiles(
      Array.from({ length: 10 }, (_, i) => file(`f${i}.ts`)),
      3
    );

    expect(files.map((f) => f.path)).toEqual(["f0.ts", "f1.ts", "f2.ts"]);
    expect(truncated).toBe(true);
  });

  it("reports truncation for an unexplored directory even under the limit", () => {
    const { files, truncated } = collectFiles([dir("assets", undefined, true)], 100);

    expect(files).toEqual([]);
    expect(truncated).toBe(true);
  });

  it("treats a loaded-but-empty directory as fully explored", () => {
    const { truncated } = collectFiles([dir("empty", [], false)], 100);

    expect(truncated).toBe(false);
  });

  it("walks a pathologically deep tree without blowing the stack", () => {
    // A recursive flatten dies here; the iterative one must not.
    let node = file("deep/leaf.ts");
    for (let i = 0; i < 50000; i++) {
      node = dir(`d${i}`, [node], true);
    }

    const { files, truncated } = collectFiles([node], 100);

    expect(files.map((f) => f.path)).toEqual(["deep/leaf.ts"]);
    expect(truncated).toBe(false);
  });
});
