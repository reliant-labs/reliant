import { Code, ConnectError } from "@connectrpc/connect";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { FileNodeType } from "../../gen/reliant/v1/filesystem_pb";
import { setTransport } from "../../lib/connect";
import { mockTransport } from "../../lib/test-utils";
import { serviceHandlers } from "../../test/serviceTransport";
import { renderWithQuery as render } from "../../test/renderWithQuery";
import { FileTree } from "./FileTree";

// The tree's "Download" through the real fileSystem API, stopping only at the
// transport, so the request's project/chat/worktree scope is what is checked.
const mocks = vi.hoisted(() => ({
  project: { currentProject: { id: "project-1", name: "Project", path: "/workspace/project" } },
  downloadFile: vi.fn(),
  saveBlob: vi.fn(),
  toast: { loading: vi.fn(), dismiss: vi.fn(), error: vi.fn() },
}));

vi.mock("../../store/projectStore", () => ({
  useProjectStore: Object.assign(
    (selector: (state: typeof mocks.project) => unknown) => selector(mocks.project),
    { getState: () => mocks.project },
  ),
}));
vi.mock("../../store/viewerStore", () => ({
  useViewerStore: () => ({ openFileViewer: vi.fn(), getActiveViewer: vi.fn(() => null) }),
}));
vi.mock("../../store/chatStore", () => ({
  useChatStore: { getState: () => ({ activeChatId: "chat-1" }) },
}));
vi.mock("../../hooks/worktree-queries", () => ({
  useCurrentWorktree: () => null,
  useWorktreeById: () => ({ id: "branch-1", path: "/workspace/branch" }),
  resolveWorktree: async () => ({ id: "branch-1", path: "/workspace/branch" }),
}));
vi.mock("../../hooks/useDaemonWait", () => ({
  useDaemonWait: () => ({ state: null, retryNow: vi.fn() }),
}));
vi.mock("../../hooks/settings-queries", () => ({ cachedUserPreferences: () => ({}) }));
vi.mock("../../lib/toast-manager", () => ({ toast: { notify: vi.fn() } }));
vi.mock("../../lib/saveBlob", () => ({ saveBlob: mocks.saveBlob }));
vi.mock("sonner", () => ({ toast: mocks.toast }));
vi.mock("./FileOperationsModal", () => ({ FileOperationsModal: () => null }));
// Keep the actual row's menu actions, without testing the menu primitive here.
vi.mock("../forge-ui/context_menu", () => ({
  default: ({ groups }: { groups: { items: { label: string; onClick?: () => void }[] }[] }) => (
    <div>{groups.flatMap(group => group.items).map(item => (
      <button key={item.label} onClick={item.onClick}>{item.label}</button>
    ))}</div>
  ),
}));

function renderTree(worktreeId?: string) {
  return render(<FileTree
    searchQuery=""
    onFileSelect={vi.fn()}
    onPathChange={vi.fn()}
    selectedFile={null}
    showHidden={false}
    onRefresh={vi.fn()}
    collapseKey={0}
    worktreeId={worktreeId}
  />);
}

async function downloadFromMenu(name: string) {
  fireEvent.contextMenu(await screen.findByRole("button", { name }));
  fireEvent.click(await screen.findByRole("button", { name: "Download" }));
}

beforeEach(() => {
  vi.clearAllMocks();
  setTransport(mockTransport(serviceHandlers("FileSystemService", {
    getFileTree: () => ({ files: [
      { name: "docs", path: "docs", type: FileNodeType.DIRECTORY, children: [] },
      { name: "report.xlsx", path: "report.xlsx", type: FileNodeType.FILE },
    ] }),
    downloadFile: mocks.downloadFile,
  })));
});

afterEach(() => setTransport(null));

describe.each([
  { scope: "project checkout", worktreeId: undefined },
  { scope: "branch workspace", worktreeId: "branch-1" },
])("Download in $scope", ({ worktreeId }) => {
  it("saves the file's bytes under its name", async () => {
    const bytes = new Uint8Array([0x50, 0x4b, 0x03, 0x04, 0xff]);
    mocks.downloadFile.mockResolvedValue({
      content: bytes, contentType: "application/vnd.ms-excel", filename: "report.xlsx", size: BigInt(bytes.length),
    });
    renderTree(worktreeId);
    await downloadFromMenu("report.xlsx");

    await waitFor(() => expect(mocks.saveBlob).toHaveBeenCalledTimes(1));
    expect(mocks.downloadFile.mock.calls[0][0]).toMatchObject({
      projectId: "project-1", chatId: "chat-1", path: "report.xlsx",
    });
    expect(mocks.downloadFile.mock.calls[0][0].worktreeId).toBe(worktreeId);
    const [blob, filename] = mocks.saveBlob.mock.calls[0] as [Blob, string];
    expect(filename).toBe("report.xlsx");
    expect(blob.type).toBe("application/vnd.ms-excel");
    // jsdom's Blob has no arrayBuffer(); FileReader reads it.
    const saved = await new Promise<ArrayBuffer>((resolve) => {
      const reader = new FileReader();
      reader.onload = () => resolve(reader.result as ArrayBuffer);
      reader.readAsArrayBuffer(blob);
    });
    expect(new Uint8Array(saved)).toEqual(bytes);
    expect(mocks.toast.dismiss).toHaveBeenCalled();
    expect(mocks.toast.error).not.toHaveBeenCalled();
  });
});

it("toasts the server's reason when the download is refused", async () => {
  mocks.downloadFile.mockRejectedValue(new ConnectError(
    "file is larger than 25 MB (it is 40 MB); large downloads are coming", Code.FailedPrecondition));
  renderTree();
  await downloadFromMenu("report.xlsx");

  await waitFor(() => expect(mocks.toast.error).toHaveBeenCalledTimes(1));
  expect(mocks.toast.error).toHaveBeenCalledWith("Couldn't download report.xlsx", expect.objectContaining({
    description: "file is larger than 25 MB (it is 40 MB); large downloads are coming",
  }));
  expect(mocks.saveBlob).not.toHaveBeenCalled();
});

it("offers Download on files only", async () => {
  renderTree();
  fireEvent.contextMenu(await screen.findByRole("button", { name: "docs" }));
  await screen.findByRole("button", { name: "New File" });
  expect(screen.queryByRole("button", { name: "Download" })).toBeNull();
});
