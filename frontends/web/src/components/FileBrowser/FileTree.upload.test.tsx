import { createRef, type ComponentProps } from "react";
import { fireEvent, render as plainRender, screen, waitFor } from "@testing-library/react";
import { FileNodeType } from "../../gen/reliant/v1/filesystem_pb";
import { setTransport } from "../../lib/connect";
import { mockTransport } from "../../lib/test-utils";
import { serviceHandlers } from "../../test/serviceTransport";
import { renderWithQuery as render } from "../../test/renderWithQuery";
import { useFileUploadStore } from "../../store/fileUploadStore";
import { FileTree, type FileTreeHandle } from "./FileTree";
import { FileTreeToolbar } from "./FileTreeToolbar";

// The real tree and its row menus; the upload queue's enqueue is replaced so
// the test sees exactly which folder each entry point targets, and with what
// scope.
const mocks = vi.hoisted(() => ({
  project: { currentProject: { id: "project-1", name: "Project", path: "/workspace/project" } },
  openFileViewer: vi.fn(),
  getActiveViewer: vi.fn(() => null),
  enqueue: vi.fn(),
  notify: vi.fn(),
}));

vi.mock("../../store/projectStore", () => ({
  useProjectStore: Object.assign(
    (selector: (state: typeof mocks.project) => unknown) => selector(mocks.project),
    { getState: () => mocks.project },
  ),
}));
vi.mock("../../store/viewerStore", () => ({
  useViewerStore: (selector?: (state: typeof mocks) => unknown) => selector ? selector(mocks) : mocks,
}));
vi.mock("../../store/chatStore", () => ({
  useChatStore: { getState: () => ({ activeChatId: "chat-1" }) },
}));
vi.mock("../../store/gitStatusStore", () => ({ triggerGitStatusRefresh: vi.fn() }));
vi.mock("../../hooks/worktree-queries", () => ({
  useCurrentWorktree: () => null,
  useWorktreeById: () => ({ id: "branch-1", path: "/workspace/branch" }),
}));
vi.mock("../../hooks/useDaemonWait", () => ({
  useDaemonWait: () => ({ state: null, retryNow: vi.fn() }),
}));
vi.mock("../../hooks/settings-queries", () => ({ cachedUserPreferences: () => ({}) }));
vi.mock("../../lib/toast-manager", () => ({ toast: { notify: mocks.notify } }));
vi.mock("./FileOperationsModal", () => ({ FileOperationsModal: () => null }));
vi.mock("../forge-ui/context_menu", () => ({
  default: ({ groups }: { groups: { items: { label: string; onClick?: () => void }[] }[] }) => (
    <div>{groups.flatMap(group => group.items).map(item => (
      <button key={item.label} onClick={item.onClick}>{item.label}</button>
    ))}</div>
  ),
}));

const scope = { projectId: "project-1", worktreeId: "branch-1", chatId: "chat-1" };

function renderTree(props: Partial<ComponentProps<typeof FileTree>> = {}, ref?: React.Ref<FileTreeHandle>) {
  return render(<FileTree
    ref={ref}
    searchQuery=""
    onFileSelect={vi.fn()}
    onPathChange={vi.fn()}
    selectedFile={null}
    showHidden={false}
    onRefresh={vi.fn()}
    collapseKey={0}
    worktreeId="branch-1"
    enableUploads
    {...props}
  />);
}

function fileOf(name: string): File {
  return new File(["hello"], name);
}

// What a browser's drop carries: one item per dragged thing, a folder telling
// itself apart through webkitGetAsEntry().
function filesDrag(entries: Array<{ file: File; directory?: boolean }>) {
  return {
    types: ["Files"],
    files: entries.filter(e => !e.directory).map(e => e.file),
    items: entries.map(({ file, directory }) => ({
      kind: "file",
      getAsFile: () => file,
      webkitGetAsEntry: () => ({ isDirectory: !!directory, name: file.name }),
    })),
    dropEffect: "none",
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  useFileUploadStore.setState({ enqueue: mocks.enqueue });
  setTransport(mockTransport(serviceHandlers("FileSystemService", {
    getFileTree: () => ({ files: [
      { name: "docs", path: "docs", type: FileNodeType.DIRECTORY, hasChildren: true, children: [
        { name: "readme.md", path: "docs/readme.md", type: FileNodeType.FILE },
      ] },
      { name: "a.txt", path: "a.txt", type: FileNodeType.FILE },
    ] }),
  })));
});

afterEach(() => setTransport(null));

