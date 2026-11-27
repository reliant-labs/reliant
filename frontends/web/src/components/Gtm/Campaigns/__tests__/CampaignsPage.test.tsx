// Copyright (c) 2026 Reliant Labs

/**
 * GTM › Campaigns over GtmService (a mock transport): each campaign's counts,
 * its board by stage, and the moves a card may make: Drafting ↔ In review as
 * UpdateItem on the revision on screen, Approved only through a person's
 * confirm, and nothing into Out. The selected campaign's coverage comes from
 * GetCampaign (one-sided plans read as "any"); a campaign is created under a
 * strategy and edited on its revision, its planned lists sent whole.
 */

import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  GtmCampaignSchema,
  GtmCampaignStatus,
  GtmChannelMode,
  GtmChannelSchema,
  GtmCoverageRowSchema,
  GtmIdeaSchema,
  GtmIdeaStatus,
  GtmItemFormat,
  GtmItemSchema,
  GtmItemStatus,
  GtmStrategySchema,
  type CreateCampaignRequest,
  type UpdateCampaignRequest,
} from "@/gen/reliant/v1/gtm_pb";

import { renderGtm, TEST_ORG } from "../../shared/__tests__/gtmHarness";
import { CampaignsView, columnsOf, countsOf } from "../CampaignsPage";
import { coverageLabel, coverageSummary, gapGroups } from "../Coverage";

const toasts = vi.hoisted(() => ({ info: vi.fn(), warning: vi.fn(), error: vi.fn() }));
vi.mock("@/lib/toast-manager", () => ({ toast: toasts }));

