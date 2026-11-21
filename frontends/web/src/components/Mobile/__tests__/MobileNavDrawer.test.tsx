/**
 * The navigation drawer — open/close mechanics, closing on route change and
 * scrim tap, Escape, and the focus trap. Rendered against a real router
 * (memory history) rather than a mocked `useRouterState`, same reasoning as
 * `MobileTabBar.test.tsx` had: the thing worth pinning is that the drawer
 * reads the router's own location, not whatever a mock hands it.
 */

import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
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

vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (selector: (s: unknown) => unknown) =>
    selector({ currentProject: { id: "p1", name: "reliant" } }),
}));

// No org resolved: the org card renders nothing, so these tests need no
// QueryClient (its own tests are in MobileOrgSwitcher.test.tsx).
vi.mock("../../../hooks/useOrgs", () => ({
  useActiveOrg: () => ({ orgs: [], active: null, canSwitch: false, select: vi.fn() }),
}));

// The nav itself (the one hybrid model) is covered in
// Layout/sidebar/__tests__/MobileHybridNav.test.tsx; here it is a stand-in
// with a destination and a drill-in, to pin the drawer's own mechanics.
vi.mock("../../Layout/sidebar/MobileHybridNav", () => ({
  MobileHybridNav: ({ onNavigate }: { onNavigate: (href: string, options?: { drill?: boolean }) => void }) => (
    <div data-testid="mobile-hybrid-nav">
      <a
        href="/m/daemons"
        onClick={(event) => {
          event.preventDefault();
          onNavigate("/m/daemons");
        }}
      >
        Sandboxes
      </a>
      <a
        href="/m/inbox"
        onClick={(event) => {
          event.preventDefault();
          onNavigate("/m/inbox", { drill: true });
        }}
      >
        Drill in
      </a>
    </div>
  ),
}));

const { MobileNavDrawer } = await import("../MobileNavDrawer");

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

  const paths = ["/m/chats", "/m/new", "/m/daemons", "/m/github", "/m/account", "/m/inbox"];

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
  render(<RouterProvider router={router} />);
  return router;
}

describe("MobileNavDrawer", () => {
  it("renders nothing when closed", () => {
    renderAt("/m/chats", false, vi.fn());
    expect(screen.queryByRole("dialog", { name: "Navigation" })).not.toBeInTheDocument();
  });

  it("draws the one nav model under the org and the project", async () => {
    renderAt("/m/chats", true, vi.fn());
    const dialog = await screen.findByRole("dialog", { name: "Navigation" });
    const nav = within(dialog).getByTestId("mobile-hybrid-nav");
    const project = screen.getByRole("link", { name: /Project/ });
    expect(project).toHaveAttribute("href", "/m/projects");
    expect(screen.getByText("reliant")).toBeInTheDocument();
    // The switcher's place: above the nav.
    expect(project.compareDocumentPosition(nav) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("stays open through a drill-in, and closes on a destination", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const router = renderAt("/m/chats", true, onClose);
    await screen.findByRole("dialog", { name: "Navigation" });
    await user.click(screen.getByRole("link", { name: "Drill in" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/m/inbox"));
    expect(onClose).not.toHaveBeenCalled();
    await user.click(screen.getByRole("link", { name: "Sandboxes" }));
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

  it("calls onClose via the explicit close button", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    renderAt("/m/chats", true, onClose);
    await user.click(await screen.findByRole("button", { name: "Close menu" }));
    expect(onClose).toHaveBeenCalled();
  });

  it("calls onClose when navigating to a different route", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    renderAt("/m/chats", true, onClose);
    await screen.findByRole("dialog", { name: "Navigation" });

    await user.click(screen.getByRole("link", { name: /Sandboxes/ }));
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
    render(<RouterProvider router={router} />);

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
