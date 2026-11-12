/**
 * Settings › Members › Automations: an admin sees what former members left
 * (param values withheld), adopts an automation with their own project and
 * machine, offers one to a member (who must accept: nothing is re-owned by
 * the offer), withdraws an offer, and forks a saved workflow. A refusal says
 * why in words.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";

import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";

vi.mock("@/hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-queries")>()),
  useProjects: () => ({
    projects: [
      { id: "proj-acme", name: "Acme app", org_id: "org-a" },
      { id: "proj-other", name: "Elsewhere", org_id: "org-b" },
    ],
    isLoading: false,
    error: null,
    refetch: () => undefined,
  }),
  useProjectDaemonInstalls: () => ({ data: [], isLoading: false }),
}));
vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({
    daemons: [{ daemonId: "d-laptop", name: "laptop", status: DaemonStatus.ACTIVE }],
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

import { OrphanedAutomations } from "../OrphanedAutomations";

const org: MyOrg = { id: "org-a", name: "Acme", slug: "acme", role: "admin", isOwner: false, isDefault: true };
const member = (userId: string, email: string): OrgMember => ({
  userId, email, name: "", avatarUrl: "", role: "member", joinedAt: null, isFounder: false,
});
const members = [member("u-bob", "bob@acme.test"), member("u-carol", "carol@acme.test")];

const nightly = {
  trigger: { id: "t-nightly", name: "Nightly audit", source: { case: "schedule", value: { cron: ["0 3 * * *"] } } },
  paramNames: ["api_token"],
  creatorUserId: "u-cora",
};
const hook = {
  trigger: { id: "t-hook", name: "Deploy hook", source: { case: "webhook", value: { hmac: { header: "X-Sig" } } } },
  paramNames: [],
  creatorUserId: "u-cora",
  pendingOffer: { id: "offer-1", triggerId: "t-hook", offereeUserId: "u-bob", status: "pending", expiresAt: "2099-01-01T00:00:00Z" },
};

function render(handlers: Record<string, MockHandler>) {
  setTransport(
    mockTransport({
      "AutomationTakeoverService.ListOrphanedAutomations": {
        response: { triggers: [nightly, hook], workflows: [{ id: "wf-1", name: "Release", slug: "release", creatorUserId: "u-cora" }] },
      },
      ...handlers,
    }),
  );
  renderWithQuery(<OrphanedAutomations org={org} members={members} />);
}

beforeEach(() => setTransport(null));

describe("OrphanedAutomations", () => {
  it("lists what former members left, names the params but never their values", async () => {
    render({});
    const row = await screen.findByTestId("orphan-t-nightly");
    expect(row).toHaveTextContent("Nightly audit");
    expect(row).toHaveTextContent("made by a former member");
    expect(row).toHaveTextContent("sets api_token");
    expect(screen.getByTestId("orphan-offer-t-hook")).toHaveTextContent("Offered to bob@acme.test");
    expect(screen.getByTestId("orphan-workflow-wf-1")).toHaveTextContent("Release");
  });

  it("adopts with the admin's own project and machine, and says it is paused", async () => {
    const adopt = vi.fn((_req: unknown) => ({ trigger: { id: "t-nightly", name: "Nightly audit" }, notices: ["It is paused."] }));
    render({ "AutomationTakeoverService.AdoptTrigger": adopt });
    await userEvent.click(await screen.findByTestId("orphan-adopt-t-nightly"));
    const form = await screen.findByTestId("adopt-form");
    // Only this org's projects are offered.
    expect(within(form).queryByRole("option", { name: "Elsewhere" })).toBeNull();
    await userEvent.click(screen.getByTestId("adopt-submit"));
    await waitFor(() =>
      expect(adopt).toHaveBeenCalledWith(
        expect.objectContaining({
          triggerId: "t-nightly",
          newOwnerUserId: "",
          rebinding: expect.objectContaining({ projectId: "proj-acme", daemonId: "d-laptop", noMachine: false }),
        }),
      ),
    );
    expect(await screen.findByTestId("adopt-result")).toHaveTextContent("is yours now. It is paused");
    expect(screen.getByTestId("adopt-notices")).toHaveTextContent("It is paused.");
  });

  it("a signed webhook needs a new secret before it can be adopted", async () => {
    const adopt = vi.fn((_req: unknown) => ({ trigger: { id: "t-hook" }, notices: [] }));
    render({ "AutomationTakeoverService.AdoptTrigger": adopt });
    await userEvent.click(await screen.findByTestId("orphan-adopt-t-hook"));
    expect(await screen.findByTestId("adopt-submit")).toBeDisabled();
    await userEvent.type(screen.getByTestId("adopt-secret"), "fresh-secret");
    await userEvent.click(screen.getByTestId("adopt-submit"));
    await waitFor(() =>
      expect(adopt).toHaveBeenCalledWith(expect.objectContaining({ rebinding: expect.objectContaining({ webhookHmacSecret: "fresh-secret" }) })),
    );
  });

  it("offers to a member without choosing anything for them", async () => {
    const offer = vi.fn((_req: unknown) => ({ offer: { id: "offer-2", status: "pending" } }));
    render({ "AutomationTakeoverService.AdoptTrigger": offer });
    await userEvent.click(await screen.findByRole("button", { name: "Offer Nightly audit to a member" }));
    await userEvent.selectOptions(await screen.findByTestId("offer-member"), "u-carol");
    await userEvent.click(screen.getByTestId("offer-submit"));
    await waitFor(() => expect(offer).toHaveBeenCalledTimes(1));
    const req = offer.mock.calls[0][0] as { triggerId: string; newOwnerUserId: string; rebinding?: unknown };
    expect(req.triggerId).toBe("t-nightly");
    expect(req.newOwnerUserId).toBe("u-carol");
    expect(req.rebinding).toBeUndefined();
  });

  it("withdraws a pending offer", async () => {
    const revoke = vi.fn((_req: unknown) => ({ offer: { id: "offer-1", status: "revoked" } }));
    render({ "AutomationTakeoverService.RevokeTriggerOffer": revoke });
    await userEvent.click(await screen.findByRole("button", { name: "Withdraw the offer of Deploy hook" }));
    await userEvent.click(await screen.findByRole("button", { name: "Withdraw" }));
    await waitFor(() => expect(revoke).toHaveBeenCalledWith(expect.objectContaining({ offerId: "offer-1" })));
  });

  it("forks a saved workflow into one of the admin's projects", async () => {
    const fork = vi.fn((_req: unknown) => ({ id: "wf-2", slug: "release-2", name: "Release" }));
    render({ "WorkflowService.ForkOrphanedWorkflow": fork });
    await userEvent.click(await screen.findByRole("button", { name: "Fork Release" }));
    await userEvent.click(await screen.findByTestId("fork-submit"));
    await waitFor(() => expect(fork).toHaveBeenCalledWith(expect.objectContaining({ workflowId: "wf-1", projectId: "proj-acme" })));
  });

  it("says why when it is refused", async () => {
    setTransport(
      mockTransport({
        "AutomationTakeoverService.ListOrphanedAutomations": {
          error: new ConnectError("prose", Code.PermissionDenied, { "x-forge-error-reason": "org_admin_required" }),
        },
      }),
    );
    renderWithQuery(<OrphanedAutomations org={org} members={members} />);
    expect(await screen.findByTestId("orphans-error")).toHaveTextContent("Only an admin or owner of this organization can do this.");
  });
});
