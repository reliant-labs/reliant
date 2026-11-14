// Copyright (c) 2026 Reliant Labs

/**
 * Settings › Organization › AI usage: a billing:read holder sees what the org
 * pays for, by member, account and model, with every cost labelled an
 * estimate and "—" (never "$0.00") where nothing was priced; a member without
 * the grant is told how to get it, not shown an error.
 */

import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { LLMUsageCompleteness } from "@/gen/reliant/v1/usage_pb";
import { OrgLLMCostEstimateStatus, OrgLLMUsageGroupBy } from "@/gen/reliant/v1/org_usage_pb";
import type { MyOrg } from "@/services/controlPlane/orgs";

import { bucketName, formatEstimate, rangeDates } from "../orgUsageModel";

const mocks = vi.hoisted(() => ({
  org: null as MyOrg | null,
  read: vi.fn(),
  listMembers: vi.fn(),
}));

vi.mock("@/services/controlPlane/config", () => ({ hasControlPlane: true }));
vi.mock("@/store/authStore", () => ({
  useAuthStore: (sel: (s: unknown) => unknown) => sel({ user: { id: "u-me" }, session: { access_token: "t" } }),
}));
vi.mock("@/hooks/useOrgs", () => ({
  useActiveOrg: () => ({ active: mocks.org, orgs: mocks.org ? [mocks.org] : [], isLoading: false }),
}));
// The roster is read for real (useMembers' infinite query) over a fake
// control plane: two pages, Ada on the first, Bea on the second.
vi.mock("@/services/controlPlane/members", async (importActual) => ({
  ...(await importActual<typeof import("@/services/controlPlane/members")>()),
  listMembers: (orgId: string, pageToken: string) => mocks.listMembers(orgId, pageToken),
}));

const member = (externalId: string, name: string) => ({ userId: `cp-${externalId}`, externalId, name, email: `${externalId}@acme.com` });
const rosterPages = (_orgId: string, pageToken: string) =>
  pageToken === ""
    ? { members: [member("u-ada", "Ada")], nextPageToken: "p2" }
    : { members: [member("u-bea", "Bea")], nextPageToken: "" };

import { OrgAIUsagePage } from "../OrgAIUsagePage";

const acme: MyOrg = { id: "org-a", name: "Acme", slug: "acme", role: "member", isOwner: false, isDefault: true };

const bucket = (key: string, extra: Record<string, unknown> = {}) => ({
  key,
  label: "",
  calls: 2n,
  errorCalls: 0n,
  inputTokens: 1_000_000n,
  outputTokens: 100_000n,
  cacheReadTokens: 0n,
  cacheWriteTokens: 0n,
  latencyMsTotal: 10n,
  avgLatencyMs: 5,
  ...extra,
});

function answer(groupBy: OrgLLMUsageGroupBy) {
  const byMember = [
    bucket("u-ada", { estimatedListCost: { usdNanos: 4_500_000_000n, pricedCalls: 1n, unpricedCalls: 0n } }),
    bucket("u-bea", { calls: 1n, estimatedListCost: { pricedCalls: 0n, unpricedCalls: 0n } }),
    bucket("u-gone", { calls: 1n, estimatedListCost: { pricedCalls: 0n, unpricedCalls: 0n } }),
  ];
  const byAccount = [bucket("acct-key", { label: "Acme prod", estimatedListCost: { usdNanos: 4_500_000_000n, pricedCalls: 1n, unpricedCalls: 0n } }), bucket("acct-gone")];
  return {
    orgId: "org-a",
    groupBy,
    buckets: groupBy === OrgLLMUsageGroupBy.ORG_LLM_USAGE_GROUP_BY_ACCOUNT ? byAccount : byMember,
    totals: bucket("", { calls: 3n, estimatedListCost: { usdNanos: 4_500_000_000n, pricedCalls: 1n, unpricedCalls: 1n } }),
    completeness: LLMUsageCompleteness.LLM_USAGE_COMPLETENESS_PARTIAL,
    note: "Members' own accounts are never included.",
    estimate: {
      isEstimate: true,
      status: OrgLLMCostEstimateStatus.ORG_LLM_COST_ESTIMATE_STATUS_AVAILABLE,
      label: "Estimate at list prices from LiteLLM's model catalog. Your provider bill is authoritative.",
      priceSource: "LiteLLM's model catalog",
      unpricedModels: ["openai/mystery-1"],
    },
  };
}

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <OrgAIUsagePage />
    </QueryClientProvider>,
  );
}

// A fixed UTC instant: the ranges are computed from today, so a run near a
// month boundary or in another zone must not change what is asked.
const NOW = new Date("2026-11-14T15:00:00Z");

afterEach(() => {
  vi.useRealTimers();
});

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(NOW);
  mocks.listMembers.mockReset();
  mocks.listMembers.mockImplementation(async (orgId: string, pageToken: string) => rosterPages(orgId, pageToken));
  mocks.read.mockReset();
  mocks.org = acme;
  mocks.read.mockImplementation((req: { groupBy: OrgLLMUsageGroupBy }) => answer(req.groupBy));
  setTransport(mockTransport({ "OrgUsageService.GetOrgLLMUsage": (req) => mocks.read(req) }));
});

