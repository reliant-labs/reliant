/**
 * A new chat's attached projects go with its first send: the draft's
 * (store/newChatProjectsStore, the chips beside the project chip) become
 * StartChat's attached_project_ids. A host without the project chip (the
 * workflow editor) owns the chat's project, so it sends none; neither does a
 * chat with no machine.
 */
import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const startChat = vi.hoisted(() => vi.fn());
const selectChat = vi.hoisted(() => vi.fn());
const toastError = vi.hoisted(() => vi.fn());

function storeMock(state: () => any) {
  return Object.assign((selector?: any) => (selector ? selector(state()) : state()), {
    getState: () => state(),
    setState: vi.fn(),
    subscribe: vi.fn(() => () => undefined),
  });
}

const laptop = { daemonId: "d-laptop", hostname: "laptop", status: 1 };

// Where the project is installed decides which machine a new chat names
// (lib/chatMachine defaultMachineDaemon); these suites are not about that.
// "Make default" in the machine picker writes through the query client;
// these tests render no provider and never click it.
vi.mock("@/hooks/useMakeDefaultMachine", () => ({
  useMakeDefaultMachine: () => ({ makeDefault: vi.fn(), isPending: false }),
}));
vi.mock("@/hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-queries")>()),
  useProjectDaemonInstalls: () => ({ data: [], isLoading: false }),
}));
vi.mock("../../../hooks/chat-queries", () => ({
  useChatList: () => ({ data: [], isSuccess: true }),
}));
vi.mock("../../../store/chatStore", () => ({
  useChatStore: storeMock(() => ({ hasLoaded: true, chats: new Map(), startChat, selectChat })),
}));
vi.mock("../../../store/worktreeStore", () => ({
  useWorktreeStore: storeMock(() => ({
    currentWorktree: { id: "w-main", branch: "main", name: "main", is_main: true },
    worktrees: [{ id: "w-main", branch: "main", name: "main", is_main: true }],
    switchWorktreeContext: vi.fn(),
    loadWorktrees: vi.fn(),
  })),
}));
// Workspace rows are read through hooks/worktree-queries; answer them from the
// same fixture.
vi.mock("../../../hooks/worktree-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../hooks/worktree-queries")>()),
  ...(await import("../../../test/worktreeQueriesMock")).worktreeQueriesMock(() => ({
    currentWorktree: { id: "w-main", branch: "main", name: "main", is_main: true },
    worktrees: [{ id: "w-main", branch: "main", name: "main", is_main: true }],
    switchWorktreeContext: vi.fn(),
    loadWorktrees: vi.fn(),
  })),
}));
vi.mock("../../../store/projectStore", () => ({
  useProjectStore: storeMock(() => ({ currentProject: { id: "p1", name: "proj", default_branch: "main" } })),
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
  useDaemonStatus: () => ({ activeDaemon: laptop, daemons: [laptop], loading: false, answered: true, refresh: vi.fn() }),
}));
vi.mock("@/hooks/useDaemonWait", () => ({
  useDaemonWait: () => ({ state: null, retryNow: vi.fn() }),
}));
vi.mock("@/hooks/useBundledDaemonPending", () => ({ useBundledDaemonPending: () => false }));
vi.mock("@/services/controlPlane/capabilities", () => ({ capabilities: { cloudDaemons: true } }));
// The composer: a Send button, plus whatever text a prefill last put in it.
vi.mock("../ChatInput", () => ({
  ChatInput: ({
    onSend,
    prefill,
  }: {
    onSend: (c: string) => Promise<void>;
    prefill?: { text: string; id: number };
  }) => (
    <div>
      <output data-testid="composer-prefill">{prefill?.text ?? ""}</output>
      <button type="button" onClick={() => void onSend("refactor the parser").catch(() => undefined)}>
        Send
      </button>
    </div>
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
vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("../../../lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("../../../lib/logger", () => ({ logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() } }));
vi.mock("sonner", () => ({ toast: { error: toastError, success: vi.fn() } }));

// The chips are their own tests (ChatProjectChips, useChatProjects); here
// only what the send carries.
vi.mock("../NewChatProjectChip", () => ({ NewChatProjectChip: () => null }));
vi.mock("../ChatProjectChips", () => ({ ChatProjectChips: () => <div data-testid="draft-project-chips" /> }));
vi.mock("@/hooks/useChatProjects", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/useChatProjects")>()),
  useDraftChatProjects: () => ({}),
}));

import { NewChatView } from "../NewChatView";
import { getNewChatAttachedProjects, setNewChatAttachedProjects } from "../../../store/newChatProjectsStore";

beforeEach(() => {
  startChat.mockReset();
  selectChat.mockReset();
  toastError.mockReset();
  startChat.mockResolvedValue({ id: "chat-new" });
  setNewChatAttachedProjects("p1", ["p-docs", "p-web"]);
});

const sentAbout = () => startChat.mock.calls[0]?.[7] as { attachedProjectIds?: readonly string[] } | undefined;

describe("NewChatView: the projects attached to a new chat", () => {
  it("sends the draft's attached projects with the first message, then starts the next draft over", async () => {
    render(<NewChatView tabId="t1" showProjectChip />);
    expect(screen.getByTestId("draft-project-chips")).toBeInTheDocument();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Send" })));
    expect(startChat).toHaveBeenCalledTimes(1);
    expect(sentAbout()?.attachedProjectIds).toEqual(["p-docs", "p-web"]);
    expect(getNewChatAttachedProjects("p1")).toEqual([]);
  });

  it("sends none from a host without the project chip", async () => {
    render(<NewChatView tabId="t1" />);
    expect(screen.queryByTestId("draft-project-chips")).toBeNull();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Send" })));
    expect(sentAbout()?.attachedProjectIds).toEqual([]);
  });
});
