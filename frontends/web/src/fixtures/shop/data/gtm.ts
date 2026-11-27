// Copyright (c) 2026 Reliant Labs

/**
 * The GTM sample pack: a FICTIONAL coffee company (Brightwell Coffee) and its
 * two made-up products (the Weekly Box, a roasted-to-order subscription, and
 * the Tasting Club, one small-lot origin a month), as the GtmService's own
 * messages. Every number, price, plan name, claim and source here is invented
 * and says so; nothing in it describes a real product, price or customer,
 * because this repo is public. The business is deliberately unrelated to
 * anything Reliant or forge sells, so no sample fact can echo a real one
 * (gtm.test.ts keeps a denylist of the phrases that must never appear).
 *
 * Time is relative to the `now` it is built with (the pack clock): the items
 * sit from 10 days before today to 20 days after it, whatever day it is, so
 * the calendar and the phone's "Coming up" list are never empty, and an item
 * is in its past or future state by whether its moment has gone by.
 *
 * The strategy library holds a few made-up PLACEHOLDER entries, written for
 * this pack: a real org imports its own library, and none of that lives here.
 * Only the 13 category names are real, and they are generic.
 */

import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate, type Timestamp } from "@bufbuild/protobuf/wkt";

import {
  GtmAssetSchema,
  GtmAssetSource,
  GtmAssetStatus,
  GtmCampaignSchema,
  GtmCampaignStatus,
  GtmChannelMode,
  GtmChannelSchema,
  GtmChannelSource,
  GtmChannelStatus,
  GtmClaimCategory,
  GtmClaimSchema,
  GtmClaimSourceKind,
  GtmClaimStatus,
  GtmConnectionState,
  GtmCredentialKind,
  GtmEvidenceSchema,
  GtmIdeaKind,
  GtmIdeaSchema,
  GtmIdeaSource,
  GtmIdeaStatus,
  GtmItemFormat,
  GtmItemSchema,
  GtmItemStatus,
  GtmLibraryDepth,
  GtmLibraryImportSchema,
  GtmLibraryPruneRequestSchema,
  GtmLibraryStepSchema,
  GtmPositioningSchema,
  GtmPruneRequestStatus,
  GtmStrategyCategory,
  GtmStrategyLibraryEntrySchema,
  GtmStrategySchema,
  GtmStrategyStatus,
  type GtmAsset,
  type GtmCampaign,
  type GtmChannel,
  type GtmClaim,
  type GtmEvidence,
  type GtmIdea,
  type GtmItem,
  type GtmLibraryImport,
  type GtmLibraryPruneRequest,
  type GtmPositioning,
  type GtmStrategy,
  type GtmStrategyLibraryEntry,
} from "@/gen/reliant/v1/gtm_pb";

import { DEMO_ORG } from "../shopStory";
import { APP_USER } from "../user";

/** The person every "by a person" field names (a display name, so the pages read well). */
export const GTM_SAMPLE_PERSON = APP_USER.name;

export const GTM_STRATEGY_IDS = {
  subscriberLed: "gtm-str-subscriber-led",
  retention: "gtm-str-retention",
} as const;

export const GTM_CAMPAIGN_IDS = {
  weeklyBox: "gtm-cmp-weekly-box",
  tastingClub: "gtm-cmp-tasting-club",
} as const;

export const GTM_CHANNEL_IDS = {
  email: "gtm-ch-email",
  outreach: "gtm-ch-outreach",
  blog: "gtm-ch-blog",
  hackerNews: "gtm-ch-hacker-news",
  reddit: "gtm-ch-reddit",
  linkedin: "gtm-ch-linkedin",
  x: "gtm-ch-x",
  youtube: "gtm-ch-youtube",
  brewingForums: "gtm-ch-brewing-forums",
  podcasts: "gtm-ch-podcast-guests",
} as const;

export const GTM_PRUNE_REQUEST_ID = "gtm-prune-sample-1";

/** The 13 library categories' generic display names, by enum: the one part of a catalog that may live in this repo. */
export const GTM_CATEGORY_LABELS: Record<number, string> = {
  [GtmStrategyCategory.PRICING_OFFERS_INCENTIVES]: "Pricing, offers & incentives",
  [GtmStrategyCategory.REFERRAL_VIRAL_GROWTH]: "Referral & viral growth",
  [GtmStrategyCategory.PAID_ACQUISITION]: "Paid acquisition",
  [GtmStrategyCategory.ORGANIC_CONTENT_SEO]: "Organic content & SEO",
  [GtmStrategyCategory.OUTBOUND_SALES_PROSPECTING]: "Outbound sales & prospecting",
  [GtmStrategyCategory.PARTNERSHIPS_DISTRIBUTION]: "Partnerships & distribution",
  [GtmStrategyCategory.LAUNCHES_PR]: "Launches & PR",
  [GtmStrategyCategory.COMMUNITY_EVENTS]: "Community & events",
  [GtmStrategyCategory.PRODUCT_LED_GROWTH]: "Product-led growth",
  [GtmStrategyCategory.CONVERSION_OPTIMIZATION]: "Conversion optimization",
  [GtmStrategyCategory.RETENTION_EXPANSION]: "Retention & expansion",
  [GtmStrategyCategory.TRUST_REPUTATION]: "Trust & reputation",
  [GtmStrategyCategory.MARKET_RESEARCH_POSITIONING]: "Market research & positioning",
};

export interface GtmSampleData {
  strategies: GtmStrategy[];
  channels: GtmChannel[];
  ideas: GtmIdea[];
  campaigns: GtmCampaign[];
  items: GtmItem[];
  assets: GtmAsset[];
  /** Keyed by `key`, in no particular order (the list rpc sorts). */
  library: GtmStrategyLibraryEntry[];
  libraryImports: GtmLibraryImport[];
  pruneRequests: GtmLibraryPruneRequest[];
  positioning: GtmPositioning;
  claims: GtmClaim[];
}

