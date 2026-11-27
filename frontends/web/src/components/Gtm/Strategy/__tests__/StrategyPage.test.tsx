// Copyright (c) 2026 Reliant Labs

/**
 * GTM › Strategy over GtmService (a mock transport): the org's strategies,
 * one strategy with its campaigns and a person's status moves on the
 * revision on screen, a new strategy; the library filtered by category,
 * search and facet, "Use this strategy" with an optional first campaign; a
 * pending prune request confirmed or cancelled by a person; the import
 * history. The library entries here are made-up placeholders.
 */

import { create } from "@bufbuild/protobuf";
import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  GtmCampaignSchema,
  GtmCredentialKind,
  GtmLibraryDepth,
  GtmLibraryImportSchema,
  GtmLibraryPruneRequestSchema,
  GtmPruneRequestStatus,
  GtmStrategyCategory,
  GtmStrategyLibraryEntrySchema,
  GtmStrategySchema,
  GtmStrategyStatus,
  type UseLibraryStrategyRequest,
} from "@/gen/reliant/v1/gtm_pb";

import { renderGtm, TEST_ORG } from "../../shared/__tests__/gtmHarness";
import { StrategyView } from "../StrategyPage";

const toasts = vi.hoisted(() => ({ success: vi.fn(), warning: vi.fn(), error: vi.fn() }));
vi.mock("@/lib/toast-manager", () => ({ toast: toasts }));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const active = create(GtmStrategySchema, {
  id: "str-1",
  orgId: TEST_ORG,
  name: "Developer-led growth",
  goalMd: "Win developers who try things from the terminal.",
  targetMetric: "500 weekly active teams",
  periodStart: "2026-10-01",
  periodEnd: "2027-03-31",
  status: GtmStrategyStatus.ACTIVE,
  category: GtmStrategyCategory.LAUNCHES_PR,
  revision: 4,
});
const draft = create(GtmStrategySchema, { id: "str-2", orgId: TEST_ORG, name: "Office hours", status: GtmStrategyStatus.DRAFT, revision: 1 });
const campaign = create(GtmCampaignSchema, { id: "cmp-1", orgId: TEST_ORG, strategyId: "str-1", name: "Sites launch" });

const entry = (key: string, name: string, category: GtmStrategyCategory, extra: Partial<{ stage: string; cost: string; fits: string[] }> = {}) =>
  create(GtmStrategyLibraryEntrySchema, { key, name, category, depth: GtmLibraryDepth.PLAY, summaryMd: `Placeholder: ${name}.`, stage: "early", cost: "low", ...extra });
const launchWeek = entry("sample-launch-week", "Launch week", GtmStrategyCategory.LAUNCHES_PR, { fits: ["b2b"] });
const officeHours = entry("sample-office-hours", "Office hours", GtmStrategyCategory.COMMUNITY_EVENTS, { stage: "growth", cost: "free", fits: ["b2c"] });
const faq = entry("sample-faq", "Answer pages", GtmStrategyCategory.ORGANIC_CONTENT_SEO, { fits: ["b2b"] });

const reads = {
  "GtmService.ListStrategies": { response: { strategies: [draft, active] } },
  "GtmService.GetStrategy": { response: { strategy: active, campaigns: [campaign] } },
  "GtmService.ListStrategyLibrary": { response: { entries: [launchWeek, officeHours, faq] } },
  "GtmService.ListLibraryPruneRequests": { response: { requests: [] } },
  "GtmService.ListLibraryImports": { response: { imports: [] } },
};

const at = (search = "") => ({ path: `/gtm/strategy${search}`, routePath: "/gtm/strategy" });

