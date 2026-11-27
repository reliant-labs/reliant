// Copyright (c) 2026 Reliant Labs

/**
 * GTM › Strategy over GtmService (a mock transport): the org's strategies,
 * one strategy with its campaigns and a person's status moves on the
 * revision on screen, a new strategy. The library: Fits my business by
 * default with Show all beside it, the filters sent to ListStrategyLibrary,
 * the hint to set the business profile, an entry's detail (why it works,
 * tailor questions, suggested workflow), and "Use this strategy" with an
 * optional first campaign. The library entries here are made-up.
 */

import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  GtmBusinessModel,
  GtmBusinessType,
  GtmCampaignSchema,
  GtmPositioningSchema,
  GtmStrategyCategory,
  GtmStrategyLibraryEntrySchema,
  GtmStrategySchema,
  GtmStrategyStatus,
  type ListStrategyLibraryRequest,
  type UpdateStrategyRequest,
  type UseLibraryStrategyRequest,
} from "@/gen/reliant/v1/gtm_pb";

import { renderGtm, TEST_ORG } from "../../shared/__tests__/gtmHarness";
import { entryFacets, entryFits } from "../Library";
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

const launchWeek = create(GtmStrategyLibraryEntrySchema, {
  key: "sample-launch-week",
  name: "Launch week",
  category: GtmStrategyCategory.LAUNCHES_PR,
  summaryMd: "Ship one small thing a day for a week.",
  whyItWorksMd: "Five reasons to talk about you in one week.",
  stages: ["launch"],
  objective: "awareness",
  effort: "medium",
  cost: "free",
  signalDays: 7,
  tailorQuestions: ["What five small things could ship this month?", "Who would share a daily update?"],
  suggestedWorkflowMd: "Each morning of launch week, draft the day's post from the changelog for a person to approve.",
  kpis: ["Sign-ups during the week"],
});
const meetup = create(GtmStrategyLibraryEntrySchema, {
  key: "sample-local-meetup",
  name: "Host a local meetup",
  category: GtmStrategyCategory.COMMUNITY_EVENTS,
  summaryMd: "A monthly evening for people who use tools like yours.",
  businessTypes: [GtmBusinessType.DEVELOPER_TOOLS],
  businessModels: [GtmBusinessModel.FREE_OSS],
  effort: "high",
  cost: "low",
});

