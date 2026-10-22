import { createRef } from "react";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import { renderWithQuery as render } from "../../test/renderWithQuery";
import { FileTree, type FileTreeHandle } from "./FileTree";
import {
  ProjectCheckoutMissingSchema,
  ProjectCheckoutState,
} from "../../gen/reliant/v1/filesystem_pb";

// MACHINE_LIST_BUGS_2026-10-07, follow-ups:
//   - the owner's forge clone was queued at 15:04:10 and landed at 15:04:18;
//     the project opened immediately and every tree read in between rendered
//     "read dir /home/workspace/projects/forge: … no such file or directory";
//   - houndersclub was not on the owner's new machine at all, and its tree was
//     the same raw 500 on every read, with no way forward.
// The server now answers NOT_FOUND + ProjectCheckoutMissing; the tree must
// render the state, not the text.

const apiMocks = vi.hoisted(() => ({
  getFileTree: vi.fn(),
  createProjectFromRepo: vi.fn(),
}));

vi.mock("../../api/fileSystem", () => ({
  getFileTree: apiMocks.getFileTree,
  createFile: vi.fn(),
  createFolder: vi.fn(),
  deleteFileOrFolder: vi.fn(),
  copyFile: vi.fn(),
  getFileContent: vi.fn(),
  getFilePreviewInfo: vi.fn(),
}));
vi.mock("../../api/project-grpc", () => ({
  projectGrpc: { createProjectFromRepo: apiMocks.createProjectFromRepo },
}));
vi.mock("../../hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({
    daemons: [
      {
        daemonId: "2aab1465",
        name: "default",
        hostname: "ws-ws-2aab1465",
        daemonType: "managed",
        status: 1,
      },
    ],
    activeDaemon: undefined,
    loading: false,
    refresh: vi.fn(),
  }),
}));

const mockProjectStore = { currentProject: { id: "proj-1", name: "houndersclub", path: "/home/workspace/projects/houndersclub" } };
vi.mock("../../store/projectStore", () => ({
  useProjectStore: (selector: (state: typeof mockProjectStore) => unknown) => selector(mockProjectStore),
}));
vi.mock("../../store/viewerStore", () => ({
  useViewerStore: (selector: (state: { openFileViewer: () => void; getActiveViewer: () => null }) => unknown) =>
    selector({ openFileViewer: vi.fn(), getActiveViewer: () => null }),
}));
vi.mock("../../store/fileDeletionStore", () => ({
  useFileDeletionStore: (selector: (state: { addDeletedFile: () => void }) => unknown) =>
    selector({ addDeletedFile: vi.fn() }),
}));
vi.mock("../../lib/toast-manager", () => ({ toast: { notify: vi.fn() } }));
vi.mock("./FileOperationsModal", () => ({ FileOperationsModal: () => null }));

function checkoutMissing(state: ProjectCheckoutState, remoteUrl = "https://github.com/acme/houndersclub.git") {
  const detail = create(ProjectCheckoutMissingSchema, {
    projectId: "proj-1",
    daemonId: "2aab1465",
    path: "/home/workspace/projects/houndersclub",
    state,
    remoteUrl,
  });
  return new ConnectError(
    "project directory /home/workspace/projects/houndersclub does not exist on this machine",
    Code.NotFound,
    undefined,
    [{ desc: ProjectCheckoutMissingSchema, value: detail }],
  );
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
    />,
  );
}

describe("FileTree — the project's directory is not on this machine", () => {
  beforeEach(() => vi.clearAllMocks());

  it("shows a clone still landing as cloning, not as an error", async () => {
    apiMocks.getFileTree.mockRejectedValue(checkoutMissing(ProjectCheckoutState.CLONING));
    renderTree();

    expect(await screen.findByTestId("project-checkout-cloning")).toHaveTextContent(
      "Cloning houndersclub onto default…",
    );
    expect(screen.queryByText(/no such file or directory|does not exist/i)).not.toBeInTheDocument();
  });

  it("offers to clone a project that isn't on this machine, onto this machine", async () => {
    apiMocks.getFileTree.mockRejectedValue(checkoutMissing(ProjectCheckoutState.ABSENT));
    apiMocks.createProjectFromRepo.mockResolvedValue({ queued: true });
    renderTree();

    const panel = await screen.findByTestId("project-checkout-missing");
    expect(panel).toHaveTextContent("houndersclub isn't on default");
    expect(screen.queryByText(/no such file or directory/i)).not.toBeInTheDocument();

    fireEvent.click(screen.getByTestId("project-checkout-clone-here"));
    await waitFor(() =>
      expect(apiMocks.createProjectFromRepo).toHaveBeenCalledWith({
        cloneUrl: "https://github.com/acme/houndersclub.git",
        daemonId: "2aab1465",
        name: "houndersclub",
        path: "/home/workspace/projects/houndersclub",
      }),
    );
  });

  it("explains a plain folder that is missing, with nothing to clone", async () => {
    apiMocks.getFileTree.mockRejectedValue(checkoutMissing(ProjectCheckoutState.ABSENT, ""));
    renderTree();

    const panel = await screen.findByTestId("project-checkout-missing");
    expect(panel).toHaveTextContent("doesn't exist on default");
    expect(screen.queryByTestId("project-checkout-clone-here")).not.toBeInTheDocument();
  });
});
