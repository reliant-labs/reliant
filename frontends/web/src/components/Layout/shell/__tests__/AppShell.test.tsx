// Copyright (c) 2026 Reliant Labs

/**
 * The one shell (U6): ONE nav instance above the route outlet, on every app
 * page and on no other, that survives moving between a chat, the Inbox and an
 * app's console without remounting — and never remounts the page under it
 * when it docks, collapses or turns on.
 */

import { act, screen, waitFor } from "@testing-library/react";
import { useEffect, type ReactElement } from "react";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { renderWithSession } from "@/lib/test-utils";
import { useWorkspaceStateStore } from "@/store/workspaceStateStore";

const probe = vi.hoisted(() => ({ navMounts: 0, pageMounts: 0, signedIn: true, narrow: false }));

// The nav itself is covered in Sidebar*.test.tsx; here it only says when it
// mounts and which page it was told is on screen. It is the frame's `nav`
// seam: the frame draws whatever nav body its host hands it.
function ProbeNav({ pathname, onNavigate }: ShellNavProps) {
  useEffect(() => {
    probe.navMounts += 1;
  }, []);
  return (
    <>
      <div data-testid="nav-probe">{pathname}</div>
      <button type="button" onClick={() => onNavigate("/inbox", { drill: true })}>drill</button>
      <button type="button" onClick={() => onNavigate("/forge/p/shop")}>destination</button>
    </>
  );
}

vi.mock("@/hooks/useNarrowViewport", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/useNarrowViewport")>()),
  useNarrowViewport: () => probe.narrow,
}));

import { AppShellFrame, type ShellNavProps } from "../AppShell";
import { PageMain, shellFrameScope, toggleShellNav, useShellFrame } from "../shellFrame";

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

/** Rendered signed in, or signed out while `probe.signedIn` is false. */
function renderInSession(ui: ReactElement) {
  return renderWithSession(ui, { session: probe.signedIn ? undefined : null });
}

function renderAt(path: string) {
  const root = createRootRoute({
    component: () => (
      <AppShellFrame nav={ProbeNav}>
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
  renderInSession(<RouterProvider router={router} />);
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
  probe.narrow = false;
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
        <AppShellFrame nav={ProbeNav}>
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
    renderInSession(<RouterProvider router={router} />);

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

// ── One main landmark ───────────────────────────────────────────────────────

describe("the shell's main landmark", () => {
  // Pages that draw a body region of their own (the chat shell, AreaShell,
  // ConsoleFrame, a project's Home) render it as PageMain: inside the shell
  // the frame is the ONE main.
  function renderPageAt(path: string) {
    const root = createRootRoute({
      component: () => (
        <AppShellFrame nav={ProbeNav}>
          <Outlet />
        </AppShellFrame>
      ),
    });
    const app = createRoute({ getParentRoute: () => root, id: "_app", component: () => <Outlet /> });
    const authenticated = createRoute({ getParentRoute: () => root, id: "_authenticated", component: () => <Outlet /> });
    const body = (name: string) => () => (
      <PageMain data-testid="page-body">
        <Page name={name} />
      </PageMain>
    );
    const router = createRouter({
      routeTree: root.addChildren([
        app.addChildren([
          createRoute({ getParentRoute: () => app, path: "/", component: body("home") }),
          createRoute({ getParentRoute: () => app, path: "c/$chatId", component: body("chat") }),
        ]),
        authenticated.addChildren([
          createRoute({ getParentRoute: () => authenticated, path: "/projects/$projectId", component: body("project-home") }),
          createRoute({ getParentRoute: () => authenticated, path: "/forge/p/$project", component: body("console") }),
          createRoute({ getParentRoute: () => authenticated, path: "/inbox", component: body("inbox") }),
        ]),
      ]),
      history: createMemoryHistory({ initialEntries: [path] }),
    });
    renderInSession(<RouterProvider router={router} />);
  }

  it.each(["/", "/c/c0ffee00-2222-4aaa-8bbb-00000000000a", "/projects/00000002-0000-4000-8000-0000000000a2", "/forge/p/shop", "/inbox"])(
    "has exactly one main on %s, the frame's",
    async (path) => {
      renderPageAt(path);
      await screen.findByTestId("page");
      const mains = screen.getAllByRole("main");
      expect(mains).toHaveLength(1);
      expect(mains[0]).toHaveAttribute("data-testid", "shell-main");
    },
  );

  it("lets a page body be the main landmark outside the shell", async () => {
    probe.signedIn = false;
    renderPageAt("/inbox");
    await screen.findByTestId("page");
    expect(screen.getAllByRole("main")).toEqual([screen.getByTestId("page-body")]);
  });

  // A <main> rendered by a page inside the shell would be a second landmark.
  // The ones allowed: forge-ui's SidebarLayout (the public demo's console frame,
  // outside the shell), the demo's 404, and the dev previews.
  it("no shell page renders a <main> of its own", () => {
    const src = join(__dirname, "../../../..");
    const allowed = new Set(["components/forge-ui/sidebar_layout.tsx", "demoShellRoutes.tsx"]);
    const offenders: string[] = [];
    const walk = (dir: string) => {
      for (const entry of readdirSync(dir)) {
        const path = join(dir, entry);
        if (statSync(path).isDirectory()) {
          if (entry === "__tests__" || entry === "__preview__" || entry === "gen" || entry === "node_modules") continue;
          walk(path);
        } else if (/\.tsx$/.test(entry) && /<main[\s>]/.test(readFileSync(path, "utf8"))) {
          const rel = relative(src, path);
          if (!allowed.has(rel)) offenders.push(rel);
        }
      }
    };
    walk(src);
    expect(offenders).toEqual([]);
  });

  // U6e, mobile: the narrow window's drawer draws the one nav; a drill-in
  // (Agents, Observability, or back out) keeps it open on the area's rows, a
  // destination closes it.
  it("keeps the narrow drawer open through a drill-in and closes it on a destination", async () => {
    probe.narrow = true;
    const router = renderAt("/");
    await screen.findByTestId("nav-probe");
    const { useNavDrawer } = await import("../shellFrame");
    act(() => useNavDrawer.getState().setOpen(true));
    expect(screen.getByTestId("shell-nav")).toHaveAttribute("data-state", "open");

    act(() => screen.getByRole("button", { name: "drill", hidden: true }).click());
    await waitFor(() => expect(router.state.location.pathname).toBe("/inbox"));
    expect(screen.getByTestId("shell-nav")).toHaveAttribute("data-state", "open");

    act(() => screen.getByRole("button", { name: "destination", hidden: true }).click());
    await waitFor(() => expect(router.state.location.pathname).toBe("/forge/p/shop"));
    expect(screen.getByTestId("shell-nav")).toHaveAttribute("data-state", "closed");
  });
});