describe("Your strategies", () => {
  it("lists them, active first", async () => {
    renderGtm(<StrategyView />, reads, at());
    const list = await screen.findByTestId("gtm-strategies");
    await waitFor(() => expect(within(list).getAllByRole("button", { name: /Developer-led growth|Office hours/ })).toHaveLength(2));
    const names = within(list).getAllByRole("button", { name: /Developer-led growth|Office hours/ }).map((b) => b.textContent);
    expect(names[0]).toContain("Developer-led growth");
    expect(within(list).getByText("Target: 500 weekly active teams")).toBeInTheDocument();
  });

  it("shows one strategy with its campaigns, and a person's status moves on the revision on screen", async () => {
    const update = vi.fn(() => ({ strategy: { ...active, status: GtmStrategyStatus.DONE } }));
    renderGtm(<StrategyView />, { ...reads, "GtmService.UpdateStrategy": update }, at("?strategy=str-1"));
    const detail = await screen.findByTestId("gtm-strategy-detail");
    expect(within(detail).getByRole("link", { name: "Sites launch" })).toBeInTheDocument();
    const status = within(detail).getByTestId("gtm-strategy-status");
    expect(within(status).queryByRole("button", { name: "Make active" })).not.toBeInTheDocument();
    expect(within(status).getByText("Only a person")).toBeInTheDocument();
    fireEvent.click(within(status).getByRole("button", { name: "Mark done" }));
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(update).toHaveBeenCalledWith(expect.objectContaining({ orgId: TEST_ORG, id: "str-1", expectedRevision: 4, status: GtmStrategyStatus.DONE }));
  });

  it("writes a new strategy, a draft", async () => {
    const createS = vi.fn(() => ({ strategy: { ...draft, id: "str-9" } }));
    renderGtm(<StrategyView />, { ...reads, "GtmService.CreateStrategy": createS }, at());
    fireEvent.click(await screen.findByRole("button", { name: "New strategy" }));
    const form = await screen.findByTestId("gtm-strategy-form");
    fireEvent.change(within(form).getByLabelText("Name"), { target: { value: "Partner launch" } });
    fireEvent.change(within(form).getByLabelText("Category"), { target: { value: String(GtmStrategyCategory.PARTNERSHIPS_DISTRIBUTION) } });
    fireEvent.click(screen.getByRole("button", { name: "Create strategy" }));
    await waitFor(() => expect(createS).toHaveBeenCalled());
    expect(createS).toHaveBeenCalledWith(expect.objectContaining({ orgId: TEST_ORG, name: "Partner launch", category: GtmStrategyCategory.PARTNERSHIPS_DISTRIBUTION }));
  });
});

describe("the library", () => {
  it("has all 13 categories, and filters by category, search and facet", async () => {
    renderGtm(<StrategyView />, reads, at("?view=library"));
    const lib = await screen.findByTestId("gtm-library");
    expect(await within(lib).findByText("3 of 3 entries")).toBeInTheDocument();
    const chips = within(within(lib).getByRole("group", { name: "Category" })).getAllByRole("button");
    expect(chips).toHaveLength(14);
    fireEvent.click(within(lib).getByRole("button", { name: /^Launches & PR/ }));
    expect(within(lib).getByText("1 of 3 entries")).toBeInTheDocument();
    expect(within(lib).getByRole("heading", { name: "Launch week" })).toBeInTheDocument();
    fireEvent.click(within(lib).getByRole("button", { name: /^All categories/ }));
    fireEvent.change(within(lib).getByLabelText("Fits"), { target: { value: "b2b" } });
    expect(within(lib).getByText("2 of 3 entries")).toBeInTheDocument();
    fireEvent.change(within(lib).getByPlaceholderText("Search plays and tactics"), { target: { value: "answer" } });
    expect(within(lib).getByText("1 of 3 entries")).toBeInTheDocument();
    expect(within(lib).getByRole("heading", { name: "Answer pages" })).toBeInTheDocument();
  });

  it("uses a strategy, with a first campaign, and opens it", async () => {
    const use = vi.fn(() => ({ strategy: { ...draft, id: "str-new" } }));
    const { router } = renderGtm(<StrategyView />, { ...reads, "GtmService.UseLibraryStrategy": use }, at("?view=library"));
    fireEvent.click(await screen.findByRole("button", { name: "Use this strategy: Launch week" }));
    const dialog = await screen.findByTestId("gtm-use-strategy-dialog");
    fireEvent.change(within(dialog).getByLabelText("Campaign name"), { target: { value: "Launch week, March" } });
    fireEvent.click(screen.getByRole("button", { name: "Create strategy" }));
    await waitFor(() => expect(use).toHaveBeenCalled());
    const req = (use.mock.calls[0] as unknown[])[0] as UseLibraryStrategyRequest;
    expect(req).toEqual(expect.objectContaining({ orgId: TEST_ORG, libraryKey: "sample-launch-week", createFirstCampaign: true, campaignName: "Launch week, March" }));
    await waitFor(() => expect(router.state.location.search).toEqual(expect.objectContaining({ strategy: "str-new" })));
  });

  it("uses a strategy without a campaign", async () => {
    const use = vi.fn(() => ({ strategy: { ...draft, id: "str-new" } }));
    renderGtm(<StrategyView />, { ...reads, "GtmService.UseLibraryStrategy": use }, at("?view=library"));
    fireEvent.click(await screen.findByRole("button", { name: "Use this strategy: Office hours" }));
    const dialog = await screen.findByTestId("gtm-use-strategy-dialog");
    fireEvent.click(within(dialog).getByRole("switch"));
    fireEvent.click(screen.getByRole("button", { name: "Create strategy" }));
    await waitFor(() => expect(use).toHaveBeenCalled());
    const req = (use.mock.calls[0] as unknown[])[0] as UseLibraryStrategyRequest;
    expect(req.createFirstCampaign).toBe(false);
    expect(req.campaignName).toBeUndefined();
  });
});

