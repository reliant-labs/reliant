// Copyright (c) 2026 Reliant Labs

/**
 * A CHAT'S URL IS THE TRUTH (the 2026-11-23 prod incident: opening a chat
 * landed on another project's last chat, or on `/new?project=<other>&ws=…`).
 *
 * selectProject over `/c/<id>` used to move the URL to the selected project's
 * remembered chat whenever the project changed, whoever selected it. Only a
 * person's own switch (leaving the chat page before selecting a project)
 * may do that now; a background selection (a page's context, an org change,
 * a fallback) leaves the chat the URL names on screen.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Project } from "../projectStore";

const { chatsProject, pickedProject, restoreLastWorktree, fetchProjects } = vi.hoisted(() => {
  const chatsProject = { id: "p-labs", name: "reliant-labs", path: "/labs", org_id: "org-1" } as unknown as Project;
  const pickedProject = { id: "p-cp", name: "control-plane", path: "/cp", org_id: "org-1" } as unknown as Project;
  return {
    chatsProject,
    pickedProject,
    restoreLastWorktree: vi.fn(async (_projectId: string) => true),
    fetchProjects: vi.fn(async (): Promise<Project[]> => [chatsProject, pickedProject]),
  };
});

vi.mock("../../hooks/worktree-actions", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../hooks/worktree-actions")>()),
  restoreLastWorktree,
}));
vi.mock("../../hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../hooks/project-queries")>()),
  touchProject: vi.fn(),
  fetchProjects,
  getCachedProjects: () => [chatsProject, pickedProject],
}));

const { useProjectStore } = await import("../projectStore");
const { useWorktreeStore } = await import("../worktreeStore");
const { useChatStore } = await import("../chatStore");
const { useWorkspaceStateStore } = await import("../workspaceStateStore");
const { seedChatDetail } = await import("../../hooks/chat-queries");
const { beginWorkspaceOpen } = await import("../../lib/openChatInWorkspace");

import type { Chat } from "../../types/chat";

/** The chat the URL names, in the project the person opened it from. */
const openChatId = "chat-labs";
/** The chat the picked project last had open: what the old code jumped to. */
const remembered = { id: "chat-cp-last", projectId: pickedProject.id, title: "last opened" } as unknown as Chat;

const navigate = vi.fn();
let pathname = `/c/${openChatId}`;
const router = {
  get latestLocation() {
    return { pathname, search: {} };
  },
  state: {
    get location() {
      return { pathname, search: {} };
    },
    status: "idle",
  },
  navigate: (options: { href?: string; to?: string }) => {
    navigate(options);
    return Promise.resolve();
  },
};

const selectChat = vi.fn();
const clearCurrentChat = vi.fn();
const loadChats = vi.fn(async () => {});

beforeEach(() => {
  pathname = `/c/${openChatId}`;
  (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER = router;
  navigate.mockClear();
  selectChat.mockClear();
  clearCurrentChat.mockClear();
  loadChats.mockClear();
  restoreLastWorktree.mockClear();
  fetchProjects.mockClear();
  useWorktreeStore.setState({ currentWorktreeId: null });
  useChatStore.setState({ selectChat, clearCurrentChat, loadChats, activeChatId: openChatId });
  useProjectStore.setState({ currentProject: chatsProject });
  useWorkspaceStateStore.getState().setProjectFilterId(null);
  seedChatDetail(remembered);
  useWorkspaceStateStore.getState().setActiveChatId(pickedProject.id, null, remembered.id);
});
afterEach(() => {
  delete (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER;
});

describe("selecting a project over a chat's URL", () => {
  it("a background selection (the nav, a page's context) leaves the URL's chat open", async () => {
    await useProjectStore.getState().selectProject(pickedProject);

    expect(navigate).not.toHaveBeenCalled();
    expect(selectChat).not.toHaveBeenCalled();
    expect(clearCurrentChat).not.toHaveBeenCalled();
  });

  it("over the new-chat view too: no hop to the picked project's last chat", async () => {
    pathname = "/new";
    await useProjectStore.getState().selectProject(pickedProject);

    expect(navigate).not.toHaveBeenCalled();
    expect(selectChat).not.toHaveBeenCalled();
  });

  it("setProjectFilter moves only the list while the chat's URL owns the pane", async () => {
    await useProjectStore.getState().setProjectFilter(pickedProject.id);

    expect(navigate).not.toHaveBeenCalled();
    expect(selectChat).not.toHaveBeenCalled();
    expect(useWorkspaceStateStore.getState().projectFilterId).toBe(pickedProject.id);
  });
});

describe("an org change over a chat's URL", () => {
  it("discards a delayed org fetch after a newer workspace open", async () => {
    let resolveProjects!: (projects: Project[]) => void;
    fetchProjects.mockReturnValueOnce(new Promise((resolve) => { resolveProjects = resolve; }));
    seedChatDetail({ id: openChatId, projectId: chatsProject.id } as Chat);
    useProjectStore.setState({ currentProject: pickedProject });
    useWorkspaceStateStore.getState().setProjectFilterId(pickedProject.id);

    const following = useProjectStore.getState().followActiveOrg("org-1");
    // A newer navigation wins while the old org's request is pending, even
    // when the URL still names a chat and the old answer could open it.
    beginWorkspaceOpen();
    resolveProjects([chatsProject]);
    await following;

    expect(useWorkspaceStateStore.getState().projectFilterId).toBe(pickedProject.id);
    expect(useProjectStore.getState().currentProject?.id).toBe(pickedProject.id);
    expect(selectChat).not.toHaveBeenCalled();
    expect(clearCurrentChat).not.toHaveBeenCalled();
    expect(navigate).not.toHaveBeenCalled();
    expect(loadChats).not.toHaveBeenCalled();
  });

  it("does not load chats if its URL chat open is superseded", async () => {
    seedChatDetail({ id: openChatId, projectId: chatsProject.id } as Chat);
    useProjectStore.setState({ currentProject: { ...pickedProject, org_id: "org-2" } });
    fetchProjects.mockResolvedValueOnce([chatsProject]);
    selectChat.mockImplementationOnce(() => { beginWorkspaceOpen(); });

    await useProjectStore.getState().followActiveOrg("org-1");

    expect(selectChat).toHaveBeenCalled();
    expect(loadChats).not.toHaveBeenCalled();
  });

  it("does not leave the chat's project for the new org's most recent one", async () => {
    const otherOrgProject = { id: "p-other", name: "other", path: "/o", org_id: "org-2" } as unknown as Project;
    fetchProjects.mockResolvedValueOnce([otherOrgProject]);

    await useProjectStore.getState().followActiveOrg("org-2");

    // The route's open of the chat brings its project (and org) along.
    expect(clearCurrentChat).not.toHaveBeenCalled();
    expect(navigate).not.toHaveBeenCalled();
    expect(useProjectStore.getState().currentProject?.id).toBe(chatsProject.id);
    expect(loadChats).toHaveBeenCalled();
  });

  it("off a chat's URL it still moves to the new org's most recent project", async () => {
    pathname = "/inbox";
    const otherOrgProject = { id: "p-other", name: "other", path: "/o", org_id: "org-2" } as unknown as Project;
    fetchProjects.mockResolvedValueOnce([otherOrgProject]);

    await useProjectStore.getState().followActiveOrg("org-2");

    expect(clearCurrentChat).toHaveBeenCalledWith(null);
    expect(useProjectStore.getState().currentProject?.id).toBe(otherOrgProject.id);
  });
});
