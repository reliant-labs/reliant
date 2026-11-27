// Copyright (c) 2026 Reliant Labs

/**
 * The GTM sample pack (handlers/reliant/gtm.ts, data/gtm.ts), over the same
 * read-only fixture transport the preview serves: the volumes the preview
 * promises, the reads' filters, the in-memory writes and their revision rule,
 * the org gate, and the deterministic claim check.
 */
import { timestampFromMs, timestampMs } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { beforeEach, describe, expect, it } from "vitest";

import { ChatService } from "@/gen/reliant/v1/chat_pb";
import {
  GtmAssetStatus,
  GtmCampaignStatus,
  GtmChannelMode,
  GtmChannelSource,
  GtmClaimCategory,
  GtmClaimFindingKind,
  GtmClaimSourceKind,
  GtmClaimStatus,
  GtmConnectionState,
  GtmCredentialKind,
  GtmIdeaKind,
  GtmIdeaStatus,
  GtmItemFormat,
  GtmItemStatus,
  GtmLibraryDepth,
  GtmPruneRequestStatus,
  GtmRevisionConflictSchema,
  GtmService,
  GtmStrategyCategory,
  GtmStrategyStatus,
} from "@/gen/reliant/v1/gtm_pb";

import { createFixtureTransport } from "../../transport";
import { shopAppPack } from "../app";
import { GTM_CATEGORY_LABELS, GTM_CHANNEL_IDS, GTM_PRUNE_REQUEST_ID } from "../data/gtm";
import { GTM_ANSWERS } from "../handlers/reliant/gtm";
import { SHOP_READ_ONLY_MESSAGE } from "../index";
import { DEMO_ORG, demoNow, resetDemoNowForTests } from "../shopStory";

// The 15th of a 31-day month, noon: the calendar's month is July 2026.
const NOW = new Date(2026, 6, 15, 12, 0, 0).getTime();

/** A fresh pack (and so a fresh in-memory store), served as the preview serves it. */
function gtmClient() {
  const transport = createFixtureTransport({ pack: shopAppPack({ now: NOW }), now: demoNow, readOnly: true, readOnlyMessage: SHOP_READ_ONLY_MESSAGE });
  return createClient(GtmService, transport);
}

const org = { orgId: DEMO_ORG };

/** Whole calendar days from the day `now` falls on to the day `at` falls on (negative: before). */
function daysFromToday(now: number, at: number): number {
  const day = (ms: number) => {
    const d = new Date(ms);
    return Date.UTC(d.getFullYear(), d.getMonth(), d.getDate());
  };
  return Math.round((day(at) - day(now)) / 86_400_000);
}

async function rejection(promise: Promise<unknown>): Promise<ConnectError> {
  try {
    await promise;
  } catch (e) {
    return ConnectError.from(e);
  }
  throw new Error("expected a rejection");
}

beforeEach(() => resetDemoNowForTests(NOW));

