import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Chat } from "../../api/client";
import type { Project } from "../projectStore";

const mocks = vi.hoisted(() => ({
  projects: ["x", "a", "b"].map((id) => ({ id, name: id, path: `/${id}` })),
  fetchProjects: vi.fn(),
  deleteProject: vi.fn(async () => {}),
  resolveWorktree: vi.fn(),
  resolveMainWorktree: vi.fn(),
  resolveChat: vi.fn(),
  notification: vi.fn(),
}));
vi.mock("../../hooks/project-queries", async (original) => ({
  ...(await original<typeof import("../../hooks/project-queries")>()),
  getCachedProjects: () => mocks.projects,
  fetchProjects: () => mocks.fetchProjects(),
  deleteProject: () => mocks.deleteProject(),
  touchProject: vi.fn(),
}));
vi.mock("../../hooks/worktree-queries", async (original) => ({
  ...(await original<typeof import("../../hooks/worktree-queries")>()),
  resolveWorktree: (...args: unknown[]) => mocks.resolveWorktree(...args),
  resolveMainWorktree: (...args: unknown[]) => mocks.resolveMainWorktree(...args),
}));
vi.mock("../../hooks/chat-queries", async (original) => ({
  ...(await original<typeof import("../../hooks/chat-queries")>()),
  resolveChat: (...args: unknown[]) => mocks.resolveChat(...args),
}));
vi.mock("../../lib/notifications", async (original) => ({
  ...(await original<typeof import("../../lib/notifications")>()),
  showWorkflowFailedNotification: (...args: unknown[]) => mocks.notification(...args),
  getNotificationPermission: () => "granted",
}));
vi.mock("../../lib/analytics", () => ({ trackEvent: vi.fn() }));

import { useProjectStore } from "../projectStore";
import { useChatStore } from "../chatStore";
import { useViewerStore } from "../viewerStore";
import { useWorktreeStore } from "../worktreeStore";
import { useWorkspaceStateStore } from "../workspaceStateStore";
import { useChatNavigationStore } from "../chatNavigationStore";
import { useGlobalUpdatesStore } from "../globalUpdatesStore";
import { openChat, openNewChat } from "../openChat";
import { seedChatDetail } from "../../hooks/chat-queries";
import { queryClient } from "../../lib/query-client";
import { ChatState } from "../../gen/reliant/v1/chat_pb";
import { UserUpdateType, EntityType } from "../../gen/reliant/v1/streaming_pb";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}
const project = (id: string) => mocks.projects.find((p) => p.id === id)! as Project;
const chat = (id: string, projectId: string, worktreeId?: string) => ({ id, projectId, worktreeId, title: id }) as Chat;
function pane() {
  return {
    project: useProjectStore.getState().currentProject?.id,
    chat: useChatStore.getState().activeChatId,
    workspace: useWorktreeStore.getState().currentWorktreeId,
    viewers: useViewerStore.getState().viewers,
    queue: useChatNavigationStore.getState().chatQueue,
  };
}
let notificationSequence = 0;
function showNotification(id: string) {
  notificationSequence += 1;
  useGlobalUpdatesStore.getState().handleUpdate([{
    id: `update-${id}-${notificationSequence}`, user_id: "user", sequence_number: 1, chat_id: id,
    project_id: "a", update_type: UserUpdateType.CHAT_STATE_CHANGE,
    entity_type: EntityType.CHAT, entity_id: id,
    data: { state: ChatState.IDLE, previous_state: ChatState.IDLE, reason: "workflow_failed", unread: true, title: id },
    created_at: new Date(Date.now() + 60_000).toISOString(),
  }]);
  expect(mocks.notification).toHaveBeenCalled();
  return mocks.notification.mock.calls.at(-1)![2] as (id: string) => Promise<void>;
}

beforeEach(() => {
  vi.restoreAllMocks();
  vi.clearAllMocks();
  queryClient.clear();
  mocks.fetchProjects.mockImplementation(async () => mocks.projects);
  mocks.resolveWorktree.mockImplementation(async (id: string) => ({ id, project_id: id.includes("b") ? "b" : "a" }));
  mocks.resolveMainWorktree.mockImplementation(async (id: string) => ({ id: `main-${id}`, project_id: id }));
  mocks.resolveChat.mockResolvedValue(undefined);
  useWorkspaceStateStore.setState({ projects: {}, projectFilterId: null, lastProjectId: null });
  useProjectStore.setState({ currentProject: project("x") });
  useWorktreeStore.getState().reset();
  useChatStore.setState({ activeChatId: "x-chat" });
  useViewerStore.setState({ viewers: [], activeViewerId: null });
  useChatNavigationStore.setState({ chatQueue: [] });
  // Keep real chat selection/persistence, but prevent network side effects.
  vi.spyOn(useGlobalUpdatesStore.getState(), "subscribeToChatDetails").mockImplementation(() => {});
  vi.spyOn(useChatStore.getState(), "loadChats").mockResolvedValue();
  vi.spyOn(useChatStore.getState(), "dismissChat").mockResolvedValue();
  vi.spyOn(window, "focus").mockImplementation(() => {});
});

