// Copyright (c) 2026 Reliant Labs

/**
 * The one shell (U6): ONE nav instance above the route outlet, on every app
 * page and on no other, that survives moving between a chat, the Inbox and an
 * app's console without remounting — and never remounts the page under it
 * when it docks, collapses or turns on.
 */

import { act, render, screen, waitFor } from "@testing-library/react";
import { useEffect } from "react";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useWorkspaceStateStore } from "@/store/workspaceStateStore";

const probe = vi.hoisted(() => ({ navMounts: 0, pageMounts: 0, signedIn: true }));

// The nav itself is covered in Sidebar*.test.tsx; here it only says when it
// mounts and which page it was told is on screen.
vi.mock("../../Sidebar", () => ({
  Sidebar: ({ pathname }: { pathname: string }) => {
    useEffect(() => {
      probe.navMounts += 1;
    }, []);
    return <div data-testid="nav-probe">{pathname}</div>;
  },
}));

vi.mock("@/store/authStore", () => ({
  useAuthStore: (select: (state: { user: object | null }) => unknown) => select({ user: probe.signedIn ? { id: "u1" } : null }),
}));

import { AppShellFrame } from "../AppShell";
import { shellFrameScope, toggleShellNav, useShellFrame } from "../shellFrame";

/** A page that counts its mounts and says what the frame told it. */
function Page({ name }: { name: string }) {
  const { inShell, navDocked } = useShellFrame();
  useEffect(() => {
    probe.pageMounts += 1;
  }, []);
  return (
    <div data-testid="page" data-name={name} data-in-shell={String(inShell)} data-docked={String(navDocked)}>
      {name}
    </div>
  );
}