// The board's drag and keyboard moves are kanban_board's own (its tests); here
// a stand-in reports a move the way it does, through onCardMove.
vi.mock("@/components/forge-ui/kanban_board", () => ({
  default: ({ columns, onCardMove }: { columns: { id: string; cards: { id: string; title: string }[] }[]; onCardMove: (card: string, from: string, to: string) => void }) => (
    <div>
      {columns.map((col) => (
        <section key={col.id} data-testid={`col-${col.id}`}>
          {col.cards.map((card) => (
            <div key={card.id}>
              <span>{card.title}</span>
              {["drafting", "review", "approved", "out"].map((to) => (
                <button key={to} type="button" onClick={() => onCardMove(card.id, col.id, to)}>{`move ${card.title} to ${to}`}</button>
              ))}
            </div>
          ))}
        </section>
      ))}
    </div>
  ),
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const strategy = create(GtmStrategySchema, { id: "str-1", orgId: TEST_ORG, name: "Developer-led growth" });
const reddit = create(GtmChannelSchema, { id: "ch-reddit", orgId: TEST_ORG, key: "reddit", name: "Reddit", mode: GtmChannelMode.MANUAL });
const blog = create(GtmChannelSchema, { id: "ch-blog", orgId: TEST_ORG, key: "blog", name: "Blog", mode: GtmChannelMode.MANUAL });
const campaign = create(GtmCampaignSchema, {
  id: "cmp-1",
  orgId: TEST_ORG,
  strategyId: "str-1",
  name: "Sites launch",
  goal: "200 sites",
  status: GtmCampaignStatus.ACTIVE,
  revision: 1,
  plannedChannelIds: ["ch-blog", "ch-reddit"],
  plannedFormats: [GtmItemFormat.LONG_POST],
});
const coverage = [
  create(GtmCoverageRowSchema, { channelId: "ch-blog", format: GtmItemFormat.LONG_POST, planned: true, counts: [{ status: GtmItemStatus.DRAFT, count: 1 }], total: 1 }),
  create(GtmCoverageRowSchema, { channelId: "ch-reddit", format: GtmItemFormat.LONG_POST, planned: true, total: 0 }),
  create(GtmCoverageRowSchema, { channelId: "", format: GtmItemFormat.EMAIL, planned: false, counts: [{ status: GtmItemStatus.REVIEW, count: 1 }, { status: GtmItemStatus.SENT, count: 1 }], total: 2 }),
];
const other = create(GtmCampaignSchema, { id: "cmp-2", orgId: TEST_ORG, name: "Newsletter", status: GtmCampaignStatus.DONE, revision: 1 });
const idea = create(GtmIdeaSchema, { id: "idea-1", orgId: TEST_ORG, campaignId: "cmp-1", title: "Launch idea", status: GtmIdeaStatus.ACCEPTED });
const rejected = create(GtmIdeaSchema, { id: "idea-2", orgId: TEST_ORG, campaignId: "cmp-1", title: "Dropped idea", status: GtmIdeaStatus.REJECTED });
const draft = create(GtmItemSchema, { id: "item-1", orgId: TEST_ORG, campaignId: "cmp-1", format: GtmItemFormat.LONG_POST, channelId: "ch-blog", title: "Launch post", status: GtmItemStatus.DRAFT, revision: 5 });
const email = create(GtmItemSchema, { id: "item-2", orgId: TEST_ORG, campaignId: "cmp-1", format: GtmItemFormat.EMAIL, title: "Invite email", status: GtmItemStatus.REVIEW, revision: 2 });
const sent = create(GtmItemSchema, { id: "item-3", orgId: TEST_ORG, campaignId: "cmp-1", format: GtmItemFormat.EMAIL, title: "Sent email", status: GtmItemStatus.SENT, revision: 4 });

const reads = (update: unknown = { response: {} }) => ({
  "GtmService.ListCampaigns": { response: { campaigns: [campaign, other] } },
  "GtmService.ListIdeas": { response: { ideas: [idea, rejected] } },
  "GtmService.ListItems": { response: { items: [draft, email, sent] } },
  "GtmService.UpdateItem": update as never,
  "GtmService.ListStrategies": { response: { strategies: [strategy] } },
  "GtmService.ListChannels": { response: { channels: [blog, reddit] } },
  "GtmService.GetCampaign": { response: { campaign, coverage } },
  "GtmService.ListAssets": { response: { assets: [] } },
});

describe("the board's model", () => {
  it("counts a campaign's live ideas, posts and emails", () => {
    expect(countsOf([idea, rejected], [draft, email, sent], "cmp-1")).toEqual({ ideas: 1, posts: 1, emails: 2 });
  });

  it("puts each record in its stage", () => {
    const cols = Object.fromEntries(columnsOf([idea, rejected], [draft, email, sent], "cmp-1").map((c) => [c.id, c.cards.map((card) => card.title)]));
    expect(cols).toEqual({ ideas: ["Launch idea"], drafting: ["Launch post"], review: ["Invite email"], approved: [], out: ["Sent email"] });
  });
});

describe("CampaignsView", () => {
  it("shows the org's campaigns with their counts", async () => {
    renderGtm(<CampaignsView />, reads(), { path: "/gtm/campaigns" });
    expect(await screen.findByText("Sites launch")).toBeInTheDocument();
    // Toggle buttons, one pressed: each is a tab stop, with no radio semantics to half-implement.
    expect(screen.getByRole("button", { name: /Sites launch/, pressed: true })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Newsletter/, pressed: false })).toBeInTheDocument();
    expect(await screen.findByText("1 idea")).toBeInTheDocument();
    expect(screen.getByText("2 emails")).toBeInTheDocument();
  });

  it("moves a draft into review on the revision on screen", async () => {
    const update = vi.fn(() => ({ item: { ...draft, status: GtmItemStatus.REVIEW, revision: 6 } }));
    renderGtm(<CampaignsView />, reads(update), { path: "/gtm/campaigns" });
    fireEvent.click(await screen.findByRole("button", { name: "move Launch post to review" }));
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(update).toHaveBeenCalledWith(expect.objectContaining({ orgId: TEST_ORG, id: "item-1", expectedRevision: 5, status: GtmItemStatus.REVIEW }));
  });

  it("approves only after a person confirms", async () => {
    const update = vi.fn(() => ({ item: { ...email, status: GtmItemStatus.APPROVED } }));
    renderGtm(<CampaignsView />, reads(update), { path: "/gtm/campaigns" });
    fireEvent.click(await screen.findByRole("button", { name: "move Invite email to approved" }));
    expect(await screen.findByTestId("person-action-dialog-approve")).toBeInTheDocument();
    expect(update).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(update).toHaveBeenCalledWith(expect.objectContaining({ id: "item-2", expectedRevision: 2, status: GtmItemStatus.APPROVED })));
  });

  it("shows the claim check in the approval dialog, as the item's drawer does", async () => {
    const withBody = create(GtmItemSchema, { ...email, bodyMd: "We are SOC 2 certified." });
    const check = vi.fn(() => ({ findings: [{ mention: "SOC 2", blocking: true, reason: "no verified fact" }], blocking: true }));
    renderGtm(
      <CampaignsView />,
      { ...reads(), "GtmService.ListItems": { response: { items: [withBody] } }, "GtmService.CheckClaims": check },
      { path: "/gtm/campaigns" },
    );
    fireEvent.click(await screen.findByRole("button", { name: "move Invite email to approved" }));
    const dialog = await screen.findByTestId("person-action-dialog-approve");
    expect(await within(dialog).findByText("· 1 blocking")).toBeInTheDocument();
    expect(within(dialog).getByText("SOC 2")).toBeInTheDocument();
    expect(check).toHaveBeenCalledWith(expect.objectContaining({ orgId: TEST_ORG, text: "We are SOC 2 certified." }));
  });

  it("sends and publishes nothing: a move into Out only explains", async () => {
    const update = vi.fn(() => ({}));
    renderGtm(<CampaignsView />, reads(update), { path: "/gtm/campaigns" });
    fireEvent.click(await screen.findByRole("button", { name: "move Invite email to out" }));
    expect(toasts.info).toHaveBeenCalledWith(expect.stringMatching(/through the agent/));
    expect(update).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: /^send/i })).not.toBeInTheDocument();
  });

  it("resumes a paused item only as a draft, and leaves an approved one where it is", async () => {
    const paused = create(GtmItemSchema, { ...draft, id: "item-4", title: "Paused post", status: GtmItemStatus.PAUSED, revision: 7 });
    const approved = create(GtmItemSchema, { ...draft, id: "item-5", title: "Approved post", status: GtmItemStatus.APPROVED, revision: 8 });
    const update = vi.fn(() => ({}));
    renderGtm(<CampaignsView />, { ...reads(update), "GtmService.ListItems": { response: { items: [paused, approved] } } }, { path: "/gtm/campaigns" });
    fireEvent.click(await screen.findByRole("button", { name: "move Paused post to review" }));
    expect(toasts.info).toHaveBeenCalledWith(expect.stringMatching(/resumes as a draft/));
    fireEvent.click(screen.getByRole("button", { name: "move Approved post to drafting" }));
    expect(update).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "move Paused post to drafting" }));
    await waitFor(() => expect(update).toHaveBeenCalledWith(expect.objectContaining({ id: "item-4", expectedRevision: 7, status: GtmItemStatus.DRAFT })));
    expect(update).toHaveBeenCalledTimes(1);
  });

  it("says when a list was cut at its first page", async () => {
    renderGtm(<CampaignsView />, { ...reads(), "GtmService.ListItems": { response: { items: [draft], nextPageToken: "more" } } }, { path: "/gtm/campaigns" });
    expect(await screen.findByTestId("gtm-campaigns-truncated")).toBeInTheDocument();
  });

  it("does not move what is already out", async () => {
    const update = vi.fn(() => ({}));
    renderGtm(<CampaignsView />, reads(update), { path: "/gtm/campaigns" });
    fireEvent.click(await screen.findByRole("button", { name: "move Sent email to drafting" }));
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(update).not.toHaveBeenCalled();
  });
});

