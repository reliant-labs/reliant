/**
 * The org switcher and the binding that keeps the transport's org true.
 *
 * Pins the product rules: one org shows its name and "Create organization"
 * (the phone list, a pure choice, still needs two); two or more list every
 * org and mark the server default; creating one switches to it; choosing one changes the org the next
 * control-plane request names and drops what the previous org cached; and a
 * person who left the org they had selected falls back to the default instead
 * of continuing to send an org the control plane would refuse.
 */
import { act, render, screen, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { renderWithQuery } from "@/test/renderWithQuery";
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
vi.mock("@/lib/toast-manager", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { Code, ConnectError } from "@connectrpc/connect";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";

import { ActiveOrgBinding } from "../ActiveOrgBinding";
import { OrgSwitcher } from "../OrgSwitcher";

const org = (id: string, name: string, over: Record<string, unknown> = {}) => ({
  id, name, slug: id, role: "owner", isOwner: true, isDefault: false, ...over,
});
const ACME = org("org-a", "Acme", { isDefault: true });
const SIDE = org("org-b", "Side Project", { role: "member", isOwner: false });

function Harness({ variant }: { variant?: "sidebar" | "list" }) {
  return (
    <>
      <ActiveOrgBinding />
      <OrgSwitcher variant={variant} />
    </>
  );
}

beforeEach(() => {
  localStorage.clear();
  listMyOrganizations.mockReset();
  setDefaultOrganization.mockReset().mockResolvedValue(undefined);
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null });
  useAuthStore.setState({ user: { id: "alice" } as never });
  setTransport(null);
});

