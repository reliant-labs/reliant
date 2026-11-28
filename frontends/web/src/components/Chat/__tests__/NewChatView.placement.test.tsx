/**
 * The new chat never starts on a machine its project is not set up on.
 *
 * The owner's report (2026-10-31): pick a project, pick a machine, and the
 * page says "temp isn't on cicd" only afterwards. StartChat refuses a machine
 * without the project's checkout (daemonchoice.Validate), and a chat that got
 * past it opened on a file tree with nothing in it. Now:
 *
 *   - "Runs on" defaults to a machine the project is on (as before),
 *   - a machine without it is set up from the picker, which then chooses it,
 *   - and while the clone lands the composer waits and says so.
 */
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ProjectInstallState, ProjectMachineMoveCheck } from "@/gen/reliant/v1/project_pb";

const daemonState = vi.hoisted(() => ({
  current: {
    activeDaemon: undefined as { daemonId: string; hostname: string; status: number } | undefined,
    daemons: [] as Array<{ daemonId: string; hostname: string; status: number }>,
    loading: false,
  },
}));
const startChat = vi.hoisted(() => vi.fn(async () => ({ id: "chat-new" })));
const installState = vi.hoisted(() => ({
  rows: [] as Array<{ project_id: string; daemon_id: string; install_state: number }>,
}));
const refetchInstalls = vi.hoisted(() => vi.fn(async () => undefined));
const createProjectFromRepo = vi.hoisted(() => vi.fn(async () => ({ queued: true })));
const detachProjectFromDaemon = vi.hoisted(() => vi.fn(async () => ({})));
const previewRequests = vi.hoisted(() => [] as Array<{ projectId: string; daemonId: string }>);
const project = vi.hoisted(() => ({
  current: {
    id: "temp",
    name: "temp",
    path: "/Users/me/src/temp",
    default_branch: "main",
    remote_url: "https://github.com/acme/temp" as string | undefined,
  },
}));

function storeMock(state: () => any) {
  return Object.assign((selector?: any) => (selector ? selector(state()) : state()), {
    getState: () => state(),
    setState: vi.fn(),
    subscribe: vi.fn(() => () => undefined),
  });
}

