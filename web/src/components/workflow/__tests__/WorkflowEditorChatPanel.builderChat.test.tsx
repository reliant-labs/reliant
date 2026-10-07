/**
 * The workflow editor's chat, rendered through the real NewChatView
 * (research/WORKFLOW_EDITOR_UX_REVIEW.md issue 8, research/NO_MACHINE_CHATS.md).
 *
 *   - Its empty state is about THIS workflow: suggested prompts.
 *   - It never blocks on a machine it does not need. With no usable machine it
 *     starts with no machine, says so, and its composer works; with one, it
 *     runs there as every chat does.
 *   - Picking a suggestion fills the composer behind the workflow reference.
 *
 * Only the composer is stubbed: it renders its value, takes `prefill` the way
 * ChatInput does (once per id), and sends what it shows.
 */
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useEffect, useRef, useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const daemonState = vi.hoisted(() => ({
  current: {
    activeDaemon: undefined as { daemonId: string; hostname: string; status: number } | undefined,
    daemons: [] as Array<{ daemonId: string; hostname: string; status: number }>,
    loading: false,
  },
}));
const startChat = vi.hoisted(() => vi.fn(async () => ({ id: "chat-new" })));
const drafts = vi.hoisted(() => ({ byProject: new Map<string, string>() }));

function storeMock(state: () => any) {
  return Object.assign((selector?: any) => (selector ? selector(state()) : state()), {
    getState: () => state(),
    setState: vi.fn(),
    subscribe: vi.fn(() => () => undefined),
  });
}

vi.mock("../../../hooks/chat-queries", () => ({
  useChatList: () => ({ data: [], isSuccess: true }),
}));
vi.mock("../../../store/chatStore", () => ({
  useChatStore: storeMock(() => ({ hasLoaded: true, chats: new Map(), startChat, selectChat: vi.fn() })),
}));
vi.mock("../../../store/worktreeStore", () => ({
  useWorktreeStore: storeMock(() => ({
    currentWorktree: { id: "w-main", branch: "main", name: "main", is_main: true },
    worktrees: [{ id: "w-main", branch: "main", name: "main", is_main: true }],
    switchWorktreeContext: vi.fn(),
    loadWorktrees: vi.fn(),
  })),
}));
vi.mock("../../../store/projectStore", () => ({
  useProjectStore: storeMock(() => ({ currentProject: { id: "p1", name: "proj", default_branch: "main" } })),
}));
vi.mock("../../../store/workspaceStateStore", () => ({
  useWorkspaceStateStore: storeMock(() => ({
    getNewChatDraft: (projectId: string) => drafts.byProject.get(projectId) ?? "",
    setNewChatDraft: (projectId: string, text: string) => drafts.byProject.set(projectId, text),
    clearNewChatDraft: (projectId: string) => drafts.byProject.delete(projectId),
  })),
}));
vi.mock("../../../store/attachmentStore", () => ({
  useAttachmentStore: storeMock(() => ({ clearAttachments: vi.fn() })),
}));
vi.mock("../../../store/apiKeySetupStore", () => ({
  useApiKeySetupStore: storeMock(() => ({ ensureApiKeyOrShowModal: vi.fn() })),
}));
vi.mock("../../../store/chatParamsStore", () => ({
  useChatParamsStore: storeMock(() => ({ transferTempToChat: vi.fn() })),
}));
vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({ ...daemonState.current, refresh: vi.fn() }),
}));
// The real wait copy for a machine that is not connected yet.
vi.mock("@/hooks/useDaemonWait", () => ({
  useDaemonWait: ({ waiting }: { waiting: boolean }) => ({
    state: waiting ? { tone: "waiting", title: "Starting your machine" } : null,
    retryNow: vi.fn(),
  }),
}));
vi.mock("@/hooks/useBundledDaemonPending", () => ({ useBundledDaemonPending: () => false }));
vi.mock("@/services/controlPlane/capabilities", () => ({ capabilities: { cloudDaemons: true } }));
vi.mock("../../Chat/ChatInput", () => ({
  ChatInput: ({
    onSend,
    disabled,
    prefill,
  }: {
    onSend: (content: string) => Promise<void>;
    disabled?: boolean;
    prefill?: { text: string; id: number };
  }) => {
    // Like useChatInputState: seeded from the project's new-chat draft when it
    // mounts, and saved back as the draft.
    const [value, setRaw] = useState(() => drafts.byProject.get("p1") ?? "");
    const setValue = (next: string) => {
      drafts.byProject.set("p1", next);
      setRaw(next);
    };
    const applied = useRef<number | undefined>(undefined);
    useEffect(() => {
      if (!prefill || applied.current === prefill.id) return;
      applied.current = prefill.id;
      setValue(prefill.text);
    }, [prefill]);
    return (
      <>
        <textarea aria-label="Composer" value={value} onChange={(e) => setValue(e.target.value)} />
        <button type="button" disabled={disabled} onClick={() => void onSend(value)}>
          Send
        </button>
      </>
    );
  },
}));
vi.mock("../../Chat/ResumeDaemonPill", () => ({ ResumeDaemonPill: () => null }));
vi.mock("../../Chat/OomKillBanner", () => ({ OomKillBanner: () => null }));
vi.mock("../../DaemonWaitState", () => ({
  DaemonWaitState: ({ state }: { state: { title: string } }) => <div role="status">{state.title}</div>,
}));
vi.mock("../../Layout/ConnectDaemonModal", () => ({ ConnectDaemonModal: () => null }));
vi.mock("../../Worktrees/CreateWorktreeModal", () => ({ CreateWorktreeModal: () => null }));
vi.mock("../../Worktrees/DiscoverWorktreesModal", () => ({ DiscoverWorktreesModal: () => null }));
vi.mock("../../ui/Tooltip", () => ({ Tooltip: ({ children }: any) => <>{children}</> }));
vi.mock("../../icons/ReliantIcon", () => ({ ReliantIcon: () => null }));
vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("../../../lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("../../../lib/logger", () => ({ logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() } }));
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));
vi.mock("../../Chat/ChatContainer", () => ({ ChatContainer: () => null }));
vi.mock("../../runs/RunRouteLoader", () => ({ useRunRoute: () => ({ status: "loading" }) }));