describe("OrgSwitcher", () => {
  it("shows a person with one org its name and a way to create another", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME], defaultOrgId: "org-a" });
    renderWithQuery(<Harness />);
    expect(await screen.findByTestId("org-switcher-name")).toHaveTextContent("Acme");
    await waitFor(() => expect(getActiveOrgId()).toBe("org-a"));
    await userEvent.click(screen.getByTestId("org-switcher"));
    expect(await screen.findByTestId("org-create")).toHaveTextContent("Create organization");
  });

  it("creating an organization makes it the org the app acts in", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME], defaultOrgId: "org-a" });
    const create = vi.fn((_req: unknown) => ({ organization: { id: "org-new", name: "New Co", slug: "new-co", role: "owner", isOwner: true } }));
    setTransport(mockTransport({ "OrganizationService.CreateOrganization": create }));
    // The app's one client: switching re-reads the membership list through it.
    const { queryClient } = await import("@/lib/query-client");
    queryClient.clear();
    render(
      <QueryClientProvider client={queryClient}>
        <Harness />
      </QueryClientProvider>,
    );
    await userEvent.click(await screen.findByTestId("org-switcher"));
    await userEvent.click(await screen.findByTestId("org-create"));
    await userEvent.type(await screen.findByTestId("create-org-name"), "  New Co ");
    // The new org is in the list the binding reconciles against once re-read.
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, org("org-new", "New Co")], defaultOrgId: "org-a" });
    await userEvent.click(screen.getByTestId("create-org-submit"));
    await waitFor(() => expect(create).toHaveBeenCalledWith(expect.objectContaining({ name: "New Co" })));
    await waitFor(() => expect(getActiveOrgId()).toBe("org-new"));
    expect(localStorage.getItem("reliant.activeOrg.alice")).toBe("org-new");
  });

  it("a refused create says why in words", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME], defaultOrgId: "org-a" });
    setTransport(
      mockTransport({
        "OrganizationService.CreateOrganization": {
          error: new ConnectError("prose", Code.ResourceExhausted, { "x-forge-error-reason": "owned_org_limit" }),
        },
      }),
    );
    renderWithQuery(<Harness />);
    await userEvent.click(await screen.findByTestId("org-switcher"));
    await userEvent.click(await screen.findByTestId("org-create"));
    await userEvent.type(await screen.findByTestId("create-org-name"), "Third");
    await userEvent.click(screen.getByTestId("create-org-submit"));
    expect(await screen.findByTestId("create-org-error")).toHaveTextContent(/as many organizations as one person can/);
    expect(getActiveOrgId()).toBe("org-a");
  });

  it("the phone list still needs a choice to show", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME], defaultOrgId: "org-a" });
    const { container, queryClient } = renderWithQuery(<Harness variant="list" />);
    await waitFor(() => expect(queryClient.getQueryData(["orgs", "mine"])).toBeTruthy());
    expect(container.querySelector('[data-testid="org-list"]')).toBeNull();
  });

  it("lists every org with the default marked, starting on the default", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, SIDE], defaultOrgId: "org-a" });
    renderWithQuery(<Harness />);
    const trigger = await screen.findByTestId("org-switcher");
    expect(screen.getByTestId("org-switcher-name")).toHaveTextContent("Acme");

    await userEvent.click(trigger);
    const acme = await screen.findByRole("menuitemradio", { name: /Acme/ });
    expect(acme).toHaveAttribute("aria-checked", "true");
    expect(acme).toHaveTextContent("Default");
    expect(screen.getByRole("menuitemradio", { name: /Side Project/ })).toHaveAttribute("aria-checked", "false");
  });

  it("choosing an org changes what the next request names, remembers it, and resets org-scoped caches", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, SIDE], defaultOrgId: "org-a" });
    const { queryClient } = renderWithQuery(<Harness />);
    // The test client garbage-collects unobserved entries at once; these must
    // survive until the switch, or "was reset" would pass for the wrong reason.
    for (const root of ["forge", "chats"]) queryClient.setQueryDefaults([root], { gcTime: Infinity });
    queryClient.setQueryData(["forge", "cloud-envs", "app"], ["acme-env"]);
    queryClient.setQueryData(["chats", "list"], ["a chat"]);
    expect(queryClient.getQueryData(["forge", "cloud-envs", "app"])).toEqual(["acme-env"]);
    await screen.findByTestId("org-switcher");
    await waitFor(() => expect(getActiveOrgId()).toBe("org-a"));

    await userEvent.click(screen.getByTestId("org-switcher"));
    await userEvent.click(await screen.findByRole("menuitemradio", { name: /Side Project/ }));

    await waitFor(() => expect(getActiveOrgId()).toBe("org-b"));
    expect(localStorage.getItem("reliant.activeOrg.alice")).toBe("org-b");
    await waitFor(() => expect(queryClient.getQueryData(["forge", "cloud-envs", "app"])).toBeUndefined());
    // The binding drops only the org-scoped roots; the product's lists are
    // its onSwitch's to reset (followActiveOrg), which this harness omits.
    expect(queryClient.getQueryData(["chats", "list"])).toEqual(["a chat"]);
    expect(screen.getByTestId("org-switcher-name")).toHaveTextContent("Side Project");
  });

  it("tells the product app about a switch, once, and not about the first load", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, SIDE], defaultOrgId: "org-a" });
    const onSwitch = vi.fn();
    const { queryClient } = renderWithQuery(
      <>
        <ActiveOrgBinding onSwitch={onSwitch} />
        <OrgSwitcher />
      </>,
    );
    await waitFor(() => expect(getActiveOrgId()).toBe("org-a"));
    expect(onSwitch).not.toHaveBeenCalled();

    await userEvent.click(await screen.findByTestId("org-switcher"));
    await userEvent.click(await screen.findByRole("menuitemradio", { name: /Side Project/ }));
    await waitFor(() => expect(onSwitch).toHaveBeenCalledTimes(1));
    expect(onSwitch).toHaveBeenCalledWith("org-b", queryClient);
  });

  it("restores the person's own choice on the next visit", async () => {
    localStorage.setItem("reliant.activeOrg.alice", "org-b");
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, SIDE], defaultOrgId: "org-a" });
    renderWithQuery(<Harness />);
    expect(await screen.findByTestId("org-switcher-name")).toHaveTextContent("Side Project");
    expect(getActiveOrgId()).toBe("org-b");
  });

  it("falls back to the default, and forgets the choice, when the person has left the org", async () => {
    localStorage.setItem("reliant.activeOrg.alice", "org-gone");
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, SIDE], defaultOrgId: "org-a" });
    renderWithQuery(<Harness />);
    expect(await screen.findByTestId("org-switcher-name")).toHaveTextContent("Acme");
    await waitFor(() => expect(getActiveOrgId()).toBe("org-a"));
    await waitFor(() => expect(localStorage.getItem("reliant.activeOrg.alice")).toBeNull());
  });

  it("drops caches fetched under a stale remembered org once it reconciles away from it", async () => {
    localStorage.setItem("reliant.activeOrg.alice", "org-gone");
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, SIDE], defaultOrgId: "org-a" });
    const { queryClient } = renderWithQuery(<Harness />);
    queryClient.setQueryDefaults(["observability"], { gcTime: Infinity });
    queryClient.setQueryData(["observability", "org-gone", "x"], "stale");
    expect(queryClient.getQueryData(["observability", "org-gone", "x"])).toBe("stale");
    await waitFor(() => expect(getActiveOrgId()).toBe("org-a"));
    await waitFor(() => expect(queryClient.getQueryData(["observability", "org-gone", "x"])).toBeUndefined());
  });

  it("follows leaving the org the person is acting in, on refetch", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, SIDE], defaultOrgId: "org-a" });
    const { queryClient } = renderWithQuery(<Harness />);
    await screen.findByTestId("org-switcher");
    await userEvent.click(screen.getByTestId("org-switcher"));
    await userEvent.click(await screen.findByRole("menuitemradio", { name: /Side Project/ }));
    await waitFor(() => expect(getActiveOrgId()).toBe("org-b"));

    // They are removed from Side Project elsewhere; the list is re-read.
    listMyOrganizations.mockResolvedValue({ orgs: [ACME], defaultOrgId: "org-a" });
    await act(async () => {
      await queryClient.refetchQueries({ queryKey: ["orgs", "mine"] });
    });
    await waitFor(() => expect(getActiveOrgId()).toBe("org-a"));
  });

  it("offers to make the active org the default only when it is not already", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, SIDE], defaultOrgId: "org-a" });
    renderWithQuery(<Harness />);
    await userEvent.click(await screen.findByTestId("org-switcher"));
    expect(screen.queryByTestId("org-make-default")).toBeNull();

    await userEvent.click(await screen.findByRole("menuitemradio", { name: /Side Project/ }));
    await userEvent.click(await screen.findByTestId("org-switcher"));
    await userEvent.click(await screen.findByTestId("org-make-default"));
    await waitFor(() => expect(setDefaultOrganization).toHaveBeenCalledWith("org-b"));
  });

  it("the list variant (phone) shows every org as a 44px radio row", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, SIDE], defaultOrgId: "org-a" });
    renderWithQuery(<Harness variant="list" />);
    const rows = await screen.findAllByRole("radio");
    expect(rows).toHaveLength(2);
    for (const r of rows) expect(r.className).toContain("min-h-[44px]");
    await userEvent.click(screen.getByRole("radio", { name: /Side Project/ }));
    await waitFor(() => expect(getActiveOrgId()).toBe("org-b"));
  });

  it("a signed-out session names no org", async () => {
    useAuthStore.setState({ user: null });
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, SIDE], defaultOrgId: "org-a" });
    renderWithQuery(<Harness />);
    await waitFor(() => expect(listMyOrganizations).toHaveBeenCalled());
    expect(useActiveOrgStore.getState().userId).toBeNull();
  });
});
