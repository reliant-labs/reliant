import { Code, ConnectError } from "@connectrpc/connect";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { act } from "react";
import { setTransport } from "../../lib/connect";
import { mockTransport } from "../../lib/test-utils";
import { serviceHandlers } from "../../test/serviceTransport";
import { renderWithQuery as render } from "../../test/renderWithQuery";
import { useFileBrowserScopeStore } from "../../store/fileBrowserScopeStore";
import { MachineBrowser } from "./MachineBrowser";
import { FileScopeSwitcher } from "./FileScopeSwitcher";

// The machine view through the real fileSystem API, stopping only at the
// transport, so each request's path and project/chat scope is what is checked.
const mocks = vi.hoisted(() => ({
  project: { currentProject: { id: "project-1", name: "Project", path: "/home/me/proj" } },
  openFileViewer: vi.fn(),
  listMachineDirectory: vi.fn(),
}));

vi.mock("../../store/projectStore", () => ({
  useProjectStore: Object.assign(
    (selector: (state: typeof mocks.project) => unknown) => selector(mocks.project),
    { getState: () => mocks.project },
  ),
}));
vi.mock("../../store/viewerStore", () => ({
  useViewerStore: (selector: (s: { openFileViewer: typeof mocks.openFileViewer }) => unknown) =>
    selector({ openFileViewer: mocks.openFileViewer }),
}));
vi.mock("../../store/chatStore", () => ({
  useChatStore: { getState: () => ({ activeChatId: "chat-1" }) },
}));
vi.mock("../../lib/toast-manager", () => ({ toast: { notify: vi.fn(), warning: vi.fn() } }));

const reason = (r: string) => new Headers({ "x-forge-error-reason": r });

/** A machine with a home, a scratch directory and a project. */
function listing(path: string) {
  const dirs: Record<string, Array<{ name: string; isDirectory?: boolean; isSymlink?: boolean }>> = {
    "/home/me": [{ name: "proj", isDirectory: true }, { name: ".reliant", isDirectory: true }, { name: "notes.md" }],
    "/home/me/.reliant/scratch/chat-1": [{ name: "summary.csv" }, { name: "runs", isDirectory: true }],
    "/": [{ name: "home", isDirectory: true }, { name: "tmp", isDirectory: true }],
    "/home": [{ name: "me", isDirectory: true }],
    "/tmp": [{ name: "report.csv" }, { name: "latest", isDirectory: true, isSymlink: true }],
  };
  const entries = dirs[path];
  if (!entries) throw new ConnectError("open: no such file or directory", Code.NotFound);
  return {
    path,
    homeDir: "/home/me",
    workspaceRoot: "/home/me/proj",
    truncated: false,
    entries: entries.map((e) => ({
      name: e.name, path: `${path === "/" ? "" : path}/${e.name}`, isDirectory: !!e.isDirectory, isSymlink: !!e.isSymlink, size: 1n, modified: "",
    })),
  };
}

function machineAnswers(req: { path: string }) {
  const resolved: Record<string, string> = { "": "/home/me", "~/.reliant/scratch/chat-1": "/home/me/.reliant/scratch/chat-1", "..": "/home/me" };
  const path = resolved[req.path] ?? req.path;
  if (path === "/tmp/report.csv") throw new ConnectError("path is not a directory", Code.FailedPrecondition, reason("not_a_directory"));
  return listing(path);
}

function requestedPaths(): string[] {
  return mocks.listMachineDirectory.mock.calls.map((c) => c[0].path);
}

beforeEach(() => {
  vi.clearAllMocks();
  useFileBrowserScopeStore.getState().reset();
  mocks.listMachineDirectory.mockImplementation(machineAnswers);
  setTransport(mockTransport(serviceHandlers("FileSystemService", { listMachineDirectory: mocks.listMachineDirectory })));
});

afterEach(() => setTransport(null));

