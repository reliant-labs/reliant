/**
 * The new-chat view's route with the real draft apply (store/openChat.ts
 * applyNewChat): the project lookup is the only thing held, so the project
 * the view shows really is the store's until the lookup returns.
 *
 *   - a draft URL naming another project keeps naming it while the view
 *     still shows the last project (the draft mirror waits for the apply)
 *   - the shell stays mounted across chat ⇄ new-chat moves, and Back to
 *     either page applies it again
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
import { useEffect, useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Chat } from "@/api/client";
import type { Project } from "@/hooks/project-queries";
import { markNewChatApplied } from "@/lib/chatRoute";
import type { ChatRouteData } from "@/lib/chatRouteResolve";
import { chatViewSearchSchema, newChatSearchSchema } from "@/routeSchemas";
import { useChatStore } from "@/store/chatStore";
import { applyNewChat } from "@/store/openChat";
import { useProjectStore } from "@/store/projectStore";
import { useWorktreeStore } from "@/store/worktreeStore";
import { setRuntimeConfig } from "@/test/runtimeConfig";

import { useChatRoute } from "../useChatRoute";

const lookup = vi.hoisted(() => ({
  held: null as null | { promise: Promise<void>; release: () => void },
}));
const opened = vi.hoisted(() => [] as string[]);

const project = (id: string) => ({ id, name: id }) as unknown as Project;

vi.mock("@/hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-queries")>()),
  getCachedProjects: () => [],
  fetchProjects: async () => {
    if (lookup.held) await lookup.held.promise;
    return ["p-a", "p-b", "p-c"].map(project);
  },
}));

vi.mock("@/hooks/worktree-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/worktree-queries")>()),
  resolveMainWorktree: async () => null,
  resolveWorktree: async () => null,
}));

vi.mock("@/lib/openChatInWorkspace", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/openChatInWorkspace")>()),
  openChatInWorkspace: vi.fn(async (chat: Chat) => {
    opened.push(chat.id);
    useChatStore.setState({ activeChatId: chat.id });
  }),
}));

vi.mock("@/store/openChat", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/store/openChat")>();
  return { ...actual, applyNewChat: vi.fn(actual.applyNewChat) };
});

/** Hold the next project lookup until release(). */
function holdLookup(): () => void {
  let release = () => {};
  const promise = new Promise<void>((resolve) => (release = resolve));
  lookup.held = { promise, release };
  return () => {
    lookup.held = null;
    release();
  };
}

let mounts = 0;

function Shell() {
  const [panelOpen, setPanelOpen] = useState(false);
  const projectId = useProjectStore((s) => s.currentProject?.id ?? null);
  useChatRoute({ ready: true, panelOpen, setPanelOpen, projectId, worktreeId: null });
  const active = useChatStore((s) => s.activeChatId);
  useEffect(() => {
    mounts += 1;
  }, []);
  return <div data-testid="shell">{JSON.stringify({ active, projectId })}</div>;
}

const chatLoader = ({ params }: { params: { chatId: string } }): ChatRouteData => ({
  kind: "chat",
  chat: { id: params.chatId, orgId: "", projectId: "p-a" } as Chat,
});

function tree() {
  const root = createRootRoute({ component: () => <Outlet /> });
  const app = createRoute({ getParentRoute: () => root, id: "_app", component: () => <Outlet /> });
  const home = createRoute({ getParentRoute: () => app, path: "/", component: Shell });
  const chat = createRoute({ getParentRoute: () => app, path: "c/$chatId", validateSearch: chatViewSearchSchema, loader: chatLoader, component: Shell });
  const draft = createRoute({ getParentRoute: () => app, path: "new", validateSearch: newChatSearchSchema, component: Shell });
  return root.addChildren([app.addChildren([home, chat, draft])]);
}

let router: AnyRouter;

