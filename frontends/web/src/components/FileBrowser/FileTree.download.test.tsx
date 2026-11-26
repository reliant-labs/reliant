import { Code, ConnectError } from "@connectrpc/connect";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { FileNodeType, FolderArchiveState } from "../../gen/reliant/v1/filesystem_pb";
import { useFileDownloadStore } from "../../store/fileDownloadStore";
import { setTransport } from "../../lib/connect";
import { mockTransport } from "../../lib/test-utils";
import { serviceHandlers } from "../../test/serviceTransport";
import { renderWithQuery as render } from "../../test/renderWithQuery";
import { FileTree } from "./FileTree";
import { downloadWorkspaceFile, downloadWorkspaceFolder } from "./downloadWorkspaceFile";

// The tree's "Download" through the real fileSystem API, stopping only at the
// transport, so the request's project/chat/worktree scope is what is checked.
const mocks = vi.hoisted(() => ({
  chat: { activeChatId: "chat-1" },
  project: { currentProject: { id: "project-1", name: "Project", path: "/workspace/project" } },
  downloadFile: vi.fn(),
  readFileRange: vi.fn(),
  prepareFolderArchive: vi.fn(),
  getFolderArchiveStatus: vi.fn(),
  releaseFolderArchive: vi.fn((_req: unknown) => ({})),
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
  useChatStore: { getState: () => mocks.chat },
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

function renderTree(worktreeId?: string, enableUploads = false) {
  return render(<FileTree
    enableUploads={enableUploads}
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
  mocks.project.currentProject.id = "project-1";
  mocks.chat.activeChatId = "chat-1";
  setTransport(mockTransport(serviceHandlers("FileSystemService", {
    getFileTree: () => ({ files: [
      { name: "docs", path: "docs", type: FileNodeType.DIRECTORY, children: [] },
      { name: "report.xlsx", path: "report.xlsx", type: FileNodeType.FILE, size: 5n },
      { name: "movie.mov", path: "movie.mov", type: FileNodeType.FILE, size: 40n * 1024n * 1024n },
    ] }),
    downloadFile: mocks.downloadFile,
    readFileRange: mocks.readFileRange,
    prepareFolderArchive: mocks.prepareFolderArchive,
    getFolderArchiveStatus: mocks.getFolderArchiveStatus,
    releaseFolderArchive: mocks.releaseFolderArchive,
  })));
  useFileDownloadStore.setState({ items: [] });
});

afterEach(() => {
  setTransport(null);
  delete (window as { showSaveFilePicker?: unknown }).showSaveFilePicker;
});

// A machine serving `bytes` in pieces of 3 for ReadFileRange.
function servePieces(bytes: Uint8Array, filename: string) {
  mocks.readFileRange.mockImplementation((req: { offset: bigint }) => {
    const start = Number(req.offset);
    const data = bytes.slice(start, start + 3);
    return {
      data, totalSize: BigInt(bytes.length), version: "v1", filename,
      contentType: "application/octet-stream", eof: start + data.length >= bytes.length,
    };
  });
}

function fakeSaveFilePicker() {
  const written: number[] = [];
  const writable = {
    write: vi.fn(async (chunk: Uint8Array) => { written.push(...chunk); }),
    close: vi.fn(async () => undefined),
    abort: vi.fn(async () => undefined),
  };
  const picker = vi.fn(async (_opts: { suggestedName?: string }) => ({ createWritable: async () => writable }));
  (window as { showSaveFilePicker?: unknown }).showSaveFilePicker = picker;
  return { picker, writable, written };
}

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

describe("a file over 25 MB", () => {
  it("streams into the file picked in the click, with a row in the transfers panel", async () => {
    const bytes = new Uint8Array([1, 2, 3, 4, 5, 6, 7]);
    servePieces(bytes, "movie.mov");
    const { picker, writable, written } = fakeSaveFilePicker();
    renderTree("branch-1");
    await downloadFromMenu("movie.mov");

    expect(picker).toHaveBeenCalledWith({ suggestedName: "movie.mov" });
    await waitFor(() => expect(writable.close).toHaveBeenCalled());
    expect(new Uint8Array(written)).toEqual(bytes);
    expect(mocks.downloadFile).not.toHaveBeenCalled();
    expect(mocks.readFileRange.mock.calls[0][0]).toMatchObject({
      projectId: "project-1", chatId: "chat-1", worktreeId: "branch-1", path: "movie.mov",
    });
    expect(useFileDownloadStore.getState().items).toEqual([
      expect.objectContaining({ name: "movie.mov", status: "done", bytesDone: 7 }),
    ]);
  });

  it("does nothing when the save picker is closed", async () => {
    const { picker } = fakeSaveFilePicker();
    picker.mockRejectedValueOnce(new DOMException("closed", "AbortError"));
    renderTree();
    await downloadFromMenu("movie.mov");
    await waitFor(() => expect(picker).toHaveBeenCalled());
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(mocks.readFileRange).not.toHaveBeenCalled();
    expect(useFileDownloadStore.getState().items).toEqual([]);
    expect(mocks.toast.error).not.toHaveBeenCalled();
  });

  it("downloads through the browser where there is no save picker", async () => {
    const bytes = new Uint8Array([9, 8, 7, 6]);
    servePieces(bytes, "movie.mov");
    renderTree();
    await downloadFromMenu("movie.mov");
    await waitFor(() => expect(mocks.saveBlob).toHaveBeenCalledTimes(1));
    expect(mocks.saveBlob.mock.calls[0][1]).toBe("movie.mov");
  });
});

it.each([false, true])("pins the source before the save picker resolves (folder=%s)", async (folder) => {
  const { picker, writable } = fakeSaveFilePicker();
  let resolvePicker!: (handle: { createWritable: () => Promise<typeof writable> }) => void;
  picker.mockImplementationOnce(() => new Promise((resolve) => { resolvePicker = resolve; }));
  servePieces(new Uint8Array([1]), "docs.zip");
  mocks.prepareFolderArchive.mockResolvedValue({ archiveId: "ar-1" });
  mocks.getFolderArchiveStatus.mockResolvedValue({ state: FolderArchiveState.READY, skipped: 0n });
  const transfer = folder
    ? downloadWorkspaceFolder("docs", "docs", "branch-1")
    : downloadWorkspaceFile("movie.mov", "movie.mov", "branch-1");
  mocks.project.currentProject.id = "project-2";
  mocks.chat.activeChatId = "chat-2";
  resolvePicker({ createWritable: async () => writable });
  await transfer;
  expect(writable.close).toHaveBeenCalledOnce();
  expect(mocks.readFileRange.mock.calls[0][0]).toMatchObject({ projectId: "project-1", chatId: "chat-1", worktreeId: "branch-1" });
  if (folder) expect(mocks.prepareFolderArchive.mock.calls[0][0]).toMatchObject({ projectId: "project-1", chatId: "chat-1" });
});

describe("Download as .zip", () => {
  async function zipFromMenu() {
    fireEvent.contextMenu(await screen.findByRole("button", { name: "docs" }));
    fireEvent.click(await screen.findByRole("button", { name: "Download as .zip" }));
  }

  it("zips the folder, polls until it is ready, reads the archive and releases it", async () => {
    mocks.prepareFolderArchive.mockReturnValue({
      archiveId: "ar-1", filename: "docs.zip", totalFiles: 2n, totalBytes: 9n, maxSize: 1n << 31n,
    });
    mocks.getFolderArchiveStatus.mockReturnValue({
      state: FolderArchiveState.READY, filesDone: 2n, filesTotal: 2n, bytesDone: 9n, bytesTotal: 9n,
      archiveSize: 5n, error: "", skipped: 0n,
    });
    const zip = new Uint8Array([0x50, 0x4b, 5, 6, 7]);
    servePieces(zip, "docs.zip");
    const { picker, written, writable } = fakeSaveFilePicker();
    renderTree("branch-1", true);
    await zipFromMenu();

    expect(picker).toHaveBeenCalledWith({ suggestedName: "docs.zip" });
    await waitFor(() => expect(mocks.releaseFolderArchive).toHaveBeenCalledTimes(1));
    expect(mocks.prepareFolderArchive.mock.calls[0][0]).toMatchObject({
      projectId: "project-1", chatId: "chat-1", worktreeId: "branch-1", path: "docs",
    });
    expect(mocks.getFolderArchiveStatus.mock.calls[0][0]).toMatchObject({ archiveId: "ar-1" });
    expect(mocks.readFileRange.mock.calls[0][0]).toMatchObject({ archiveId: "ar-1" });
    expect(mocks.releaseFolderArchive.mock.calls[0][0]).toMatchObject({ archiveId: "ar-1", worktreeId: "branch-1" });
    expect(writable.close).toHaveBeenCalled();
    expect(new Uint8Array(written)).toEqual(zip);
    await waitFor(() => expect(useFileDownloadStore.getState().items).toEqual([
      expect.objectContaining({ name: "docs.zip", kind: "folder", status: "done" }),
    ]));
  });

  it("releases the archive and shows why when zipping fails", async () => {
    mocks.prepareFolderArchive.mockReturnValue({
      archiveId: "ar-2", filename: "docs.zip", totalFiles: 2n, totalBytes: 9n, maxSize: 1n << 31n,
    });
    mocks.getFolderArchiveStatus.mockReturnValue({
      state: FolderArchiveState.FAILED, filesDone: 1n, filesTotal: 2n, bytesDone: 0n, bytesTotal: 9n,
      archiveSize: 0n, error: "the machine's disk is full", skipped: 0n,
    });
    renderTree(undefined, true);
    await zipFromMenu();

    await waitFor(() => expect(mocks.releaseFolderArchive).toHaveBeenCalledTimes(1));
    expect(mocks.releaseFolderArchive.mock.calls[0][0]).toMatchObject({ archiveId: "ar-2" });
    expect(mocks.readFileRange).not.toHaveBeenCalled();
    await waitFor(() => expect(useFileDownloadStore.getState().items).toEqual([
      expect.objectContaining({ status: "failed", error: "the machine's disk is full" }),
    ]));
    expect(mocks.toast.error).toHaveBeenCalledWith("Couldn't download docs.zip", expect.objectContaining({
      description: "the machine's disk is full",
    }));
    expect(mocks.saveBlob).not.toHaveBeenCalled();
  });

  it("is offered only where the transfers panel is", async () => {
    renderTree();
    fireEvent.contextMenu(await screen.findByRole("button", { name: "docs" }));
    await screen.findByRole("button", { name: "New File" });
    expect(screen.queryByRole("button", { name: "Download as .zip" })).toBeNull();
  });
});