vi.mock("@/hooks/useMakeDefaultMachine", () => ({
  useMakeDefaultMachine: () => ({ makeDefault: vi.fn(), isPending: false }),
}));
vi.mock("@/hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-queries")>()),
  useProjectDaemonInstalls: () => ({ data: installState.rows, isLoading: false, refetch: refetchInstalls }),
  createProjectFromRepo,
}));
// What stays on laptop when temp moves: one uncommitted file.
vi.mock("@/hooks/project-service-hooks_gen", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-service-hooks_gen")>()),
  usePreviewProjectMachineMove: (req: { projectId: string; daemonId: string }) => {
    previewRequests.push(req);
    return {
      isLoading: false,
      error: null,
      data: {
        daemonId: req.daemonId,
        daemonName: "laptop",
        path: "/home/workspace/projects/temp",
        check: ProjectMachineMoveCheck.CHECKED,
        checkError: "",
        repos: [{ repoId: "r", name: "temp", relativePath: "", clonable: true, changedFileCount: 1, changedFiles: ["notes.md"], unpushedBranches: [], stashCount: 0, otherWorktrees: [], checkError: "" }],
        workspaces: [],
      },
    };
  },
  useDetachProjectFromDaemon: () => ({ mutateAsync: detachProjectFromDaemon }),
}));
vi.mock("../../../hooks/chat-queries", () => ({
  useChatList: () => ({ data: [], isSuccess: true }),
}));
vi.mock("../../../store/chatStore", () => ({
  useChatStore: storeMock(() => ({ hasLoaded: true, chats: new Map(), startChat, selectChat: vi.fn() })),
}));
const worktrees = [{ id: "w-main", branch: "main", name: "main", is_main: true }];
vi.mock("../../../store/worktreeStore", () => ({
  useWorktreeStore: storeMock(() => ({
    currentWorktree: worktrees[0],
    worktrees,
    switchWorktreeContext: vi.fn(),
    loadWorktrees: vi.fn(),
  })),
}));
vi.mock("../../../hooks/worktree-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../hooks/worktree-queries")>()),
  ...(await import("../../../test/worktreeQueriesMock")).worktreeQueriesMock(() => ({
    currentWorktree: worktrees[0],
    worktrees,
    switchWorktreeContext: vi.fn(),
    loadWorktrees: vi.fn(),
  })),
}));
vi.mock("../../../store/projectStore", () => ({
  useProjectStore: storeMock(() => ({ currentProject: project.current })),
}));
vi.mock("../../../store/attachmentStore", () => ({
  useAttachmentStore: storeMock(() => ({ clearAttachments: vi.fn() })),
}));
vi.mock("../../../store/workspaceStateStore", () => ({
  useWorkspaceStateStore: storeMock(() => ({ clearNewChatDraft: vi.fn() })),
}));
vi.mock("../../../store/apiKeySetupStore", () => ({
  useApiKeySetupStore: storeMock(() => ({ ensureApiKeyOrShowModal: vi.fn() })),
}));
vi.mock("../../../store/chatParamsStore", () => ({
  useChatParamsStore: storeMock(() => ({ transferTempToChat: vi.fn() })),
}));
vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({ ...daemonState.current, answered: true, refresh: vi.fn() }),
}));
vi.mock("@/hooks/useDaemonWait", () => ({ useDaemonWait: () => ({ state: null, retryNow: vi.fn() }) }));
vi.mock("@/hooks/useBundledDaemonPending", () => ({ useBundledDaemonPending: () => false }));
vi.mock("@/services/controlPlane/capabilities", () => ({ capabilities: { cloudDaemons: true } }));
vi.mock("../ChatInput", () => ({
  ChatInput: ({ onSend, disabled }: { onSend: (c: string) => Promise<void>; disabled?: boolean }) => (
    <button type="button" disabled={disabled} onClick={() => void onSend("hello").catch(() => undefined)}>
      Send
    </button>
  ),
}));
vi.mock("../ResumeDaemonPill", () => ({ ResumeDaemonPill: () => null }));
vi.mock("../OomKillBanner", () => ({ OomKillBanner: () => null }));
vi.mock("../DiskPressureBanner", () => ({ DiskPressureBanner: () => null }));
vi.mock("../MachineHealthBanner", () => ({ MachineHealthBanner: () => null }));
vi.mock("../../DaemonWaitState", () => ({ DaemonWaitState: () => null }));
vi.mock("../../Machines/AddMachineSheet", () => ({ AddMachineSheet: () => null }));
vi.mock("../../Worktrees/CreateWorktreeModal", () => ({ CreateWorktreeModal: () => null }));
vi.mock("../../Worktrees/DiscoverWorktreesModal", () => ({ DiscoverWorktreesModal: () => null }));
vi.mock("../../forge-ui/tooltip", () => ({ default: ({ children }: any) => <>{children}</> }));
vi.mock("../../icons/ReliantIcon", () => ({ ReliantIcon: () => null }));
vi.mock("../../../lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("../../../lib/logger", () => ({ logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() } }));
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

import { Code, ConnectError } from "@connectrpc/connect";
import { toast } from "sonner";

import { NewChatView } from "../NewChatView";
import { useNewChatMachineStore } from "../../../store/newChatMachineStore";

// DaemonStatus: ACTIVE = 1. cicd is the user's default machine, but temp
// lives on the laptop.
const laptop = { daemonId: "d-laptop", hostname: "laptop", status: 1 };
const cicd = { daemonId: "d-cicd", hostname: "cicd", status: 1, isDefault: true };

const sendButton = () => screen.getByRole("button", { name: "Send" });
const machineArg = () => (startChat.mock.calls[0] as unknown[])[6];

beforeEach(() => {
  startChat.mockClear();
  createProjectFromRepo.mockClear();
  detachProjectFromDaemon.mockClear();
  previewRequests.length = 0;
  useNewChatMachineStore.setState({ pending: null });
  vi.mocked(toast.error).mockClear();
  project.current = { ...project.current, remote_url: "https://github.com/acme/temp" };
  daemonState.current = { activeDaemon: cicd, daemons: [laptop, cicd], loading: false };
  installState.rows = [{ project_id: "temp", daemon_id: "d-laptop", install_state: ProjectInstallState.INSTALLED }];
});

describe("NewChatView: the project and machine always go together", () => {
  it("defaults to the machine the project is on, not the user's default machine", async () => {
    render(<NewChatView tabId="t1" />);
    expect(screen.getByTestId("machine-picker")).toHaveTextContent("laptop");
    expect(screen.queryByTestId("new-chat-placement-note")).toBeNull();
    fireEvent.click(sendButton());
    await waitFor(() => expect(startChat).toHaveBeenCalled());
  });

  it("does not let a machine without the project be picked, and sets it up there instead", async () => {
    render(<NewChatView tabId="t1" />);
    fireEvent.click(screen.getByTestId("machine-picker"));
    // cicd is listed, but as Not set up: clicking it chooses nothing.
    fireEvent.click(screen.getByTestId("machine-not-set-up-d-cicd"));
    expect(screen.getByTestId("machine-picker")).toHaveTextContent("laptop");

    // Setting it up clones temp there (the server's real path), into the
    // target's default folder, and chooses the machine. The project's path is
    // where it sits on the laptop; sent to cicd it fails (prod, 2026-11-23:
    // "mkdir /Users: permission denied").
    createProjectFromRepo.mockImplementationOnce(async () => {
      installState.rows = [
        ...installState.rows,
        { project_id: "temp", daemon_id: "d-cicd", install_state: ProjectInstallState.INSTALLING },
      ];
      return { queued: true };
    });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Set up temp on cicd" }));
    });
    expect(createProjectFromRepo).toHaveBeenCalledWith({
      cloneUrl: "https://github.com/acme/temp",
      daemonId: "d-cicd",
      name: "temp",
    });
    expect((createProjectFromRepo.mock.calls[0] as unknown[])[0]).not.toHaveProperty("path");
    expect(screen.getByTestId("machine-picker")).toHaveTextContent("cicd");

    // While the clone lands, the composer waits and says why.
    expect(sendButton()).toBeDisabled();
    expect(screen.getByTestId("new-chat-placement-note")).toHaveTextContent(
      "Setting up temp on cicd. You can send once it's there.",
    );
  });

  it("lists what stays on the old machine before the set-up, and offers (never runs) a detach once it landed", async () => {
    const view = render(<NewChatView tabId="t1" />);
    fireEvent.click(screen.getByTestId("machine-picker"));
    // Before anything is set up: what the clone leaves on laptop.
    expect(screen.getByTestId("stays-on-machine")).toHaveTextContent("These stay on laptop");
    expect(screen.getByTestId("stays-on-machine")).toHaveTextContent("temp: 1 uncommitted file (notes.md)");
    expect(previewRequests[0]).toEqual({ projectId: "temp", daemonId: "d-laptop" });

    createProjectFromRepo.mockImplementationOnce(async () => {
      installState.rows = [
        ...installState.rows,
        { project_id: "temp", daemon_id: "d-cicd", install_state: ProjectInstallState.INSTALLING },
      ];
      return { queued: true };
    });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Set up temp on cicd" }));
    });
    // Still cloning: nothing to offer yet.
    expect(screen.queryByTestId("detach-old-machine-offer")).toBeNull();

    installState.rows = [
      { project_id: "temp", daemon_id: "d-laptop", install_state: ProjectInstallState.INSTALLED },
      { project_id: "temp", daemon_id: "d-cicd", install_state: ProjectInstallState.INSTALLED },
    ];
    view.rerender(<NewChatView tabId="t1" />);
    const detach = screen.getByRole("button", { name: "Detach from laptop (keep files)" });
    expect(detachProjectFromDaemon).not.toHaveBeenCalled();
    await act(async () => {
      fireEvent.click(detach);
    });
    expect(detachProjectFromDaemon).toHaveBeenCalledWith({ projectId: "temp", daemonId: "d-laptop" });
    expect(screen.getByText("Detached from laptop. Its files stay on disk.")).toBeInTheDocument();
  });

  it("shows each nested repository's clone, and its git error, while the set-up is held", () => {
    installState.rows = [
      { project_id: "temp", daemon_id: "d-laptop", install_state: ProjectInstallState.INSTALLED },
      {
        project_id: "temp",
        daemon_id: "d-cicd",
        install_state: ProjectInstallState.INSTALLING,
        nested_repos: [
          { name: "forge", relative_path: "forge", install_state: ProjectInstallState.INSTALLING },
          { name: "sdk", relative_path: "tools/sdk", install_state: ProjectInstallState.FAILED, install_error: "git clone failed: Permission denied (publickey)." },
        ],
      } as (typeof installState.rows)[number],
    ];
    render(<NewChatView tabId="t1" />);
    fireEvent.click(screen.getByTestId("machine-picker"));
    fireEvent.click(screen.getByRole("option", { name: /cicd/ }));
    expect(screen.getByTestId("nested-repo-status")).toHaveTextContent("Cloning forge/…");
    expect(screen.getByTestId("nested-repo-status")).toHaveTextContent("tools/sdk/: git clone failed: Permission denied (publickey).");
  });

  it("sends on that machine once the clone has landed", async () => {
    installState.rows = [
      { project_id: "temp", daemon_id: "d-laptop", install_state: ProjectInstallState.INSTALLED },
      { project_id: "temp", daemon_id: "d-cicd", install_state: ProjectInstallState.INSTALLED },
    ];
    render(<NewChatView tabId="t1" />);
    fireEvent.click(screen.getByTestId("machine-picker"));
    fireEvent.click(screen.getByRole("option", { name: /cicd/ }));
    fireEvent.click(sendButton());
    await waitFor(() => expect(startChat).toHaveBeenCalled());
    expect(machineArg()).toEqual({ daemonId: "d-cicd", noMachine: undefined });
  });

  it("holds the composer, with the reason, when only a clone in flight could hold the project", () => {
    installState.rows = [{ project_id: "temp", daemon_id: "d-laptop", install_state: ProjectInstallState.INSTALLING }];
    render(<NewChatView tabId="t1" />);
    // The default names where the project is landing, not cicd, which will
    // never have it.
    expect(screen.getByTestId("machine-picker")).toHaveTextContent("laptop");
    expect(sendButton()).toBeDisabled();
    expect(screen.getByTestId("new-chat-placement-note")).toHaveTextContent("Setting up temp on laptop.");
  });

  it("explains, rather than offering a clone, for a project that is a plain folder", () => {
    project.current = { ...project.current, remote_url: undefined };
    render(<NewChatView tabId="t1" />);
    fireEvent.click(screen.getByTestId("machine-picker"));
    expect(screen.getByText("temp is a folder on another machine, so it can't be cloned here.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Set up temp/ })).toBeNull();
  });

  // The owner's report (2026-11-25): a chat started with a project that isn't
  // on the machine should offer to add it there, right where the composer is.
  it("offers \"Add to <machine>\" inline when the chosen machine lacks the project, and opens once it lands", async () => {
    useNewChatMachineStore.getState().offer({ projectId: "temp", daemonId: "d-cicd" });
    const view = render(<NewChatView tabId="t1" />);
    expect(sendButton()).toBeDisabled();
    expect(screen.getByTestId("new-chat-placement-note")).toHaveTextContent(
      "temp isn't on cicd yet. Add it there, or pick a machine it's on.",
    );
    // The machine it is on is one click away too.
    expect(screen.getByTestId("new-chat-placement-switch")).toHaveTextContent("Use laptop instead");

    createProjectFromRepo.mockImplementationOnce(async () => {
      installState.rows = [
        ...installState.rows,
        { project_id: "temp", daemon_id: "d-cicd", install_state: ProjectInstallState.INSTALLING },
      ];
      return { queued: true };
    });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Add to cicd" }));
    });
    expect(createProjectFromRepo).toHaveBeenCalledWith({
      cloneUrl: "https://github.com/acme/temp",
      daemonId: "d-cicd",
      name: "temp",
    });
    // No machine like cicd has temp, so it lands in cicd's default folder
    // (setUpPathOn), never the laptop's path.
    expect((createProjectFromRepo.mock.calls[0] as unknown[])[0]).not.toHaveProperty("path");
    // The machine stays cicd (not the default, which would move to laptop).
    expect(screen.getByTestId("machine-picker")).toHaveTextContent("cicd");
    expect(screen.getByTestId("new-chat-placement-note")).toHaveTextContent("Setting up temp on cicd.");
    expect(screen.queryByTestId("new-chat-placement-add")).toBeNull();

    installState.rows = [
      { project_id: "temp", daemon_id: "d-laptop", install_state: ProjectInstallState.INSTALLED },
      { project_id: "temp", daemon_id: "d-cicd", install_state: ProjectInstallState.INSTALLED },
    ];
    view.rerender(<NewChatView tabId="t1" />);
    expect(screen.queryByTestId("new-chat-placement-note")).toBeNull();
    fireEvent.click(sendButton());
    await waitFor(() => expect(startChat).toHaveBeenCalled());
    expect(machineArg()).toEqual({ daemonId: "d-cicd", noMachine: undefined });
  });

  it("switches to a machine the project is on from the note", () => {
    useNewChatMachineStore.getState().offer({ projectId: "temp", daemonId: "d-cicd" });
    render(<NewChatView tabId="t1" />);
    fireEvent.click(screen.getByTestId("new-chat-placement-switch"));
    expect(screen.getByTestId("machine-picker")).toHaveTextContent("laptop");
    expect(screen.queryByTestId("new-chat-placement-note")).toBeNull();
    expect(sendButton()).toBeEnabled();
  });

  it("says why a failed add failed, inline", async () => {
    useNewChatMachineStore.getState().offer({ projectId: "temp", daemonId: "d-cicd" });
    render(<NewChatView tabId="t1" />);
    createProjectFromRepo.mockImplementationOnce(async () => {
      throw new Error("no git credential found for provider github");
    });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Add to cicd" }));
    });
    expect(screen.getByRole("alert")).toHaveTextContent("no git credential found for provider github");
    expect(sendButton()).toBeDisabled();
  });

  it("offers no add for a plain folder, only a machine it's on or a folder on this one", () => {
    project.current = { ...project.current, remote_url: undefined };
    useNewChatMachineStore.getState().offer({ projectId: "temp", daemonId: "d-cicd" });
    render(<NewChatView tabId="t1" />);
    expect(screen.getByTestId("new-chat-placement-note")).toHaveTextContent(
      "temp is a folder on another machine, so it can't be copied to cicd.",
    );
    expect(screen.queryByTestId("new-chat-placement-add")).toBeNull();
    expect(screen.getByTestId("new-chat-placement-switch")).toHaveTextContent("Use laptop instead");
    expect(screen.getByTestId("new-chat-placement-use-folder")).toHaveTextContent("Choose a folder on cicd");
  });

  it("offers the add from the error when the send is refused for the project's machine", async () => {
    // The composer's record said cicd had it; the server knew better.
    installState.rows = [
      { project_id: "temp", daemon_id: "d-laptop", install_state: ProjectInstallState.INSTALLED },
      { project_id: "temp", daemon_id: "d-cicd", install_state: ProjectInstallState.INSTALLED },
    ];
    useNewChatMachineStore.getState().offer({ projectId: "temp", daemonId: "d-cicd" });
    startChat.mockImplementationOnce(async () => {
      throw new ConnectError("project is not installed on that daemon", Code.FailedPrecondition);
    });
    render(<NewChatView tabId="t1" />);
    fireEvent.click(sendButton());
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    const [title, options] = vi.mocked(toast.error).mock.calls.at(-1) as [string, { description: string; action?: { label: string; onClick: () => void } }];
    expect(title).toBe("Couldn't start the chat");
    expect(options.description).toBe("temp isn't on cicd. Your message is back in the box.");
    // The stale record is re-read, so the note takes over from here.
    expect(refetchInstalls).toHaveBeenCalled();
    expect(options.action?.label).toBe("Add to cicd");
    await act(async () => {
      options.action?.onClick();
    });
    expect(createProjectFromRepo).toHaveBeenCalledWith(expect.objectContaining({ daemonId: "d-cicd", name: "temp" }));
  });

  it("constrains nothing for a project with no install record (the server accepts any machine)", async () => {
    installState.rows = [];
    render(<NewChatView tabId="t1" />);
    fireEvent.click(screen.getByTestId("machine-picker"));
    fireEvent.click(screen.getByRole("option", { name: /laptop/ }));
    expect(sendButton()).toBeEnabled();
  });
});
