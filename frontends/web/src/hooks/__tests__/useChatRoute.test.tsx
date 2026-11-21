/**
 * The chat shell follows its URL (hooks/useChatRoute.ts, lib/chatUrlFollow.ts):
 * a chat's URL opens that chat, Back and Forward move between chats, a thread
 * is a push Back closes, a chat the app opens or closes by itself moves the
 * URL, and the right panel and Detailed mode ride in the search.
 *
 * A real router and the real stores; the workspace open (project, worktree,
 * stream) is reduced to the selection it ends in.
 */

import { act, render, screen, waitFor } from "@testing-library/react";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
  type AnyRouter,
} from "@tanstack/react-router";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Chat } from "@/api/client";
import { useChatViewOverride } from "@/components/Chat/thread-views/useChatThreadMode";
import { markNewChatApplied } from "@/lib/chatRoute";
import type { ChatRouteData } from "@/lib/chatRouteResolve";
import { chatViewSearchSchema, newChatSearchSchema } from "@/routeSchemas";
import { useChatStore } from "@/store/chatStore";
import { useWorkspaceStateStore } from "@/store/workspaceStateStore";
import { setRuntimeConfig } from "@/test/runtimeConfig";

import { useChatRoute, type ChatRouteView } from "../useChatRoute";

const opened = vi.hoisted(() => [] as string[]);
const applied = vi.hoisted(() => [] as unknown[]);

vi.mock("@/lib/openChatInWorkspace", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/openChatInWorkspace")>()),
  openChatInWorkspace: vi.fn(async (chat: Chat) => {
    opened.push(chat.id);
    useChatStore.setState({ activeChatId: chat.id });
  }),
}));

vi.mock("@/store/openChat", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/store/openChat")>();
  return {
    ...actual,
    applyNewChat: vi.fn(async (target: { projectId?: string; daemonId?: string; worktreeId?: string; appName?: string }) => {
      applied.push(target);
      markNewChatApplied({ project: target.projectId, machine: target.daemonId, ws: target.worktreeId, app: target.appName });
      useChatStore.setState({ activeChatId: null });
    }),
  };
});

const PROJECT = "p-1";
let view: ChatRouteView;
let setPanel: (open: boolean) => void;

function Shell() {
  const [panelOpen, setPanelOpen] = useState(false);
  setPanel = setPanelOpen;
  view = useChatRoute({ ready: true, panelOpen, setPanelOpen, projectId: PROJECT, worktreeId: null });
  const active = useChatStore((s) => s.activeChatId);
  return <div data-testid="shell">{JSON.stringify({ active, thread: view.thread?.threadId ?? null, panelOpen, data: view.data?.kind ?? null })}</div>;
}

const chatLoader = ({ params }: { params: { chatId: string } }): ChatRouteData =>
  params.chatId === "nope" ? { kind: "missing", chatId: "nope" } : { kind: "chat", chat: { id: params.chatId, orgId: "" } as Chat };

function tree() {
  const root = createRootRoute({ component: () => <Outlet /> });
  const app = createRoute({ getParentRoute: () => root, id: "_app", component: () => <Outlet /> });
  const home = createRoute({ getParentRoute: () => app, path: "/", component: Shell });
  const chat = createRoute({ getParentRoute: () => app, path: "c/$chatId", validateSearch: chatViewSearchSchema, loader: chatLoader, component: Shell });
  const thread = createRoute({ getParentRoute: () => app, path: "c/$chatId/t/$threadId", validateSearch: chatViewSearchSchema, loader: chatLoader, component: Shell });
  const draft = createRoute({ getParentRoute: () => app, path: "new", validateSearch: newChatSearchSchema, component: Shell });
  return root.addChildren([app.addChildren([home, chat, thread, draft])]);
}

let router: AnyRouter;

