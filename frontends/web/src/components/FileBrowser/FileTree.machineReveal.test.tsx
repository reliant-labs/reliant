import { fireEvent, screen } from "@testing-library/react";
import { FileNodeType } from "../../gen/reliant/v1/filesystem_pb";
import { setTransport } from "../../lib/connect";
import { mockTransport } from "../../lib/test-utils";
import { serviceHandlers } from "../../test/serviceTransport";
import { renderWithQuery as render } from "../../test/renderWithQuery";
import { FileTree } from "./FileTree";
import { useFileBrowserScopeStore } from "../../store/fileBrowserScopeStore";

// "Reveal in Machine View" from the project tree: the pop-out to the machine
// view at the folder that holds the entry, which it highlights.
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

function renderTree() {
  return render(<FileTree
    searchQuery=""
    onFileSelect={vi.fn()}
    onPathChange={vi.fn()}
    selectedFile={null}
    showHidden={false}
    onRefresh={vi.fn()}
    collapseKey={0}
    expandedPaths={new Set(["docs"])}
    onExpandedPathsChange={vi.fn()}
  />);
}

beforeEach(() => {
  vi.clearAllMocks();
  useFileBrowserScopeStore.getState().reset();
  setTransport(mockTransport(serviceHandlers("FileSystemService", {
    getFileTree: () => ({ files: [
      { name: "docs", path: "docs", type: FileNodeType.DIRECTORY, children: [
        { name: "guide.md", path: "docs/guide.md", type: FileNodeType.FILE },
      ] },
      { name: "report.xlsx", path: "report.xlsx", type: FileNodeType.FILE },
    ] }),
  })));
});

afterEach(() => setTransport(null));

it.each([
  { name: "guide.md", folder: "docs" },
  { name: "report.xlsx", folder: "." },
  { name: "docs", folder: "." },
])("reveals $name in the machine view at $folder", async ({ name, folder }) => {
  renderTree();
  fireEvent.contextMenu(await screen.findByRole("button", { name }));
  fireEvent.click(await screen.findByRole("button", { name: "Reveal in Machine View" }));

  const s = useFileBrowserScopeStore.getState();
  expect(s.scope).toBe("machine");
  expect(s.paths.machine).toBe(folder);
  expect(s.highlight).toBe(name);
});
