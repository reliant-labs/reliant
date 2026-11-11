import type { ComponentProps } from "react";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { FileNodeType } from "../../gen/reliant/v1/filesystem_pb";
import { setTransport } from "../../lib/connect";
import { mockTransport } from "../../lib/test-utils";
import { serviceHandlers } from "../../test/serviceTransport";
import { renderWithQuery as render } from "../../test/renderWithQuery";
import { FileTree } from "./FileTree";

// Exercise the real tree controls AND fileSystem API, stopping only at the
// transport. Mocking createFile would miss dropped project/chat/worktree scope.
const mocks = vi.hoisted(() => ({
  project: { currentProject: { id: "project-1", name: "Project", path: "/workspace/project" } },
  openFileViewer: vi.fn(),
  getActiveViewer: vi.fn(() => null),
  createFileOrFolder: vi.fn(),
  triggerGitStatusRefresh: vi.fn(),
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
vi.mock("../../store/gitStatusStore", () => ({ triggerGitStatusRefresh: mocks.triggerGitStatusRefresh }));
vi.mock("../../hooks/worktree-queries", () => ({
  useCurrentWorktree: () => null,
  useWorktreeById: () => ({ id: "branch-1", path: "/workspace/branch" }),
}));
vi.mock("../../hooks/useDaemonWait", () => ({
  useDaemonWait: () => ({ state: null, retryNow: vi.fn() }),
}));
vi.mock("../../hooks/settings-queries", () => ({ cachedUserPreferences: () => ({}) }));
vi.mock("../../lib/toast-manager", () => ({ toast: { notify: vi.fn() } }));
vi.mock("./FileOperationsModal", () => ({ FileOperationsModal: () => null }));
// Keep the actual row's menu actions, without testing the menu primitive here.
vi.mock("../forge-ui/context_menu", () => ({
  default: ({ groups }: { groups: { items: { label: string; onClick?: () => void }[] }[] }) => (
    <div>{groups.flatMap(group => group.items).map(item => (
      <button key={item.label} onClick={item.onClick}>{item.label}</button>
    ))}</div>
  ),
}));

function renderTree(props: Partial<ComponentProps<typeof FileTree>>) {
  return render(<FileTree
    searchQuery=""
    onFileSelect={vi.fn()}
    onPathChange={vi.fn()}
    selectedFile={null}
    showHidden={false}
    onRefresh={vi.fn()}
    collapseKey={0}
    {...props}
  />);
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.createFileOrFolder.mockResolvedValue({});
  setTransport(mockTransport(serviceHandlers("FileSystemService", {
    getFileTree: () => ({ files: [{
      name: "docs", path: "docs", type: FileNodeType.DIRECTORY, children: [],
    }] }),
    createFileOrFolder: mocks.createFileOrFolder,
  })));
});

afterEach(() => setTransport(null));

describe.each([
  { scope: "project checkout", worktreeId: undefined },
  { scope: "branch workspace", worktreeId: "branch-1" },
])("file creation in $scope", ({ worktreeId }) => {
  it.each(["file", "folder"] as const)("creates a root %s with a relative RPC path", async (kind) => {
    renderTree({ worktreeId, creatingType: kind, onCreatingComplete: vi.fn() });
    const input = await screen.findByPlaceholderText(kind === "file" ? "filename.ext" : "folder-name");
    const name = kind === "file" ? "strategy_dump.txt" : "strategies";
    fireEvent.change(input, { target: { value: `  ${name}  ` } });
    fireEvent.keyDown(input, { key: "Enter" });

    await waitFor(() => expect(mocks.createFileOrFolder).toHaveBeenCalledTimes(1));
    expect(mocks.createFileOrFolder.mock.calls[0][0]).toMatchObject({
      projectId: "project-1", chatId: "chat-1",
      path: name, type: kind === "file" ? FileNodeType.FILE : FileNodeType.DIRECTORY,
    });
    expect(mocks.createFileOrFolder.mock.calls[0][0].worktreeId).toBe(worktreeId);
    if (kind === "file") {
      await waitFor(() => expect(mocks.openFileViewer).toHaveBeenCalledWith(
        { name, path: name, type: "file" }, "project-1", worktreeId, true,
      ));
    }
    expect(mocks.triggerGitStatusRefresh).toHaveBeenCalledWith(worktreeId, "project-1");
  });

  it.each(["file", "folder"] as const)("creates a nested %s without losing workspace scope", async (kind) => {
    renderTree({ worktreeId });
    fireEvent.contextMenu(await screen.findByRole("button", { name: "docs" }));
    fireEvent.click(await screen.findByRole("button", { name: kind === "file" ? "New File" : "New Folder" }));
    const input = await screen.findByPlaceholderText(kind === "file" ? "filename.ext" : "folder-name");
    const name = kind === "file" ? "strategy_dump.txt" : "strategies";
    fireEvent.change(input, { target: { value: name } });
    fireEvent.keyDown(input, { key: "Enter" });

    await waitFor(() => expect(mocks.createFileOrFolder).toHaveBeenCalledTimes(1));
    expect(mocks.createFileOrFolder.mock.calls[0][0]).toMatchObject({
      projectId: "project-1", chatId: "chat-1",
      path: `docs/${name}`, type: kind === "file" ? FileNodeType.FILE : FileNodeType.DIRECTORY,
    });
    expect(mocks.createFileOrFolder.mock.calls[0][0].worktreeId).toBe(worktreeId);
    if (kind === "file") {
      await waitFor(() => expect(mocks.openFileViewer).toHaveBeenCalledWith(
        { name, path: `docs/${name}`, type: "file" }, "project-1", worktreeId,
      ));
    }
    expect(mocks.triggerGitStatusRefresh).toHaveBeenCalledWith(worktreeId, "project-1");
  });
});