import { WorkflowEditorChatPanel, workflowReferencePrefill } from "../WorkflowEditorChatPanel";

// DaemonStatus: ACTIVE = 1, PENDING = 4, SUSPENDED = 5.
const laptop = { daemonId: "d-laptop", hostname: "laptop", status: 1 };
const provisioning = { daemonId: "d-cloud", hostname: "cloud", status: 4 };

function panel(workflowSlug: string | null = "swift-fox-a1b2") {
  return (
    <WorkflowEditorChatPanel
      workflowSlug={workflowSlug ?? undefined}
      onChatIdChange={vi.fn()}
      isOpen
      onOpenChange={vi.fn()}
      panelSize="normal"
      onPanelSizeChange={vi.fn()}
    />
  );
}

const composer = () => screen.getByRole("textbox", { name: "Composer" }) as HTMLTextAreaElement;
const machineArg = () => (startChat.mock.calls[0] as unknown[])[6];

beforeEach(() => {
  startChat.mockClear();
  drafts.byProject.clear();
  daemonState.current = { activeDaemon: undefined, daemons: [], loading: false };
});

describe("the workflow editor's chat", () => {
  it("offers prompts about this workflow", () => {
    render(panel());
    expect(screen.getByTestId("builder-chat-empty-state")).toHaveTextContent("swift-fox-a1b2");
    expect(screen.getAllByTestId("builder-chat-suggestion").map((b) => b.textContent)).toEqual([
      "Add a step that…",
      "Add a trigger when…",
      "Explain this workflow",
      "Write tests",
    ]);
  });

  it("starts with no machine, without waiting for one, when the user has no usable machine", async () => {
    daemonState.current = { activeDaemon: undefined, daemons: [provisioning], loading: false };
    render(panel());

    expect(screen.getByTestId("builder-chat-no-machine-pill")).toHaveTextContent("No machine · web & integrations");
    expect(screen.queryByText(/Starting your machine/)).toBeNull();
    expect(composer().value).toBe(workflowReferencePrefill("swift-fox-a1b2"));

    const send = screen.getByRole("button", { name: "Send" });
    expect(send).toBeEnabled();
    fireEvent.click(send);
    await waitFor(() => expect(startChat).toHaveBeenCalled());
    expect((startChat.mock.calls[0] as unknown[])[0]).toBe("w-main");
    expect(machineArg()).toEqual({ daemonId: undefined, noMachine: true });
  });

  it("runs on the user's machine when they have one, like every other chat", async () => {
    daemonState.current = { activeDaemon: laptop, daemons: [laptop], loading: false };
    render(panel());

    expect(screen.queryByTestId("builder-chat-no-machine-pill")).toBeNull();
    expect(screen.getByTestId("builder-chat-empty-state")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(startChat).toHaveBeenCalled());
    expect(machineArg()).toEqual({ daemonId: undefined, noMachine: undefined });
  });

  it("fills the composer with a picked suggestion, behind the workflow reference, and sends it", async () => {
    render(panel());
    const reference = workflowReferencePrefill("swift-fox-a1b2");

    fireEvent.click(screen.getByRole("button", { name: "Add a step that…" }));
    expect(composer().value).toBe(`${reference}Add a step that `);

    // Picking replaces the text rather than appending to it, and the same
    // suggestion can be picked again after an edit.
    fireEvent.change(composer(), { target: { value: "something else" } });
    fireEvent.click(screen.getByRole("button", { name: "Add a step that…" }));
    expect(composer().value).toBe(`${reference}Add a step that `);

    fireEvent.click(screen.getByRole("button", { name: "Explain this workflow" }));
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(startChat).toHaveBeenCalled());
    expect((startChat.mock.calls[0] as unknown[])[1]).toBe(`${reference}Explain what this workflow does, step by step.`);
  });

  // "New workflow" lands on /workflow/new (no slug yet) and then on the
  // draft's slug. The composer used to mount and read the new-chat draft
  // before the reference was written to it, and opened empty.
  it("puts the reference in the composer once a new workflow's slug arrives", () => {
    const { rerender } = render(panel(null));
    act(() => {
      rerender(panel("quick-wolf-d2e1"));
    });
    expect(composer().value).toBe(workflowReferencePrefill("quick-wolf-d2e1"));
  });

  it("replaces the reference when the workflow's slug changes", () => {
    const { rerender } = render(panel("first-a1"));
    expect(composer().value).toBe(workflowReferencePrefill("first-a1"));

    act(() => {
      rerender(panel("second-b2"));
    });
    expect(composer().value).toBe(workflowReferencePrefill("second-b2"));
  });

  it("keeps a draft the user typed rather than replacing it with the reference", () => {
    drafts.byProject.set("p1", "my own words");
    render(panel());
    expect(screen.getByTestId("builder-chat-empty-state")).toBeInTheDocument();
    expect(composer().value).toBe("my own words");
  });
});
