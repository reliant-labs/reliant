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
import type React from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { renderWithQuery } from "@/test/renderWithQuery";
import { getActiveOrgId, useActiveOrgStore } from "@/store/activeOrgStore";
import { useAuthStore } from "@/store/authStore";

const listMyOrganizations = vi.fn();
const setDefaultOrganization = vi.fn();
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
vi.mock("@/lib/toast-manager", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { Code, ConnectError } from "@connectrpc/connect";
import { setTransport } from "@/lib/connect";
import { mockTransport, type MockHandlers } from "@/lib/test-utils";
import { orgKeys } from "@/services/controlPlane/orgs";
import { secretStoreServiceKeys } from "@/hooks/controlplane/secret-store-service-hooks_gen";
import { myOrgsHandlers } from "@/test/orgsTransport";

import { ActiveOrgBinding } from "../ActiveOrgBinding";
import { OrgSwitcher } from "../OrgSwitcher";

const org = (id: string, name: string, over: Record<string, unknown> = {}) => ({
  id, name, slug: id, role: "owner", isOwner: true, isDefault: false, ...over,
});
const ACME = org("org-a", "Acme", { isDefault: true });
const SIDE = org("org-b", "Side Project", { role: "member", isOwner: false });

/** The membership list and its default, beside any other OrganizationService answers a test adds. */
const orgsTransport = (extra: MockHandlers = {}) =>
  mockTransport({
    ...myOrgsHandlers({
      listMyOrganizations: () => listMyOrganizations(),
      setDefaultOrganization: (id) => setDefaultOrganization(id),
    }),
    ...extra,
  });

function Harness({ variant, fallback }: { variant?: "sidebar" | "sidebar-top" | "list"; fallback?: React.ReactNode }) {
  return (
    <>
      <ActiveOrgBinding />
      <OrgSwitcher variant={variant} fallback={fallback} />
    </>
  );
}

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  listMyOrganizations.mockReset();
  setDefaultOrganization.mockReset().mockResolvedValue(undefined);
  useAuthStore.setState({ user: { id: "alice" } as never });
  // Unbound, so the binding loads what each test saved before it renders.
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false, failure: null });
  setTransport(orgsTransport());
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

  it("is the sidebar's top row: the brand mark and the org's name, the whole row the switcher", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, SIDE], defaultOrgId: "org-a" });
    renderWithQuery(<Harness variant="sidebar-top" />);
    const trigger = await screen.findByTestId("org-switcher");
    expect(screen.getByTestId("org-switcher-name")).toHaveTextContent("Acme");
    expect(trigger.querySelector("svg")).not.toBeNull();
    await userEvent.click(trigger);
    expect(await screen.findByTestId("org-option-org-b")).toHaveTextContent("Side Project");
  });

  it("renders its fallback, not nothing, with no org", () => {
    listMyOrganizations.mockReturnValue(new Promise(() => {}));
    renderWithQuery(<Harness variant="sidebar-top" fallback={<span data-testid="no-org">Reliant</span>} />);
    expect(screen.getByTestId("no-org")).toBeInTheDocument();
    expect(screen.queryByTestId("org-switcher")).toBeNull();
  });

  it("creating an organization makes it the org the app acts in", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME], defaultOrgId: "org-a" });
    const create = vi.fn((_req: unknown) => ({ organization: { id: "org-new", name: "New Co", slug: "new-co", role: "owner", isOwner: true } }));
    setTransport(orgsTransport({ "OrganizationService.CreateOrganization": create }));
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
      orgsTransport({
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
    await waitFor(() => expect(queryClient.getQueryData(orgKeys.mine)).toBeTruthy());
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

  it("drops generated secret listings and version histories when switching orgs", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, SIDE], defaultOrgId: "org-a" });
    const { queryClient } = renderWithQuery(<Harness />);
    await screen.findByTestId("org-switcher");
    await waitFor(() => expect(getActiveOrgId()).toBe("org-a"));

    // Seed after the initial binding settles: this must test the switch, not
    // first-load cleanup or the test client's immediate garbage collection.
    queryClient.setQueryDefaults(secretStoreServiceKeys.all, { gcTime: Infinity });
    const listingKey = secretStoreServiceKeys.listSecrets({ environmentId: "acme-env" });
    const historyKey = secretStoreServiceKeys.getSecretVersions({ environmentId: "acme-env", name: "API_KEY" });
    const listing = { secrets: [{ name: "API_KEY" }], protected: false };
    const history = { versions: [{ version: "1" }] };
    queryClient.setQueryData(listingKey, listing);
    queryClient.setQueryData(historyKey, history);
    expect(queryClient.getQueryData(listingKey)).toEqual(listing);
    expect(queryClient.getQueryData(historyKey)).toEqual(history);

    await userEvent.click(screen.getByTestId("org-switcher"));
    await userEvent.click(await screen.findByRole("menuitemradio", { name: /Side Project/ }));

    await waitFor(() => expect(getActiveOrgId()).toBe("org-b"));
    await waitFor(() => {
      expect(queryClient.getQueryData(listingKey)).toBeUndefined();
      expect(queryClient.getQueryData(historyKey)).toBeUndefined();
    });
  });

  it("tells the product app about a tab's first org, then about each switch, once", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, SIDE], defaultOrgId: "org-a" });
    const onSwitch = vi.fn();
    const { queryClient } = renderWithQuery(
      <>
        <ActiveOrgBinding onSwitch={onSwitch} />
        <OrgSwitcher />
      </>,
    );
    await waitFor(() => expect(getActiveOrgId()).toBe("org-a"));
    // Nothing was saved, so the tab went from naming no org to org-a: what was
    // read with no org (every org's rows) must not stay cached.
    await waitFor(() => expect(onSwitch).toHaveBeenCalledTimes(1));
    expect(onSwitch).toHaveBeenLastCalledWith("org-a", queryClient, { firstLoad: true });

    await userEvent.click(await screen.findByTestId("org-switcher"));
    await userEvent.click(await screen.findByRole("menuitemradio", { name: /Side Project/ }));
    await waitFor(() => expect(onSwitch).toHaveBeenCalledTimes(2));
    expect(onSwitch).toHaveBeenLastCalledWith("org-b", queryClient, { firstLoad: false });
  });

  it("says nothing on the first load of a tab that already had its org", async () => {
    sessionStorage.setItem("reliant.activeOrg.alice", "org-b");
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, SIDE], defaultOrgId: "org-a" });
    const onSwitch = vi.fn();
    renderWithQuery(<ActiveOrgBinding onSwitch={onSwitch} />);
    await waitFor(() => expect(useActiveOrgStore.getState().resolved).toBe(true));
    expect(getActiveOrgId()).toBe("org-b");
    expect(onSwitch).not.toHaveBeenCalled();
  });

  it("a switch in another tab does not move this one", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, SIDE], defaultOrgId: "org-a" });
    const onSwitch = vi.fn();
    renderWithQuery(<ActiveOrgBinding onSwitch={onSwitch} />);
    await waitFor(() => expect(getActiveOrgId()).toBe("org-a"));
    onSwitch.mockClear();

    // Another tab picks Side Project: the browser writes the shared last
    // choice and fires `storage` in every other tab of the origin. A listener
    // runs synchronously inside dispatchEvent, and act() flushes any store
    // update and effect it causes, so the outcome is settled when act returns.
    act(() => {
      localStorage.setItem("reliant.activeOrg.alice", "org-b");
      window.dispatchEvent(new StorageEvent("storage", { key: "reliant.activeOrg.alice", newValue: "org-b" }));
    });
    expect(getActiveOrgId()).toBe("org-a");
    expect(onSwitch).not.toHaveBeenCalled();
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
      await queryClient.refetchQueries({ queryKey: orgKeys.mine });
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
    await new Promise((r) => setTimeout(r, 20));
    expect(listMyOrganizations).not.toHaveBeenCalled();
    expect(useActiveOrgStore.getState().userId).toBeNull();
  });
});
