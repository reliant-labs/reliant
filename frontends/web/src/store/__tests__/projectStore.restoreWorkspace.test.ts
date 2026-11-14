/**
 * selectProject's desktop workspace restore, and the mobile opt-out.
 *
 * By default a project switch restores the desktop workspace the user left in
 * that project: its last worktree and its last ACTIVE CHAT, which it selects
 * (selectChat marks it read, subscribes the update stream to it and loads its
 * plan). The mobile surface names the chat on screen in its route, so that
 * remembered chat is one the user is not looking at; it passes
 * `restoreWorkspace: false`, and the switch is then the project only — no
 * worktree / chat RPCs, no chat selected behind the user's back. (The chat
 * list is not reloaded either way: it spans every project, not the context.)
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Project } from "../projectStore";

const { from, to, restoreLastWorktree } = vi.hoisted(() => ({
  from: { id: "proj-a", name: "a", path: "/a" } as unknown as Project,
  to: { id: "proj-b", name: "b", path: "/b" } as unknown as Project,
  restoreLastWorktree: vi.fn(async (_projectId: string) => true),
}));

// The switch's own RPCs are replaced at their module seams: the project list
// and touch (project-queries), and the desktop worktree restore
// (worktree-actions), so the test observes whether the restore ran without a
// transport.
vi.mock("../../hooks/worktree-actions", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../hooks/worktree-actions")>()),
  restoreLastWorktree,
}));
vi.mock("../../hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../hooks/project-queries")>()),
  touchProject: vi.fn(),
  fetchProjects: vi.fn(async () => [from, to]),
}));

const { useProjectStore } = await import("../projectStore");
const { useWorktreeStore } = await import("../worktreeStore");
const { useChatStore } = await import("../chatStore");
const { useWorkspaceStateStore } = await import("../workspaceStateStore");
const { seedChatDetail } = await import("../../hooks/chat-queries");

import type { Chat } from "../../types/chat";

const rememberedChat = { id: "chat-remembered", projectId: "proj-b", title: "left open on desktop" } as unknown as Chat;

const selectChat = vi.fn();
const loadChats = vi.fn(async () => {});

beforeEach(() => {
  restoreLastWorktree.mockClear();
  selectChat.mockClear();
  loadChats.mockClear();
  useWorktreeStore.setState({ currentWorktreeId: null });
  useChatStore.setState({ selectChat, loadChats });
  useProjectStore.setState({ currentProject: from });
  // The desktop left `rememberedChat` active in project B's main checkout.
  seedChatDetail(rememberedChat);
  useWorkspaceStateStore.getState().setActiveChatId(to.id, null, rememberedChat.id);
});

describe("selectProject workspace restore", () => {
  it("restores the desktop workspace and its active chat by default", async () => {
    await useProjectStore.getState().selectProject(to);

    expect(useProjectStore.getState().currentProject?.id).toBe(to.id);
    expect(restoreLastWorktree).toHaveBeenCalledWith(to.id, undefined, { isCurrent: expect.any(Function) });
    expect(selectChat).toHaveBeenCalledWith(expect.objectContaining({ id: rememberedChat.id }), { deferPersist: true });
  });

  it("with restoreWorkspace: false, switches the project only", async () => {
    await useProjectStore.getState().selectProject(to, { restoreWorkspace: false });

    expect(useProjectStore.getState().currentProject?.id).toBe(to.id);
    expect(loadChats).not.toHaveBeenCalled();
    expect(restoreLastWorktree).not.toHaveBeenCalled();
    expect(selectChat).not.toHaveBeenCalled();
  });

  it("makes the project current before its first await", () => {
    // The mobile project list navigates without awaiting the switch, and the
    // chat list it lands on scopes by currentProject — so the new project must
    // be current synchronously, not after the chat list RPC.
    void useProjectStore.getState().selectProject(to, { restoreWorkspace: false });
    expect(useProjectStore.getState().currentProject?.id).toBe(to.id);
  });

  it("restoreLastProject passes restoreWorkspace through", async () => {
    useWorkspaceStateStore.setState({ lastProjectId: to.id });
    useProjectStore.setState({ currentProject: null });

    expect(await useProjectStore.getState().restoreLastProject({ restoreWorkspace: false })).toBe(true);

    expect(useProjectStore.getState().currentProject?.id).toBe(to.id);
    expect(restoreLastWorktree).not.toHaveBeenCalled();
    expect(selectChat).not.toHaveBeenCalled();
  });
});
