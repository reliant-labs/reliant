/**
 * The navigation drawer — open/close mechanics, closing on route change and
 * scrim tap, Escape, and the focus trap. Rendered against a real router
 * (memory history) rather than a mocked `useRouterState`, same reasoning as
 * `MobileTabBar.test.tsx` had: the thing worth pinning is that the drawer
 * reads the router's own location, not whatever a mock hands it.
 */

import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
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

  const paths = ["/m/chats", "/m/new", "/m/daemons", "/m/github", "/m/account"];

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

  it("renders all seven destinations plus the project switcher when open", async () => {
    renderAt("/m/chats", true, vi.fn());
    const dialog = await screen.findByRole("dialog", { name: "Navigation" });
    expect(dialog).toBeInTheDocument();

    expect(screen.getByRole("link", { name: /New chat/ })).toHaveAttribute("href", "/m/new");
    expect(screen.getByRole("link", { name: /Search/ })).toHaveAttribute("href", "/m/search");
    expect(screen.getByRole("link", { name: /Workflows/ })).toHaveAttribute("href", "/m/workflows");
    expect(screen.getByRole("link", { name: /Machines/ })).toHaveAttribute("href", "/m/daemons");
    expect(screen.getByRole("link", { name: /GitHub/ })).toHaveAttribute("href", "/m/github");
    expect(screen.getByRole("link", { name: /Settings/ })).toHaveAttribute("href", "/m/settings");
    expect(screen.getByRole("link", { name: /Account/ })).toHaveAttribute("href", "/m/account");
    expect(screen.getByRole("link", { name: /Project/ })).toHaveAttribute("href", "/m/projects");
    expect(screen.getByText("reliant")).toBeInTheDocument();
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

    await user.click(screen.getByRole("link", { name: /Machines/ }));
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
