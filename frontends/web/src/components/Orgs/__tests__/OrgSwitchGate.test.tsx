// Copyright (c) 2025 Reliant Labs

/**
 * A link that names an org (`?org=`) opened while the app acts in another.
 * The owner's rule: ask before switching, never switch silently, and render
 * nothing that acts until the person has chosen.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { ActiveOrg } from "@/hooks/useOrgs";

const select = vi.fn();
const refetch = vi.fn();
const orgState: { value: Partial<ActiveOrg> } = { value: {} };
vi.mock("@/hooks/useOrgs", () => ({
  useActiveOrg: () => ({
    orgs: [],
    active: null,
    activeOrgId: null,
    defaultOrgId: "",
    canSwitch: false,
    isLoading: false,
    error: null,
    refetch,
    select,
    ...orgState.value,
  }),
}));
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { OrgSwitchGate } from "../OrgSwitchGate";

const org = (id: string, name: string) => ({ id, name, slug: id, role: "owner", isOwner: true, isDefault: false });
const ACME = org("org-a", "Acme");
const SIDE = org("org-b", "Side Project");

function inOrgs(activeId: string) {
  const orgs = [ACME, SIDE];
  orgState.value = { orgs, active: orgs.find((o) => o.id === activeId) ?? null, activeOrgId: activeId, canSwitch: true };
}

function renderGate(requestedOrgId: string | undefined, onDismiss = vi.fn()) {
  render(
    <OrgSwitchGate requestedOrgId={requestedOrgId} onDismiss={onDismiss} subject="billing link">
      <div data-testid="page">billing</div>
    </OrgSwitchGate>
  );
  return onDismiss;
}

beforeEach(() => {
  select.mockReset();
  refetch.mockReset();
  orgState.value = {};
});

describe("OrgSwitchGate", () => {
  it("renders the page when the link names no org", () => {
    inOrgs("org-a");
    renderGate(undefined);
    expect(screen.getByTestId("page")).toBeInTheDocument();
    expect(screen.queryByTestId("org-link-gate")).toBeNull();
  });

  it("renders the page when the link names the active org", () => {
    inOrgs("org-a");
    renderGate("org-a");
    expect(screen.getByTestId("page")).toBeInTheDocument();
  });

  it("shows a quiet status line, not the page, while the membership list loads", () => {
    orgState.value = { isLoading: true };
    renderGate("org-b");
    expect(screen.getByTestId("org-link-gate")).toHaveAttribute("data-state", "loading");
    expect(screen.getByRole("status")).toHaveTextContent("Checking which organization this billing link is for");
    expect(screen.queryByTestId("page")).toBeNull();
  });

  it("asks before switching to another org the person belongs to, and shows nothing that acts", () => {
    inOrgs("org-a");
    renderGate("org-b");
    const gate = screen.getByTestId("org-link-gate");
    expect(gate).toHaveAttribute("data-state", "switch");
    expect(gate).toHaveTextContent("This billing link is for Side Project");
    expect(gate).toHaveTextContent("You're acting in Acme");
    expect(screen.getByTestId("org-link-switch")).toHaveTextContent("Switch to Side Project");
    expect(screen.getByTestId("org-link-stay")).toHaveTextContent("Stay in Acme");
    // The page is not rendered underneath: nobody authorizes spend in the wrong org.
    expect(screen.queryByTestId("page")).toBeNull();
    // And nothing switched on its own.
    expect(select).not.toHaveBeenCalled();
  });

  it("switches only when asked to, then drops the link's org", async () => {
    inOrgs("org-a");
    const onDismiss = renderGate("org-b");
    await userEvent.click(screen.getByTestId("org-link-switch"));
    expect(select).toHaveBeenCalledWith("org-b");
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it("staying keeps the active org and drops the link's org", async () => {
    inOrgs("org-a");
    const onDismiss = renderGate("org-b");
    await userEvent.click(screen.getByTestId("org-link-stay"));
    expect(select).not.toHaveBeenCalled();
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it("says so when the person is not a member of the link's org, and offers to continue", async () => {
    inOrgs("org-a");
    const onDismiss = renderGate("org-zzz");
    const gate = screen.getByTestId("org-link-gate");
    expect(gate).toHaveAttribute("data-state", "not-member");
    expect(gate).toHaveTextContent("You're not a member of the organization this billing link is for");
    expect(screen.queryByTestId("org-link-switch")).toBeNull();
    expect(screen.queryByTestId("page")).toBeNull();
    await userEvent.click(screen.getByTestId("org-link-stay"));
    expect(screen.getByTestId("org-link-stay")).toHaveTextContent("Continue in Acme");
    expect(onDismiss).toHaveBeenCalledTimes(1);
    expect(select).not.toHaveBeenCalled();
  });

  it("offers a retry when the membership list failed", async () => {
    orgState.value = { error: new Error("unavailable") };
    renderGate("org-b");
    expect(screen.getByTestId("org-link-gate")).toHaveAttribute("data-state", "error");
    await userEvent.click(screen.getByTestId("org-link-retry"));
    expect(refetch).toHaveBeenCalled();
    expect(screen.queryByTestId("page")).toBeNull();
  });

  it("names the environment for an environment link", () => {
    inOrgs("org-a");
    render(
      <OrgSwitchGate requestedOrgId="org-b" onDismiss={vi.fn()} subject="environment link" testIdPrefix="env-org">
        <div data-testid="page" />
      </OrgSwitchGate>
    );
    expect(screen.getByTestId("env-org-gate")).toHaveTextContent("This environment link is for Side Project");
    expect(screen.getByTestId("env-org-switch")).toBeInTheDocument();
  });
});