async function open(url: string) {
  router = createRouter({ routeTree: tree(), history: createMemoryHistory({ initialEntries: [url] }) });
  (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER = router;
  render(<RouterProvider router={router} />);
  await screen.findByTestId("shell");
}

const shown = () => JSON.parse(screen.getByTestId("shell").textContent ?? "{}") as { active: string | null; thread: string | null; panelOpen: boolean; data: string | null };
const path = () => router.state.location.pathname;
const search = () => router.state.location.search as Record<string, unknown>;

beforeEach(() => {
  setRuntimeConfig({});
  opened.length = 0;
  applied.length = 0;
  useChatStore.setState({ activeChatId: null });
  // What the last test's new-chat view showed is not on screen in this one.
  markNewChatApplied({ project: "(none)" });
  useChatViewOverride.setState({ routed: false, view: null });
  useWorkspaceStateStore.getState().setRightSidebarTab(PROJECT, null, "files");
});

afterEach(() => {
  delete (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER;
});

describe("the open chat follows the URL", () => {
  it("opens the chat a URL names on a direct load", async () => {
    await open("/c/a-1");
    await waitFor(() => expect(shown().active).toBe("a-1"));
    expect(opened).toEqual(["a-1"]);
  });

  it("moves between two chats with Back and Forward", async () => {
    await open("/c/a-1");
    await waitFor(() => expect(shown().active).toBe("a-1"));
    await act(() => router.navigate({ href: "/c/a-2" }));
    await waitFor(() => expect(shown().active).toBe("a-2"));
    act(() => router.history.back());
    await waitFor(() => expect(shown().active).toBe("a-1"));
    expect(path()).toBe("/c/a-1");
    act(() => router.history.forward());
    await waitFor(() => expect(shown().active).toBe("a-2"));
  });

  it("opens a thread with a push that Back closes", async () => {
    await open("/c/a-1");
    await waitFor(() => expect(shown().active).toBe("a-1"));
    act(() => view.onSelectThread("t-1"));
    await waitFor(() => expect(path()).toBe("/c/a-1/t/t-1"));
    expect(shown().thread).toBe("t-1");
    act(() => view.onSelectThread(null));
    await waitFor(() => expect(path()).toBe("/c/a-1"));
    act(() => router.history.back());
    await waitFor(() => expect(path()).toBe("/c/a-1/t/t-1"));
    expect(shown().thread).toBe("t-1");
    // The chat stays open across its threads: it is not reopened.
    expect(opened).toEqual(["a-1"]);
  });

  it("shows an unknown chat as not found and opens nothing", async () => {
    useChatStore.setState({ activeChatId: "a-1" });
    await open("/c/nope");
    expect(shown().data).toBe("missing");
    expect(path()).toBe("/c/nope");
    expect(opened).toEqual([]);
  });
});

describe("the URL follows what the app opens and closes by itself", () => {
  it("pushes another chat the app opened from a chat's page, so Back returns", async () => {
    await open("/c/a-1");
    await waitFor(() => expect(shown().active).toBe("a-1"));
    act(() => useChatStore.setState({ activeChatId: "b-9" }));
    await waitFor(() => expect(path()).toBe("/c/b-9"));
    act(() => router.history.back());
    await waitFor(() => expect(shown().active).toBe("a-1"));
  });

  it("replaces a closed chat's URL with the new-chat view", async () => {
    await open("/c/a-1");
    await waitFor(() => expect(shown().active).toBe("a-1"));
    const before = router.history.length;
    act(() => useChatStore.setState({ activeChatId: null }));
    await waitFor(() => expect(path()).toBe("/new"));
    expect(router.history.length).toBe(before);
    // Not reopened: the app closed it.
    expect(shown().active).toBeNull();
    expect(opened).toEqual(["a-1"]);
  });

  it("puts the chat the home page restored in the URL, in place", async () => {
    useChatStore.setState({ activeChatId: "r-1" });
    await open("/");
    await waitFor(() => expect(path()).toBe("/c/r-1"));
    expect(router.history.length).toBe(1);
  });

  it("replaces the new-chat view with the chat its first message made", async () => {
    await open("/new?project=p-1");
    await waitFor(() => expect(applied).toHaveLength(1));
    act(() => useChatStore.setState({ activeChatId: "fresh" }));
    await waitFor(() => expect(path()).toBe("/c/fresh"));
    expect(router.history.length).toBe(1);
  });
});

describe("the new-chat view", () => {
  it("applies its draft context once", async () => {
    await open("/new?project=p-1&machine=d-1");
    await waitFor(() => expect(applied).toEqual([{ projectId: "p-1", daemonId: "d-1", worktreeId: undefined, appName: undefined }]));
    // The same context again (a replace that only reorders the search) applies nothing.
    await act(() => router.navigate({ to: "/new", search: { machine: "d-1", project: "p-1" }, replace: true } as never));
    expect(applied).toHaveLength(1);
    // Another one (Back to an older draft, a link) does.
    await act(() => router.navigate({ to: "/new", search: { project: "p-1", machine: "d-2" } } as never));
    await waitFor(() => expect(applied).toHaveLength(2));
  });
});

describe("a new chat about an app", () => {
  it("applies the app the URL names with the draft, and a change of app applies again", async () => {
    await open("/new?project=p-1&app=shopfront");
    await waitFor(() => expect(applied).toEqual([{ projectId: "p-1", daemonId: undefined, worktreeId: undefined, appName: "shopfront" }]));
    await act(() => router.navigate({ to: "/new", search: { project: "p-1" } } as never));
    await waitFor(() => expect(applied).toHaveLength(2));
    expect(applied[1]).toMatchObject({ projectId: "p-1", appName: undefined });
  });
});

describe("view state in the search", () => {
  it("shows the URL's Detailed mode and writes a toggle back, in place", async () => {
    await open("/c/a-1?view=detailed");
    await waitFor(() => expect(useChatViewOverride.getState()).toEqual({ routed: true, view: "detailed" }));
    act(() => useChatViewOverride.setState({ view: "simplified" }));
    await waitFor(() => expect(search().view).toBe("simplified"));
    expect(router.history.length).toBe(1);
  });

  it("opens the panel the URL names, and keeps the URL as the panel changes", async () => {
    await open("/c/a-1?panel=changes");
    await waitFor(() => expect(shown().panelOpen).toBe(true));
    expect(useWorkspaceStateStore.getState().getWorktreeState(PROJECT, null).rightSidebarTab).toBe("changes");
    expect(search().panel).toBe("changes");
    act(() => useWorkspaceStateStore.getState().setRightSidebarTab(PROJECT, null, "files"));
    await waitFor(() => expect(search().panel).toBe("files"));
    act(() => setPanel(false));
    await waitFor(() => expect(search().panel).toBeUndefined());
    expect(router.history.length).toBe(1);
  });

  it("drops the override off a chat's page", async () => {
    await open("/c/a-1?view=detailed");
    await waitFor(() => expect(useChatViewOverride.getState().routed).toBe(true));
    await act(() => router.navigate({ href: "/new" }));
    await waitFor(() => expect(useChatViewOverride.getState()).toEqual({ routed: false, view: null }));
  });
});