describe("OrgAIUsagePage", () => {
  it("shows the org's usage by member with labelled estimates, and — where nothing was priced", async () => {
    renderPage();
    const totals = await screen.findByTestId("org-usage-totals");
    expect(within(totals).getByText("~$4.50")).toBeInTheDocument();
    expect(within(totals).getByText("Estimate")).toBeInTheDocument();

    expect(screen.getByText("Ada")).toBeInTheDocument();
    // Bea is on the roster's SECOND page: read to the end, she is named.
    expect(await screen.findByText("Bea")).toBeInTheDocument();
    expect(screen.getByText("Former member")).toBeInTheDocument();
    expect(mocks.listMembers).toHaveBeenCalledWith("org-a", "p2");
    const costs = screen.getAllByTestId("org-usage-row-cost").map((c) => c.textContent);
    expect(costs).toEqual(["~$4.50", "—", "—"]);
    expect(screen.queryByText("$0.00")).toBeNull();

    expect(screen.getByTestId("org-usage-estimate-label")).toHaveTextContent("provider bill is authoritative");
    expect(screen.getByTestId("org-usage-footnote")).toHaveTextContent("openai/mystery-1");
    expect(screen.getByTestId("org-usage-footnote")).toHaveTextContent("never included");

    const req = mocks.read.mock.calls[0][0];
    expect(req).toMatchObject({ orgId: "org-a", groupBy: OrgLLMUsageGroupBy.ORG_LLM_USAGE_GROUP_BY_MEMBER, startDate: "2026-11-01", endDate: "2026-11-14" });
  });

  it("calls a member not yet read 'loading', never 'former', while the roster is still being read", async () => {
    let releasePage2: () => void = () => {};
    mocks.listMembers.mockImplementation((orgId: string, pageToken: string) =>
      pageToken === "" ? Promise.resolve(rosterPages(orgId, pageToken)) : new Promise((resolve) => (releasePage2 = () => resolve(rosterPages(orgId, pageToken)))),
    );
    renderPage();
    await screen.findByText("Ada");
    await waitFor(() => expect(mocks.listMembers).toHaveBeenCalledWith("org-a", "p2"));
    expect(screen.getAllByText("Loading name…")).toHaveLength(2);
    expect(screen.queryByText("Former member")).toBeNull();
    releasePage2();
    expect(await screen.findByText("Bea")).toBeInTheDocument();
    expect(screen.getByText("Former member")).toBeInTheDocument();
  });

  it("calls a member 'name unavailable', never 'former', when the roster fails", async () => {
    mocks.listMembers.mockImplementation(async (orgId: string, pageToken: string) => {
      if (pageToken === "") return rosterPages(orgId, pageToken);
      throw new ConnectError("roster down", Code.Unavailable);
    });
    renderPage();
    expect(await screen.findAllByText("Name unavailable")).toHaveLength(2);
    expect(screen.getByText("Ada")).toBeInTheDocument();
    expect(screen.queryByText("Former member")).toBeNull();
  });

  it("regroups by account, naming each by its label", async () => {
    renderPage();
    await screen.findByText("Ada");
    fireEvent.click(screen.getByRole("tab", { name: "By account" }));
    expect(await screen.findByText("Acme prod")).toBeInTheDocument();
    expect(screen.getByText("Removed account")).toBeInTheDocument();
    await waitFor(() =>
      expect(mocks.read).toHaveBeenLastCalledWith(expect.objectContaining({ groupBy: OrgLLMUsageGroupBy.ORG_LLM_USAGE_GROUP_BY_ACCOUNT })),
    );
  });

  it("asks for the range picked", async () => {
    renderPage();
    await screen.findByText("Ada");
    fireEvent.change(screen.getByTestId("org-usage-range"), { target: { value: "90d" } });
    await waitFor(() => expect(mocks.read).toHaveBeenLastCalledWith(expect.objectContaining({ startDate: "2026-08-17", endDate: "2026-11-14" })));
  });

  it("tells a member without billing:read how to get it, as a note rather than an error", async () => {
    mocks.read.mockImplementation(() => {
      throw new ConnectError("denied", Code.PermissionDenied, new Headers({ "x-forge-error-reason": "missing_scope" }));
    });
    renderPage();
    const note = await screen.findByTestId("org-usage-denied");
    expect(note).toHaveTextContent("billing:read");
    expect(note).toHaveTextContent("Ask an owner of Acme");
    expect(screen.queryByTestId("org-usage-error")).toBeNull();
    expect(screen.queryByTestId("org-usage-totals")).toBeNull();
  });

  it("offers a retry when the read fails", async () => {
    mocks.read.mockImplementation(() => {
      throw new ConnectError("boom", Code.Unavailable);
    });
    renderPage();
    expect(await screen.findByTestId("org-usage-error")).toHaveTextContent("Acme's AI usage could not be loaded.");
  });
});

describe("orgUsageModel", () => {
  it("never renders an unpriced cost as zero", () => {
    expect(formatEstimate(undefined)).toBeNull();
    expect(formatEstimate(0n)).toBe("~$0.00");
    expect(formatEstimate(3_000_000n)).toBe("<$0.01");
    expect(formatEstimate(1_234_560_000_000n)).toBe("~$1,234.56");
  });

  it("a member is former only once the whole roster was read", () => {
    const b = { key: "u-x", label: "" };
    expect(bucketName("member", b, { names: new Map(), state: "complete" })).toBe("Former member");
    expect(bucketName("member", b, { names: new Map(), state: "loading" })).toBe("Loading name…");
    expect(bucketName("member", b, { names: new Map(), state: "failed" })).toBe("Name unavailable");
    expect(bucketName("member", b, { names: new Map([["u-x", "Xi"]]), state: "failed" })).toBe("Xi");
  });

  it("ranges are whole UTC days ending today", () => {
    const now = new Date("2026-11-14T15:00:00Z");
    expect(rangeDates("month", now)).toEqual({ startDate: "2026-11-01", endDate: "2026-11-14" });
    expect(rangeDates("30d", now)).toEqual({ startDate: "2026-10-16", endDate: "2026-11-14" });
    expect(rangeDates("90d", now)).toEqual({ startDate: "2026-08-17", endDate: "2026-11-14" });
  });
});
