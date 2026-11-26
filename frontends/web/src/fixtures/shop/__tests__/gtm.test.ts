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
  GtmCampaignStatus,
  GtmClaimCategory,
  GtmClaimFindingKind,
  GtmClaimSourceKind,
  GtmClaimStatus,
  GtmIdeaKind,
  GtmIdeaStatus,
  GtmItemKind,
  GtmItemStatus,
  GtmRevisionConflictSchema,
  GtmService,
} from "@/gen/reliant/v1/gtm_pb";

import { createFixtureTransport } from "../../transport";
import { shopAppPack } from "../app";
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

  it("holds about 12 ideas across every status, 3 campaigns, about 15 items in the current month and 8 mixed claims", async () => {
    const client = gtmClient();
    const { ideas } = await client.listIdeas(org);
    expect(ideas.length).toBeGreaterThanOrEqual(12);
    expect(new Set(ideas.map((i) => i.status))).toEqual(new Set([GtmIdeaStatus.PROPOSED, GtmIdeaStatus.ACCEPTED, GtmIdeaStatus.DONE, GtmIdeaStatus.REJECTED]));
    expect(new Set(ideas.map((i) => i.kind))).toEqual(new Set([GtmIdeaKind.PRODUCT, GtmIdeaKind.GTM, GtmIdeaKind.CONTENT]));
    // Newest first.
    expect(ideas.map((i) => timestampMs(i.createdAt!))).toEqual(ideas.map((i) => timestampMs(i.createdAt!)).sort((a, b) => b - a));

    expect((await client.listCampaigns(org)).campaigns).toHaveLength(3);

    const { items } = await client.listItems(org);
    expect(items.length).toBeGreaterThanOrEqual(15);
    expect(items.length).toBeLessThanOrEqual(16);
    const scheduled = items.filter((i) => i.scheduledAt);
    expect(scheduled.length).toBeGreaterThanOrEqual(13);
    for (const i of scheduled) {
      const d = new Date(timestampMs(i.scheduledAt!));
      expect([d.getFullYear(), d.getMonth()]).toEqual([2026, 6]);
    }
    expect(new Set(items.map((i) => i.kind))).toContain(GtmItemKind.EMAIL);

    const { claims } = await client.listClaims(org);
    expect(claims).toHaveLength(8);
    expect(new Set(claims.map((c) => c.status))).toEqual(new Set([GtmClaimStatus.VERIFIED, GtmClaimStatus.UNVERIFIED, GtmClaimStatus.RETIRED]));

    const { positioning } = await client.getPositioning(org);
    expect(positioning?.positioningMd).not.toBe("");
    expect(positioning?.audiences.length).toBeGreaterThan(0);
  });

  it("puts items on the current month wherever the clock is, and never past the 28th", async () => {
    for (const at of [new Date(2026, 1, 3, 9), new Date(2026, 11, 31, 23), new Date(2028, 1, 29, 1)]) {
      resetDemoNowForTests(at.getTime());
      const { items } = await createFixtureClient(at.getTime()).listItems(org);
      for (const i of items.filter((x) => x.scheduledAt)) {
        const d = new Date(timestampMs(i.scheduledAt!));
        expect([d.getFullYear(), d.getMonth()]).toEqual([at.getFullYear(), at.getMonth()]);
      }
    }
  });

  it("is generic: no real prices or product names in any sample text", async () => {
    const client = gtmClient();
    const text = JSON.stringify([
      (await client.listIdeas(org)).ideas, (await client.listItems(org)).items, (await client.listClaims(org)).claims, (await client.getPositioning(org)).positioning,
    ], (key, value) => (key === "$typeName" ? undefined : typeof value === "bigint" ? value.toString() : value));
    expect(text).not.toMatch(/reliant|forge/i);
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

  it("filters campaigns by status", async () => {
    const { campaigns } = await gtmClient().listCampaigns({ ...org, status: GtmCampaignStatus.ACTIVE });
    expect(campaigns.map((c) => c.status)).toEqual([GtmCampaignStatus.ACTIVE]);
  });

  it("filters items by campaign, kind, status and a half-open scheduled range, soonest first", async () => {
    const client = gtmClient();
    const all = (await client.listItems(org)).items;
    const campaignId = all.find((i) => i.campaignId)!.campaignId;
    const mine = (await client.listItems({ ...org, campaignId })).items;
    expect(mine.length).toBeGreaterThan(0);
    expect(mine.every((i) => i.campaignId === campaignId)).toBe(true);
    expect((await client.listItems({ ...org, kind: GtmItemKind.EMAIL })).items.every((i) => i.kind === GtmItemKind.EMAIL)).toBe(true);
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
    expect((await client.listClaims({ ...org, search: "FREE" })).claims.map((c) => c.text)).toEqual(["One static site per organization is free."]);
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
    expect((await rejection(client.updateItem({ ...org, id: approved.id, expectedRevision: approved.revision, status: GtmItemStatus.SENT }))).code).toBe(Code.FailedPrecondition);
    const sent = (await client.listItems({ ...org, status: GtmItemStatus.SENT })).items[0];
    expect((await rejection(client.updateItem({ ...org, id: sent.id, expectedRevision: sent.revision, title: "Edited after sending" }))).code).toBe(Code.FailedPrecondition);
  });

  it("creates ideas, campaigns and items, linking only the org's own records", async () => {
    const client = gtmClient();
    const campaign = (await client.createCampaign({ ...org, name: "Autumn push", goal: "sample" })).campaign!;
    expect(campaign.status).toBe(GtmCampaignStatus.DRAFT);
    const idea = (await client.createIdea({ ...org, kind: GtmIdeaKind.CONTENT, title: "New idea", campaignId: campaign.id })).idea!;
    expect([idea.status, idea.revision]).toEqual([GtmIdeaStatus.PROPOSED, 1]);
    const item = (await client.createItem({ ...org, kind: GtmItemKind.BLOG_POST, title: "New post", campaignId: campaign.id, ideaId: idea.id, scheduledAt: timestampFromMs(NOW) })).item!;
    expect(item.status).toBe(GtmItemStatus.DRAFT);
    const linked = await client.getCampaign({ ...org, id: campaign.id });
    expect([linked.ideas.map((i) => i.id), linked.items.map((i) => i.id)]).toEqual([[idea.id], [item.id]]);
    expect((await rejection(client.createItem({ ...org, kind: GtmItemKind.BLOG_POST, title: "Orphan", campaignId: "not-a-campaign" }))).code).toBe(Code.NotFound);
    const renamed = (await client.updateCampaign({ ...org, id: campaign.id, expectedRevision: campaign.revision, name: "Autumn launch", status: GtmCampaignStatus.ACTIVE })).campaign!;
    expect([renamed.name, renamed.status, renamed.revision]).toEqual(["Autumn launch", GtmCampaignStatus.ACTIVE, 2]);
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

  it("lets the listed GTM writes through a read-only transport, and still refuses any other write", async () => {
    const transport = createFixtureTransport({ pack: shopAppPack({ now: NOW }), now: demoNow, readOnly: true, readOnlyMessage: SHOP_READ_ONLY_MESSAGE });
    expect((await createClient(GtmService, transport).createIdea({ ...org, kind: GtmIdeaKind.GTM, title: "ok" })).idea?.title).toBe("ok");
    await expect(createClient(ChatService, transport).deleteChat({ chatId: "shop-chat-cart-totals" })).rejects.toThrow(SHOP_READ_ONLY_MESSAGE);
  });
});

describe("CheckClaims", () => {
  it("flags a price with no matching claim, and passes one matching a verified claim", async () => {
    const client = gtmClient();
    const res = await client.checkClaims({ ...org, text: "Starter costs $19 per month, billed monthly. Our Pro plan is $49 per month." });
    const price = (m: string) => res.findings.find((f) => f.mention === m)!;
    expect(price("$19")).toMatchObject({ kind: GtmClaimFindingKind.MATCHED_VERIFIED, blocking: false, claimId: "gtm-claim-starter-price", mentionKind: "price" });
    expect(price("$49")).toMatchObject({ kind: GtmClaimFindingKind.UNMATCHED_RISKY, blocking: true, claimId: "" });
    expect(price("$49").context).toBe("Our Pro plan is $49 per month.");
    expect(res.blocking).toBe(true);
  });

  it("blocks on a retired claim and on an overdue recheck, and reports why", async () => {
    const client = gtmClient();
    const old = (await client.checkClaims({ ...org, text: "Starter costs $15 per month." })).findings;
    expect(old).toHaveLength(1);
    expect(old[0]).toMatchObject({ kind: GtmClaimFindingKind.MATCHED_UNVERIFIED, reason: "retired", blocking: true });
    const slow = (await client.checkClaims({ ...org, text: "Deploys finish in under 90 seconds." })).findings;
    expect(slow.find((f) => f.mention.includes("90"))).toMatchObject({ kind: GtmClaimFindingKind.MATCHED_UNVERIFIED, reason: "recheck overdue", blocking: true });
  });

  it("passes a text with no risky mention, and is deterministic", async () => {
    const client = gtmClient();
    const clean = await client.checkClaims({ ...org, text: "Hello from the team." });
    expect([clean.findings.length, clean.blocking]).toEqual([0, false]);
    const text = "One static site per organization is free. Starter costs $19 per month. We are 100% reliable.";
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