describe("coverage", () => {
  it("names each pair, one-sided plans as any", () => {
    const channels = [blog, reddit];
    expect(coverageLabel(coverage[1]!, channels)).toBe("Reddit · Long post");
    expect(coverageLabel(create(GtmCoverageRowSchema, { channelId: "ch-reddit", planned: true }), channels)).toBe("Reddit · any format");
    expect(coverageLabel(create(GtmCoverageRowSchema, { format: GtmItemFormat.VIDEO, planned: true }), channels)).toBe("Any channel · Video");
    expect(coverageLabel(coverage[2]!, channels)).toBe("No channel · Email");
  });

  it("says a planned pair with nothing drafted plainly, and sums the rest by stage", () => {
    expect(coverageSummary(coverage[1]!)).toBe("planned, nothing drafted");
    expect(coverageSummary(coverage[0]!)).toBe("1 in progress");
    expect(coverageSummary(coverage[2]!)).toBe("1 out · 1 in progress");
  });

  it("shows the selected campaign's coverage from GetCampaign", async () => {
    const get = vi.fn(() => ({ campaign, coverage }));
    renderGtm(<CampaignsView />, { ...reads(), "GtmService.GetCampaign": get }, { path: "/gtm/campaigns" });
    const panel = await screen.findByTestId("gtm-campaign-coverage");
    expect(await within(panel).findByText("Blog · Long post")).toBeInTheDocument();
    expect(within(panel).getByText("Not planned")).toBeInTheDocument();
    // The gaps read one line per channel, and expand to each pair.
    const gaps = within(panel).getByTestId("gtm-coverage-gaps");
    expect(within(gaps).getByText("Reddit:")).toBeInTheDocument();
    expect(within(gaps).getByText("Long post")).toBeInTheDocument();
    fireEvent.click(within(gaps).getByRole("button", { name: "Show each pair" }));
    expect(within(gaps).getByText("planned, nothing drafted")).toBeInTheDocument();
    expect(get).toHaveBeenCalledWith(expect.objectContaining({ orgId: TEST_ORG, id: "cmp-1" }));
  });
});

