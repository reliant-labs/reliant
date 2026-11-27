/**
 * The navigation drawer — open/close mechanics, closing on route change and
 * scrim tap, Escape, and the focus trap. Rendered against a real router
 * (memory history) rather than a mocked `useRouterState`, same reasoning as
 * `MobileTabBar.test.tsx` had: the thing worth pinning is that the drawer
 * reads the router's own location, not whatever a mock hands it.
 */

import { useState, type ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Link,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { setTransport } from "../../../lib/connect";
import { mockTransport } from "../../../lib/test-utils";

// Data hooks only: the column itself (Layout/sidebar/MobileHybridNav →
// ShellNavColumn, the org row, Find, the project switcher, the rows and the
// phone's inline chat list) is the real one, so this pins what the phone
// draws, not a stand-in.
vi.mock("../../../hooks/useOrgs", async (original) => ({
  ...(await original<typeof import("../../../hooks/useOrgs")>()),
  // No org resolved: the org row is the brand, and opens nothing.
  useActiveOrg: () => ({ orgs: [], active: null, canSwitch: false, select: vi.fn() }),
}));
const projects = vi.hoisted(() => [
  { id: "p1", name: "reliant", path: "/p/reliant", last_active: "2024-01-02T00:00:00.000Z", is_git_repo: true, is_forge: false },
]);
vi.mock("../../../hooks/project-queries", async (original) => ({
  ...(await original<typeof import("../../../hooks/project-queries")>()),
  useProjects: () => ({ projects, isLoading: false }),
}));
vi.mock("../../../hooks/project-catalog-queries", async (original) => ({
  ...(await original<typeof import("../../../hooks/project-catalog-queries")>()),
  useProjectCatalog: () => ({ catalog: undefined, isLoading: false, error: null }),
}));
const chats = vi.hoisted(() => [
  {
    id: "c1",
    title: "Fix the drawer",
    projectId: "p1",
    worktreeId: "",
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    lastMessageAt: "2024-01-01T00:00:00.000Z",
    unread: false,
    activity: 0,
    workingThreads: 0,
    state: 1,
  },
]);
vi.mock("../../../hooks/chat-queries", async (original) => ({
  ...(await original<typeof import("../../../hooks/chat-queries")>()),
  useChatListWithWorkspaces: () => ({ data: { chats, mainWorktrees: [], lastUserUpdateSequence: 0 }, isPending: false, isError: false }),
  useChat: () => ({ data: undefined }),
}));
vi.mock("../../../hooks/inbox-queries", async (original) => ({
  ...(await original<typeof import("../../../hooks/inbox-queries")>()),
  useInboxCounts: () => ({ data: { blockingCount: 0, hasInformational: false }, isError: false }),
  useInboxProjectId: () => undefined,
}));

const { MobileNavDrawer } = await import("../MobileNavDrawer");

beforeEach(() => setTransport(mockTransport({})));
afterEach(() => setTransport(null));

function withQueries(children: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

function Screen({ path }: { path: string }) {
  return (
    <div>
      <span>screen:{path}</span>
      <Link to="/m/chats">go to chats</Link>
    </div>
  );
}

function buildTree(onClose: () => void, isOpenGetter: () => boolean) {
  const rootRoute = createRootRoute({
    component: () => (
      <>
        <Outlet />
        <MobileNavDrawer isOpen={isOpenGetter()} onClose={onClose} />
      </>
    ),
  });

  const paths = ["/m/chats", "/m/chats/$chatId", "/m/new", "/m/search", "/m/settings", "/m/inbox", "/inbox", "/settings"];

  return rootRoute.addChildren(
    paths.map((path) =>
      createRoute({
        getParentRoute: () => rootRoute,
        path,
        component: () => <Screen path={path} />,
      }),
    ),
  );
}

function renderAt(path: string, isOpen: boolean, onClose: () => void) {
  const router = createRouter({
    routeTree: buildTree(onClose, () => isOpen),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(withQueries(<RouterProvider router={router} />));
  return router;
}

describe("MobileNavDrawer", () => {
  it("renders nothing when closed", () => {
    renderAt("/m/chats", false, vi.fn());
    expect(screen.queryByRole("dialog", { name: "Navigation" })).not.toBeInTheDocument();
  });

  // The owner's bug: on chats the phone drew an old nav ("Menu" bar, an org
  // card, a "Find" row, a "Project" card), on other tabs the shell's.
  it.each(["/m/chats", "/m/inbox", "/m/settings"])("is the shell's nav column on %s, never the old nav", async (path) => {
    renderAt(path, true, vi.fn());
    const dialog = await screen.findByRole("dialog", { name: "Navigation" });
    const column = dialog.querySelector('[data-onboarding="left-sidebar"]');
    expect(column).not.toBeNull();
    expect(within(dialog).getByTestId("shell-switcher")).toBeInTheDocument();
    expect(within(dialog).getByTestId("nav-find")).toBeInTheDocument();
    expect(column!.contains(within(dialog).getByTestId("shell-switcher"))).toBe(true);
    expect(within(dialog).queryByTestId("mobile-nav-project")).toBeNull();
    expect(within(dialog).queryByTestId("mobile-org-switcher")).toBeNull();
    expect(within(dialog).queryByText("Menu")).toBeNull();
  });

  it("lists the chats inline on /m/chats, as the shell's drawer does under Agents", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const router = renderAt("/m/chats", true, onClose);
    const dialog = await screen.findByRole("dialog", { name: "Navigation" });
    expect(within(dialog).getByTestId("nav-drill-in")).toHaveAttribute("data-area", "agents");
    const list = within(dialog).getByTestId("mobile-nav-chat-list");
    expect(list).toHaveAttribute("data-place", "fill");
    // Not the old single "Chats" row to a screen of its own.
    expect(within(dialog).queryByTestId("mobile-nav-chats")).toBeNull();
    await user.click(within(list).getByRole("button", { name: /Fix the drawer/ }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/m/chats/c1"));
    expect(onClose).toHaveBeenCalled();
  });

  it("opens the phone's Search screen from Find, and closes", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const router = renderAt("/m/inbox", true, onClose);
    const dialog = await screen.findByRole("dialog", { name: "Navigation" });
    await user.click(within(dialog).getByTestId("nav-find"));
    await waitFor(() => expect(router.state.location.pathname).toBe("/m/search"));
    expect(onClose).toHaveBeenCalled();
  });

  it("closes on a project pick, even one that leaves the route as it is", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    renderAt("/m/inbox", true, onClose);
    const dialog = await screen.findByRole("dialog", { name: "Navigation" });
    await user.click(within(dialog).getByTestId("shell-project-switcher"));
    await user.click(await screen.findByTestId("shell-switcher-all-projects"));
    expect(onClose).toHaveBeenCalled();
  });

  it("stays open through a drill-in, and closes on a destination", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const router = renderAt("/m/inbox", true, onClose);
    const dialog = await screen.findByRole("dialog", { name: "Navigation" });
    // Agents › drills in: the drawer stays open on its rows.
    await user.click(within(dialog).getByTestId("nav-agents"));
    await waitFor(() => expect(router.state.location.pathname).not.toBe("/m/inbox"));
    expect(onClose).not.toHaveBeenCalled();
    // A row (Settings) is a destination.
    await user.click(within(dialog).getByTestId("sidebar-settings-button"));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("calls onClose when the scrim is tapped", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    renderAt("/m/chats", true, onClose);
    await screen.findByRole("dialog", { name: "Navigation" });

    // The scrim is the first `aria-hidden` sibling — clicking any element
    // inside the panel must NOT trigger this, so target it directly.
    const scrim = document.querySelector('[aria-hidden="true"]');
    expect(scrim).not.toBeNull();
    await user.click(scrim as Element);
    expect(onClose).toHaveBeenCalled();
  });

  it("calls onClose on Escape", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    renderAt("/m/chats", true, onClose);
    await screen.findByRole("dialog", { name: "Navigation" });

    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalled();
  });

  it("calls onClose via the screen reader's close button", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    renderAt("/m/chats", true, onClose);
    await user.click(await screen.findByRole("button", { name: "Close menu" }));
    expect(onClose).toHaveBeenCalled();
  });

  it("calls onClose when navigating to a different route", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    renderAt("/m/inbox", true, onClose);
    await screen.findByRole("dialog", { name: "Navigation" });

    // A navigation the drawer did not start (the screen's own link).
    await user.click(screen.getByRole("link", { name: "go to chats" }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });
  it("traps focus: Tab from the last element wraps to the first", async () => {
    const user = userEvent.setup();
    renderAt("/m/chats", true, vi.fn());
    const dialog = await screen.findByRole("dialog", { name: "Navigation" });

    const focusable = dialog.querySelectorAll<HTMLElement>(
      'a[href], button:not([disabled])',
    );
    const first = focusable[0]!;
    const last = focusable[focusable.length - 1]!;

    await waitFor(() => expect(document.activeElement).toBe(first));

    last.focus();
    await user.tab();
    expect(document.activeElement).toBe(first);
  });

  it("traps focus: every Tab is moved by the trap, never left to the browser", async () => {
    // Safari's default Tab order skips links, so a trap that only wrapped at
    // the last element let focus escape past a trailing link. A bare keydown
    // (which moves nothing by itself) proves the trap advances focus on every
    // Tab, links included, and wraps at the end.
    renderAt("/m/chats", true, vi.fn());
    const dialog = await screen.findByRole("dialog", { name: "Navigation" });

    const focusable = Array.from(
      dialog.querySelectorAll<HTMLElement>('a[href], button:not([disabled])'),
    );
    await waitFor(() => expect(document.activeElement).toBe(focusable[0]));

    for (let i = 1; i <= focusable.length; i++) {
      const tab = new KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true });
      document.activeElement!.dispatchEvent(tab);
      expect(tab.defaultPrevented).toBe(true);
      expect(document.activeElement).toBe(focusable[i % focusable.length]);
    }
  });

  it("traps focus: Shift+Tab from the first element wraps to the last", async () => {
    const user = userEvent.setup();
    renderAt("/m/chats", true, vi.fn());
    const dialog = await screen.findByRole("dialog", { name: "Navigation" });

    const focusable = dialog.querySelectorAll<HTMLElement>(
      'a[href], button:not([disabled])',
    );
    const first = focusable[0]!;
    const last = focusable[focusable.length - 1]!;

    await waitFor(() => expect(document.activeElement).toBe(first));

    await user.tab({ shift: true });
    expect(document.activeElement).toBe(last);
  });

  it("moves focus into the panel on open and restores it on close", async () => {
    const user = userEvent.setup();

    function ToggleHost() {
      const [open, setOpen] = useState(false);
      return (
        <>
          <button onClick={() => setOpen(true)}>open menu</button>
          <MobileNavDrawer isOpen={open} onClose={() => setOpen(false)} />
        </>
      );
    }

    const rootRoute = createRootRoute({ component: () => <Outlet /> });
    const indexRoute = createRoute({
      getParentRoute: () => rootRoute,
      path: "/",
      component: ToggleHost,
    });
    const router = createRouter({
      routeTree: rootRoute.addChildren([indexRoute]),
      history: createMemoryHistory({ initialEntries: ["/"] }),
    });
    render(withQueries(<RouterProvider router={router} />));

    const trigger = await screen.findByRole("button", { name: "open menu" });
    trigger.focus();
    expect(document.activeElement).toBe(trigger);

    await user.click(trigger);
    const dialog = await screen.findByRole("dialog", { name: "Navigation" });
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
    expect(document.activeElement).not.toBe(trigger);

    await user.click(screen.getByRole("button", { name: "Close menu" }));
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });
});