describe("uploading from the file tree", () => {
  it("“Upload Files Here…” on a folder uploads the picked files into it", async () => {
    const { container } = renderTree();
    fireEvent.contextMenu(await screen.findByRole("button", { name: "docs" }));
    fireEvent.click(await screen.findByRole("button", { name: "Upload Files Here…" }));

    const input = container.querySelector<HTMLInputElement>('[data-testid="file-tree-upload-input"]')!;
    const file = fileOf("notes.pdf");
    fireEvent.change(input, { target: { files: [file] } });

    await waitFor(() => expect(mocks.enqueue).toHaveBeenCalledWith([file], "docs", scope));
  });

  it("offers no upload where uploads are off", async () => {
    renderTree({ enableUploads: false });
    fireEvent.contextMenu(await screen.findByRole("button", { name: "docs" }));
    await screen.findByRole("button", { name: "New Folder" });
    expect(screen.queryByRole("button", { name: "Upload Files Here…" })).toBeNull();
  });

  it("dropping OS files on a folder row uploads into that folder, highlighting it while over it", async () => {
    renderTree();
    const row = await screen.findByRole("button", { name: "docs" });
    const file = fileOf("photo.png");
    const dataTransfer = filesDrag([{ file }]);

    expect(fireEvent.dragOver(row, { dataTransfer })).toBe(false); // accepted
    expect(row.closest("[data-drop-target]")).toHaveAttribute("data-drop-target", "true");

    fireEvent.drop(row, { dataTransfer });
    await waitFor(() => expect(mocks.enqueue).toHaveBeenCalledWith([file], "docs", scope));
    expect(document.querySelector("[data-drop-target]")).toBeNull();
  });

  it("a drop on a file row goes to its folder, and on the background to the root", async () => {
    renderTree({ expandedPaths: new Set(["docs"]), onExpandedPathsChange: vi.fn() });
    const nested = fileOf("nested.txt");
    fireEvent.drop(await screen.findByRole("button", { name: "readme.md" }), { dataTransfer: filesDrag([{ file: nested }]) });
    await waitFor(() => expect(mocks.enqueue).toHaveBeenCalledWith([nested], "docs", scope));

    const top = fileOf("top.txt");
    fireEvent.drop(screen.getByRole("button", { name: "a.txt" }), { dataTransfer: filesDrag([{ file: top }]) });
    await waitFor(() => expect(mocks.enqueue).toHaveBeenCalledWith([top], "", scope));

    const bg = fileOf("bg.txt");
    fireEvent.drop(screen.getByTestId("file-tree"), { dataTransfer: filesDrag([{ file: bg }]) });
    await waitFor(() => expect(mocks.enqueue).toHaveBeenCalledWith([bg], "", scope));
  });

  it("skips dropped folders and says so", async () => {
    renderTree();
    const file = fileOf("keep.txt");
    fireEvent.drop(await screen.findByRole("button", { name: "docs" }), {
      dataTransfer: filesDrag([{ file }, { file: fileOf("assets"), directory: true }]),
    });
    await waitFor(() => expect(mocks.enqueue).toHaveBeenCalledWith([file], "docs", scope));
    expect(mocks.notify).toHaveBeenCalledWith("Folder not uploaded", expect.objectContaining({
      description: expect.stringContaining("“assets”"),
    }));
  });

  it("ignores drags that carry no files", async () => {
    renderTree();
    const row = await screen.findByRole("button", { name: "docs" });
    const dataTransfer = { types: ["text/plain"], files: [], items: [] };
    expect(fireEvent.dragOver(row, { dataTransfer })).toBe(true); // not taken
    fireEvent.drop(row, { dataTransfer });
    expect(document.querySelector("[data-drop-target]")).toBeNull();
    expect(mocks.enqueue).not.toHaveBeenCalled();
  });

  it.each([
    ["a focused folder", "docs", "docs"],
    ["a focused file's folder", "docs/readme.md", "docs"],
    ["the root with nothing focused", null, ""],
  ])("the toolbar's upload targets %s", async (_, focusedPath, directory) => {
    const ref = createRef<FileTreeHandle>();
    renderTree({ focusedPath, onFocusedPathChange: vi.fn() }, ref);
    await screen.findByRole("button", { name: "docs" });
    const file = fileOf("pick.txt");
    ref.current!.uploadFiles([file]);
    await waitFor(() => expect(mocks.enqueue).toHaveBeenCalledWith([file], directory, scope));
  });
});

describe("FileTreeToolbar", () => {
  it("has an Upload Files button whose picker hands the files over", () => {
    const onUploadFiles = vi.fn();
    const { container } = plainRender(
      <FileTreeToolbar onNewFile={vi.fn()} onNewFolder={vi.fn()} onRefresh={vi.fn()} onCollapseAll={vi.fn()} onUploadFiles={onUploadFiles} />,
    );
    const input = container.querySelector<HTMLInputElement>('[data-testid="toolbar-upload-input"]')!;
    const click = vi.spyOn(input, "click");
    fireEvent.click(screen.getByRole("button", { name: "Upload Files" }));
    expect(click).toHaveBeenCalled();
    expect(input).toHaveAttribute("multiple");

    const files = [fileOf("one.txt"), fileOf("two.txt")];
    fireEvent.change(input, { target: { files } });
    expect(onUploadFiles).toHaveBeenCalledWith(files);
  });

  it("has no Upload button without a handler", () => {
    plainRender(<FileTreeToolbar onNewFile={vi.fn()} onNewFolder={vi.fn()} onRefresh={vi.fn()} onCollapseAll={vi.fn()} />);
    expect(screen.queryByRole("button", { name: "Upload Files" })).toBeNull();
  });
});
