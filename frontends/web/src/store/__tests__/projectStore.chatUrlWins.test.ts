/**
 * The URL names the open chat, and it wins over every background selection.
 *
 * On `/c/<id>` (or `/o/<org>/c/<id>`) the chat on screen is the one the URL
 * names, and the context project is that chat's. Callers that select a
 * project as background context — the console's project hook, the startup
 * and Settings/Forge/Workflows restores (restoreLastProject), the org
 * follow — used to move the URL instead: to the selected project's
 * remembered chat (a direct `/c/<id>` link opened the LAST chat instead), or
 * to `/new?project=<that project>` (clicking an existing chat from
 * `/project/<id>` ended on the new-chat view of another project).
 *
 * These drive the real router: the bugs were navigations, so the assertion
 * is the location the router ends at.
 */
import { createMemoryHistory, createRootRoute, createRoute, createRouter, type AnyRouter } from "@tanstack/react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Chat } from "../../types/chat";
import type { Project } from "../projectStore";

const { projectA, projectB, restoreLastWorktree } = vi.hoisted(() => ({
  projectA: { id: "proj-a", name: "a", path: "/a" } as unknown as Project,
  projectB: { id: "proj-b", name: "b", path: "/b" } as unknown as Project,
  restoreLastWorktree: vi.fn(async (_projectId: string) => true),
}));

vi.mock("../../hooks/worktree-actions", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../hooks/worktree-actions")>()),
  restoreLastWorktree,
}));
vi.mock("../../hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../hooks/project-queries")>()),
  touchProject: vi.fn(),
  getCachedProjects: () => [projectA, projectB],
  fetchProjects: vi.fn(async () => [projectA, projectB]),
}));

const { useProjectStore } = await import("../projectStore");
const { useWorktreeStore } = await import("../worktreeStore");
const { useChatStore } = await import("../chatStore");
const { useWorkspaceStateStore } = await import("../workspaceStateStore");
const { seedChatDetail } = await import("../../hooks/chat-queries");

// The chat the URL names, in project A; and the chat project B left open.
const urlChat = { id: "chat-url", projectId: projectA.id, title: "the linked chat" } as unknown as Chat;
const rememberedChat = { id: "chat-remembered", projectId: projectB.id, title: "left open in B" } as unknown as Chat;

const selectChat = vi.fn((chat: Chat) => useChatStore.setState({ activeChatId: chat.id }));

function makeRouter(initialEntry: string): AnyRouter {
  const root = createRootRoute();
  const home = createRoute({ getParentRoute: () => root, path: "/" });
  const project = createRoute({ getParentRoute: () => root, path: "project/$projectId" });
  const chat = createRoute({ getParentRoute: () => root, path: "c/$chatId" });
  const draft = createRoute({ getParentRoute: () => root, path: "new" });
  return createRouter({
    routeTree: root.addChildren([home, project, chat, draft]),
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
  }) as unknown as AnyRouter;
}

let router: AnyRouter;
const location = () => `${router.state.location.pathname}${router.state.location.searchStr ?? ""}`;
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