describe("a pending prune request", () => {
  const request = create(GtmLibraryPruneRequestSchema, {
    id: "pr-1",
    orgId: TEST_ORG,
    credentialKind: GtmCredentialKind.TOKEN,
    sourceRevision: "abc1234",
    removeKeys: ["sample-old-one", "sample-old-two"],
    status: GtmPruneRequestStatus.PENDING,
  });
  const withRequest = { ...reads, "GtmService.ListLibraryPruneRequests": { response: { requests: [request] } } };

  it("lists the keys and asks a person, then confirms", async () => {
    const list = vi.fn(() => ({ requests: [request] }));
    const confirm = vi.fn(() => ({ removed: 2, request: { ...request, status: GtmPruneRequestStatus.CONFIRMED } }));
    renderGtm(<StrategyView />, { ...withRequest, "GtmService.ListLibraryPruneRequests": list, "GtmService.ConfirmLibraryPrune": confirm }, at("?view=library"));
    const banner = await screen.findByTestId("gtm-prune-banner");
    expect(within(banner).getByText("sample-old-one")).toBeInTheDocument();
    expect(within(banner).getByText("Only a person")).toBeInTheDocument();
    expect(list).toHaveBeenCalledWith(expect.objectContaining({ orgId: TEST_ORG, status: GtmPruneRequestStatus.PENDING }));
    fireEvent.click(within(banner).getByRole("button", { name: "Remove them" }));
    await waitFor(() => expect(confirm).toHaveBeenCalledWith(expect.objectContaining({ orgId: TEST_ORG, requestId: "pr-1" })));
    await waitFor(() => expect(toasts.success).toHaveBeenCalledWith("Removed 2 entries from the library."));
  });

  it("cancels, removing nothing", async () => {
    const cancel = vi.fn(() => ({ request: { ...request, status: GtmPruneRequestStatus.CANCELLED } }));
    renderGtm(<StrategyView />, { ...withRequest, "GtmService.CancelLibraryPrune": cancel }, at("?view=library"));
    fireEvent.click(await screen.findByRole("button", { name: "Keep them" }));
    await waitFor(() => expect(cancel).toHaveBeenCalledWith(expect.objectContaining({ orgId: TEST_ORG, requestId: "pr-1" })));
  });

  it("shows the import history", async () => {
    const imp = create(GtmLibraryImportSchema, { id: "imp-1", orgId: TEST_ORG, credentialKind: GtmCredentialKind.TOKEN, sourceRevision: "abc1234", created: 3, unchanged: 5, pruneRequestId: "pr-1" });
    renderGtm(<StrategyView />, { ...withRequest, "GtmService.ListLibraryImports": { response: { imports: [imp] } } }, at("?view=library"));
    const history = await screen.findByTestId("gtm-import-history");
    expect(within(history).getByText("Access token")).toBeInTheDocument();
    expect(within(history).getByText("3 new · 5 unchanged · a removal waiting for a person")).toBeInTheDocument();
  });
});
