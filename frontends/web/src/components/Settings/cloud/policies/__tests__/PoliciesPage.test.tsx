/**
 * Settings › Policies, over the real generated clients on a mock transport.
 *
 * Pinned: a member reads the rules that bind them and their history, and
 * nothing to edit; an org with no policy says so; an admin's edit is a draft
 * until it is previewed and saved with the version it started from; a
 * refusal is shown on its card and blocks the save; a teammate's newer save
 * is never overwritten unseen: the admin reapplies onto it (keeping the
 * teammate's other sections) or loads theirs.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
const toastSuccess = vi.fn();
vi.mock("@/lib/toast-manager", () => ({ toast: { success: (m: string) => toastSuccess(m), error: vi.fn() } }));

import { OrgPolicyMode, SpendPeriod } from "@/gen/controlplane/controlplane/v1/org_policy_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import { useAuthStore } from "@/store/authStore";
import { renderWithQuery } from "@/test/renderWithQuery";

import { PoliciesPage } from "../PoliciesPage";

const ORG = "org-a";

function myOrgs(role: string) {
  return {
    organizations: [{ id: ORG, name: "Acme", slug: "acme", role, isOwner: role === "owner", isDefault: true }],
    defaultOrgId: ORG,
  };
}

const ROSTER = {
  members: [
    { userId: "u_ada", email: "ada@example.com", name: "Ada", role: "owner" },
    { userId: "u_bob", email: "bob@example.com", name: "Bob", role: "member" },
  ],
  seats: { members: 2, pendingInvitations: 0, limit: 5 },
};

const V3 = {
  orgId: ORG,
  version: 3n,
  policy: { models: { mode: OrgPolicyMode.AUDIT, allowed: ["gpt-5"] } },
  updatedByEmail: "ada@example.com",
  updatedAt: timestampFromDate(new Date(Date.now() - 3_600_000)),
};

const V4 = {
  orgId: ORG,
  version: 4n,
  policy: {
    models: { mode: OrgPolicyMode.AUDIT, allowed: ["gpt-5"] },
    spend: { mode: OrgPolicyMode.AUDIT, memberDefaultCap: { usdNanos: 50_000_000_000n, period: SpendPeriod.MONTH } },
  },
  updatedByEmail: "bob@example.com",
  updatedAt: timestampFromDate(new Date()),
};

const HISTORY = {
  changes: [
    { id: "c1", actorEmail: "ada@example.com", resource: "policy.models.allowed", field: "allowed", oldValue: "", newValue: '["gpt-5"]', at: timestampFromDate(new Date()) },
    { id: "c2", actorEmail: "ada@example.com", resource: "observability.spend_cap", field: "spend_cap_cents", oldValue: "5000", newValue: "9000" },
  ],
  nextPageToken: "",
};

type Handlers = Parameters<typeof mockTransport>[0];
type Req = { orgId: string; policy?: Record<string, unknown>; expectedVersion?: bigint };

function setup(role: string, opts: { stored?: unknown; overrides?: Handlers } = {}) {
  let stored = (opts.stored ?? V3) as Record<string, unknown>;
  const calls = {
    get: vi.fn((_req: unknown) => ({ policy: stored })),
    preview: vi.fn((_req: unknown) => ({
      currentVersion: stored.version as bigint,
      valid: true,
      changes: [{ resource: "policy.mcp.mode", field: "mode", oldValue: "", newValue: '"ORG_POLICY_MODE_AUDIT"' }],
    })),
    update: vi.fn((req: unknown) => {
      const r = req as Req;
      stored = { ...stored, version: (r.expectedVersion ?? 0n) + 1n, policy: r.policy, updatedByEmail: "ada@example.com" };
      return { policy: stored, changed: true };
    }),
    setStored: (s: object) => {
      stored = s as Record<string, unknown>;
    },
  };
  setTransport(
    mockTransport({
      "OrganizationService.ListMyOrganizations": { response: myOrgs(role) },
      "OrganizationService.ListMembers": { response: ROSTER },
      "OrgPolicyService.GetOrgPolicy": calls.get,
      "OrgPolicyService.PreviewOrgPolicy": calls.preview,
      "OrgPolicyService.UpdateOrgPolicy": calls.update,
      "BillingService.ListOrgLimitChanges": { response: HISTORY },
      "LLMGatewayService.ListAvailableModels": { response: { models: ["gpt-5", "claude-sonnet-4"] } },
      "ConnectionService.ListIntegrations": { response: { integrations: [] } },
      ...opts.overrides,
    }),
  );
  return { ...renderWithQuery(<PoliciesPage />), calls };
}

beforeEach(() => {
  localStorage.clear();
  toastSuccess.mockReset();
  setTransport(null);
  useActiveOrgStore.setState({ userId: "u_ada", chosenId: ORG, effectiveId: ORG });
  useAuthStore.setState({ user: { id: "u_ada", email: "ada@example.com" } as never });
});

describe("PoliciesPage (member)", () => {
  it("shows the rules that bind them, read-only, with who saved them", async () => {
    setup("member");
    const card = await screen.findByTestId("policy-card-models");
    expect(within(card).getByText("Audit only")).toBeInTheDocument();
    expect(screen.getByTestId("policies-version")).toHaveTextContent("Version 3 · saved by ada@example.com");
    // Only the rules that exist, and nothing to edit.
    expect(screen.queryByTestId("policy-card-spend")).toBeNull();
    expect(screen.queryAllByRole("switch")).toHaveLength(0);
    expect(screen.getByText(/They apply to you too/)).toBeInTheDocument();
    // The denial counts name members and what they tried: admins and owners only.
    expect(screen.queryByRole("tab", { name: "Denials" })).toBeNull();
  });

  it("shows the policy history, and only its policy rows", async () => {
    setup("member");
    await screen.findByTestId("policy-card-models");
    await userEvent.click(screen.getByRole("tab", { name: "History" }));
    const row = await screen.findByTestId("policy-history-c1");
    expect(row).toHaveTextContent("ada@example.com changed Models › allowed");
    expect(row).toHaveTextContent("not set → gpt-5");
    expect(screen.queryByTestId("policy-history-c2")).toBeNull();
  });

  it("says so when the org has no policy", async () => {
    setup("member", { stored: { orgId: ORG, version: 0n } });
    expect(await screen.findByTestId("policies-empty")).toHaveTextContent("Acme has no policies");
    expect(screen.getByTestId("policies-version")).toHaveTextContent("No policies yet");
  });

  it("shows the refusal's words when the policy cannot be read", async () => {
    setup("member", {
      overrides: { "OrgPolicyService.GetOrgPolicy": { error: new ConnectError("prose", Code.PermissionDenied, { "x-forge-error-reason": "not_org_member" }) } },
    });
    expect(await screen.findByText(/not a member of this organization any more/)).toBeInTheDocument();
  });
});

describe("PoliciesPage (admin)", () => {
  it("shows an admin the Denials tab, read from the org being looked at", async () => {
    const denials = vi.fn((_req: unknown) => ({
      counts: [{ day: "2026-11-14", policy: "models", reason: "model_not_allowed", userId: "u_bob", outcome: "audited", count: 9n }],
      refusedTotal: 0n,
      auditedTotal: 9n,
    }));
    setup("admin", { overrides: { "OrgPolicyService.ListOrgPolicyDenials": denials } });
    await screen.findByTestId("policy-card-models");
    await userEvent.click(screen.getByRole("tab", { name: "Denials" }));
    expect(await screen.findByTestId("policy-denial-models-model_not_allowed-audited")).toHaveTextContent("Bob");
    expect(screen.getByTestId("policy-denial-models-model_not_allowed-audited")).toHaveTextContent("9 calls would have been refused");
    expect(denials).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG }));
  });

  it("previews a draft, then saves it with the version it started from", async () => {
    const { calls } = setup("admin");
    await screen.findByTestId("policy-card-mcp");
    await userEvent.click(screen.getByRole("switch", { name: "Tools (MCP) policy" }));
    expect(screen.getByTestId("policies-savebar")).toHaveTextContent("Unsaved changes to Tools (MCP).");
    expect(calls.update).not.toHaveBeenCalled();

    await userEvent.click(screen.getByTestId("policies-review"));
    await waitFor(() => expect(calls.preview).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG })));
    expect((calls.preview.mock.calls[0][0] as Req).policy).toHaveProperty("mcp");
    expect(await screen.findByTestId("policy-review-changes")).toHaveTextContent("Tools (MCP) › mode");

    await userEvent.click(screen.getByTestId("policy-save"));
    await waitFor(() => expect(calls.update).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG, expectedVersion: 3n })));
    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith("Policies saved as version 4."));
    expect(screen.queryByTestId("policies-savebar")).toBeNull();
    expect(screen.getByTestId("policies-version")).toHaveTextContent("Version 4");
  });

  it("shows a refusal on its card and will not save", async () => {
    setup("admin", {
      overrides: {
        "OrgPolicyService.PreviewOrgPolicy": {
          response: { currentVersion: 3n, valid: false, violations: [{ field: "models.allowed", message: "an empty allow-list admits no model" }], changes: [] },
        },
      },
    });
    await screen.findByTestId("policy-card-models");
    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Models mode" }), "enforce");
    await userEvent.click(screen.getByTestId("policies-review"));
    expect(await screen.findByTestId("policy-review-violations")).toHaveTextContent("an empty allow-list");
    expect(screen.getByTestId("policy-save")).toBeDisabled();
    expect(screen.getByTestId("policy-violations-models")).toHaveTextContent("an empty allow-list");
  });

  it("reapplies the draft onto a teammate's newer save, keeping their change", async () => {
    const { calls } = setup("admin");
    await screen.findByTestId("policy-card-mcp");
    await userEvent.click(screen.getByRole("switch", { name: "Tools (MCP) policy" }));
    // Bob saves version 4 (a spend limit) while Ada edits.
    calls.setStored(V4);

    await userEvent.click(screen.getByTestId("policies-review"));
    expect(await screen.findByTestId("policy-conflict")).toHaveTextContent("bob@example.com saved version 4");
    expect(screen.getByTestId("policy-save")).toBeDisabled();

    await userEvent.click(screen.getByTestId("policy-reapply"));
    await waitFor(() => expect(calls.preview).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByTestId("policy-conflict")).toBeNull());
    await waitFor(() => expect(screen.getByTestId("policy-save")).toBeEnabled());

    await userEvent.click(screen.getByTestId("policy-save"));
    await waitFor(() => expect(calls.update).toHaveBeenCalledWith(expect.objectContaining({ expectedVersion: 4n })));
    const saved = (calls.update.mock.calls[0][0] as Req).policy;
    expect(saved).toHaveProperty("mcp");
    expect(saved).toHaveProperty("spend");
  });

  it("turns a lost save race into the same choice", async () => {
    const { calls } = setup("admin", {
      overrides: {
        "OrgPolicyService.UpdateOrgPolicy": {
          error: new ConnectError("prose", Code.FailedPrecondition, { "x-forge-error-reason": "policy_version_conflict" }),
        },
      },
    });
    await screen.findByTestId("policy-card-mcp");
    await userEvent.click(screen.getByRole("switch", { name: "Tools (MCP) policy" }));
    await userEvent.click(screen.getByTestId("policies-review"));
    await waitFor(() => expect(screen.getByTestId("policy-save")).toBeEnabled());
    calls.setStored(V4);
    await userEvent.click(screen.getByTestId("policy-save"));
    expect(await screen.findByTestId("policy-conflict")).toHaveTextContent("version 4");

    await userEvent.click(screen.getByTestId("policy-discard-to-latest"));
    await waitFor(() => expect(screen.queryByTestId("policies-savebar")).toBeNull());
    expect(screen.getByTestId("policies-version")).toHaveTextContent("Version 4 · saved by bob@example.com");
    expect(screen.getByTestId("policy-summary-spend")).toHaveTextContent("each member at most $50 a month");
  });

  it("discards a draft", async () => {
    setup("owner");
    await screen.findByTestId("policy-card-machines");
    await userEvent.click(screen.getByRole("switch", { name: "Machine credentials policy" }));
    await userEvent.click(within(screen.getByTestId("policies-savebar")).getByRole("button", { name: "Discard" }));
    expect(screen.queryByTestId("policies-savebar")).toBeNull();
    expect(within(screen.getByTestId("policy-card-machines")).getByText("Off")).toBeInTheDocument();
  });
});