describe("Machine scope", () => {
  it("starts at the home directory, hides dotfiles, and descends into a folder", async () => {
    render(<MachineBrowser scope="machine" chatId="chat-1" showHidden={false} />);

    expect(await screen.findByRole("button", { name: /proj/ })).toBeTruthy();
    expect(screen.queryByText(".reliant")).toBeNull();
    expect(mocks.listMachineDirectory.mock.calls[0][0]).toMatchObject({ projectId: "project-1", chatId: "chat-1", path: "" });

    fireEvent.click(screen.getByRole("button", { name: /proj/ }));
    await waitFor(() => expect(requestedPaths()).toContain("/home/me/proj"));
  });

  it("climbs above home to / through the breadcrumb, and opens a file there read-only in the viewer", async () => {
    render(<MachineBrowser scope="machine" chatId="chat-1" showHidden={false} />);
    await screen.findByRole("button", { name: /notes\.md/ });

    fireEvent.click(screen.getByRole("button", { name: "/" }));
    fireEvent.click(await screen.findByRole("button", { name: /tmp/ }));
    fireEvent.click(await screen.findByRole("button", { name: /report\.csv/ }));

    expect(mocks.openFileViewer).toHaveBeenCalledWith(
      expect.objectContaining({ name: "report.csv", path: "/tmp/report.csv", type: "file" }), "project-1", undefined, false);
    expect(screen.getByLabelText("symbolic link")).toBeTruthy();
  });

  it("Go to path opens a pasted file path in the viewer and shows its folder", async () => {
    render(<MachineBrowser scope="machine" chatId="chat-1" showHidden={false} />);
    await screen.findByRole("button", { name: /notes\.md/ });

    fireEvent.change(screen.getByLabelText("Go to path"), { target: { value: "/tmp/report.csv" } });
    fireEvent.submit(screen.getByLabelText("Go to path").closest("form")!);

    await waitFor(() => expect(mocks.openFileViewer).toHaveBeenCalledWith(
      expect.objectContaining({ path: "/tmp/report.csv" }), "project-1", undefined, false));
    await waitFor(() => expect(useFileBrowserScopeStore.getState().paths.machine).toBe("/tmp"));
  });

  it("tells the user to update an old machine instead of failing", async () => {
    mocks.listMachineDirectory.mockRejectedValue(new ConnectError(
      "this machine's Reliant is too old to browse the whole machine: update it", Code.FailedPrecondition, reason("machine_browse_unsupported")));
    render(<MachineBrowser scope="machine" chatId="chat-1" showHidden={false} />);
    expect(await screen.findByText("Update Reliant on this machine to browse the whole machine.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
  });
});

describe("Chat scratch scope", () => {
  it("lists this chat's scratch directory", async () => {
    render(<MachineBrowser scope="scratch" chatId="chat-1" showHidden={false} />);
    expect(await screen.findByRole("button", { name: /summary\.csv/ })).toBeTruthy();
    expect(requestedPaths()[0]).toBe("~/.reliant/scratch/chat-1");
  });

  it("says when the chat has no scratch directory yet", async () => {
    mocks.listMachineDirectory.mockRejectedValue(new ConnectError("path not found on this machine", Code.NotFound));
    render(<MachineBrowser scope="scratch" chatId="chat-1" showHidden={false} />);
    expect(await screen.findByText("This chat hasn't written any scratch files yet.")).toBeTruthy();
  });
});

describe("FileScopeSwitcher", () => {
  it("switches scopes, and pops out of the project to the folder it is in", () => {
    render(<FileScopeSwitcher hasChat />);
    fireEvent.click(screen.getByRole("radio", { name: "Scratch" }));
    expect(useFileBrowserScopeStore.getState().scope).toBe("scratch");
    fireEvent.click(screen.getByRole("radio", { name: "Project" }));

    fireEvent.click(screen.getByRole("button", { name: "Show the folder this project is in" }));
    const s = useFileBrowserScopeStore.getState();
    expect(s.scope).toBe("machine");
    expect(s.paths.machine).toBe("..");
  });

  it("offers no scratch scope without a chat", () => {
    render(<FileScopeSwitcher hasChat={false} />);
    expect((screen.getByRole("radio", { name: "Scratch" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("highlights the project's own folder after climbing above it", async () => {
    act(() => useFileBrowserScopeStore.getState().browseMachine(".."));
    render(<MachineBrowser scope="machine" chatId="chat-1" showHidden={false} />);
    const row = await screen.findByRole("button", { name: /proj/ });
    expect(row.className).toContain("bg-foreground/5");
    expect(requestedPaths()[0]).toBe("..");
  });
});
