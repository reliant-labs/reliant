/**
 * The mobile drawer's org card, against the real active-org store and binding
 * and a real router: it names the tab's org, a switch changes THIS tab's org
 * (sessionStorage, never the server) and leaves the old org's open chat for
 * the chat list, and a person with one org gets the name with no picker.
 */
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { createTestQueryClient } from "@/test/renderWithQuery";
import { getActiveOrgId, useActiveOrgStore } from "@/store/activeOrgStore";
import { useAuthStore } from "@/store/authStore";

const listMyOrganizations = vi.fn();
const setDefaultOrganization = vi.fn();
vi.mock("@/services/controlPlane/orgs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/orgs")>()),
  listMyOrganizations: () => listMyOrganizations(),
  setDefaultOrganization: (id: string) => setDefaultOrganization(id),
}));
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

const { ActiveOrgBinding } = await import("../../Orgs/ActiveOrgBinding");
const { MobileOrgSwitcher } = await import("../MobileOrgSwitcher");

const org = (id: string, name: string, over: Record<string, unknown> = {}) => ({
  id, name, slug: id, role: "owner", isOwner: true, isDefault: false, ...over,
});
const ACME = org("org-a", "Acme", { isDefault: true });
const SIDE = org("org-b", "Side Project", { role: "member", isOwner: false });

function renderAt(path: string, onSwitched = vi.fn()) {
  const rootRoute = createRootRoute({
    component: () => (
      <>
        <ActiveOrgBinding />
        <MobileOrgSwitcher onSwitched={onSwitched} />
        <Outlet />
      </>
    ),
  });
  const chats = createRoute({ getParentRoute: () => rootRoute, path: "/m/chats", component: () => <p>chat list</p> });
  const chat = createRoute({
    getParentRoute: () => rootRoute,
    path: "/m/chats/$chatId",
    component: function Chat() {
      return <p>chat:{chat.useParams().chatId}</p>;
    },
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([chats, chat]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(
    <QueryClientProvider client={createTestQueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { router, onSwitched };
}

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  listMyOrganizations.mockReset();
  setDefaultOrganization.mockReset().mockResolvedValue(undefined);
  useAuthStore.setState({ user: { id: "alice" } as never });
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false, failure: null });
});

describe("MobileOrgSwitcher", () => {
  it("shows the org this tab acts in", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, SIDE], defaultOrgId: "org-a" });
    renderAt("/m/chats");
    expect(await screen.findByTestId("mobile-org-name")).toHaveTextContent("Acme");
    await waitFor(() => expect(getActiveOrgId()).toBe("org-a"));
    // Collapsed until asked: the drawer's destinations stay on screen.
    expect(screen.queryByTestId("mobile-org-list")).not.toBeInTheDocument();
  });

  it("switching changes this tab's org and leaves the old org's chat", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, SIDE], defaultOrgId: "org-a" });
    const { router, onSwitched } = renderAt("/m/chats/chat-in-acme");
    expect(await screen.findByText("chat:chat-in-acme")).toBeInTheDocument();
    await waitFor(() => expect(getActiveOrgId()).toBe("org-a"));

    const toggle = screen.getByRole("button", { name: /switch organization/i });
    await userEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("radio", { name: /Acme/ })).toHaveAttribute("aria-checked", "true");
    await userEvent.click(screen.getByRole("radio", { name: /Side Project/ }));

    // The tab's org, through the desktop's store: sessionStorage, no server write.
    expect(getActiveOrgId()).toBe("org-b");
    expect(sessionStorage.getItem("reliant.activeOrg.alice")).toBe("org-b");
    expect(setDefaultOrganization).not.toHaveBeenCalled();
    // Off the old org's chat, onto the chat list.
    await waitFor(() => expect(router.state.location.pathname).toBe("/m/chats"));
    expect(screen.queryByText("chat:chat-in-acme")).not.toBeInTheDocument();
    expect(screen.getByText("chat list")).toBeInTheDocument();
    expect(onSwitched).toHaveBeenCalledTimes(1);
    expect(await screen.findByTestId("mobile-org-name")).toHaveTextContent("Side Project");
    expect(screen.queryByTestId("mobile-org-list")).not.toBeInTheDocument();
  });

  it("re-choosing the current org goes nowhere", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, SIDE], defaultOrgId: "org-a" });
    const { router, onSwitched } = renderAt("/m/chats/chat-in-acme");
    await waitFor(() => expect(getActiveOrgId()).toBe("org-a"));
    await userEvent.click(await screen.findByRole("button", { name: /switch organization/i }));
    await userEvent.click(screen.getByRole("radio", { name: /Acme/ }));
    expect(router.state.location.pathname).toBe("/m/chats/chat-in-acme");
    expect(onSwitched).not.toHaveBeenCalled();
  });

  it("shows a person with one org its name and no picker", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME], defaultOrgId: "org-a" });
    renderAt("/m/chats");
    expect(await screen.findByTestId("mobile-org-name")).toHaveTextContent("Acme");
    expect(screen.queryByRole("button", { name: /switch organization/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("radio")).not.toBeInTheDocument();
  });
});