describe("the GTM sample pack", () => {
  it("answers every GtmService write the pack lists, and lists every write", () => {
    const writes = GtmService.methods.filter((m) => m.idempotency !== 1 /* NO_SIDE_EFFECTS */).map((m) => `reliant.v1.GtmService/${m.name}`);
    expect([...GTM_ANSWERS].sort()).toEqual(writes.sort());
  });

  it("enables GTM for the shop user's org only", async () => {
    const client = gtmClient();
    expect((await client.getGtmAccess(org)).enabled).toBe(true);
    expect((await client.getGtmAccess({ orgId: "org-someone-else" })).enabled).toBe(false);
    expect((await rejection(client.listIdeas({ orgId: "org-someone-else" }))).code).toBe(Code.NotFound);
  });

  it("holds the sample volumes: 12 ideas, 2 strategies, 2 campaigns, 9 channels, about 15 items around today, 2 assets, 6 library entries, 8 claims", async () => {
    const client = gtmClient();
    const { ideas } = await client.listIdeas(org);
    expect(ideas.length).toBeGreaterThanOrEqual(12);
    expect(new Set(ideas.map((i) => i.status))).toEqual(new Set([GtmIdeaStatus.PROPOSED, GtmIdeaStatus.ACCEPTED, GtmIdeaStatus.DONE, GtmIdeaStatus.REJECTED]));
    expect(new Set(ideas.map((i) => i.kind))).toEqual(new Set([GtmIdeaKind.PRODUCT, GtmIdeaKind.GTM, GtmIdeaKind.CONTENT]));
    // Newest first.
    expect(ideas.map((i) => timestampMs(i.createdAt!))).toEqual(ideas.map((i) => timestampMs(i.createdAt!)).sort((a, b) => b - a));

    const { strategies } = await client.listStrategies(org);
    expect(strategies.map((s) => s.status).sort()).toEqual([GtmStrategyStatus.DRAFT, GtmStrategyStatus.ACTIVE]);
    const { campaigns } = await client.listCampaigns(org);
    expect(campaigns).toHaveLength(2);
    expect(campaigns.every((c) => strategies.some((s) => s.id === c.strategyId))).toBe(true);

    const { items } = await client.listItems(org);
    expect(items.length).toBeGreaterThanOrEqual(15);
    expect(items.length).toBeLessThanOrEqual(16);
    const scheduled = items.filter((i) => i.scheduledAt);
    expect(scheduled.length).toBeGreaterThanOrEqual(13);
    // From 10 days before today to 20 days after it.
    for (const i of scheduled) expect(daysFromToday(NOW, timestampMs(i.scheduledAt!))).toBeGreaterThanOrEqual(-10);
    for (const i of scheduled) expect(daysFromToday(NOW, timestampMs(i.scheduledAt!))).toBeLessThanOrEqual(20);
    expect(new Set(items.map((i) => i.format))).toContain(GtmItemFormat.EMAIL);

    expect((await client.listAssets({ ...org, itemId: "gtm-item-show-hn" })).assets).toHaveLength(1);
    expect((await client.listAssets({ ...org, campaignId: campaigns[0]!.id })).assets.length + (await client.listAssets({ ...org, campaignId: campaigns[1]!.id })).assets.length).toBe(1);

    const { entries } = await client.listStrategyLibrary(org);
    expect(entries).toHaveLength(6);
    expect(entries.every((e) => e.name.endsWith("(placeholder)"))).toBe(true);

    const { claims } = await client.listClaims(org);
    expect(claims).toHaveLength(8);
    expect(new Set(claims.map((c) => c.status))).toEqual(new Set([GtmClaimStatus.VERIFIED, GtmClaimStatus.UNVERIFIED, GtmClaimStatus.RETIRED]));

    const { positioning } = await client.getPositioning(org);
    expect(positioning?.positioningMd).not.toBe("");
    expect(positioning?.audiences.length).toBeGreaterThan(0);
  });

  it("has the seven default channels plus a second Gmail one and two agent-added manual ones", async () => {
    const { channels } = await gtmClient().listChannels(org);
    expect(channels.map((c) => c.key)).toEqual(["blog", "brewing-forums", "email", "hacker-news", "linkedin", "outreach", "podcast-guests", "reddit", "x", "youtube"]);
    const seeded = channels.filter((c) => c.source === GtmChannelSource.SEED).map((c) => c.key);
    expect(seeded).toEqual(["blog", "email", "hacker-news", "linkedin", "reddit", "x", "youtube"]);
    const gmail = channels.filter((c) => c.mode === GtmChannelMode.INTEGRATED);
    expect(gmail.map((c) => [c.key, c.integrationKind, c.connectionState])).toEqual([
      ["email", "gmail", GtmConnectionState.CONNECTED], ["outreach", "gmail", GtmConnectionState.NOT_CONNECTED],
    ]);
    expect(channels.filter((c) => c.source === GtmChannelSource.AGENT).map((c) => [c.key, c.mode])).toEqual([["brewing-forums", GtmChannelMode.MANUAL], ["podcast-guests", GtmChannelMode.MANUAL]]);
    expect(channels.filter((c) => c.mode === GtmChannelMode.MANUAL).every((c) => !c.integrationKind && !c.connectionId)).toBe(true);
  });

  it("has an approved manual item awaiting its URL, a published one with a URL, and a held email", async () => {
    const client = gtmClient();
    const hn = (await client.getItem({ ...org, id: "gtm-item-show-hn" })).item!;
    expect([hn.status, hn.channelId, hn.publishedUrl]).toEqual([GtmItemStatus.APPROVED, GTM_CHANNEL_IDS.hackerNews, ""]);
    const published = (await client.listItems({ ...org, status: GtmItemStatus.PUBLISHED })).items;
    expect(published.length).toBeGreaterThan(0);
    expect(published.every((i) => /^https?:\/\//.test(i.publishedUrl))).toBe(true);
    const held = (await client.listItems(org)).items.filter((i) => i.approvalHash !== "" && i.status !== GtmItemStatus.SENT);
    expect(held.map((i) => [i.id, i.status, i.format])).toEqual([["gtm-item-grind-guide-email", GtmItemStatus.APPROVED, GtmItemFormat.EMAIL]]);
    const sent = (await client.listItems({ ...org, status: GtmItemStatus.SENT })).items;
    expect(sent.every((i) => i.gmailMessageId !== "" && i.sentAt)).toBe(true);
  });

  it("has email-type items that are never scheduled or published, whatever the clock says", async () => {
    for (const at of [new Date(2026, 6, 1, 8), new Date(2026, 6, 15, 12), new Date(2026, 6, 30, 23)]) {
      resetDemoNowForTests(at.getTime());
      const { items } = await createFixtureClient(at.getTime()).listItems(org);
      for (const i of items.filter((x) => x.format === GtmItemFormat.EMAIL || x.format === GtmItemFormat.COLD_OUTREACH)) {
        expect([GtmItemStatus.DRAFT, GtmItemStatus.REVIEW, GtmItemStatus.APPROVED, GtmItemStatus.SENT, GtmItemStatus.PAUSED], `${i.id} at ${at.toISOString()}`).toContain(i.status);
      }
    }
  });

  it("places items from 10 days ago to 20 days ahead of today, so the calendar and the phone's agenda are never empty", async () => {
    // Month ends, year ends, a leap day, and the day the preview showed nothing on (27 Nov).
    const clocks = [new Date(2026, 10, 27, 9), new Date(2026, 11, 31, 23), new Date(2028, 1, 29, 1), new Date(2026, 1, 3, 9), new Date(2026, 0, 1, 0, 5), new Date(2026, 6, 31, 12)];
    for (const at of clocks) {
      resetDemoNowForTests(at.getTime());
      const { items } = await createFixtureClient(at.getTime()).listItems(org);
      const dated = items.filter((x) => x.scheduledAt);
      const days = dated.map((x) => daysFromToday(at.getTime(), timestampMs(x.scheduledAt!)));
      expect(Math.min(...days), at.toISOString()).toBeGreaterThanOrEqual(-10);
      expect(Math.max(...days), at.toISOString()).toBeLessThanOrEqual(20);
      // The phone's agenda is what is dated from the start of today on.
      const startOfToday = new Date(at.getFullYear(), at.getMonth(), at.getDate()).getTime();
      const upcoming = dated.filter((x) => timestampMs(x.scheduledAt!) >= startOfToday);
      expect(upcoming.length, `${at.toISOString()}: agenda`).toBeGreaterThanOrEqual(8);
      expect(dated.length - upcoming.length, `${at.toISOString()}: already out`).toBeGreaterThanOrEqual(4);
      // The Show HN item is dated today, so it leads the agenda.
      expect(upcoming.sort((p, q) => timestampMs(p.scheduledAt!) - timestampMs(q.scheduledAt!))[0]?.id).toBe("gtm-item-show-hn");
    }
  });

  it("is generic: nothing in the sample copy names, or restates a fact of, a real product", async () => {
    const client = gtmClient();
    const downloads = await Promise.all(["gtm-asset-hn-hero", "gtm-asset-brand-sheet"].map(async (assetId) => decodeURIComponent((await client.getAssetDownload({ ...org, assetId })).url)));
    const text = JSON.stringify([
      (await client.listIdeas(org)).ideas, (await client.listItems(org)).items, (await client.listClaims(org)).claims, (await client.getPositioning(org)).positioning,
      (await client.listStrategies(org)).strategies, (await client.listChannels(org)).channels, (await client.listCampaigns(org)).campaigns, (await client.listStrategyLibrary(org)).entries,
      (await client.listLibraryImports(org)).imports, (await client.listLibraryPruneRequests(org)).requests, (await client.listAssets({ ...org, itemId: "gtm-item-show-hn" })).assets,
      (await client.listAssets({ ...org, campaignId: "gtm-cmp-weekly-box" })).assets, downloads,
    ], (key, value) => (key === "$typeName" ? undefined : typeof value === "bigint" ? value.toString() : value));
    // The fiction is a coffee company, so none of the real company's own words or facts has a place in it.
    const DENYLIST: [string, RegExp][] = [
      ["a product's own name", /reliant|forge/i],
      ["a free static site per organization", /static site|static hosting|per organization is free|one site per/i],
      ["the CDN and custom-domain pitch", /\bcdn\b|custom domain/i],
      ["releases promoted by digest", /promoted by digest|built once|by digest/i],
      ["runs without a dedicated machine", /dedicated machine|without a machine|no machine|machine-free/i],
      ["a credential the author never sees", /saved credential|never sees/i],
      ["approval before anything sends, publishes or spends", /sends, publishes or spends|waits for your approval/i],
      ["scheduled flows and their steps", /\bflows?\b|scheduled job|cron/i],
      ["a real plan name", /\b(starter|enterprise)\b|\b(pro|team) (plan|tier)\b/i],
      ["the launch-week play", /launch week|launch-week/i],
    ];
    for (const [what, pattern] of DENYLIST) expect(text, what).not.toMatch(pattern);
  });
});

function createFixtureClient(now: number) {
  return createClient(GtmService, createFixtureTransport({ pack: shopAppPack({ now }), now: demoNow, readOnly: true, readOnlyMessage: SHOP_READ_ONLY_MESSAGE }));
}

describe("the reads' filters", () => {
  it("filters ideas by kind, status and campaign", async () => {
    const client = gtmClient();
    const proposed = (await client.listIdeas({ ...org, status: GtmIdeaStatus.PROPOSED })).ideas;
    expect(proposed.length).toBeGreaterThan(0);
    expect(proposed.every((i) => i.status === GtmIdeaStatus.PROPOSED)).toBe(true);
    const product = (await client.listIdeas({ ...org, kind: GtmIdeaKind.PRODUCT })).ideas;
    expect(product.every((i) => i.kind === GtmIdeaKind.PRODUCT)).toBe(true);
    const all = (await client.listIdeas(org)).ideas;
    const campaignId = all.find((i) => i.campaignId)!.campaignId;
    const inCampaign = (await client.listIdeas({ ...org, campaignId })).ideas;
    expect(inCampaign.length).toBeGreaterThan(0);
    expect(inCampaign.every((i) => i.campaignId === campaignId)).toBe(true);
    // Both filters together.
    const both = (await client.listIdeas({ ...org, kind: GtmIdeaKind.CONTENT, status: GtmIdeaStatus.DONE })).ideas;
    expect(both.every((i) => i.kind === GtmIdeaKind.CONTENT && i.status === GtmIdeaStatus.DONE)).toBe(true);
  });

  it("pages ideas by token", async () => {
    const client = gtmClient();
    const first = await client.listIdeas({ ...org, pageSize: 5 });
    expect(first.ideas).toHaveLength(5);
    expect(first.nextPageToken).not.toBe("");
    const second = await client.listIdeas({ ...org, pageSize: 100, pageToken: first.nextPageToken });
    expect(second.nextPageToken).toBe("");
    expect(new Set([...first.ideas, ...second.ideas].map((i) => i.id)).size).toBe((await client.listIdeas(org)).ideas.length);
  });

  it("filters campaigns by status and strategy, and strategies by status", async () => {
    const client = gtmClient();
    expect((await client.listCampaigns({ ...org, status: GtmCampaignStatus.ACTIVE })).campaigns.map((c) => c.status)).toEqual([GtmCampaignStatus.ACTIVE]);
    const active = (await client.listStrategies({ ...org, status: GtmStrategyStatus.ACTIVE })).strategies;
    expect(active).toHaveLength(1);
    expect((await client.listCampaigns({ ...org, strategyId: active[0]!.id })).campaigns).toHaveLength(2);
    const draft = (await client.listStrategies({ ...org, status: GtmStrategyStatus.DRAFT })).strategies[0]!;
    expect((await client.listCampaigns({ ...org, strategyId: draft.id })).campaigns).toHaveLength(0);
    const detail = await client.getStrategy({ ...org, id: active[0]!.id });
    expect([detail.campaigns.length, detail.truncated]).toEqual([2, false]);
  });

  it("filters the library by category, depth, stage, cost, difficulty, fit and a text query, and pages it", async () => {
    const client = gtmClient();
    const list = async (req: object) => (await client.listStrategyLibrary({ ...org, ...req })).entries;
    expect((await list({ category: GtmStrategyCategory.PARTNERSHIPS_DISTRIBUTION })).map((e) => e.key)).toEqual(["sample-tasting-popup"]);
    expect((await list({ depth: GtmLibraryDepth.PLAY })).every((e) => e.depth === GtmLibraryDepth.PLAY)).toBe(true);
    expect((await list({ stage: "EARLY" })).every((e) => e.stage === "EARLY")).toBe(true);
    expect((await list({ cost: "FREE" })).every((e) => e.cost === "FREE")).toBe(true);
    expect((await list({ difficulty: "EASY" })).every((e) => e.difficulty === "EASY")).toBe(true);
    expect((await list({ fit: "MARKETPLACE" })).map((e) => e.key)).toEqual(["sample-annual-nudge"]);
    expect((await list({ query: "QUOTES" })).map((e) => e.key)).toEqual(["sample-quote-wall"]);
    expect(await list({ query: "no such thing" })).toHaveLength(0);
    // By category then name.
    const all = await list({});
    expect(all.map((e) => e.category)).toEqual([...all.map((e) => e.category)].sort((a, b) => a - b));
    const first = await client.listStrategyLibrary({ ...org, pageSize: 4 });
    expect([first.entries.length, first.nextPageToken !== ""]).toEqual([4, true]);
    expect((await client.listStrategyLibrary({ ...org, pageSize: 4, pageToken: first.nextPageToken })).entries).toHaveLength(2);
    expect((await client.getStrategyLibraryEntry({ ...org, key: "sample-faq-pages" })).entry?.category).toBe(GtmStrategyCategory.ORGANIC_CONTENT_SEO);
    expect((await rejection(client.getStrategyLibraryEntry({ ...org, key: "nope" }))).code).toBe(Code.NotFound);
  });

  it("names all 13 library categories, and the sample library uses a few", () => {
    const real = Object.values(GtmStrategyCategory).filter((v): v is number => typeof v === "number" && v !== GtmStrategyCategory.UNSPECIFIED);
    expect(real).toHaveLength(13);
    expect(real.every((c) => GTM_CATEGORY_LABELS[c])).toBe(true);
  });

  it("answers channel filters, channel reads, the audit list and the prune requests", async () => {
    const client = gtmClient();
    expect((await client.listChannels({ ...org, mode: GtmChannelMode.INTEGRATED })).channels).toHaveLength(2);
    expect((await client.listChannels({ ...org, status: 1 })).channels).toHaveLength(10);
    expect((await client.getChannel({ ...org, id: GTM_CHANNEL_IDS.email })).channel?.integrationKind).toBe("gmail");
    const { imports } = await client.listLibraryImports(org);
    expect(imports).toHaveLength(3);
    expect(imports.every((i) => i.credentialKind === GtmCredentialKind.TOKEN)).toBe(true);
    expect(imports.map((i) => timestampMs(i.createdAt!))).toEqual(imports.map((i) => timestampMs(i.createdAt!)).sort((a, b) => b - a));
    const pending = (await client.listLibraryPruneRequests({ ...org, status: GtmPruneRequestStatus.PENDING })).requests;
    expect(pending.map((r) => [r.id, r.removeKeys.length, r.status])).toEqual([[GTM_PRUNE_REQUEST_ID, 2, GtmPruneRequestStatus.PENDING]]);
    expect(pending[0]!.removeKeys.every((k) => ["sample-annual-nudge", "sample-quote-wall"].includes(k))).toBe(true);
    expect((await client.listLibraryPruneRequests({ ...org, status: GtmPruneRequestStatus.CONFIRMED })).requests).toHaveLength(0);
  });

  it("filters items by campaign, format, channel, status and a half-open scheduled range, soonest first", async () => {
    const client = gtmClient();
    const all = (await client.listItems(org)).items;
    const campaignId = all.find((i) => i.campaignId)!.campaignId;
    const mine = (await client.listItems({ ...org, campaignId })).items;
    expect(mine.length).toBeGreaterThan(0);
    expect(mine.every((i) => i.campaignId === campaignId)).toBe(true);
    const emails = (await client.listItems({ ...org, format: GtmItemFormat.EMAIL })).items;
    expect(emails.length).toBeGreaterThan(0);
    expect(emails.every((i) => i.format === GtmItemFormat.EMAIL)).toBe(true);
    const onHn = (await client.listItems({ ...org, channelId: GTM_CHANNEL_IDS.hackerNews })).items;
    expect(onHn.map((i) => i.id)).toEqual(["gtm-item-show-hn"]);
    expect((await client.listItems({ ...org, status: GtmItemStatus.SENT })).items.every((i) => i.status === GtmItemStatus.SENT)).toBe(true);

    const from = new Date(2026, 6, 10).getTime();
    const to = new Date(2026, 6, 20).getTime();
    const week = (await client.listItems({ ...org, scheduledFrom: timestampFromMs(from), scheduledTo: timestampFromMs(to) })).items;
    expect(week.length).toBeGreaterThan(0);
    const times = week.map((i) => timestampMs(i.scheduledAt!));
    expect(times.every((t) => t >= from && t < to)).toBe(true);
    expect(times).toEqual([...times].sort((a, b) => a - b));
    // The upper bound is exclusive: a range ending exactly at an item's time leaves it out.
    const edge = timestampMs(week[0].scheduledAt!);
    const upToEdge = (await client.listItems({ ...org, scheduledFrom: timestampFromMs(from), scheduledTo: timestampFromMs(edge) })).items;
    expect(upToEdge.some((i) => i.id === week[0].id)).toBe(false);
    // A range lists scheduled items only; the backlog item has no date.
    const ranged = (await client.listItems({ ...org, scheduledFrom: timestampFromMs(0) })).items;
    expect(ranged.length).toBe(all.filter((i) => i.scheduledAt).length);
  });

  it("filters claims by status, category and search", async () => {
    const client = gtmClient();
    expect((await client.listClaims({ ...org, status: GtmClaimStatus.RETIRED })).claims).toHaveLength(1);
    expect((await client.listClaims({ ...org, category: GtmClaimCategory.PRICING })).claims.every((c) => c.category === GtmClaimCategory.PRICING)).toBe(true);
    expect((await client.listClaims({ ...org, search: "FREE" })).claims.map((c) => c.text)).toEqual(["Shipping on every Weekly Box is free."]);
  });

  it("returns a campaign with its linked ideas and items", async () => {
    const client = gtmClient();
    const { campaigns } = await client.listCampaigns(org);
    for (const c of campaigns) {
      const res = await client.getCampaign({ ...org, id: c.id });
      expect(res.campaign?.id).toBe(c.id);
      expect(res.ideas.every((i) => i.campaignId === c.id)).toBe(true);
      expect(res.items.every((i) => i.campaignId === c.id)).toBe(true);
      expect(res.ideas.length).toBe((await client.listIdeas({ ...org, campaignId: c.id })).ideas.length);
      expect(res.items.length).toBe((await client.listItems({ ...org, campaignId: c.id })).items.length);
    }
    const sites = campaigns.find((c) => c.status === GtmCampaignStatus.ACTIVE)!;
    const detail = await client.getCampaign({ ...org, id: sites.id });
    expect(detail.ideas.length).toBeGreaterThan(0);
    expect(detail.items.length).toBeGreaterThan(0);
    // Coverage: Reddit is planned and nothing is drafted for it; a pair with items counts them by status.
    const reddit = detail.coverage.filter((r) => r.channelId === "gtm-ch-reddit");
    expect(reddit.length).toBeGreaterThan(0);
    expect(reddit.every((r) => r.planned && r.total === 0 && r.counts.length === 0)).toBe(true);
    const hn = detail.coverage.find((r) => r.channelId === GTM_CHANNEL_IDS.hackerNews && r.format === GtmItemFormat.SHORT_POST)!;
    expect([hn.planned, hn.total, hn.counts.map((c) => [c.status, c.count])]).toEqual([true, 1, [[GtmItemStatus.APPROVED, 1]]]);
    // An item on a pair the campaign did not plan still shows, as not planned.
    const unplanned = detail.coverage.find((r) => !r.planned && r.total > 0);
    expect(unplanned).toBeDefined();
    expect(detail.coverage.reduce((n, r) => n + r.total, 0)).toBe(detail.items.length);
    const times = detail.items.filter((i) => i.scheduledAt).map((i) => timestampMs(i.scheduledAt!));
    expect(times).toEqual([...times].sort((a, b) => a - b));
    expect((await rejection(client.getCampaign({ ...org, id: "nope" }))).code).toBe(Code.NotFound);
  });
});

describe("the writes", () => {
  it("accepts an idea and bumps its revision, in memory", async () => {
    const client = gtmClient();
    const idea = (await client.listIdeas({ ...org, status: GtmIdeaStatus.PROPOSED })).ideas[0];
    const res = await client.setIdeaStatus({ ...org, id: idea.id, expectedRevision: idea.revision, status: GtmIdeaStatus.ACCEPTED });
    expect(res.idea?.status).toBe(GtmIdeaStatus.ACCEPTED);
    expect(res.idea?.revision).toBe(idea.revision + 1);
    expect((await client.getIdea({ ...org, id: idea.id })).idea?.status).toBe(GtmIdeaStatus.ACCEPTED);
    // A reload (a new pack) forgets it.
    expect((await gtmClient().getIdea({ ...org, id: idea.id })).idea?.status).toBe(GtmIdeaStatus.PROPOSED);
  });

  it("edits an idea, leaving unset fields alone and replacing evidence only when set", async () => {
    const client = gtmClient();
    const idea = (await client.listIdeas(org)).ideas.find((i) => i.evidence.length > 0)!;
    const edited = (await client.updateIdea({ ...org, id: idea.id, expectedRevision: idea.revision, title: "A better title" })).idea!;
    expect([edited.title, edited.bodyMd, edited.evidence.length]).toEqual(["A better title", idea.bodyMd, idea.evidence.length]);
    const cleared = (await client.updateIdea({ ...org, id: idea.id, expectedRevision: edited.revision, evidence: { evidence: [] } })).idea!;
    expect(cleared.evidence).toHaveLength(0);
    const unlinked = (await client.updateIdea({ ...org, id: idea.id, expectedRevision: cleared.revision, campaignId: "" })).idea!;
    expect(unlinked.campaignId).toBe("");
  });

  it("rejects a stale expected_revision with Aborted and a revision-conflict detail", async () => {
    const client = gtmClient();
    const idea = (await client.listIdeas(org)).ideas[0];
    await client.updateIdea({ ...org, id: idea.id, expectedRevision: idea.revision, title: "First edit" });
    const err = await rejection(client.updateIdea({ ...org, id: idea.id, expectedRevision: idea.revision, title: "Second edit, from the old copy" }));
    expect(err.code).toBe(Code.Aborted);
    const detail = err.findDetails(GtmRevisionConflictSchema);
    expect(detail).toHaveLength(1);
    expect(detail[0]).toMatchObject({ id: idea.id, expectedRevision: idea.revision, currentRevision: idea.revision + 1 });
    // The row did not change.
    expect((await client.getIdea({ ...org, id: idea.id })).idea?.title).toBe("First edit");
  });

  it("reschedules an item (drag on the calendar) and can take it off the calendar", async () => {
    const client = gtmClient();
    const item = (await client.listItems({ ...org, status: GtmItemStatus.DRAFT })).items.find((i) => i.scheduledAt)!;
    const target = new Date(2026, 6, 30, 9).getTime();
    const moved = (await client.updateItem({ ...org, id: item.id, expectedRevision: item.revision, scheduledAt: timestampFromMs(target) })).item!;
    expect(timestampMs(moved.scheduledAt!)).toBe(target);
    expect(moved.revision).toBe(item.revision + 1);
    expect(moved.title).toBe(item.title);
    const off = (await client.updateItem({ ...org, id: item.id, expectedRevision: moved.revision, clearScheduledAt: true })).item!;
    expect(off.scheduledAt).toBeUndefined();
    const window = await client.listItems({ ...org, scheduledFrom: timestampFromMs(target - 1000), scheduledTo: timestampFromMs(target + 1000) });
    expect(window.items.some((i) => i.id === item.id)).toBe(false);
  });

  it("approves an item, never sends one, and never edits a sent one", async () => {
    const client = gtmClient();
    const review = (await client.listItems({ ...org, status: GtmItemStatus.REVIEW })).items[0];
    const approved = (await client.updateItem({ ...org, id: review.id, expectedRevision: review.revision, status: GtmItemStatus.APPROVED })).item!;
    expect(approved.status).toBe(GtmItemStatus.APPROVED);
    expect((await rejection(client.updateItem({ ...org, id: approved.id, expectedRevision: approved.revision, status: GtmItemStatus.SENT }))).code).toBe(Code.InvalidArgument);
    const sent = (await client.listItems({ ...org, status: GtmItemStatus.SENT })).items[0];
    expect((await rejection(client.updateItem({ ...org, id: sent.id, expectedRevision: sent.revision, title: "Edited after sending" }))).code).toBe(Code.FailedPrecondition);
  });

  it("creates ideas, campaigns and items, linking only the org's own records", async () => {
    const client = gtmClient();
    const strategyId = (await client.listStrategies({ ...org, status: GtmStrategyStatus.ACTIVE })).strategies[0]!.id;
    expect((await rejection(client.createCampaign({ ...org, name: "No strategy" }))).code).toBe(Code.InvalidArgument);
    expect((await rejection(client.createCampaign({ ...org, name: "Other org's strategy", strategyId: "nope" }))).code).toBe(Code.NotFound);
    const campaign = (await client.createCampaign({ ...org, name: "Autumn push", goal: "sample", strategyId, plannedChannelIds: [GTM_CHANNEL_IDS.blog], plannedFormats: [GtmItemFormat.LONG_POST] })).campaign!;
    expect([campaign.status, campaign.strategyId, campaign.plannedChannelIds]).toEqual([GtmCampaignStatus.DRAFT, strategyId, [GTM_CHANNEL_IDS.blog]]);
    expect((await rejection(client.createCampaign({ ...org, name: "Bad plan", strategyId, plannedChannelIds: ["nope"] }))).code).toBe(Code.NotFound);
    const idea = (await client.createIdea({ ...org, kind: GtmIdeaKind.CONTENT, title: "New idea", campaignId: campaign.id })).idea!;
    expect([idea.status, idea.revision]).toEqual([GtmIdeaStatus.PROPOSED, 1]);
    const item = (await client.createItem({ ...org, format: GtmItemFormat.LONG_POST, channelId: GTM_CHANNEL_IDS.blog, title: "New post", campaignId: campaign.id, ideaId: idea.id, scheduledAt: timestampFromMs(NOW) })).item!;
    expect(item.status).toBe(GtmItemStatus.DRAFT);
    const linked = await client.getCampaign({ ...org, id: campaign.id });
    expect([linked.ideas.map((i) => i.id), linked.items.map((i) => i.id)]).toEqual([[idea.id], [item.id]]);
    expect((await rejection(client.createItem({ ...org, format: GtmItemFormat.LONG_POST, title: "Orphan", campaignId: "not-a-campaign" }))).code).toBe(Code.NotFound);
    const renamed = (await client.updateCampaign({ ...org, id: campaign.id, expectedRevision: campaign.revision, name: "Autumn launch", status: GtmCampaignStatus.ACTIVE })).campaign!;
    expect([renamed.name, renamed.status, renamed.revision]).toEqual(["Autumn launch", GtmCampaignStatus.ACTIVE, 2]);
    // Move it to the other strategy, and replace its plan only when the list is set.
    const other = (await client.listStrategies({ ...org, status: GtmStrategyStatus.DRAFT })).strategies[0]!.id;
    const moved = (await client.updateCampaign({ ...org, id: campaign.id, expectedRevision: renamed.revision, strategyId: other, plannedFormats: { formats: [GtmItemFormat.EMAIL] } })).campaign!;
    expect([moved.strategyId, moved.plannedChannelIds, moved.plannedFormats]).toEqual([other, [GTM_CHANNEL_IDS.blog], [GtmItemFormat.EMAIL]]);
    expect((await rejection(client.updateCampaign({ ...org, id: campaign.id, expectedRevision: moved.revision, strategyId: "" }))).code).toBe(Code.InvalidArgument);
  });

  it("updates positioning by revision", async () => {
    const client = gtmClient();
    const { positioning } = await client.getPositioning(org);
    const next = (await client.updatePositioning({ ...org, expectedRevision: positioning!.revision, positioningMd: "New statement.", audiences: ["Founders"] })).positioning!;
    expect([next.positioningMd, next.audiences, next.revision]).toEqual(["New statement.", ["Founders"], positioning!.revision + 1]);
    expect((await client.getPositioning(org)).positioning?.positioningMd).toBe("New statement.");
    expect((await rejection(client.updatePositioning({ ...org, expectedRevision: positioning!.revision, positioningMd: "Stale." }))).code).toBe(Code.Aborted);
  });

  it("verifies a claim only with its evidence, and a comparison claim only with a third party's", async () => {
    const client = gtmClient();
    const claims = (await client.listClaims(org)).claims;
    const unverified = claims.find((c) => c.status === GtmClaimStatus.UNVERIFIED && c.category !== GtmClaimCategory.COMPARISON)!;
    expect((await rejection(client.upsertClaim({ ...org, id: unverified.id, expectedRevision: unverified.revision, status: GtmClaimStatus.VERIFIED }))).code).toBe(Code.InvalidArgument);
    const verified = (await client.upsertClaim({
      ...org, id: unverified.id, expectedRevision: unverified.revision, status: GtmClaimStatus.VERIFIED, sourceUrl: "https://example.com/sample/docs", sourceKind: GtmClaimSourceKind.DOCS,
    })).claim!;
    expect([verified.status, verified.verifiedBy, verified.revision, verified.verifiedAt !== undefined]).toEqual([GtmClaimStatus.VERIFIED, "Alex Demo", unverified.revision + 1, true]);

    const comparison = claims.find((c) => c.category === GtmClaimCategory.COMPARISON)!;
    const own = { ...org, id: comparison.id, expectedRevision: comparison.revision, status: GtmClaimStatus.VERIFIED, sourceUrl: "https://example.com/sample/ours" };
    expect((await rejection(client.upsertClaim({ ...own, sourceKind: GtmClaimSourceKind.DOCS }))).code).toBe(Code.InvalidArgument);
    expect((await client.upsertClaim({ ...own, sourceKind: GtmClaimSourceKind.PRIMARY_THIRD_PARTY })).claim?.status).toBe(GtmClaimStatus.VERIFIED);

    const created = await client.upsertClaim({ ...org, text: "Sample claim about limits.", category: GtmClaimCategory.LIMIT });
    expect([created.created, created.claim?.status]).toEqual([true, GtmClaimStatus.UNVERIFIED]);
    expect((await rejection(client.upsertClaim({ ...org, text: "sample  CLAIM about limits.", category: GtmClaimCategory.LIMIT }))).code).toBe(Code.AlreadyExists);
    const stale = await rejection(client.upsertClaim({ ...org, id: unverified.id, expectedRevision: unverified.revision, notes: "stale" }));
    expect(stale.code).toBe(Code.Aborted);
  });

  it("creates and edits strategies, validates their dates, and conflicts on a stale revision", async () => {
    const client = gtmClient();
    const made = (await client.createStrategy({ ...org, name: "New plan", goalMd: "g", periodStart: "2026-08-01", periodEnd: "2026-09-30", category: GtmStrategyCategory.PAID_ACQUISITION })).strategy!;
    expect([made.status, made.revision, made.category]).toEqual([GtmStrategyStatus.DRAFT, 1, GtmStrategyCategory.PAID_ACQUISITION]);
    expect((await rejection(client.createStrategy({ ...org, name: "Backwards", periodStart: "2026-09-30", periodEnd: "2026-08-01" }))).code).toBe(Code.InvalidArgument);
    expect((await rejection(client.createStrategy({ ...org, name: "Impossible", periodStart: "2026-02-30" }))).code).toBe(Code.InvalidArgument);
    const active = (await client.updateStrategy({ ...org, id: made.id, expectedRevision: 1, status: GtmStrategyStatus.ACTIVE, targetMetric: "100 sample teams" })).strategy!;
    expect([active.status, active.targetMetric, active.name, active.revision]).toEqual([GtmStrategyStatus.ACTIVE, "100 sample teams", "New plan", 2]);
    expect((await rejection(client.updateStrategy({ ...org, id: made.id, expectedRevision: 1, name: "Stale" }))).code).toBe(Code.Aborted);
  });

  it("records the URL and publishes a manual-channel item in one update, and refuses publishing without it", async () => {
    const client = gtmClient();
    const hn = (await client.getItem({ ...org, id: "gtm-item-show-hn" })).item!;
    const refused = await rejection(client.updateItem({ ...org, id: hn.id, expectedRevision: hn.revision, status: GtmItemStatus.PUBLISHED }));
    expect([refused.code, refused.rawMessage]).toEqual([Code.FailedPrecondition, expect.stringContaining("published URL")]);
    expect((await rejection(client.updateItem({ ...org, id: hn.id, expectedRevision: hn.revision, publishedUrl: "news.example/item" }))).code).toBe(Code.InvalidArgument);
    // Recording the URL alone is not a content edit and changes no status.
    const recorded = (await client.updateItem({ ...org, id: hn.id, expectedRevision: hn.revision, publishedUrl: "https://news.example/item?id=1" })).item!;
    expect([recorded.status, recorded.publishedUrl]).toEqual([GtmItemStatus.APPROVED, "https://news.example/item?id=1"]);
    const published = (await client.updateItem({ ...org, id: hn.id, expectedRevision: recorded.revision, status: GtmItemStatus.PUBLISHED })).item!;
    expect(published.status).toBe(GtmItemStatus.PUBLISHED);
    // The same in one call, on a new manual item: record the URL and mark it published together.
    const post = (await client.createItem({ ...org, format: GtmItemFormat.SHORT_POST, channelId: GTM_CHANNEL_IDS.linkedin, title: "A post" })).item!;
    const approved = (await client.updateItem({ ...org, id: post.id, expectedRevision: 1, status: GtmItemStatus.APPROVED })).item!;
    const both = (await client.updateItem({ ...org, id: post.id, expectedRevision: approved.revision, status: GtmItemStatus.PUBLISHED, publishedUrl: "https://linkedin.example/p/1" })).item!;
    expect([both.status, both.publishedUrl]).toEqual([GtmItemStatus.PUBLISHED, "https://linkedin.example/p/1"]);
    // An item with no channel needs no URL to be marked published.
    const loose = (await client.createItem({ ...org, format: GtmItemFormat.SHORT_POST, title: "No channel" })).item!;
    const looseApproved = (await client.updateItem({ ...org, id: loose.id, expectedRevision: 1, status: GtmItemStatus.APPROVED })).item!;
    expect((await client.updateItem({ ...org, id: loose.id, expectedRevision: looseApproved.revision, status: GtmItemStatus.PUBLISHED })).item?.status).toBe(GtmItemStatus.PUBLISHED);
  });

  it("never schedules or publishes an email-type item, and keeps its recipients to email-type formats", async () => {
    const client = gtmClient();
    const email = (await client.createItem({ ...org, format: GtmItemFormat.EMAIL, channelId: GTM_CHANNEL_IDS.email, title: "Hello", emailTo: ["a@lists.example.com"], emailSubject: "Hi" })).item!;
    const review = (await client.updateItem({ ...org, id: email.id, expectedRevision: email.revision, status: GtmItemStatus.REVIEW })).item!;
    const approved = (await client.updateItem({ ...org, id: email.id, expectedRevision: review.revision, status: GtmItemStatus.APPROVED })).item!;
    for (const status of [GtmItemStatus.SCHEDULED, GtmItemStatus.PUBLISHED]) {
      const err = await rejection(client.updateItem({ ...org, id: email.id, expectedRevision: approved.revision, status }));
      expect([err.code, err.rawMessage]).toEqual([Code.FailedPrecondition, expect.stringContaining("never scheduled or published")]);
    }
    // A cold-outreach item is email-type too.
    const cold = (await client.createItem({ ...org, format: GtmItemFormat.COLD_OUTREACH, channelId: GTM_CHANNEL_IDS.outreach, title: "Intro", emailTo: ["b@lists.example.com"], emailSubject: "Hello" })).item!;
    await client.updateItem({ ...org, id: cold.id, expectedRevision: cold.revision, status: GtmItemStatus.APPROVED });
    expect((await rejection(client.updateItem({ ...org, id: cold.id, expectedRevision: cold.revision + 1, status: GtmItemStatus.PUBLISHED }))).code).toBe(Code.FailedPrecondition);
    // But a person may date an approved email; it stays APPROVED.
    const dated = (await client.updateItem({ ...org, id: email.id, expectedRevision: approved.revision, scheduledAt: timestampFromMs(NOW) })).item!;
    expect([dated.status, timestampMs(dated.scheduledAt!)]).toEqual([GtmItemStatus.APPROVED, NOW]);
    // A non-email item takes no recipients.
    expect((await rejection(client.createItem({ ...org, format: GtmItemFormat.SHORT_POST, title: "Post", emailTo: ["a@lists.example.com"] }))).code).toBe(Code.InvalidArgument);
    // SENT is never set here.
    expect((await rejection(client.updateItem({ ...org, id: email.id, expectedRevision: dated.revision, status: GtmItemStatus.SENT }))).code).toBe(Code.InvalidArgument);
  });

  it("returns an approved item to review when its content changes, but not when it is only dated", async () => {
    const client = gtmClient();
    const hn = (await client.getItem({ ...org, id: "gtm-item-show-hn" })).item!;
    const dated = (await client.updateItem({ ...org, id: hn.id, expectedRevision: hn.revision, scheduledAt: timestampFromMs(NOW + 86_400_000) })).item!;
    expect(dated.status).toBe(GtmItemStatus.APPROVED);
    const edited = (await client.updateItem({ ...org, id: hn.id, expectedRevision: dated.revision, bodyMd: "A new body." })).item!;
    expect(edited.status).toBe(GtmItemStatus.REVIEW);
    const channelChange = (await client.updateItem({ ...org, id: hn.id, expectedRevision: edited.revision, status: GtmItemStatus.APPROVED })).item!;
    expect((await client.updateItem({ ...org, id: hn.id, expectedRevision: channelChange.revision, channelId: GTM_CHANNEL_IDS.reddit })).item?.status).toBe(GtmItemStatus.REVIEW);
    expect((await rejection(client.updateItem({ ...org, id: hn.id, expectedRevision: channelChange.revision + 1, channelId: "nope" }))).code).toBe(Code.NotFound);
  });

  it("freezes a held email, and resolves it as sent or releases the hold", async () => {
    const client = gtmClient();
    const held = (await client.getItem({ ...org, id: "gtm-item-grind-guide-email" })).item!;
    expect([held.status, held.approvalHash !== ""]).toEqual([GtmItemStatus.APPROVED, true]);
    const frozen = await rejection(client.updateItem({ ...org, id: held.id, expectedRevision: held.revision, title: "Edited while held" }));
    expect([frozen.code, frozen.rawMessage]).toEqual([Code.FailedPrecondition, expect.stringContaining("resolve the send")]);
    expect((await rejection(client.resolveItemSend({ ...org, id: held.id, approvalHash: "not-its-hash" }))).code).toBe(Code.FailedPrecondition);
    expect((await rejection(client.resolveItemSend({ ...org, id: "gtm-item-show-hn", approvalHash: "x" }))).code).toBe(Code.FailedPrecondition);

    // The email did not go out: the hold is released and the item stays APPROVED, editable again.
    const released = (await client.resolveItemSend({ ...org, id: held.id, approvalHash: held.approvalHash })).item!;
    expect([released.status, released.approvalHash, released.revision]).toEqual([GtmItemStatus.APPROVED, "", held.revision + 1]);
    expect((await client.updateItem({ ...org, id: held.id, expectedRevision: released.revision, title: "Edited after release" })).item?.status).toBe(GtmItemStatus.REVIEW);

    // On a fresh pack: the person found it in Gmail, so it is SENT with that id and is final.
    const fresh = gtmClient();
    const sent = (await fresh.resolveItemSend({ ...org, id: held.id, approvalHash: held.approvalHash, gmailMessageId: "gmail-abc" })).item!;
    expect([sent.status, sent.gmailMessageId, sent.sentBy, sent.sentAt !== undefined]).toEqual([GtmItemStatus.SENT, "gmail-abc", "Alex Demo", true]);
    expect((await rejection(fresh.updateItem({ ...org, id: held.id, expectedRevision: sent.revision, title: "Too late" }))).code).toBe(Code.FailedPrecondition);
    expect((await rejection(fresh.resolveItemSend({ ...org, id: held.id, approvalHash: held.approvalHash }))).code).toBe(Code.FailedPrecondition);
  });

  it("creates and edits channels: slugs, unique keys, mode rules and types", async () => {
    const client = gtmClient();
    const made = (await client.createChannel({ ...org, key: "newsletter", name: "Newsletter", mode: GtmChannelMode.MANUAL, manualInstructionsMd: "Send by hand.", channelTypes: ["email-list"] })).channel!;
    expect([made.key, made.status, made.revision, made.connectionState]).toEqual(["newsletter", 1, 1, GtmConnectionState.UNSPECIFIED]);
    expect((await rejection(client.createChannel({ ...org, key: "Bad Key", name: "x", mode: GtmChannelMode.MANUAL }))).code).toBe(Code.InvalidArgument);
    expect((await rejection(client.createChannel({ ...org, key: "newsletter", name: "again", mode: GtmChannelMode.MANUAL }))).code).toBe(Code.AlreadyExists);
    expect((await rejection(client.createChannel({ ...org, key: "gmail-two", name: "x", mode: GtmChannelMode.INTEGRATED }))).code).toBe(Code.InvalidArgument);
    expect((await rejection(client.createChannel({ ...org, key: "manual-gmail", name: "x", mode: GtmChannelMode.MANUAL, integrationKind: "gmail" }))).code).toBe(Code.InvalidArgument);
    const integrated = (await client.createChannel({ ...org, key: "second-inbox", name: "Second inbox", mode: GtmChannelMode.INTEGRATED, integrationKind: "gmail" })).channel!;
    expect(integrated.connectionState).toBe(GtmConnectionState.CONNECTED);
    const renamed = (await client.updateChannel({ ...org, id: made.id, expectedRevision: 1, key: "news", name: "News", channelTypes: { types: ["email-list", "digest"] } })).channel!;
    expect([renamed.key, renamed.name, renamed.channelTypes, renamed.revision]).toEqual(["news", "News", ["email-list", "digest"], 2]);
    expect((await rejection(client.updateChannel({ ...org, id: made.id, expectedRevision: 2, key: "blog" }))).code).toBe(Code.AlreadyExists);
    expect((await rejection(client.updateChannel({ ...org, id: made.id, expectedRevision: 1, name: "Stale" }))).code).toBe(Code.Aborted);
    const archived = (await client.updateChannel({ ...org, id: made.id, expectedRevision: 2, status: 3 })).channel!;
    expect(archived.status).toBe(3);
  });

  it("uses a library strategy: a draft strategy, and optionally a first campaign planning the channels that fit", async () => {
    const client = gtmClient();
    const plain = await client.useLibraryStrategy({ ...org, libraryKey: "sample-tasting-popup" });
    expect(plain.campaign).toBeUndefined();
    expect([plain.strategy?.status, plain.strategy?.libraryKey, plain.strategy?.category]).toEqual([GtmStrategyStatus.DRAFT, "sample-tasting-popup", GtmStrategyCategory.PARTNERSHIPS_DISTRIBUTION]);
    expect(plain.strategy?.goalMd).toContain("Tasting-event attendees who subscribe");
    expect(plain.strategy?.targetMetric).toBe("Attendees, New subscribers");

    const both = await client.useLibraryStrategy({ ...org, libraryKey: "sample-tasting-popup", createFirstCampaign: true, campaignName: "Our tasting pop-up" });
    expect([both.campaign?.name, both.campaign?.status, both.campaign?.strategyId]).toEqual(["Our tasting pop-up", GtmCampaignStatus.DRAFT, both.strategy?.id]);
    // channel_types blog, x, reddit: matched to the org's channels by key or by type.
    expect(both.campaign?.plannedChannelIds).toEqual([GTM_CHANNEL_IDS.blog, GTM_CHANNEL_IDS.x, GTM_CHANNEL_IDS.reddit]);
    const unnamed = await client.useLibraryStrategy({ ...org, libraryKey: "sample-cupping-call", createFirstCampaign: true });
    expect(unnamed.campaign?.name).toBe("Monthly cupping call (placeholder)");
    expect(unnamed.campaign?.plannedChannelIds).toEqual([GTM_CHANNEL_IDS.email, GTM_CHANNEL_IDS.outreach, GTM_CHANNEL_IDS.linkedin]);
    expect((await client.getStrategy({ ...org, id: both.strategy!.id })).campaigns.map((c) => c.id)).toEqual([both.campaign!.id]);
    expect((await rejection(client.useLibraryStrategy({ ...org, libraryKey: "nope" }))).code).toBe(Code.NotFound);
    expect((await rejection(client.useLibraryStrategy({ ...org, libraryKey: "sample-tasting-popup", campaignName: "x" }))).code).toBe(Code.InvalidArgument);
  });

  it("confirms or cancels a pending prune request once, removing only the keys not imported since", async () => {
    const client = gtmClient();
    const before = (await client.listStrategyLibrary(org)).entries.length;
    const confirmed = await client.confirmLibraryPrune({ ...org, requestId: GTM_PRUNE_REQUEST_ID });
    expect([confirmed.removed, confirmed.request?.status, confirmed.request?.decidedBy]).toEqual([2, GtmPruneRequestStatus.CONFIRMED, "Alex Demo"]);
    expect((await client.listStrategyLibrary(org)).entries).toHaveLength(before - 2);
    expect((await client.listLibraryPruneRequests({ ...org, status: GtmPruneRequestStatus.PENDING })).requests).toHaveLength(0);
    expect((await rejection(client.confirmLibraryPrune({ ...org, requestId: GTM_PRUNE_REQUEST_ID }))).code).toBe(Code.FailedPrecondition);
    expect((await rejection(client.cancelLibraryPrune({ ...org, requestId: GTM_PRUNE_REQUEST_ID }))).code).toBe(Code.FailedPrecondition);

    // Cancel on a fresh pack: nothing is removed.
    const fresh = gtmClient();
    expect((await fresh.cancelLibraryPrune({ ...org, requestId: GTM_PRUNE_REQUEST_ID })).request?.status).toBe(GtmPruneRequestStatus.CANCELLED);
    expect((await fresh.listStrategyLibrary(org)).entries).toHaveLength(before);
    expect((await rejection(fresh.confirmLibraryPrune({ ...org, requestId: "nope" }))).code).toBe(Code.NotFound);

    // A key imported again after the request was made stays.
    const third = gtmClient();
    const entry = (await third.getStrategyLibraryEntry({ ...org, key: "sample-quote-wall" })).entry!;
    await third.importStrategyLibrary({ ...org, sourceRevision: "sample-rev-3", entries: [{ ...entry, summaryMd: "Placeholder entry, reworded." }] });
    const kept = await third.confirmLibraryPrune({ ...org, requestId: GTM_PRUNE_REQUEST_ID });
    expect(kept.removed).toBe(1);
    expect((await third.getStrategyLibraryEntry({ ...org, key: "sample-quote-wall" })).entry?.summaryMd).toBe("Placeholder entry, reworded.");
  });

  it("imports library entries: created, updated and unchanged counts, an audit row, and a prune that removes at once", async () => {
    const client = gtmClient();
    const existing = (await client.getStrategyLibraryEntry({ ...org, key: "sample-faq-pages" })).entry!;
    const fresh = { key: "sample-new-play", name: "New play (placeholder)", category: GtmStrategyCategory.PAID_ACQUISITION, depth: GtmLibraryDepth.PLAY };
    const res = await client.importStrategyLibrary({ ...org, sourceRevision: "sample-rev-3", entries: [existing, { ...existing, key: "sample-cupping-call", name: "Renamed (placeholder)" }, fresh] });
    expect([res.created, res.updated, res.unchanged, res.pruned, res.pruneRequestId, res.wouldRemove]).toEqual([1, 1, 1, 0, "", []]);
    const { imports } = await client.listLibraryImports(org);
    expect(imports).toHaveLength(4);
    expect(imports[0]).toMatchObject({ sourceRevision: "sample-rev-3", created: 1, updated: 1, unchanged: 1, credentialKind: GtmCredentialKind.PERSON });
    expect((await rejection(client.importStrategyLibrary({ ...org, sourceRevision: "x", entries: [] }))).code).toBe(Code.InvalidArgument);
    expect((await rejection(client.importStrategyLibrary({ ...org, sourceRevision: "x", entries: [{ ...fresh, key: "Bad Key" }] }))).code).toBe(Code.InvalidArgument);
    expect((await rejection(client.importStrategyLibrary({ ...org, sourceRevision: "x", entries: [fresh], prune: true }))).code).toBe(Code.InvalidArgument);
    const pruned = await client.importStrategyLibrary({ ...org, sourceRevision: "sample-rev-4", entries: [fresh], prune: true, allKeys: ["sample-new-play", "sample-faq-pages"] });
    expect([pruned.unchanged, pruned.pruned]).toEqual([1, 5]);
    expect((await client.listStrategyLibrary(org)).entries.map((e) => e.key).sort()).toEqual(["sample-faq-pages", "sample-new-play"]);
  });

  it("has no bucket: an upload is Unavailable, and a READY asset downloads as an inline image", async () => {
    const client = gtmClient();
    const target = { ...org, target: { case: "itemId" as const, value: "gtm-item-show-hn" }, filename: "a.png", contentType: "image/png", sizeBytes: 10n };
    expect((await rejection(client.createAssetUpload(target))).code).toBe(Code.Unavailable);
    expect((await rejection(client.confirmAssetUpload({ ...org, assetId: "gtm-asset-hn-hero" }))).code).toBe(Code.Unavailable);
    const { assets } = await client.listAssets({ ...org, itemId: "gtm-item-show-hn" });
    expect(assets[0]).toMatchObject({ id: "gtm-asset-hn-hero", status: GtmAssetStatus.READY, filename: "grind-calculator-hero.png" });
    expect((await client.getAsset({ ...org, id: "gtm-asset-hn-hero" })).asset?.itemId).toBe("gtm-item-show-hn");
    const download = await client.getAssetDownload({ ...org, assetId: "gtm-asset-hn-hero" });
    expect(download.url.startsWith("data:image/svg+xml")).toBe(true);
    expect(timestampMs(download.expiresAt!)).toBeGreaterThan(NOW);
    // An asset is on an item or a campaign, never both or neither.
    expect((await rejection(client.listAssets(org))).code).toBe(Code.InvalidArgument);
    expect((await rejection(client.listAssets({ ...org, itemId: "x", campaignId: "y" }))).code).toBe(Code.InvalidArgument);
    await client.deleteAsset({ ...org, assetId: "gtm-asset-hn-hero" });
    expect((await client.listAssets({ ...org, itemId: "gtm-item-show-hn" })).assets).toHaveLength(0);
    expect((await rejection(client.getAsset({ ...org, id: "gtm-asset-hn-hero" }))).code).toBe(Code.NotFound);
  });

  it("answers every GtmService rpc, and refuses every one for another org but GetGtmAccess", async () => {
    const client = gtmClient();
    const other = { orgId: "org-someone-else" };
    const calls: Record<string, () => Promise<unknown>> = {
      listStrategies: () => client.listStrategies(other), getStrategy: () => client.getStrategy({ ...other, id: "x" }), listChannels: () => client.listChannels(other),
      getChannel: () => client.getChannel({ ...other, id: "x" }), listAssets: () => client.listAssets({ ...other, itemId: "x" }), getAsset: () => client.getAsset({ ...other, id: "x" }),
      getAssetDownload: () => client.getAssetDownload({ ...other, assetId: "x" }), listStrategyLibrary: () => client.listStrategyLibrary(other),
      getStrategyLibraryEntry: () => client.getStrategyLibraryEntry({ ...other, key: "x" }), listLibraryImports: () => client.listLibraryImports(other),
      listLibraryPruneRequests: () => client.listLibraryPruneRequests(other), listIdeas: () => client.listIdeas(other), getIdea: () => client.getIdea({ ...other, id: "x" }),
      listCampaigns: () => client.listCampaigns(other), getCampaign: () => client.getCampaign({ ...other, id: "x" }), listItems: () => client.listItems(other),
      getItem: () => client.getItem({ ...other, id: "x" }), getPositioning: () => client.getPositioning(other), listClaims: () => client.listClaims(other),
      checkClaims: () => client.checkClaims({ ...other, text: "x" }), createStrategy: () => client.createStrategy({ ...other, name: "x" }),
      updateItem: () => client.updateItem({ ...other, id: "x", expectedRevision: 1 }), resolveItemSend: () => client.resolveItemSend({ ...other, id: "x", approvalHash: "x" }),
      confirmLibraryPrune: () => client.confirmLibraryPrune({ ...other, requestId: GTM_PRUNE_REQUEST_ID }), useLibraryStrategy: () => client.useLibraryStrategy({ ...other, libraryKey: "sample-tasting-popup" }),
      createAssetUpload: () => client.createAssetUpload({ ...other, target: { case: "itemId", value: "x" }, filename: "a", contentType: "image/png", sizeBytes: 1n }), deleteAsset: () => client.deleteAsset({ ...other, assetId: "x" }),
    };
    for (const [name, call] of Object.entries(calls)) expect((await rejection(call())).code, name).toBe(Code.NotFound);
        expect(GtmService.methods.length).toBe(42);
  });

  it("does not bump the revision when SetIdeaStatus names the status the idea already has", async () => {
    const client = gtmClient();
    const idea = (await client.listIdeas({ ...org, status: GtmIdeaStatus.PROPOSED })).ideas[0]!;
    const same = (await client.setIdeaStatus({ ...org, id: idea.id, expectedRevision: idea.revision, status: GtmIdeaStatus.PROPOSED })).idea!;
    expect([same.revision, same.status, timestampMs(same.updatedAt!)]).toEqual([idea.revision, GtmIdeaStatus.PROPOSED, timestampMs(idea.updatedAt!)]);
    expect((await client.getIdea({ ...org, id: idea.id })).idea?.revision).toBe(idea.revision);
    // A stale revision is still a conflict, even for the same status.
    expect((await rejection(client.setIdeaStatus({ ...org, id: idea.id, expectedRevision: idea.revision + 7, status: GtmIdeaStatus.PROPOSED }))).code).toBe(Code.Aborted);
    // A real change still bumps it, exactly once.
    const moved = (await client.setIdeaStatus({ ...org, id: idea.id, expectedRevision: idea.revision, status: GtmIdeaStatus.ACCEPTED })).idea!;
    expect(moved.revision).toBe(idea.revision + 1);
  });

  it("caps each of GetCampaign's lists at 200 and says when it cut one", async () => {
    const client = gtmClient();
    const campaignId = "gtm-cmp-tasting-club";
    const before = await client.getCampaign({ ...org, id: campaignId });
    expect(before.truncated).toBe(false);
    for (let i = before.ideas.length; i < 201; i++) await client.createIdea({ ...org, kind: GtmIdeaKind.CONTENT, title: `Idea ${i}`, campaignId });
    const ideasOnly = await client.getCampaign({ ...org, id: campaignId });
    expect([ideasOnly.ideas.length, ideasOnly.truncated]).toEqual([200, true]);
    expect(ideasOnly.items.length).toBe(before.items.length);
    // Coverage counts every item, whatever the cap.
    for (let i = before.items.length; i < 201; i++) await client.createItem({ ...org, format: GtmItemFormat.SHORT_POST, channelId: GTM_CHANNEL_IDS.x, title: `Post ${i}`, campaignId });
    const both = await client.getCampaign({ ...org, id: campaignId });
    expect([both.ideas.length, both.items.length, both.truncated]).toEqual([200, 200, true]);
    expect(both.coverage.reduce((n, r) => n + r.total, 0)).toBe(201);
    // Exactly 200 is not a cut.
    const fresh = gtmClient();
    for (let i = before.items.length; i < 200; i++) await fresh.createItem({ ...org, format: GtmItemFormat.SHORT_POST, title: `Post ${i}`, campaignId });
    const exact = await fresh.getCampaign({ ...org, id: campaignId });
    expect([exact.items.length, exact.truncated]).toEqual([200, false]);
  });

  it("lets the listed GTM writes through a read-only transport, and still refuses any other write", async () => {
    const transport = createFixtureTransport({ pack: shopAppPack({ now: NOW }), now: demoNow, readOnly: true, readOnlyMessage: SHOP_READ_ONLY_MESSAGE });
    expect((await createClient(GtmService, transport).createIdea({ ...org, kind: GtmIdeaKind.GTM, title: "ok" })).idea?.title).toBe("ok");
    await expect(createClient(ChatService, transport).deleteChat({ chatId: "shop-chat-cart-totals" })).rejects.toThrow(SHOP_READ_ONLY_MESSAGE);
  });
});

describe("CheckClaims", () => {
  it("flags a price with no matching claim, and passes one matching a verified claim", async () => {
    const client = gtmClient();
    const res = await client.checkClaims({ ...org, text: "The Sipper plan costs $19 per month, billed monthly. Our Estate tier is $49 per month." });
    const price = (m: string) => res.findings.find((f) => f.mention === m)!;
    expect(price("$19")).toMatchObject({ kind: GtmClaimFindingKind.MATCHED_VERIFIED, blocking: false, claimId: "gtm-claim-sipper-price", mentionKind: "price" });
    expect(price("$49")).toMatchObject({ kind: GtmClaimFindingKind.UNMATCHED_RISKY, blocking: true, claimId: "" });
    expect(price("$49").context).toBe("Our Estate tier is $49 per month.");
    expect(res.blocking).toBe(true);
  });

  it("blocks on a retired claim and on an overdue recheck, and reports why", async () => {
    const client = gtmClient();
    const old = (await client.checkClaims({ ...org, text: "The Sipper plan costs $15 per month." })).findings;
    expect(old).toHaveLength(1);
    expect(old[0]).toMatchObject({ kind: GtmClaimFindingKind.MATCHED_UNVERIFIED, reason: "retired", blocking: true });
    const slow = (await client.checkClaims({ ...org, text: "Orders arrive within 4 days." })).findings;
    expect(slow.find((f) => f.mention.includes("4 days"))).toMatchObject({ kind: GtmClaimFindingKind.MATCHED_UNVERIFIED, reason: "recheck overdue", blocking: true });
  });

  it("passes a text with no risky mention, and is deterministic", async () => {
    const client = gtmClient();
    const clean = await client.checkClaims({ ...org, text: "Hello from the team." });
    expect([clean.findings.length, clean.blocking]).toEqual([0, false]);
    const text = "Shipping on every Weekly Box is free. The Sipper plan costs $19 per month. We are 100% reliable.";
    const a = await client.checkClaims({ ...org, text });
    const b = await client.checkClaims({ ...org, text });
    expect(a.findings.map((f) => [f.mentionKind, f.mention, f.kind])).toEqual(b.findings.map((f) => [f.mentionKind, f.mention, f.kind]));
    expect(a.findings.find((f) => f.mention === "100%")?.kind).toBe(GtmClaimFindingKind.UNMATCHED_RISKY);
    expect(a.findings.find((f) => f.mentionKind === "free")?.kind).toBe(GtmClaimFindingKind.MATCHED_VERIFIED);
  });

  it("flags the sample items' own drafts as the UI would show them", async () => {
    const client = gtmClient();
    const items = (await client.listItems(org)).items;
    const pricing = items.find((i) => i.id === "gtm-item-pricing-explained")!;
    const res = await client.checkClaims({ ...org, text: `${pricing.title}\n\n${pricing.bodyMd}` });
    expect(res.blocking).toBe(true);
    expect(res.findings.some((f) => f.kind === GtmClaimFindingKind.MATCHED_UNVERIFIED && f.reason === "retired")).toBe(true);
    expect(res.findings.some((f) => f.kind === GtmClaimFindingKind.MATCHED_VERIFIED)).toBe(true);
  });
});