const reads = {
  "GtmService.ListStrategies": { response: { strategies: [draft, active] } },
  "GtmService.GetStrategy": { response: { strategy: active, campaigns: [campaign] } },
  "GtmService.ListStrategyLibrary": { response: { entries: [launchWeek, meetup] } },
  "GtmService.GetPositioning": { response: { positioning: create(GtmPositioningSchema, { orgId: TEST_ORG, businessType: GtmBusinessType.B2B_SAAS, revision: 1 }) } },
  "GtmService.GetStrategyLibraryEntry": { response: { entry: launchWeek } },
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

  it("after a conflict, edits the strategy as it is now: only what changed, on the current revision", async () => {
    let server = active;
    const update = vi.fn((req: unknown) => {
      if ((req as UpdateStrategyRequest).expectedRevision === 4) {
        server = create(GtmStrategySchema, { ...active, targetMetric: "800 weekly active teams", revision: 5 });
        throw new ConnectError("revision moved", Code.Aborted);
      }
      return { strategy: server };
    });
    renderGtm(
      <StrategyView />,
      { ...reads, "GtmService.GetStrategy": () => ({ strategy: server, campaigns: [campaign] }), "GtmService.UpdateStrategy": update },
      at("?strategy=str-1"),
    );
    fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    fireEvent.change(within(await screen.findByTestId("gtm-strategy-form")).getByLabelText("Name"), { target: { value: "Renamed" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(toasts.warning).toHaveBeenCalled());
    // The re-read moved it to revision 5: the form shows the other change, not the stale fields.
    await waitFor(() => expect(within(screen.getByTestId("gtm-strategy-form")).getByLabelText("Target")).toHaveValue("800 weekly active teams"));
    expect(within(screen.getByTestId("gtm-strategy-form")).getByLabelText("Name")).toHaveValue("Developer-led growth");
    fireEvent.change(within(screen.getByTestId("gtm-strategy-form")).getByLabelText("Category"), { target: { value: String(GtmStrategyCategory.UNSPECIFIED) } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(update).toHaveBeenCalledTimes(2));
    const second = (update.mock.calls[1] as unknown[])[0] as UpdateStrategyRequest;
    expect(second).toEqual(expect.objectContaining({ expectedRevision: 5, category: GtmStrategyCategory.UNSPECIFIED }));
    // Clearing the category is sent; the untouched fields are not.
    expect(second.name).toBeUndefined();
    expect(second.targetMetric).toBeUndefined();
  });

  it("says a read failed, with a retry, rather than that the strategy is gone", async () => {
    renderGtm(<StrategyView />, { ...reads, "GtmService.GetStrategy": { error: new ConnectError("down", Code.Unavailable) } }, at("?strategy=str-1"));
    expect(await screen.findByText("Couldn't load this strategy")).toBeInTheDocument();
    expect(screen.queryByText("Strategy not found")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
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
  const lastReq = (list: { mock: { calls: unknown[][] } }) => list.mock.calls[list.mock.calls.length - 1]![0] as ListStrategyLibraryRequest;

  it("opens on what fits the business, with Show all, and sends every filter to the service", async () => {
    const list = vi.fn(() => ({ entries: [launchWeek, meetup] }));
    renderGtm(<StrategyView />, { ...reads, "GtmService.ListStrategyLibrary": list }, at("?view=library"));
    const lib = await screen.findByTestId("gtm-library");
    expect(await within(lib).findByRole("heading", { name: "Launch week" })).toBeInTheDocument();
    expect(within(lib).getByRole("button", { name: "Fits my business", pressed: true })).toBeInTheDocument();
    expect(lastReq(list)).toEqual(expect.objectContaining({ orgId: TEST_ORG, fitsMyBusiness: true, categories: [], businessModels: [], businessTypes: [] }));
    // No prune banner or import history: the library is built in.
    expect(screen.queryByTestId("gtm-prune-requests")).not.toBeInTheDocument();
    expect(screen.queryByTestId("gtm-import-history")).not.toBeInTheDocument();

    fireEvent.click(within(lib).getByRole("button", { name: "Show all" }));
    await waitFor(() => expect(lastReq(list).fitsMyBusiness).toBe(false));
    expect(within(within(lib).getByRole("group", { name: "Category" })).getAllByRole("button")).toHaveLength(13);
    fireEvent.click(within(lib).getByRole("button", { name: "Community & events" }));
    fireEvent.click(within(within(lib).getByRole("group", { name: "Business model" })).getByRole("button", { name: "Subscription" }));
    fireEvent.click(within(within(lib).getByRole("group", { name: "Business model" })).getByRole("button", { name: "Freemium" }));
    fireEvent.change(within(lib).getByLabelText("Business type"), { target: { value: String(GtmBusinessType.DEVELOPER_TOOLS) } });
    fireEvent.change(within(lib).getByLabelText("Stage"), { target: { value: "launch" } });
    fireEvent.change(within(lib).getByLabelText("Effort"), { target: { value: "low" } });
    fireEvent.change(within(lib).getByLabelText("Cost"), { target: { value: "free" } });
    fireEvent.change(within(lib).getByPlaceholderText("Search the library"), { target: { value: "meetup" } });
    await waitFor(() =>
      expect(lastReq(list)).toEqual(
        expect.objectContaining({
          categories: [GtmStrategyCategory.COMMUNITY_EVENTS],
          businessModels: [GtmBusinessModel.SUBSCRIPTION, GtmBusinessModel.FREEMIUM],
          businessTypes: [GtmBusinessType.DEVELOPER_TOOLS],
          stage: "launch",
          effort: "low",
          cost: "free",
          query: "meetup",
          fitsMyBusiness: false,
        }),
      ),
    );
    fireEvent.click(within(lib).getByRole("button", { name: "Clear filters (8)" }));
    // The search is debounced, so the cleared query lands a moment after the rest.
    await waitFor(() => expect(lastReq(list)).toEqual(expect.objectContaining({ categories: [], businessModels: [], businessTypes: [] })));
    await waitFor(() => expect(lastReq(list).query).toBeUndefined());
  });

  it("hints at Positioning when the business type and models are not set", async () => {
    renderGtm(
      <StrategyView />,
      { ...reads, "GtmService.GetPositioning": { response: { positioning: create(GtmPositioningSchema, { orgId: TEST_ORG, revision: 0 }) } } },
      at("?view=library"),
    );
    const hint = await screen.findByTestId("gtm-library-profile-hint");
    expect(within(hint).getByRole("link", { name: "Set them on Positioning" })).toHaveAttribute("href", "/gtm/positioning");
    cleanup();
    renderGtm(<StrategyView />, reads, at("?view=library"));
    await screen.findByRole("heading", { name: "Launch week" });
    expect(screen.queryByTestId("gtm-library-profile-hint")).not.toBeInTheDocument();
  });

  it("labels an entry's facets and who it fits", async () => {
    expect(entryFacets(launchWeek)).toEqual(["Stage: Launch", "Effort: Medium", "Cost: Free"]);
    expect(entryFits(launchWeek)).toBe("Any business");
    expect(entryFits(meetup)).toBe("Developer tools · Free / open source");
    renderGtm(<StrategyView />, reads, at("?view=library"));
    const card = await screen.findByRole("article", { name: "Host a local meetup" });
    expect(within(card).getByText("Fits: Developer tools · Free / open source")).toBeInTheDocument();
  });

  it("opens an entry: why it works, the questions that tailor it, and its suggested workflow", async () => {
    const get = vi.fn(() => ({ entry: launchWeek }));
    const { router } = renderGtm(<StrategyView />, { ...reads, "GtmService.GetStrategyLibraryEntry": get }, at("?view=library"));
    fireEvent.click(await screen.findByRole("button", { name: "Details: Launch week" }));
    await waitFor(() => expect(router.state.location.search).toEqual(expect.objectContaining({ view: "library", entry: "sample-launch-week" })));
    const detail = await screen.findByTestId("gtm-library-entry");
    expect(await within(detail).findByText("Five reasons to talk about you in one week.")).toBeInTheDocument();
    expect(within(detail).getByText("What five small things could ship this month?")).toBeInTheDocument();
    const workflow = within(detail).getByTestId("gtm-suggested-workflow");
    expect(within(workflow).getByText(/draft the day's post from the changelog/)).toBeInTheDocument();
    expect(within(workflow).getByRole("button", { name: "Create this workflow" })).toBeInTheDocument();
    expect(get).toHaveBeenCalledWith(expect.objectContaining({ orgId: TEST_ORG, key: "sample-launch-week" }));
  });

  it("says so when a linked entry is no longer in the library", async () => {
    renderGtm(
      <StrategyView />,
      { ...reads, "GtmService.GetStrategyLibraryEntry": { error: new ConnectError("no", Code.NotFound) } },
      at("?view=library&entry=sample-gone"),
    );
    expect(await screen.findByText('The library no longer has "sample-gone".')).toBeInTheDocument();
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
    fireEvent.click(await screen.findByRole("button", { name: "Use this strategy: Host a local meetup" }));
    const dialog = await screen.findByTestId("gtm-use-strategy-dialog");
    fireEvent.click(within(dialog).getByRole("switch"));
    fireEvent.click(screen.getByRole("button", { name: "Create strategy" }));
    await waitFor(() => expect(use).toHaveBeenCalled());
    const req = (use.mock.calls[0] as unknown[])[0] as UseLibraryStrategyRequest;
    expect(req.createFirstCampaign).toBe(false);
    expect(req.campaignName).toBeUndefined();
  });

  it("offers more when the service has another page, and keeps what it read when that fails", async () => {
    const page = vi.fn((req: unknown) => {
      if ((req as { pageToken: string }).pageToken === "") return { entries: [launchWeek], nextPageToken: "p2" };
      throw new ConnectError("down", Code.Unavailable);
    });
    renderGtm(<StrategyView />, { ...reads, "GtmService.ListStrategyLibrary": page }, at("?view=library"));
    expect(await screen.findByText("1+ entry")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Show more" }));
    expect(await screen.findByTestId("gtm-library-partial")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Launch week" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(page).toHaveBeenCalledTimes(3));
  });
});