describe("one navigation intent across real entry points", () => {
  it("notification branch A cannot force selection after B supersedes its worktree lookup", async () => {
    const pending = deferred<{ id: string; project_id: string }>();
    seedChatDetail(chat("a-chat", "a", "branch-a"));
    mocks.resolveWorktree.mockReturnValueOnce(pending.promise);
    const clicked = showNotification("a-chat")("a-chat");
    await vi.waitFor(() => expect(mocks.resolveWorktree).toHaveBeenCalledWith("branch-a"));
    await openChat(chat("b-chat", "b", "branch-b"));
    const b = pane();
    pending.resolve({ id: "branch-a", project_id: "a" });
    await clicked;
    expect(pane()).toEqual(b);
    expect(b.chat).toBe("b-chat");
  });

  it("notification GetChat begun before B cannot allocate a fresh intent after B", async () => {
    const pending = deferred<Chat>();
    mocks.resolveChat.mockReturnValueOnce(pending.promise);
    const clicked = showNotification("a-chat")("a-chat");
    await openChat(chat("b-chat", "b", "branch-b"));
    const b = pane();
    pending.resolve(chat("a-chat", "a", "branch-a"));
    await clicked;
    expect(pane()).toEqual(b);
  });

  it.each(["filter", "default"])("late %s workspace restore cannot replace B's chat/workspace/viewers/queue", async (entry) => {
    const pending = deferred<{ id: string; project_id: string }>();
    mocks.resolveMainWorktree.mockReturnValueOnce(pending.promise);
    useWorkspaceStateStore.getState().setActiveChatId("a", "main-a", "remembered-a");
    const restoring = entry === "filter"
      ? useProjectStore.getState().setProjectFilter("a")
      : useProjectStore.getState().selectProject(project("a"));
    await vi.waitFor(() => expect(mocks.resolveMainWorktree).toHaveBeenCalled());
    await openChat(chat("b-chat", "b", "branch-b"));
    useViewerStore.setState({ viewers: [{ id: "viewer-b", projectId: "b", type: "file", title: "B", worktreeId: "branch-b" } as never] });
    const b = pane();
    pending.resolve({ id: "main-a", project_id: "a" });
    await restoring;
    expect(pane()).toEqual(b);
    expect(mocks.resolveChat).not.toHaveBeenCalledWith("remembered-a");
  });

  it("filter A -> All cancels A's pending restore without moving context", async () => {
    const pending = deferred<{ id: string; project_id: string }>();
    mocks.resolveMainWorktree.mockReturnValueOnce(pending.promise);
    const a = useProjectStore.getState().setProjectFilter("a");
    await useProjectStore.getState().setProjectFilter(null);
    const all = pane();
    pending.resolve({ id: "main-a", project_id: "a" });
    await a;
    expect(useWorkspaceStateStore.getState().projectFilterId).toBeNull();
    expect(pane()).toEqual(all);
  });

  it("filter saved-worktree read is canceled by new-chat B", async () => {
    const pending = deferred<{ id: string; project_id: string }>();
    useWorkspaceStateStore.getState().setLastWorktree("a", "saved-a");
    mocks.resolveWorktree.mockReturnValueOnce(pending.promise);
    const a = useProjectStore.getState().setProjectFilter("a");
    await openNewChat({ projectId: "b", worktreeId: "branch-b" });
    const b = pane();
    pending.resolve({ id: "saved-a", project_id: "a" });
    await a;
    expect(pane()).toEqual(b);
    expect(b.chat).toBeNull();
    expect(b.workspace).toBe("branch-b");
  });

  it("startup project read cannot overwrite a newer chat", async () => {
    const pending = deferred<typeof mocks.projects>();
    mocks.fetchProjects.mockReturnValueOnce(pending.promise);
    const restoring = useProjectStore.getState().restoreLastProject();
    await openChat(chat("b-chat", "b", "branch-b"));
    const b = pane();
    pending.resolve(mocks.projects);
    expect(await restoring).toBe(false);
    expect(pane()).toEqual(b);
  });

  it.each([false, true])("delete current A cleans departing pane; replacement remembered chat = %s", async (remembered) => {
    useProjectStore.setState({ currentProject: project("a") });
    useChatStore.setState({ activeChatId: "a-chat" });
    useWorktreeStore.setState({ currentWorktreeId: "branch-a", currentWorktreeProjectId: "a" });
    useViewerStore.setState({ viewers: [{ id: "viewer-a", projectId: "a", title: "A", type: "file", file: { path: "/a/file", name: "file", type: "file" } } as never] });
    useWorkspaceStateStore.setState({ projectFilterId: "a" });
    mocks.fetchProjects.mockResolvedValue([project("b")]);
    if (remembered) {
      useWorkspaceStateStore.getState().setActiveChatId("b", "main-b", "b-chat");
      mocks.resolveChat.mockResolvedValue(chat("b-chat", "b", "main-b"));
    }
    await useProjectStore.getState().deleteProject("a");
    expect(pane().project).toBe("b");
    expect(pane().chat).toBe(remembered ? "b-chat" : null);
    expect(pane().workspace).toBe("main-b");
    expect(pane().viewers).toEqual([]);
    expect(useWorkspaceStateStore.getState().projects.a).toBeUndefined();
    expect(useWorkspaceStateStore.getState().projectFilterId).toBeNull();
  });
});