const svgUrl = (svg: string) => `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`;

/** Sample images, drawn inline so the preview needs no asset store (and no network). */
const ASSET_IMAGES: Record<string, string> = {
  "gtm-asset-hn-hero": svgUrl(
    `<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="630" viewBox="0 0 1200 630"><defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#5b3a29"/><stop offset="1" stop-color="#c8864b"/></linearGradient></defs><rect width="1200" height="630" fill="url(#g)"/><circle cx="980" cy="140" r="220" fill="#fff" fill-opacity="0.08"/><text x="80" y="300" font-family="system-ui, sans-serif" font-size="84" font-weight="700" fill="#fff">Brightwell Coffee</text><text x="80" y="380" font-family="system-ui, sans-serif" font-size="40" fill="#fdf1e3">The grind-size calculator</text><text x="80" y="560" font-family="system-ui, sans-serif" font-size="26" fill="#f1d5b5">Sample image, fictional company</text></svg>`,
  ),
  "gtm-asset-brand-sheet": svgUrl(
    `<svg xmlns="http://www.w3.org/2000/svg" width="800" height="600" viewBox="0 0 800 600"><rect width="800" height="600" fill="#faf6f1"/><rect x="60" y="80" width="200" height="200" rx="24" fill="#5b3a29"/><rect x="300" y="80" width="200" height="200" rx="24" fill="#c8864b"/><rect x="540" y="80" width="200" height="200" rx="24" fill="#e8d5b5"/><text x="60" y="380" font-family="system-ui, sans-serif" font-size="44" font-weight="700" fill="#2b1d14">Weekly Box brand sheet</text><text x="60" y="440" font-family="system-ui, sans-serif" font-size="26" fill="#6b5646">Colours, label lockups, tone (sample)</text></svg>`,
  ),
};

/** A sample asset's inline image: what GetAssetDownload answers (no bucket in the preview). */
export const gtmAssetImageUrl = (assetId: string): string => ASSET_IMAGES[assetId] ?? ASSET_IMAGES["gtm-asset-brand-sheet"]!;

