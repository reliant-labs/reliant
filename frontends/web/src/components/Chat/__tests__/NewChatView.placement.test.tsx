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

import { ProjectInstallState } from "@/gen/reliant/v1/project_pb";

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
const createProjectFromRepo = vi.hoisted(() => vi.fn(async () => ({ queued: true })));
const project = vi.hoisted(() => ({
  current: {
    id: "temp",
    name: "temp",
    path: "/home/workspace/projects/temp",
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
  useProjectDaemonInstalls: () => ({ data: installState.rows, isLoading: false }),
  createProjectFromRepo,
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
    <button type="button" disabled={disabled} onClick={() => void onSend("hello")}>
      Send
    </button>
  ),
}));
vi.mock("../ResumeDaemonPill", () => ({ ResumeDaemonPill: () => null }));
vi.mock("../OomKillBanner", () => ({ OomKillBanner: () => null }));
vi.mock("../DiskPressureBanner", () => ({ DiskPressureBanner: () => null }));
vi.mock("../../DaemonWaitState", () => ({ DaemonWaitState: () => null }));
vi.mock("../../Machines/AddMachineSheet", () => ({ AddMachineSheet: () => null }));
vi.mock("../../Worktrees/CreateWorktreeModal", () => ({ CreateWorktreeModal: () => null }));
vi.mock("../../Worktrees/DiscoverWorktreesModal", () => ({ DiscoverWorktreesModal: () => null }));
vi.mock("../../forge-ui/tooltip", () => ({ default: ({ children }: any) => <>{children}</> }));
vi.mock("../../icons/ReliantIcon", () => ({ ReliantIcon: () => null }));
vi.mock("../../../lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("../../../lib/logger", () => ({ logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() } }));
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

import { NewChatView } from "../NewChatView";

// DaemonStatus: ACTIVE = 1. cicd is the user's default machine, but temp
// lives on the laptop.
const laptop = { daemonId: "d-laptop", hostname: "laptop", status: 1 };
const cicd = { daemonId: "d-cicd", hostname: "cicd", status: 1, isDefault: true };

const sendButton = () => screen.getByRole("button", { name: "Send" });
const machineArg = () => (startChat.mock.calls[0] as unknown[])[6];

beforeEach(() => {
  startChat.mockClear();
  createProjectFromRepo.mockClear();
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
    // project's own path, and chooses the machine.
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
      path: "/home/workspace/projects/temp",
    });
    expect(screen.getByTestId("machine-picker")).toHaveTextContent("cicd");

    // While the clone lands, the composer waits and says why.
    expect(sendButton()).toBeDisabled();
    expect(screen.getByTestId("new-chat-placement-note")).toHaveTextContent(
      "Setting up temp on cicd. You can send once it's there.",
    );
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

  it("constrains nothing for a project with no install record (the server accepts any machine)", async () => {
    installState.rows = [];
    render(<NewChatView tabId="t1" />);
    fireEvent.click(screen.getByTestId("machine-picker"));
    fireEvent.click(screen.getByRole("option", { name: /laptop/ }));
    expect(sendButton()).toBeEnabled();
  });
});