async function open(url: string) {
  router = createRouter({ routeTree: tree(), history: createMemoryHistory({ initialEntries: [url] }) });
  (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER = router;
  render(<RouterProvider router={router} />);
  await screen.findByTestId("shell");
}

const shown = () => JSON.parse(screen.getByTestId("shell").textContent ?? "{}") as { active: string | null; projectId: string | null };
const path = () => router.state.location.pathname;
const search = () => router.state.location.search as Record<string, unknown>;
const settle = () => act(async () => {});

beforeEach(() => {
  setRuntimeConfig({});
  opened.length = 0;
  mounts = 0;
  lookup.held = null;
  vi.mocked(applyNewChat).mockClear();
  markNewChatApplied({ project: "(none)" });
  useChatStore.setState({ activeChatId: null });
  // The workspace restore left project A on screen.
  useProjectStore.setState({
    currentProject: project("p-a"),
    selectProject: async (next: Project) => {
      useProjectStore.setState({ currentProject: next });
    },
  });
  useWorktreeStore.setState({ switchWorktreeContext: async () => {} } as never);
});

afterEach(() => {
  lookup.held = null;
  delete (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER;
});

describe("a draft URL naming another project", () => {
  it("keeps naming it while project A is still on screen, then shows it", async () => {
    await open("/new?project=p-a");
    await waitFor(() => expect(applyNewChat).toHaveBeenCalledTimes(1));
    await settle();
    const release = holdLookup();
    await act(() => router.navigate({ to: "/new", search: { project: "p-b" } } as never));
    await waitFor(() => expect(applyNewChat).toHaveBeenCalledTimes(2));
    await settle();
    // B's lookup is held: A is on screen, and nothing writes A over the link.
    expect(shown().projectId).toBe("p-a");
    expect(search()).toEqual({ project: "p-b" });
    release();
    await waitFor(() => expect(shown().projectId).toBe("p-b"));
    await settle();
    expect(search()).toEqual({ project: "p-b" });
    // Only the URL's own two contexts were applied: no apply of A superseded B.
    expect(vi.mocked(applyNewChat).mock.calls.map(([target]) => target?.projectId)).toEqual(["p-a", "p-b"]);
  });

  it("keeps naming it on a direct load over a restored project A", async () => {
    const release = holdLookup();
    await open("/new?project=p-b");
    await waitFor(() => expect(applyNewChat).toHaveBeenCalledTimes(1));
    await settle();
    expect(shown().projectId).toBe("p-a");
    expect(search()).toEqual({ project: "p-b" });
    release();
    await waitFor(() => expect(shown().projectId).toBe("p-b"));
    await settle();
    expect(search()).toEqual({ project: "p-b" });
    expect(applyNewChat).toHaveBeenCalledTimes(1);
  });

  it("follows the view again once the URL's project is on screen", async () => {
    await open("/new?project=p-b");
    await waitFor(() => expect(shown().projectId).toBe("p-b"));
    await settle();
    // The project chip's switch, made in the store: the URL follows it.
    await act(async () => useProjectStore.setState({ currentProject: project("p-c") }));
    await waitFor(() => expect(search().project).toBe("p-c"));
  });
});

describe("Back across chat and new-chat, the shell kept mounted", () => {
  it("chat → new chat → Back reopens the chat", async () => {
    await open("/c/a");
    await waitFor(() => expect(shown().active).toBe("a"));
    await act(() => router.navigate({ to: "/new", search: { project: "p-a" } } as never));
    await waitFor(() => expect(shown().active).toBeNull());
    expect(path()).toBe("/new");
    act(() => router.history.back());
    await waitFor(() => expect(path()).toBe("/c/a"));
    await waitFor(() => expect(shown().active).toBe("a"));
    expect(opened).toEqual(["a", "a"]);
    expect(mounts).toBe(1);
  });

  it("new chat → chat → Back to the same draft URL shows the draft again", async () => {
    await open("/new?project=p-a");
    await waitFor(() => expect(applyNewChat).toHaveBeenCalledTimes(1));
    await act(() => router.navigate({ href: "/c/a" }));
    await waitFor(() => expect(shown().active).toBe("a"));
    act(() => router.history.back());
    await waitFor(() => expect(path()).toBe("/new"));
    expect(search()).toEqual({ project: "p-a" });
    await waitFor(() => expect(shown().active).toBeNull());
    expect(applyNewChat).toHaveBeenCalledTimes(2);
    expect(mounts).toBe(1);
  });
});