describe("gapGroups", () => {
  it("gathers a campaign's planned pairs with nothing drafted per channel, in coverage order", () => {
    const rows = [
      create(GtmCoverageRowSchema, { channelId: "ch-reddit", format: GtmItemFormat.VIDEO, planned: true }),
      create(GtmCoverageRowSchema, { channelId: "ch-blog", format: GtmItemFormat.LONG_POST, planned: true, total: 1, counts: [{ status: GtmItemStatus.DRAFT, count: 1 }] }),
      create(GtmCoverageRowSchema, { channelId: "ch-reddit", format: GtmItemFormat.EMAIL, planned: true }),
      create(GtmCoverageRowSchema, { channelId: "", format: GtmItemFormat.SHORT_POST, planned: true }),
    ];
    expect(gapGroups(rows, [blog, reddit]).map((g) => `${g.channel}: ${g.formats.join(", ")}`)).toEqual(["Reddit: Video, Email", "Any channel: Short post"]);
  });
});

describe("creating and editing a campaign", () => {
  it("creates one under a strategy, with its planned channels and formats", async () => {
    const createC = vi.fn((req: unknown) => ({ campaign: { ...campaign, id: "cmp-9", name: (req as CreateCampaignRequest).name } }));
    renderGtm(<CampaignsView />, { ...reads(), "GtmService.CreateCampaign": createC }, { path: "/gtm/campaigns" });
    fireEvent.click(await screen.findByRole("button", { name: "New campaign" }));
    const form = await screen.findByTestId("gtm-campaign-form");
    fireEvent.change(within(form).getByLabelText("Name"), { target: { value: "Flows launch" } });
    await waitFor(() => expect(within(form).getByRole("option", { name: "Developer-led growth" })).toBeInTheDocument());
    fireEvent.click(within(form).getByLabelText("Reddit"));
    fireEvent.click(within(form).getByLabelText("Video"));
    fireEvent.click(screen.getByRole("button", { name: "Create campaign" }));
    await waitFor(() => expect(createC).toHaveBeenCalled());
    expect(createC).toHaveBeenCalledWith(
      expect.objectContaining({ orgId: TEST_ORG, strategyId: "str-1", name: "Flows launch", plannedChannelIds: ["ch-reddit"], plannedFormats: [GtmItemFormat.VIDEO] }),
    );
  });

  it("edits on the revision on screen, sending the planned lists whole", async () => {
    const updateC = vi.fn(() => ({ campaign }));
    renderGtm(<CampaignsView />, { ...reads(), "GtmService.UpdateCampaign": updateC }, { path: "/gtm/campaigns" });
    fireEvent.click(await screen.findByRole("button", { name: "Edit Sites launch" }));
    const form = await screen.findByTestId("gtm-campaign-form");
    fireEvent.click(within(form).getByLabelText("Reddit"));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(updateC).toHaveBeenCalled());
    const req = (updateC.mock.calls[0] as unknown[])[0] as UpdateCampaignRequest;
    expect(req).toEqual(expect.objectContaining({ orgId: TEST_ORG, id: "cmp-1", expectedRevision: 1, strategyId: "str-1" }));
    expect(req.plannedChannelIds?.ids).toEqual(["ch-blog"]);
    expect(req.plannedFormats?.formats).toEqual([GtmItemFormat.LONG_POST]);
  });

  it("after a conflict, edits the campaign as it is now, on its current revision", async () => {
    let server = campaign;
    const updateC = vi.fn((req: unknown) => {
      if ((req as UpdateCampaignRequest).expectedRevision === 1) {
        server = create(GtmCampaignSchema, { ...campaign, goal: "300 sites", revision: 2 });
        throw new ConnectError("revision moved", Code.Aborted);
      }
      return { campaign: server };
    });
    renderGtm(
      <CampaignsView />,
      { ...reads(), "GtmService.ListCampaigns": () => ({ campaigns: [server, other] }), "GtmService.UpdateCampaign": updateC },
      { path: "/gtm/campaigns" },
    );
    fireEvent.click(await screen.findByRole("button", { name: "Edit Sites launch" }));
    fireEvent.click(within(await screen.findByTestId("gtm-campaign-form")).getByLabelText("Reddit"));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(toasts.warning).toHaveBeenCalled());
    await waitFor(() => expect(within(screen.getByTestId("gtm-campaign-form")).getByLabelText("Goal")).toHaveValue("300 sites"));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(updateC).toHaveBeenCalledTimes(2));
    expect(updateC).toHaveBeenLastCalledWith(expect.objectContaining({ expectedRevision: 2, goal: "300 sites" }));
  });
});