beforeEach(async () => {
  restoreLastWorktree.mockClear();
  selectChat.mockClear();
  router = makeRouter(`/c/${urlChat.id}`);
  await router.load();
  (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER = router;
  useWorktreeStore.setState({ currentWorktreeId: null, switchWorktreeContext: async () => {} } as never);
  useChatStore.setState({ selectChat, persistActiveChat: vi.fn(), loadChats: vi.fn(async () => {}) } as never);
  // The route's loader resolved the chat (lib/chatRouteResolve.ts), and the
  // route opened it: its project is the context project.
  seedChatDetail(urlChat);
  seedChatDetail(rememberedChat);
  useChatStore.setState({ activeChatId: urlChat.id });
  useProjectStore.setState({ currentProject: projectA });
  useWorkspaceStateStore.setState({ lastProjectId: projectB.id });
  useWorkspaceStateStore.getState().setActiveChatId(projectB.id, null, null);
});

afterEach(() => {
  delete (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER;
});

describe("a background project selection over a chat URL", () => {
  it("never leaves the chat for the new-chat view of the selected project", async () => {
    // Project B has no remembered chat: the old switch went to /new?project=proj-b.
    await useProjectStore.getState().selectProject(projectB);
    await settle();

    expect(location()).toBe(`/c/${urlChat.id}`);
    expect(useChatStore.getState().activeChatId).toBe(urlChat.id);
    expect(useProjectStore.getState().currentProject?.id).toBe(projectA.id);
  });

  it("never opens the selected project's remembered chat over it", async () => {
    useWorkspaceStateStore.getState().setActiveChatId(projectB.id, null, rememberedChat.id);

    await useProjectStore.getState().selectProject(projectB);
    await settle();

    expect(location()).toBe(`/c/${urlChat.id}`);
    expect(useChatStore.getState().activeChatId).toBe(urlChat.id);
    expect(useProjectStore.getState().currentProject?.id).toBe(projectA.id);
    expect(selectChat).not.toHaveBeenCalledWith(expect.objectContaining({ id: rememberedChat.id }), expect.anything());
  });
});

describe("the other background callers over a chat URL", () => {
  it("setProjectFilter (the /project/<id> bridge, landing late) moves only the list", async () => {
    useWorkspaceStateStore.getState().setActiveChatId(projectB.id, null, rememberedChat.id);

    await useProjectStore.getState().setProjectFilter(projectB.id);
    await settle();

    expect(location()).toBe(`/c/${urlChat.id}`);
    expect(useChatStore.getState().activeChatId).toBe(urlChat.id);
    expect(useProjectStore.getState().currentProject?.id).toBe(projectA.id);
    expect(useWorkspaceStateStore.getState().projectFilterId).toBe(projectB.id);
  });

  it("followActiveOrg keeps the chat of the new org the link moved into", async () => {
    // The chat route moved a link into the chat's org; the context project
    // was the old org's.
    const oldOrgProject = { id: "proj-old", name: "old", path: "/old", org_id: "org-old" } as unknown as Project;
    useProjectStore.setState({ currentProject: oldOrgProject });

    await useProjectStore.getState().followActiveOrg("org-new");
    await settle();

    expect(location()).toBe(`/c/${urlChat.id}`);
    expect(useChatStore.getState().activeChatId).toBe(urlChat.id);
    expect(useProjectStore.getState().currentProject?.id).toBe(projectA.id);
  });
});

describe("a person choosing another project from the chat's page", () => {
  it("leaves the chat's page for that project's chats (a push)", async () => {
    const { leaveChatPageFor } = await import("../projectStore");

    expect(leaveChatPageFor(projectB.id)).toBe(true);
    await settle();

    expect(router.state.location.pathname).toBe(`/project/${projectB.id}`);
  });
});

describe("a direct link to a chat, with another project last used", () => {
  it("restoreLastProject keeps the linked chat, not the last chat opened", async () => {
    // Last used: project B, which left `rememberedChat` open.
    useWorkspaceStateStore.getState().setActiveChatId(projectB.id, null, rememberedChat.id);

    await useProjectStore.getState().restoreLastProject();
    await settle();

    expect(location()).toBe(`/c/${urlChat.id}`);
    expect(useChatStore.getState().activeChatId).toBe(urlChat.id);
    expect(useProjectStore.getState().currentProject?.id).toBe(projectA.id);
  });

  it("makes the linked chat's project current when another one still is", async () => {
    // The route's open of the chat was superseded before its project
    // switch committed: B is still the context under A's chat.
    useProjectStore.setState({ currentProject: projectB });
    useWorkspaceStateStore.getState().setActiveChatId(projectB.id, null, rememberedChat.id);

    await useProjectStore.getState().restoreLastProject();
    await settle();

    expect(location()).toBe(`/c/${urlChat.id}`);
    expect(useChatStore.getState().activeChatId).toBe(urlChat.id);
    expect(useProjectStore.getState().currentProject?.id).toBe(projectA.id);
  });
});