function renderAt(path: string) {
  const root = createRootRoute({
    component: () => (
      <AppShellFrame>
        <Outlet />
      </AppShellFrame>
    ),
  });
  const app = createRoute({ getParentRoute: () => root, id: "_app", component: () => <Outlet /> });
  const authenticated = createRoute({ getParentRoute: () => root, id: "_authenticated", component: () => <Outlet /> });
  const page = (parent: () => typeof app | typeof authenticated, routePath: string, name: string) =>
    createRoute({ getParentRoute: parent, path: routePath, component: () => <Page name={name} /> });
  const auth = createRoute({ getParentRoute: () => root, path: "/auth", component: () => <Page name="auth" /> });
  const router = createRouter({
    routeTree: root.addChildren([
      auth,
      app.addChildren([page(() => app, "/", "chats"), page(() => app, "project/$projectId", "project")]),
      authenticated.addChildren([
        page(() => authenticated, "/inbox", "inbox"),
        page(() => authenticated, "/forge/p/$project", "console"),
        page(() => authenticated, "/onboarding", "onboarding"),
        page(() => authenticated, "/workflow/new", "builder"),
      ]),
    ]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(<RouterProvider router={router} />);
  return router;
}

async function go(router: ReturnType<typeof renderAt>, href: string) {
  await act(async () => {
    await router.navigate({ href });
  });
}

beforeEach(() => {
  // The resizable column measures the panels beside it.
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  probe.navMounts = 0;
  probe.pageMounts = 0;
  probe.signedIn = true;
  useWorkspaceStateStore.getState().setLeftSidebarExpandedGlobal(true);
});

describe("the one shell", () => {
  it("keeps the same nav across a chat, the Inbox and an app's console", async () => {
    const router = renderAt("/");
    const nav = await screen.findByTestId("nav-probe");
    expect(nav).toHaveTextContent("/");
    expect(screen.getByTestId("shell-nav")).toHaveAttribute("data-state", "docked");

    await go(router, "/inbox");
    await waitFor(() => expect(screen.getByTestId("page")).toHaveAttribute("data-name", "inbox"));
    await go(router, "/forge/p/shop");
    await waitFor(() => expect(screen.getByTestId("page")).toHaveAttribute("data-name", "console"));

    // The very same element, mounted once, told each page in turn.
    expect(screen.getByTestId("nav-probe")).toBe(nav);
    expect(nav).toHaveTextContent("/forge/p/shop");
    expect(probe.navMounts).toBe(1);
    expect(screen.getByTestId("page")).toHaveAttribute("data-in-shell", "true");
  });

  // U5's chat URLs render the chat shell (App) as the home does, one route
  // each with the very same component (routes.tsx createOrgScopedRoutes):
  // opening a chat, another, the new-chat view and the home keeps both the
  // nav and the chat shell; only leaving the chats mounts another page.
  it("keeps the nav and the chat shell across chats, the new-chat view and the home, then the Inbox", async () => {
    const chatShellMounts = { count: 0 };
    function ChatShell() {
      useEffect(() => {
        chatShellMounts.count += 1;
      }, []);
      return <Page name="chat-shell" />;
    }
    const root = createRootRoute({
      component: () => (
        <AppShellFrame>
          <Outlet />
        </AppShellFrame>
      ),
    });
    const app = createRoute({ getParentRoute: () => root, id: "_app", component: () => <Outlet /> });
    const authenticated = createRoute({ getParentRoute: () => root, id: "_authenticated", component: () => <Outlet /> });
    const chats = ["/", "c/$chatId", "c/$chatId/t/$threadId", "new"].map((path) =>
      createRoute({ getParentRoute: () => app, path, component: ChatShell }),
    );
    const inbox = createRoute({ getParentRoute: () => authenticated, path: "/inbox", component: () => <Page name="inbox" /> });
    const router = createRouter({
      routeTree: root.addChildren([app.addChildren(chats), authenticated.addChildren([inbox])]),
      history: createMemoryHistory({ initialEntries: ["/c/a"] }),
    });
    render(<RouterProvider router={router} />);

    const nav = await screen.findByTestId("nav-probe");
    const shell = await screen.findByTestId("page");
    expect(nav).toHaveTextContent("/c/a");

    for (const href of ["/c/b", "/c/b/t/t-1", "/new", "/"]) {
      await act(async () => {
        await router.navigate({ href });
      });
      await waitFor(() => expect(nav).toHaveTextContent(new RegExp(`^${href}$`)));
      // The same chat shell element, never torn down between chats.
      expect(screen.getByTestId("page")).toBe(shell);
    }
    expect(chatShellMounts.count).toBe(1);

    await act(async () => {
      await router.navigate({ href: "/inbox" });
    });
    await waitFor(() => expect(screen.getByTestId("page")).toHaveAttribute("data-name", "inbox"));
    expect(screen.getByTestId("nav-probe")).toBe(nav);
    expect(nav).toHaveTextContent("/inbox");
    expect(probe.navMounts).toBe(1);
  });

  it.each([
    ["/auth", "auth"],
    ["/onboarding", "onboarding"],
    ["/workflow/new", "builder"],
  ])("is off on %s, which keeps its own chrome", async (path, name) => {
    renderAt(path);
    await waitFor(() => expect(screen.getByTestId("page")).toHaveAttribute("data-name", name));
    expect(screen.queryByTestId("shell-nav")).toBeNull();
    expect(screen.getByTestId("page")).toHaveAttribute("data-in-shell", "false");
  });

  it("is off while signed out", async () => {
    probe.signedIn = false;
    renderAt("/");
    await screen.findByTestId("page");
    expect(screen.queryByTestId("shell-nav")).toBeNull();
  });

  it("collapses and docks the one nav without remounting it or the page", async () => {
    renderAt("/inbox");
    const nav = await screen.findByTestId("nav-probe");
    const frame = screen.getByTestId("shell-nav");
    expect(screen.getByTestId("page")).toHaveAttribute("data-docked", "true");

    act(() => toggleShellNav());
    expect(frame).toHaveAttribute("data-state", "closed");
    // Off screen and out of the tab order until it is shown again.
    expect(frame).toHaveAttribute("inert");
    expect(screen.getByTestId("page")).toHaveAttribute("data-docked", "false");

    act(() => toggleShellNav());
    expect(frame).toHaveAttribute("data-state", "docked");
    expect(frame).not.toHaveAttribute("inert");
    expect(screen.getByTestId("nav-probe")).toBe(nav);
    expect(probe.navMounts).toBe(1);
    expect(probe.pageMounts).toBe(1);
  });
});

describe("shellFrameScope", () => {
  const scope = (routeIds: string[], pathname = "/", signedIn = true) => shellFrameScope({ routeIds, pathname, signedIn });

  it("is on under either app layout, in either URL form", () => {
    expect(scope(["__root__", "/_app", "/_app/"])).toBe(true);
    expect(scope(["__root__", "/_authenticated", "/_authenticated/inbox"], "/inbox")).toBe(true);
    expect(scope(["__root__", "/o/$org", "/o/$org/_authenticated", "/o/$org/_authenticated/inbox"], "/o/acme/inbox")).toBe(true);
  });

  // Chat URLs (U5: /c/<chat>, /new and their /o/<org> forms) mount under the
  // app layouts, so they are shell pages by the route tree alone: the frame
  // never reads which chat is open or how the workspace picks it.
  it("counts any page under the app layouts, chat URLs included", () => {
    expect(scope(["__root__", "/_app", "/_app/c/$chatId"], "/c/abc")).toBe(true);
    expect(scope(["__root__", "/o/$org", "/o/$org/_app", "/o/$org/_app/c/$chatId/t/$threadId"], "/o/acme/c/abc/t/t1")).toBe(true);
    expect(scope(["__root__", "/o/$org", "/o/$org/_app", "/o/$org/_app/new"], "/o/acme/new")).toBe(true);
  });

  it("is off outside them, and on the pages that keep their own chrome", () => {
    expect(scope(["__root__", "/auth"], "/auth")).toBe(false);
    expect(scope(["__root__", "/_app", "/_app/"], "/", false)).toBe(false);
    expect(scope(["__root__", "/_authenticated", "/_authenticated/onboarding"], "/onboarding")).toBe(false);
    expect(scope(["__root__", "/_authenticated", "/_authenticated/_mobile"], "/m/chats")).toBe(false);
    expect(scope(["__root__", "/o/$org", "/o/$org/_authenticated", "/o/$org/_authenticated/workflow/$workflowName"], "/o/acme/workflow/x")).toBe(false);
    expect(scope(["__root__", "/forge/demo"], "/forge/demo")).toBe(false);
  });
});