export function gtmSampleData(nowMs: number): GtmSampleData {
  const now = new Date(nowMs);
  const at = (dayOffset: number, hours = 9, minutes = 0): Date => {
    const d = new Date(now.getFullYear(), now.getMonth(), now.getDate() + dayOffset);
    d.setHours(hours, minutes, 0, 0);
    return d;
  };
  const ts = (d: Date): Timestamp => timestampFromDate(d);
  const dateOnly = (dayOffset: number): string => {
    const d = at(dayOffset);
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
  };

  const evidence = (title: string, summaryMd: string, observed: Date, kind = "analytics"): GtmEvidence =>
    create(GtmEvidenceSchema, { kind, title, summaryMd, observedAt: ts(observed) });

  // ── Strategies ─────────────────────────────────────────────────────────────
  const strategies: GtmStrategy[] = [
    create(GtmStrategySchema, {
      id: GTM_STRATEGY_IDS.subscriberLed, orgId: DEMO_ORG, name: "Subscriber-led growth this half",
      goalMd: "Win home brewers who care how their coffee tastes: a box that arrives roasted to order, and word of mouth in the places they already talk coffee. The Weekly Box leads; the Tasting Club follows once the box has a base to grow from.",
      targetMetric: "2,000 active subscribers by the end of the half (sample target)",
      periodStart: dateOnly(-30), periodEnd: dateOnly(150), status: GtmStrategyStatus.ACTIVE, category: GtmStrategyCategory.LAUNCHES_PR,
      revision: 2, createdBy: GTM_SAMPLE_PERSON, updatedBy: GTM_SAMPLE_PERSON, createdAt: ts(at(-32)), updatedAt: ts(at(-30)),
    }),
    create(GtmStrategySchema, {
      id: GTM_STRATEGY_IDS.retention, orgId: DEMO_ORG, name: "Keep new subscribers past their third box",
      goalMd: "Subscribers who get a third box tend to stay. A draft from the agent: a brewing guide after the first box, and a monthly note on what is roasting.",
      targetMetric: "60% of new subscribers reach a third box (sample target)",
      periodStart: dateOnly(7), periodEnd: dateOnly(97), status: GtmStrategyStatus.DRAFT, category: GtmStrategyCategory.RETENTION_EXPANSION,
      libraryKey: "sample-annual-nudge", revision: 1, createdBy: "Chat: retention review (sample)", updatedBy: "Chat: retention review (sample)", createdAt: ts(at(-3, 14)), updatedAt: ts(at(-3, 14)),
    }),
  ];

  // ── Channels ───────────────────────────────────────────────────────────────
  const CH = GTM_CHANNEL_IDS;
  const channel = (fields: MessageInitShape<typeof GtmChannelSchema> & Pick<GtmChannel, "id" | "key" | "name">, created: Date): GtmChannel =>
    create(GtmChannelSchema, {
      orgId: DEMO_ORG, mode: GtmChannelMode.MANUAL, status: GtmChannelStatus.ACTIVE, source: GtmChannelSource.SEED, createdBy: "",
      revision: 1, createdAt: ts(created), updatedAt: ts(created), ...fields,
    });
  const channels: GtmChannel[] = [
    channel({
      id: CH.email, key: "email", name: "Email", mode: GtmChannelMode.INTEGRATED, integrationKind: "gmail", connectionState: GtmConnectionState.CONNECTED, channelTypes: ["email"],
    }, at(-40)),
    channel({
      id: CH.outreach, key: "outreach", name: "Outreach inbox", mode: GtmChannelMode.INTEGRATED, integrationKind: "gmail", connectionState: GtmConnectionState.NOT_CONNECTED,
      channelTypes: ["email", "outreach"], source: GtmChannelSource.PERSON, createdBy: GTM_SAMPLE_PERSON,
    }, at(-12)),
    channel({ id: CH.blog, key: "blog", name: "Blog", manualInstructionsMd: "Publish via the site repo, then record the URL.", channelTypes: ["blog"] }, at(-40)),
    channel({ id: CH.hackerNews, key: "hacker-news", name: "Hacker News", manualInstructionsMd: "Post by hand, then paste the URL on the item.", channelTypes: ["hacker-news", "forum"] }, at(-40)),
    channel({ id: CH.reddit, key: "reddit", name: "Reddit", manualInstructionsMd: "Post by hand in the subreddit the item names, then paste the URL on the item.", channelTypes: ["reddit", "forum"] }, at(-40)),
    channel({ id: CH.linkedin, key: "linkedin", name: "LinkedIn", manualInstructionsMd: "Post by hand from the company page, then paste the URL on the item.", channelTypes: ["linkedin", "social"] }, at(-40)),
    channel({ id: CH.x, key: "x", name: "X", manualInstructionsMd: "Post by hand, then paste the URL on the item.", channelTypes: ["x", "social"] }, at(-40)),
    channel({ id: CH.youtube, key: "youtube", name: "YouTube", manualInstructionsMd: "Upload by hand to the company channel, then paste the URL on the item.", channelTypes: ["youtube", "video"] }, at(-40)),
    channel({
      id: CH.brewingForums, key: "brewing-forums", name: "Home-brewing forums", source: GtmChannelSource.AGENT, createdBy: "Chat: launch planning (sample)",
      manualInstructionsMd: "Answer in threads where someone asks how to dial in a grinder. Post by hand, then paste the URL.", channelTypes: ["forum", "community"],
    }, at(-6)),
    channel({
      id: CH.podcasts, key: "podcast-guests", name: "Podcast guest spots", source: GtmChannelSource.AGENT, createdBy: "Chat: launch planning (sample)",
      manualInstructionsMd: "Pitch the host by hand. Once the episode is out, paste its URL on the item.", channelTypes: ["podcast"],
    }, at(-6)),
  ];

  // ── Campaigns ──────────────────────────────────────────────────────────────
  const F = GtmItemFormat;
  const campaigns: GtmCampaign[] = [
    create(GtmCampaignSchema, {
      id: GTM_CAMPAIGN_IDS.weeklyBox, orgId: DEMO_ORG, strategyId: GTM_STRATEGY_IDS.subscriberLed, name: "Weekly Box launch",
      goal: "500 new subscribers in the first 30 days (sample target)",
      periodStart: ts(at(-14)), periodEnd: ts(at(16)), status: GtmCampaignStatus.ACTIVE, revision: 2, createdAt: ts(at(-20)), updatedAt: ts(at(-14)),
      plannedChannelIds: [CH.blog, CH.hackerNews, CH.x, CH.linkedin, CH.reddit, CH.email], plannedFormats: [F.LONG_POST, F.SHORT_POST, F.EMAIL, F.VIDEO],
    }),
    create(GtmCampaignSchema, {
      id: GTM_CAMPAIGN_IDS.tastingClub, orgId: DEMO_ORG, strategyId: GTM_STRATEGY_IDS.subscriberLed, name: "Tasting Club: a new origin every month",
      goal: "150 Tasting Club members by the end of the quarter (sample target)",
      periodStart: ts(at(3)), periodEnd: ts(at(40)), status: GtmCampaignStatus.DRAFT, revision: 1, createdAt: ts(at(-8)), updatedAt: ts(at(-8)),
      plannedChannelIds: [CH.blog, CH.youtube, CH.email, CH.x], plannedFormats: [F.LONG_POST, F.VIDEO, F.EMAIL, F.SHORT_POST],
    }),
  ];

  // ── Ideas ──────────────────────────────────────────────────────────────────
  const agent = { source: GtmIdeaSource.AGENT, createdBy: GTM_SAMPLE_PERSON };
  const human = { source: GtmIdeaSource.HUMAN, createdBy: GTM_SAMPLE_PERSON };
  const idea = (fields: MessageInitShape<typeof GtmIdeaSchema> & Pick<GtmIdea, "id" | "kind" | "title" | "bodyMd" | "status">, created: Date, updated: Date = created): GtmIdea =>
    create(GtmIdeaSchema, { orgId: DEMO_ORG, revision: 1, createdAt: ts(created), updatedAt: ts(updated), ...fields });

  const ideas: GtmIdea[] = [
    idea({
      id: "gtm-idea-roast-date-post", kind: GtmIdeaKind.CONTENT, status: GtmIdeaStatus.PROPOSED, ...agent, sourceChatId: "sample-chat-weekly-ideas",
      campaignId: GTM_CAMPAIGN_IDS.tastingClub, title: "Explain roast dates in one post",
      bodyMd: [
        "The roast-date help page gets a lot of visits but few visitors go on to start a subscription. One plain post that reads a roast date off a bag could close that gap.",
        "",
        "# How to read the roast date on your bag",
        "",
        "Most people look for a best-before date. The number worth finding is the day it was roasted.",
        "",
        "## Check your own bag in a minute",
        "",
        "1. Find the date printed near the valve.",
        "2. Count the days since, and note how the cup changes.",
        "3. Tell us which day tasted best.",
        "",
        "TODO (agent): add the freshness window once the claims list has a verified line for it.",
      ].join("\n"),
      evidence: [
        evidence("Roast-date help page views, last 7 days (sample)", "**1,240** views", at(-2, 7)),
        evidence("Visitors who then started a subscription (sample)", "**4%**", at(-2, 7)),
        evidence("Median time on page (sample)", "3m 10s", at(-2, 7)),
      ],
    }, at(-2, 7, 2)),
    idea({
      id: "gtm-idea-weekly-box-launch", kind: GtmIdeaKind.GTM, status: GtmIdeaStatus.ACCEPTED, ...human, campaignId: GTM_CAMPAIGN_IDS.weeklyBox, revision: 2,
      title: "Launch post: the Weekly Box is here",
      bodyMd: [
        "The Weekly Box is open to everyone, and the first bag is on us (sample offer). That is the clearest reason yet to try Brightwell, so it should lead the launch.",
        "",
        "# The Weekly Box is here",
        "",
        "Pick a size and a day. Each bag is roasted to order and sent to your door.",
        "",
        "Shipping on every Weekly Box is free.",
      ].join("\n"),
      evidence: [
        evidence("Sign-ups on the pre-launch list (sample)", "**312**", at(-18, 11)),
        evidence("List members who picked a delivery day (sample)", "**41%**", at(-18, 11)),
      ],
    }, at(-18, 11), at(-15, 16)),
    idea({
      id: "gtm-idea-pause-email", kind: GtmIdeaKind.GTM, status: GtmIdeaStatus.PROPOSED, ...agent, sourceChatId: "sample-chat-analytics-review",
      campaignId: GTM_CAMPAIGN_IDS.tastingClub, title: "Email subscribers about pausing a box",
      bodyMd: [
        "Many cancellations happen the week someone goes on holiday. Subscribers may not know a box can be paused from the app.",
        "",
        "Subject: Going away? Pause your box",
        "",
        "Hi {first_name},",
        "",
        "If you are travelling, you can pause your next box from the app and pick it up again when you are back.",
      ].join("\n"),
      evidence: [
        evidence("Cancellations that name travel (sample)", "**38%**", at(-1, 15)),
        evidence("Subscribers who have ever paused (sample)", "**22%**", at(-1, 15)),
      ],
    }, at(-1, 15, 40)),
    idea({
      id: "gtm-idea-first-box-card", kind: GtmIdeaKind.PRODUCT, status: GtmIdeaStatus.PROPOSED, ...agent, sourceChatId: "sample-chat-weekly-ideas",
      title: "Put a brewing card in the first box",
      bodyMd: "New subscribers often brew the first bag the way they brewed the last one, and the cup disappoints. A card with a starting grind, dose and time per brew method would set them up.",
      evidence: [evidence("First-box reviews that mention a weak or bitter cup (sample)", "**17%**", at(-2, 7))],
    }, at(-2, 7, 2)),
    idea({
      id: "gtm-idea-vs-grocery", kind: GtmIdeaKind.CONTENT, status: GtmIdeaStatus.REJECTED, ...human, revision: 2,
      title: "Comparison page: Weekly Box vs. supermarket beans",
      bodyMd: "Outline: what a supermarket bag gives you, what roasting to order adds, and when each fits.\n\nRejected: the claims about other sellers had no sources we could verify.",
      evidence: [],
    }, at(-20, 10), at(-12, 9)),
    idea({
      id: "gtm-idea-roastery-visit", kind: GtmIdeaKind.CONTENT, status: GtmIdeaStatus.DONE, ...agent, sourceChatId: "sample-chat-launch-planning", campaignId: GTM_CAMPAIGN_IDS.weeklyBox, revision: 3,
      title: "Behind the scenes: a day at the roastery",
      bodyMd: [
        "Readers like seeing where their coffee comes from. A first-party visit is easy to source and hard to argue with.",
        "",
        "# A day at the Brightwell roastery",
        "",
        "Green beans arrive in sacks, get weighed, and go into the drum in small batches.",
        "",
        "Every batch is cupped before it ships.",
      ].join("\n"),
      evidence: [evidence("Roastery tour page views last month (sample)", "**640**", at(-16, 14))],
    }, at(-16, 14), at(-4, 12)),
    idea({
      id: "gtm-idea-grind-calculator", kind: GtmIdeaKind.GTM, status: GtmIdeaStatus.ACCEPTED, ...human, campaignId: GTM_CAMPAIGN_IDS.weeklyBox, revision: 2,
      title: "Show HN: the grind-size calculator we built for subscribers",
      bodyMd: [
        "A short, technical Show HN fits the audience that already tinkers with their brew setup.",
        "",
        "Show HN: A grind-size calculator for home espresso and pour-over",
        "",
        "We built a small tool that suggests a starting grind from your grinder, method and beans. Here is how it works and what we got wrong at first.",
      ].join("\n"),
      evidence: [evidence("Visits from the last HN thread (sample)", "**1 in 5**", at(-10, 9), "note")],
    }, at(-10, 9), at(-6, 17)),
    idea({
      id: "gtm-idea-pricing-faq", kind: GtmIdeaKind.CONTENT, status: GtmIdeaStatus.PROPOSED, ...agent, sourceChatId: "sample-chat-analytics-review",
      title: "A plain-language pricing FAQ",
      bodyMd: "Pricing-page visitors ask the same four questions in support. A short FAQ (what the Sipper plan costs, what ships free, what changes if you pause) would answer them before they write in.",
      evidence: [
        evidence("Support threads about pricing, last 30 days (sample)", "**46**", at(-3, 8)),
        evidence("Pricing page exits without a click (sample)", "**61%**", at(-3, 8)),
      ],
    }, at(-3, 8, 15)),
    idea({
      id: "gtm-idea-live-tasting", kind: GtmIdeaKind.GTM, status: GtmIdeaStatus.PROPOSED, ...human, campaignId: GTM_CAMPAIGN_IDS.tastingClub,
      title: "Live tasting: three origins in thirty minutes",
      bodyMd: "A 30-minute live session where we brew three origins side by side, take questions, and share tasting notes afterwards.",
      evidence: [],
    }, at(-4, 13)),
    idea({
      id: "gtm-idea-taste-quiz", kind: GtmIdeaKind.PRODUCT, status: GtmIdeaStatus.ACCEPTED, ...human, revision: 2,
      title: "Taste quiz on the sign-up page",
      bodyMd: "Three questions (how you brew, what you usually drink, how adventurous you feel) and a suggested first box at the end.",
      evidence: [evidence("Visitors who leave the sign-up page before choosing a box (sample)", "**34%**", at(-9, 10))],
    }, at(-12, 10), at(-9, 10)),
    idea({
      id: "gtm-idea-referral", kind: GtmIdeaKind.GTM, status: GtmIdeaStatus.REJECTED, ...agent, sourceChatId: "sample-chat-weekly-ideas", revision: 2,
      title: "Offer referral credits",
      bodyMd: "Give both sides a bag of beans when a referral subscribes.\n\nRejected: credits need a finance review first.",
      evidence: [],
    }, at(-26, 9), at(-24, 11)),
    idea({
      id: "gtm-idea-monthly-roast-note", kind: GtmIdeaKind.CONTENT, status: GtmIdeaStatus.DONE, ...agent, sourceChatId: "sample-chat-launch-planning", revision: 3,
      title: "Monthly roast note",
      bodyMd: "Roll the month's new origins into one email with a line on how each one tastes.",
      evidence: [evidence("Roast note opens per month (sample)", "**2,900**", at(-30, 9))],
    }, at(-60, 9), at(-32, 9)),
  ];

  // ── Items: from 10 days ago to 20 days ahead ──────────────────────────────
  const S = GtmItemStatus;
  interface ItemSpec {
    id: string;
    /** Days from today (negative: past); null: not on the calendar. */
    day: number | null;
    hours: number;
    format: GtmItemFormat;
    channelId?: string;
    title: string;
    bodyMd: string;
    campaignId?: string;
    ideaId?: string;
    /** The status once its day has gone by, and before. */
    past: GtmItemStatus;
    future: GtmItemStatus;
    email?: { to: string[]; subject: string };
    /** An email whose send started and whose outcome is unknown: APPROVED with its approval hash set, whatever the date. */
    held?: boolean;
  }
  const specs: ItemSpec[] = [
    {
      id: "gtm-item-launch-blog", day: -10, hours: 10, format: F.LONG_POST, channelId: CH.blog, title: "The Weekly Box is here: beans roasted to order",
      bodyMd: "# The Weekly Box is here\n\nPick a size and a day. Each bag is roasted to order and sent to your door.\n\nShipping on every Weekly Box is free.",
      campaignId: GTM_CAMPAIGN_IDS.weeklyBox, ideaId: "gtm-idea-weekly-box-launch", past: S.PUBLISHED, future: S.APPROVED,
    },
    {
      id: "gtm-item-launch-thread", day: -8, hours: 16, format: F.SHORT_POST, channelId: CH.x, title: "Thread: what a box looks like when it lands",
      bodyMd: "1/ The Weekly Box is open. Here is what is inside.\n2/ A bag roasted to order, a brewing card, and a note from the roaster.",
      campaignId: GTM_CAMPAIGN_IDS.weeklyBox, ideaId: "gtm-idea-weekly-box-launch", past: S.PUBLISHED, future: S.SCHEDULED,
    },
    {
      id: "gtm-item-launch-email", day: -7, hours: 14, format: F.EMAIL, channelId: CH.email, title: "Your first box can ship this week",
      bodyMd: "Hi {first_name},\n\nThe Weekly Box is open. Pick a size and a day, and your first bag is roasted to order.",
      campaignId: GTM_CAMPAIGN_IDS.weeklyBox, ideaId: "gtm-idea-weekly-box-launch", past: S.SENT, future: S.APPROVED,
      email: { to: ["pre-launch-list@lists.example.com"], subject: "Your first box can ship this week" },
    },
    {
      id: "gtm-item-launch-linkedin", day: -5, hours: 11, format: F.SHORT_POST, channelId: CH.linkedin, title: "The Weekly Box is live",
      bodyMd: "The Weekly Box is live: beans roasted to order, a delivery day you choose, and free shipping on every box.",
      campaignId: GTM_CAMPAIGN_IDS.weeklyBox, past: S.PUBLISHED, future: S.SCHEDULED,
    },
    {
      // A manual channel: approved, posted by hand, and waiting for the person to paste the URL and mark it published.
      id: "gtm-item-show-hn", day: 0, hours: 16, format: F.SHORT_POST, channelId: CH.hackerNews, title: "Show HN: the grind-size calculator we built for subscribers",
      bodyMd: "Show HN: A grind-size calculator for home espresso and pour-over\n\nWe built a small tool that suggests a starting grind from your grinder, method and beans. Here is how it works and what we got wrong at first.",
      campaignId: GTM_CAMPAIGN_IDS.weeklyBox, ideaId: "gtm-idea-grind-calculator", past: S.APPROVED, future: S.APPROVED,
    },
    {
      id: "gtm-item-roastery-post", day: 1, hours: 9, format: F.LONG_POST, channelId: CH.blog, title: "Behind the scenes: a day at the roastery",
      bodyMd: "# A day at the Brightwell roastery\n\nGreen beans arrive in sacks, get weighed, and go into the drum in small batches.\n\nEvery batch is cupped before it ships.\n\nEvery batch is roasted in under 12 minutes.",
      campaignId: GTM_CAMPAIGN_IDS.weeklyBox, ideaId: "gtm-idea-roastery-visit", past: S.PAUSED, future: S.REVIEW,
    },
    {
      // Held: the send started and its outcome is unknown. Frozen until a person resolves it.
      id: "gtm-item-grind-guide-email", day: -1, hours: 8, format: F.EMAIL, channelId: CH.email, title: "Follow-up: a grind guide for your first box",
      bodyMd: "Hi {first_name},\n\nYour first bag is on its way. Here is a starting grind for the way you brew, and what to change if the cup tastes thin.",
      campaignId: GTM_CAMPAIGN_IDS.weeklyBox, past: S.APPROVED, future: S.APPROVED, held: true,
      email: { to: ["new-subscribers@lists.example.com"], subject: "A grind guide for your first box" },
    },
    {
      id: "gtm-item-tasting-club-post", day: 3, hours: 9, format: F.LONG_POST, channelId: CH.blog, title: "The Tasting Club: a new origin every month",
      bodyMd: "# A new origin on your doorstep every month\n\nEach month we pick one small lot, roast a little of it, and send it with tasting notes. You brew it, tell us what you think, and we pick the next one.",
      campaignId: GTM_CAMPAIGN_IDS.tastingClub, ideaId: "gtm-idea-roast-date-post", past: S.PUBLISHED, future: S.DRAFT,
    },
    {
      id: "gtm-item-club-email", day: 5, hours: 14, format: F.EMAIL, channelId: CH.email, title: "Meet the Tasting Club",
      bodyMd: "Hi {first_name},\n\nThis month's origin is a washed lot with notes of stone fruit. Members get a small bag and tasting notes on the 18th.",
      campaignId: GTM_CAMPAIGN_IDS.tastingClub, ideaId: "gtm-idea-pause-email", past: S.SENT, future: S.DRAFT,
      email: { to: ["weekly-box-subscribers@lists.example.com"], subject: "Meet the Tasting Club" },
    },
    {
      id: "gtm-item-club-thread", day: 7, hours: 12, format: F.SHORT_POST, channelId: CH.x, title: "A new origin on your doorstep every month",
      bodyMd: "The Tasting Club sends one small-lot origin a month, with notes on what to look for in the cup.",
      campaignId: GTM_CAMPAIGN_IDS.tastingClub, past: S.PUBLISHED, future: S.REVIEW,
    },
    {
      // Cold outreach on an integrated channel whose Gmail connection is missing: it cannot be sent until a person connects it.
      id: "gtm-item-outreach", day: 9, hours: 10, format: F.COLD_OUTREACH, channelId: CH.outreach, title: "Intro to office managers",
      bodyMd: "Hi {first_name},\n\nWe deliver freshly roasted beans to offices on a schedule you pick. Would a short call be useful?",
      campaignId: GTM_CAMPAIGN_IDS.weeklyBox, past: S.DRAFT, future: S.DRAFT,
      email: { to: ["office-managers@lists.example.com"], subject: "Fresh beans for your office" },
    },
    {
      id: "gtm-item-changelog", day: -3, hours: 10, format: F.OTHER, channelId: CH.blog, title: "What's new: skip a week from the app",
      bodyMd: "Subscribers can now skip a single week from the app without pausing the whole subscription.",
      campaignId: GTM_CAMPAIGN_IDS.tastingClub, past: S.PUBLISHED, future: S.DRAFT,
    },
    {
      id: "gtm-item-live-tasting", day: 12, hours: 17, format: F.VIDEO, channelId: CH.youtube, title: "Live tasting: three origins in thirty minutes",
      bodyMd: "A 30-minute live session: brew three origins side by side, then questions.",
      campaignId: GTM_CAMPAIGN_IDS.tastingClub, ideaId: "gtm-idea-live-tasting", past: S.PAUSED, future: S.DRAFT,
    },
    {
      id: "gtm-item-customer-quote", day: 15, hours: 11, format: F.SHORT_POST, channelId: CH.linkedin, title: "Customer quote: from supermarket beans to the Weekly Box",
      bodyMd: "\"I did not know coffee could taste like this.\" (sample quote, not a real customer)",
      campaignId: GTM_CAMPAIGN_IDS.tastingClub, past: S.PUBLISHED, future: S.DRAFT,
    },
    {
      id: "gtm-item-pricing-explained", day: 20, hours: 9, format: F.LONG_POST, channelId: CH.blog, title: "Pricing explained: what the Sipper plan costs",
      bodyMd: "# Pricing explained\n\nThe Sipper plan is $19 per month, billed monthly. It used to be $15 per month, but that price is retired.\n\nShipping on every Weekly Box is free.",
      ideaId: "gtm-idea-pricing-faq", past: S.PAUSED, future: S.REVIEW,
    },
    {
      id: "gtm-item-backlog-page", day: null, hours: 9, format: F.LONG_POST, channelId: CH.blog, title: "Backlog: a page on storing beans at home",
      bodyMd: "Not scheduled yet. Outline: where to keep beans, what a valve bag does, and how to tell when a bag has gone flat.",
      past: S.DRAFT, future: S.DRAFT,
    },
  ];

  const channelKey = (id?: string) => channels.find((c) => c.id === id)?.key ?? "site";
  const items: GtmItem[] = specs.map((spec) => {
    const scheduled = spec.day === null ? null : at(spec.day, spec.hours);
    const status = spec.held ? S.APPROVED : scheduled && scheduled.getTime() < nowMs ? spec.past : spec.future;
    const created = scheduled ? new Date(scheduled.getTime() - 6 * 24 * 3600 * 1000) : at(-5, 11);
    const sent = status === S.SENT;
    return create(GtmItemSchema, {
      id: spec.id, orgId: DEMO_ORG, campaignId: spec.campaignId ?? "", ideaId: spec.ideaId ?? "", format: spec.format, channelId: spec.channelId ?? "",
      title: spec.title, bodyMd: spec.bodyMd, status, scheduledAt: scheduled ? ts(scheduled) : undefined,
      emailTo: spec.email?.to ?? [], emailSubject: spec.email?.subject ?? "",
      publishedUrl: status === S.PUBLISHED ? `https://${channelKey(spec.channelId)}.example/brightwell/${spec.id.replace("gtm-item-", "")}` : "",
      ...(sent && scheduled ? { gmailMessageId: `sample-gmail-${spec.id}`, sentAt: ts(scheduled), sentBy: GTM_SAMPLE_PERSON, approvalHash: "sample-approval-hash" } : {}),
      ...(spec.held ? { approvalHash: "sample-held-approval-hash" } : {}),
      revision: 1, createdAt: ts(created), updatedAt: ts(spec.held ? at(-1, 8) : created),
    });
  });

  // ── Assets (READY: one on an item, one on a campaign) ─────────────────────
  const assets: GtmAsset[] = [
    create(GtmAssetSchema, {
      id: "gtm-asset-hn-hero", orgId: DEMO_ORG, itemId: "gtm-item-show-hn", filename: "grind-calculator-hero.png", contentType: "image/png", sizeBytes: 184_320n,
      sha256: "0".repeat(64), uploadedBy: "Chat: launch planning (sample)", source: GtmAssetSource.CHAT, createdAt: ts(at(-2, 13)), status: GtmAssetStatus.READY,
    }),
    create(GtmAssetSchema, {
      id: "gtm-asset-brand-sheet", orgId: DEMO_ORG, campaignId: GTM_CAMPAIGN_IDS.weeklyBox, filename: "weekly-box-brand-sheet.png", contentType: "image/png", sizeBytes: 96_512n,
      sha256: "1".repeat(64), uploadedBy: GTM_SAMPLE_PERSON, source: GtmAssetSource.PERSON, createdAt: ts(at(-13, 10)), status: GtmAssetStatus.READY,
    }),
  ];

  // ── The strategy library: PLACEHOLDERS, written for this pack ─────────────
  const entry = (fields: MessageInitShape<typeof GtmStrategyLibraryEntrySchema> & Pick<GtmStrategyLibraryEntry, "key" | "name">): GtmStrategyLibraryEntry =>
    create(GtmStrategyLibraryEntrySchema, { sourceRevision: "sample-rev-2", importedAt: ts(at(-1, 9)), importedBy: GTM_SAMPLE_PERSON, ...fields });
  const SC = GtmStrategyCategory;
  const library: GtmStrategyLibraryEntry[] = [
    entry({
      key: "sample-tasting-popup", category: SC.PARTNERSHIPS_DISTRIBUTION, depth: GtmLibraryDepth.PLAY, name: "Coffee tasting pop-up (placeholder)",
      summaryMd: "Placeholder entry: a Saturday tasting table at a partner café, three coffees and a sign-up card.",
      stage: "EARLY", cost: "LOW", difficulty: "MEDIUM", fits: ["B2C"], hours: 8, signalDays: 14, northStar: "Tasting-event attendees who subscribe", objective: "Local awareness",
      hypothesisMd: "People who taste a coffee in person subscribe more often than people who read about it (placeholder).", channelTypes: ["blog", "x", "reddit"], kpis: ["Attendees", "New subscribers"],
      steps: [create(GtmLibraryStepSchema, { who: "you", title: "Agree a Saturday with a partner café" }), create(GtmLibraryStepSchema, { who: "agent", title: "Draft the invitation and the sign-up card" })],
    }),
    entry({
      key: "sample-brew-cards", category: SC.PRODUCT_LED_GROWTH, depth: GtmLibraryDepth.PLAY, name: "Brewing card in every box (placeholder)",
      summaryMd: "Placeholder entry: a printed card per box with a starting grind, dose and time for each brew method.",
      stage: "GROWTH", cost: "MEDIUM", difficulty: "MEDIUM", fits: ["B2C", "B2B"], hours: 6, northStar: "Subscribers who reach a third box", channelTypes: ["blog", "youtube"], kpis: ["Third-box rate"],
    }),
    entry({
      key: "sample-cupping-call", category: SC.COMMUNITY_EVENTS, depth: GtmLibraryDepth.TACTIC, name: "Monthly cupping call (placeholder)",
      summaryMd: "Placeholder entry: an open video call each month where members taste the same coffee and compare notes.",
      stage: "EARLY", cost: "FREE", difficulty: "EASY", fits: ["B2C"], hours: 2, northStar: "Returning attendees", channelTypes: ["email", "linkedin"], kpis: ["Attendees"],
    }),
    entry({
      key: "sample-faq-pages", category: SC.ORGANIC_CONTENT_SEO, depth: GtmLibraryDepth.TACTIC, name: "Answer pages for common questions (placeholder)",
      summaryMd: "Placeholder entry: one short page per question support hears every week, linked from the help page.",
      stage: "PRE_LAUNCH", cost: "FREE", difficulty: "EASY", fits: ["B2B", "B2C"], hours: 3, northStar: "Visits from search to answer pages", channelTypes: ["blog"], kpis: ["Organic visits"],
    }),
    entry({
      key: "sample-annual-nudge", category: SC.PRICING_OFFERS_INCENTIVES, depth: GtmLibraryDepth.TACTIC, name: "Annual plan reminder (placeholder)",
      summaryMd: "Placeholder entry: a single reminder of the annual option after a subscriber's third month.",
      stage: "GROWTH", cost: "FREE", difficulty: "EASY", fits: ["B2C", "MARKETPLACE"], hours: 1, northStar: "Subscribers on an annual plan", channelTypes: ["email"], kpis: ["Annual conversions"],
    }),
    entry({
      key: "sample-quote-wall", category: SC.TRUST_REPUTATION, depth: GtmLibraryDepth.TACTIC, name: "Customer quote wall (placeholder)",
      summaryMd: "Placeholder entry: short quotes from real users, with their permission, on the pricing page.",
      stage: "EARLY", cost: "LOW", difficulty: "EASY", fits: ["B2B", "B2C"], hours: 2, northStar: "Pricing page conversion", channelTypes: ["blog", "linkedin"], kpis: ["Pricing page conversion"],
    }),
  ];

  // ── Library audit rows and a pending prune request ─────────────────────────
  const libraryImports: GtmLibraryImport[] = [
    create(GtmLibraryImportSchema, {
      id: "gtm-import-3", orgId: DEMO_ORG, actorUserId: GTM_SAMPLE_PERSON, credentialKind: GtmCredentialKind.TOKEN, sourceRevision: "sample-rev-2",
      created: 0, updated: 0, unchanged: 4, pruned: 0, pruneRequestId: GTM_PRUNE_REQUEST_ID, createdAt: ts(at(-1, 9)),
    }),
    create(GtmLibraryImportSchema, {
      id: "gtm-import-2", orgId: DEMO_ORG, actorUserId: GTM_SAMPLE_PERSON, credentialKind: GtmCredentialKind.TOKEN, sourceRevision: "sample-rev-1b",
      created: 0, updated: 2, unchanged: 4, pruned: 0, createdAt: ts(at(-4, 16)),
    }),
    create(GtmLibraryImportSchema, {
      id: "gtm-import-1", orgId: DEMO_ORG, actorUserId: GTM_SAMPLE_PERSON, credentialKind: GtmCredentialKind.TOKEN, sourceRevision: "sample-rev-1",
      created: 6, updated: 0, unchanged: 0, pruned: 0, createdAt: ts(at(-9, 11)),
    }),
  ];
  const pruneRequests: GtmLibraryPruneRequest[] = [
    create(GtmLibraryPruneRequestSchema, {
      id: GTM_PRUNE_REQUEST_ID, orgId: DEMO_ORG, requestedBy: GTM_SAMPLE_PERSON, credentialKind: GtmCredentialKind.TOKEN, sourceRevision: "sample-rev-2",
      removeKeys: ["sample-annual-nudge", "sample-quote-wall"], status: GtmPruneRequestStatus.PENDING, createdAt: ts(at(-1, 9)), expiresAt: ts(at(6, 9)),
    }),
  ];

  // ── Positioning ────────────────────────────────────────────────────────────
  const positioning = create(GtmPositioningSchema, {
    orgId: DEMO_ORG,
    positioningMd: [
      "Brightwell Coffee brings freshly roasted beans to your door on the day you choose, and lets you skip or pause without a phone call.",
      "",
      "For home brewers who care how their coffee tastes: the Weekly Box ships beans roasted to order, and the Tasting Club sends one small-lot origin a month with notes on what to look for.",
      "",
      "Unlike supermarket bags, every bag has a roast date you can read. Unlike a one-off order, a subscription gets better as we learn how you brew.",
      "",
      "(Sample positioning for a fictional company.)",
    ].join("\n"),
    audiences: ["Home brewers", "Office managers", "Gift buyers", "Café owners"],
    updatedBy: GTM_SAMPLE_PERSON, revision: 3, updatedAt: ts(at(-5, 10)),
  });

  // ── Claims ─────────────────────────────────────────────────────────────────
  const C = GtmClaimCategory;
  const SK = GtmClaimSourceKind;
  const claim = (fields: MessageInitShape<typeof GtmClaimSchema> & Pick<GtmClaim, "id" | "text" | "category" | "status">, created: Date): GtmClaim =>
    create(GtmClaimSchema, { orgId: DEMO_ORG, textNorm: fields.text.toLowerCase().replace(/\s+/g, " ").trim(), revision: 1, createdAt: ts(created), updatedAt: ts(created), ...fields });
  const verified = (daysAgo: number, hours = 10) => ({ verifiedAt: ts(at(-daysAgo, hours)), verifiedBy: GTM_SAMPLE_PERSON });

  const claims: GtmClaim[] = [
    claim({
      id: "gtm-claim-sipper-price", text: "The Sipper plan costs $19 per month, billed monthly.", category: C.PRICING, status: GtmClaimStatus.VERIFIED,
      sourceUrl: "https://example.com/sample/pricing", sourceKind: SK.PRICING_CATALOG, ...verified(12), notes: "Sample price from the sample catalog.",
    }, at(-30)),
    claim({
      id: "gtm-claim-free-shipping", text: "Shipping on every Weekly Box is free.", category: C.PRICING, status: GtmClaimStatus.VERIFIED,
      sourceUrl: "https://example.com/sample/shipping", sourceKind: SK.DOCS, ...verified(12),
    }, at(-30)),
    claim({
      id: "gtm-claim-roast-to-order", text: "Every bag is roasted to order and ships within 2 days.", category: C.TIMING, status: GtmClaimStatus.VERIFIED,
      sourceUrl: "https://example.com/sample/roasting", sourceKind: SK.DOCS, ...verified(12),
    }, at(-30)),
    claim({
      id: "gtm-claim-compostable", text: "The bags are compostable.", category: C.CAPABILITY, status: GtmClaimStatus.VERIFIED,
      sourceUrl: "https://example.com/sample/packaging", sourceKind: SK.CODE, ...verified(7, 11),
    }, at(-30)),
    claim({
      id: "gtm-claim-delivery-time", text: "Orders usually arrive within 4 days.", category: C.TIMING, status: GtmClaimStatus.VERIFIED,
      sourceUrl: "https://example.com/sample/delivery", sourceKind: SK.CODE, ...verified(60), expiresOrRecheckAt: ts(at(-10)),
      notes: "Measured on the sample delivery report; recheck was due ten days ago.",
    }, at(-70)),
    claim({
      id: "gtm-claim-pause", text: "A subscriber can pause a box from the app without contacting anyone.", category: C.CAPABILITY, status: GtmClaimStatus.UNVERIFIED,
      notes: "Proposed by an agent; needs a help-page link.",
    }, at(-6)),
    claim({
      id: "gtm-claim-decaf", text: "Other coffee subscriptions charge extra for decaf.", category: C.COMPARISON, status: GtmClaimStatus.UNVERIFIED,
      notes: "A claim about other companies needs a dated source of theirs before it can be verified.",
    }, at(-9)),
    claim({
      id: "gtm-claim-old-price", text: "The Sipper plan costs $15 per month.", category: C.PRICING, status: GtmClaimStatus.RETIRED,
      sourceUrl: "https://example.com/sample/pricing", sourceKind: SK.PRICING_CATALOG, notes: "Retired: replaced by the $19 claim.",
    }, at(-90)),
  ];

  return { strategies, channels, ideas, campaigns, items, assets, library, libraryImports, pruneRequests, positioning, claims };
}
