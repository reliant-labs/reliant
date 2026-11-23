/**
 * The sidebar header's org row (U6, owner): the org's avatar, its name, the
 * plan badge, one ⇕. The badge names the billing plan the app already reads
 * (GetCurrentUserComputeSubscription), and is hidden whenever that plan is
 * not known to be this org's live plan: loading, failed, none, canceled, or
 * another org's.
 */
import { create } from "@bufbuild/protobuf";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { PlanSchema, PlanTier, SubscriptionSchema, SubscriptionStatus } from "@/gen/controlplane/controlplane/v1/shared_pb";
import { renderWithQuery } from "@/test/renderWithQuery";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import { useAuthStore } from "@/store/authStore";

const listMyOrganizations = vi.fn();
const getCurrentUserComputeSubscription = vi.fn();
vi.mock("@/services/controlPlane/orgs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/orgs")>()),
  listMyOrganizations: () => listMyOrganizations(),
}));
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
vi.mock("@/services/controlPlane/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/client")>()),
  getControlPlaneClient: () => ({ getCurrentUserComputeSubscription: () => getCurrentUserComputeSubscription() }),
}));

import { ActiveOrgBinding } from "../ActiveOrgBinding";
import { OrgSwitcher } from "../OrgSwitcher";
import { orgPlanLabel } from "../orgPlan";

const ACME = { id: "org-a", name: "Acme Robotics International", slug: "acme", role: "owner", isOwner: true, isDefault: true };
const plan = (tier: PlanTier, name = "Compute M") => create(PlanSchema, { id: "plan", name, tier });
const sub = (over: { orgId?: string; status?: SubscriptionStatus; tier?: PlanTier; name?: string } = {}) =>
  create(SubscriptionSchema, {
    id: "sub",
    orgId: over.orgId ?? "org-a",
    status: over.status ?? SubscriptionStatus.ACTIVE,
    plan: plan(over.tier ?? PlanTier.PRO, over.name),
  });

describe("orgPlanLabel", () => {
  it("names the tier, else the plan's own name", () => {
    expect(orgPlanLabel(sub({ tier: PlanTier.PRO }), "org-a")).toBe("Pro");
    expect(orgPlanLabel(sub({ tier: PlanTier.STARTER }), "org-a")).toBe("Starter");
    expect(orgPlanLabel(sub({ tier: PlanTier.ENTERPRISE }), "org-a")).toBe("Enterprise");
    expect(orgPlanLabel(sub({ tier: PlanTier.UNSPECIFIED, name: "Compute M" }), "org-a")).toBe("Compute M");
    expect(orgPlanLabel(sub({ status: SubscriptionStatus.TRIALING }), "org-a")).toBe("Pro");
  });

  it("is hidden when there is no live plan for this org", () => {
    expect(orgPlanLabel(null, "org-a")).toBeNull();
    expect(orgPlanLabel(undefined, "org-a")).toBeNull();
    expect(orgPlanLabel(create(SubscriptionSchema, { orgId: "org-a", status: SubscriptionStatus.ACTIVE }), "org-a")).toBeNull();
    expect(orgPlanLabel(sub({ status: SubscriptionStatus.CANCELED }), "org-a")).toBeNull();
    expect(orgPlanLabel(sub({ status: SubscriptionStatus.UNSPECIFIED }), "org-a")).toBeNull();
    expect(orgPlanLabel(sub({ orgId: "org-b" }), "org-a")).toBeNull();
    expect(orgPlanLabel(sub({ tier: PlanTier.UNSPECIFIED, name: "  " }), "org-a")).toBeNull();
  });
});

describe("the sidebar header's org row", () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
    listMyOrganizations.mockReset().mockResolvedValue({ orgs: [ACME], defaultOrgId: "org-a" });
    getCurrentUserComputeSubscription.mockReset();
    useAuthStore.setState({ user: { id: "alice" } as never });
    useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false, failure: null });
  });

  const renderRow = () =>
    renderWithQuery(
      <>
        <ActiveOrgBinding />
        <OrgSwitcher variant="sidebar-header" />
      </>,
    );

  it("is one full-width button: avatar, name, plan, ⇕, opening the org menu from the keyboard", async () => {
    getCurrentUserComputeSubscription.mockResolvedValue({ subscription: sub({ tier: PlanTier.PRO }) });
    renderRow();
    const trigger = await screen.findByTestId("org-switcher");
    expect(trigger.tagName).toBe("BUTTON");
    expect(trigger).toHaveAttribute("aria-haspopup", "menu");
    expect(screen.getByTestId("org-avatar")).toHaveTextContent("A");
    expect(screen.getByTestId("org-switcher-name")).toHaveTextContent("Acme Robotics International");
    expect(await screen.findByTestId("org-plan-badge")).toHaveTextContent("Pro");
    expect(trigger).toHaveAccessibleName("Organization: Acme Robotics International, Pro plan. Switch organization");
    expect(trigger.querySelector(".lucide-chevrons-up-down")).not.toBeNull();
    expect(trigger.className).toContain("focus-visible:ring-ring");

    trigger.focus();
    await userEvent.keyboard("{Enter}");
    expect(await screen.findByTestId("org-create")).toBeInTheDocument();
  });

  it("hides the plan badge while the plan loads, when it fails, and with no plan", async () => {
    getCurrentUserComputeSubscription.mockReturnValue(new Promise(() => {}));
    const loading = renderRow();
    await screen.findByTestId("org-switcher");
    expect(screen.queryByTestId("org-plan-badge")).toBeNull();
    expect(screen.getByTestId("org-switcher")).toHaveAccessibleName("Organization: Acme Robotics International. Switch organization");
    loading.unmount();

    getCurrentUserComputeSubscription.mockRejectedValue(new Error("unavailable"));
    const failed = renderRow();
    await screen.findByTestId("org-switcher");
    await waitFor(() => expect(getCurrentUserComputeSubscription).toHaveBeenCalled());
    expect(screen.queryByTestId("org-plan-badge")).toBeNull();
    failed.unmount();

    getCurrentUserComputeSubscription.mockResolvedValue({});
    renderRow();
    await screen.findByTestId("org-switcher");
    await waitFor(() => expect(getCurrentUserComputeSubscription).toHaveBeenCalledTimes(3));
    expect(screen.queryByTestId("org-plan-badge")).toBeNull();
  });
});
