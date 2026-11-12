/**
 * An automation offered to the signed-in member: shown only while there is
 * one; accepting adopts it with their own project and machine; declining
 * ends the offer.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";

vi.mock("@/hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-queries")>()),
  useProjects: () => ({ projects: [{ id: "proj-bob", name: "Bob's app", org_id: "org-a" }], isLoading: false, error: null, refetch: () => undefined }),
  useProjectDaemonInstalls: () => ({ data: [], isLoading: false }),
}));
vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({
    daemons: [{ daemonId: "d-bob", name: "bob-box", status: DaemonStatus.ACTIVE }],
    loading: false,
    answered: true,
    refresh: vi.fn(),
  }),
}));

import { setTransport } from "@/lib/connect";
import { mockTransport, type MockHandler } from "@/lib/test-utils";
import { renderWithQuery } from "@/test/renderWithQuery";
import type { OrgMember } from "@/services/controlPlane/members";
import type { MyOrg } from "@/services/controlPlane/orgs";

import { TriggerOffers } from "../TriggerOffers";

const org: MyOrg = { id: "org-a", name: "Acme", slug: "acme", role: "member", isOwner: false, isDefault: true };
const members: OrgMember[] = [
  { userId: "u-ada", email: "ada@acme.test", name: "Ada", avatarUrl: "", role: "admin", joinedAt: null, isFounder: false },
];
const OFFER = {
  id: "offer-1",
  triggerId: "t-1",
  offeredByUserId: "u-ada",
  status: "pending",
  expiresAt: "2099-01-01T00:00:00Z",
  trigger: { id: "t-1", name: "Nightly audit", source: { case: "schedule", value: {} } },
};

function render(offers: unknown[], handlers: Record<string, MockHandler> = {}) {
  setTransport(mockTransport({ "AutomationTakeoverService.ListTriggerOffers": { response: { offers } }, ...handlers }));
  return renderWithQuery(<TriggerOffers org={org} members={members} />);
}

beforeEach(() => setTransport(null));

describe("TriggerOffers", () => {
  it("shows nothing when nobody offered the member anything", async () => {
    const list = vi.fn((_req: unknown) => ({ offers: [] }));
    setTransport(mockTransport({ "AutomationTakeoverService.ListTriggerOffers": list }));
    renderWithQuery(<TriggerOffers org={org} members={members} />);
    await waitFor(() => expect(list).toHaveBeenCalledWith(expect.objectContaining({ orgId: "org-a" })));
    expect(screen.queryByTestId("trigger-offers")).toBeNull();
  });

  it("accepting adopts it with the member's own project and machine", async () => {
    const accept = vi.fn((_req: unknown) => ({ trigger: { id: "t-1", name: "Nightly audit" }, notices: ["It is paused."] }));
    render([OFFER], { "AutomationTakeoverService.AcceptTriggerOffer": accept });
    expect(await screen.findByTestId("offer-offer-1")).toHaveTextContent("offered by Ada");
    await userEvent.click(screen.getByTestId("offer-accept-offer-1"));
    await userEvent.click(await screen.findByTestId("adopt-submit"));
    await waitFor(() =>
      expect(accept).toHaveBeenCalledWith(
        expect.objectContaining({ offerId: "offer-1", rebinding: expect.objectContaining({ projectId: "proj-bob", daemonId: "d-bob" }) }),
      ),
    );
    expect(await screen.findByTestId("adopt-result")).toHaveTextContent("is yours now");
  });

  it("declining ends the offer", async () => {
    const decline = vi.fn((_req: unknown) => ({ offer: { id: "offer-1", status: "revoked", statusReason: "declined" } }));
    render([OFFER], { "AutomationTakeoverService.RevokeTriggerOffer": decline });
    await userEvent.click(await screen.findByRole("button", { name: "Decline Nightly audit" }));
    await waitFor(() => expect(decline).toHaveBeenCalledWith(expect.objectContaining({ offerId: "offer-1" })));
  });
});
