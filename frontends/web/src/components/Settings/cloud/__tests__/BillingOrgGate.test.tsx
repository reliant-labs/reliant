// Copyright (c) 2025 Reliant Labs

/**
 * `/settings/billing?org=<id>`: billing acts in the ACTIVE org, so a link for
 * another one asks before switching (owner decision) and renders no billing
 * until the person has chosen. Staying, or a link for an org they are not in,
 * drops `?org=` from the URL and shows the active org's billing.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { ActiveOrg } from "@/hooks/useOrgs";

const navigate = vi.fn();
const search: { org?: string } = {};
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigate,
  useSearch: () => search,
}));

const select = vi.fn();
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
    refetch: vi.fn(),
    select,
    ...orgState.value,
  }),
}));
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { BillingOrgGate } from "../BillingOrgGate";

const org = (id: string, name: string) => ({ id, name, slug: id, role: "owner", isOwner: true, isDefault: false });
const ORGS = [org("org-a", "Acme"), org("org-b", "Side Project")];

function renderGate() {
  return render(
    <BillingOrgGate>
      <div data-testid="billing-content">tabs</div>
    </BillingOrgGate>
  );
}

/** The search the last navigate produced from `prev`. */
function strippedFrom(prev: Record<string, unknown>) {
  const call = navigate.mock.calls.at(-1)?.[0] as { to: string; replace: boolean; search: (p: Record<string, unknown>) => Record<string, unknown> };
  expect(call.to).toBe(".");
  expect(call.replace).toBe(true);
  return call.search(prev);
}

beforeEach(() => {
  navigate.mockReset();
  select.mockReset();
  delete search.org;
  orgState.value = { orgs: ORGS, active: ORGS[0], activeOrgId: "org-a", canSwitch: true };
});

describe("BillingOrgGate", () => {
  it("renders billing when the link names no org", () => {
    renderGate();
    expect(screen.getByTestId("billing-content")).toBeInTheDocument();
    expect(screen.queryByTestId("billing-org-gate")).toBeNull();
  });

  it("renders billing when the link names the active org", () => {
    search.org = "org-a";
    renderGate();
    expect(screen.getByTestId("billing-content")).toBeInTheDocument();
  });

  it("shows a status line while the org list loads", () => {
    search.org = "org-b";
    orgState.value = { isLoading: true };
    renderGate();
    expect(screen.getByTestId("billing-org-gate")).toHaveAttribute("data-state", "loading");
    expect(screen.queryByTestId("billing-content")).toBeNull();
  });

  it("asks before switching to the link's org, and renders no billing until asked", async () => {
    search.org = "org-b";
    const { rerender } = renderGate();
    expect(screen.getByRole("heading")).toHaveTextContent("This billing link is for Side Project");
    expect(screen.getByTestId("billing-org-gate")).toHaveTextContent("You're acting in Acme.");
    expect(screen.queryByTestId("billing-content")).toBeNull();
    expect(select).not.toHaveBeenCalled();

    await userEvent.click(screen.getByTestId("billing-org-switch"));
    expect(select).toHaveBeenCalledWith("org-b");
    expect(strippedFrom({ tab: "hosting", org: "org-b" })).toEqual({ tab: "hosting", org: undefined });

    // The switch lands: billing renders for the link's org.
    orgState.value = { orgs: ORGS, active: ORGS[1], activeOrgId: "org-b", canSwitch: true };
    rerender(
      <BillingOrgGate>
        <div data-testid="billing-content">tabs</div>
      </BillingOrgGate>
    );
    expect(screen.getByTestId("billing-content")).toBeInTheDocument();
  });

  it("staying strips the link's org and keeps the active one", async () => {
    search.org = "org-b";
    renderGate();
    await userEvent.click(screen.getByTestId("billing-org-stay"));
    expect(select).not.toHaveBeenCalled();
    expect(strippedFrom({ tab: "hosting", from: "chat", org: "org-b" })).toEqual({ tab: "hosting", from: "chat", org: undefined });
  });

  it("a link for an org the person is not in offers to continue in theirs", async () => {
    search.org = "org-elsewhere";
    renderGate();
    expect(screen.getByTestId("billing-org-gate")).toHaveTextContent("You're not a member of the organization this billing link is for");
    expect(screen.queryByTestId("billing-content")).toBeNull();
    await userEvent.click(screen.getByTestId("billing-org-stay"));
    expect(screen.getByTestId("billing-org-stay")).toHaveTextContent("Continue in Acme");
    expect(strippedFrom({ org: "org-elsewhere" })).toEqual({ org: undefined });
    expect(select).not.toHaveBeenCalled();
  });
});
