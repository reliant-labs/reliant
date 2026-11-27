// Copyright (c) 2026 Reliant Labs

/**
 * The GTM sample pack: a FICTIONAL company (Brightwell) and its two
 * made-up products (Sites, a static hosting service; Flows, scheduled
 * automations), as the GtmService's own messages. Every number, price, plan,
 * claim and source here is invented and says so; nothing in it describes a
 * real product, price or customer, because this repo is public.
 *
 * Time is relative to the `now` it is built with (the pack clock): the items
 * sit on fixed days of the CURRENT month, so the calendar is never empty, and
 * an item is in its past or future state by whether its day has gone by.
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
  developerLed: "gtm-str-developer-led",
  retention: "gtm-str-retention",
} as const;

export const GTM_CAMPAIGN_IDS = {
  sites: "gtm-cmp-sites-launch",
  flows: "gtm-cmp-flows-quarter",
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
  devForums: "gtm-ch-dev-forums",
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
    `<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="630" viewBox="0 0 1200 630"><defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#1e3a8a"/><stop offset="1" stop-color="#0ea5e9"/></linearGradient></defs><rect width="1200" height="630" fill="url(#g)"/><circle cx="980" cy="140" r="220" fill="#fff" fill-opacity="0.08"/><text x="80" y="300" font-family="system-ui, sans-serif" font-size="84" font-weight="700" fill="#fff">Brightwell Sites</text><text x="80" y="380" font-family="system-ui, sans-serif" font-size="40" fill="#e0f2fe">One command to a live site</text><text x="80" y="560" font-family="system-ui, sans-serif" font-size="26" fill="#bae6fd">Sample image, fictional company</text></svg>`,
  ),
  "gtm-asset-brand-sheet": svgUrl(
    `<svg xmlns="http://www.w3.org/2000/svg" width="800" height="600" viewBox="0 0 800 600"><rect width="800" height="600" fill="#f8fafc"/><rect x="60" y="80" width="200" height="200" rx="24" fill="#1e3a8a"/><rect x="300" y="80" width="200" height="200" rx="24" fill="#0ea5e9"/><rect x="540" y="80" width="200" height="200" rx="24" fill="#f59e0b"/><text x="60" y="380" font-family="system-ui, sans-serif" font-size="44" font-weight="700" fill="#0f172a">Sites launch brand sheet</text><text x="60" y="440" font-family="system-ui, sans-serif" font-size="26" fill="#475569">Colours, logo lockups, tone (sample)</text></svg>`,
  ),
};

/** A sample asset's inline image: what GetAssetDownload answers (no bucket in the preview). */
export const gtmAssetImageUrl = (assetId: string): string => ASSET_IMAGES[assetId] ?? ASSET_IMAGES["gtm-asset-brand-sheet"]!;

/** The day of the month an item with no real date falls on, when the month is short. */
const LAST_SAFE_DAY = 28;

export function gtmSampleData(nowMs: number): GtmSampleData {
  const now = new Date(nowMs);
  const at = (dayOffset: number, hours = 9, minutes = 0): Date => {
    const d = new Date(now.getFullYear(), now.getMonth(), now.getDate() + dayOffset);
    d.setHours(hours, minutes, 0, 0);
    return d;
  };
  const onDay = (day: number, hours: number): Date => new Date(now.getFullYear(), now.getMonth(), Math.min(day, LAST_SAFE_DAY), hours, 0, 0, 0);
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
      id: GTM_STRATEGY_IDS.developerLed, orgId: DEMO_ORG, name: "Developer-led growth this half",
      goalMd: "Win developers who try things from the terminal: a product that works in one command, and launches where they already read. Sites leads; Flows follows once Sites has a base to grow from.",
      targetMetric: "500 weekly active teams by the end of the half (sample target)",
      periodStart: dateOnly(-30), periodEnd: dateOnly(150), status: GtmStrategyStatus.ACTIVE, category: GtmStrategyCategory.LAUNCHES_PR,
      revision: 2, createdBy: GTM_SAMPLE_PERSON, updatedBy: GTM_SAMPLE_PERSON, createdAt: ts(at(-32)), updatedAt: ts(at(-30)),
    }),
    create(GtmStrategySchema, {
      id: GTM_STRATEGY_IDS.retention, orgId: DEMO_ORG, name: "Keep new teams coming back",
      goalMd: "Teams that deploy twice in their first month stay. A draft from the agent: a nudge after the first deploy, and a monthly digest of what shipped.",
      targetMetric: "40% of new teams deploy twice in 30 days (sample target)",
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
      id: CH.devForums, key: "dev-forums", name: "Developer forums", source: GtmChannelSource.AGENT, createdBy: "Chat: launch planning (sample)",
      manualInstructionsMd: "Answer in threads where someone asks how to host a static site. Post by hand, then paste the URL.", channelTypes: ["forum", "community"],
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
      id: GTM_CAMPAIGN_IDS.sites, orgId: DEMO_ORG, strategyId: GTM_STRATEGY_IDS.developerLed, name: "Sites launch",
      goal: "200 new sites deployed in the first 30 days (sample target)",
      periodStart: ts(at(-14)), periodEnd: ts(at(16)), status: GtmCampaignStatus.ACTIVE, revision: 2, createdAt: ts(at(-20)), updatedAt: ts(at(-14)),
      plannedChannelIds: [CH.blog, CH.hackerNews, CH.x, CH.linkedin, CH.reddit, CH.email], plannedFormats: [F.LONG_POST, F.SHORT_POST, F.EMAIL, F.VIDEO],
    }),
    create(GtmCampaignSchema, {
      id: GTM_CAMPAIGN_IDS.flows, orgId: DEMO_ORG, strategyId: GTM_STRATEGY_IDS.developerLed, name: "Flows: automations that run without you",
      goal: "50 teams with a scheduled flow by the end of the quarter (sample target)",
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
      id: "gtm-idea-no-machine-post", kind: GtmIdeaKind.CONTENT, status: GtmIdeaStatus.PROPOSED, ...agent, sourceChatId: "sample-chat-weekly-ideas",
      campaignId: GTM_CAMPAIGN_IDS.flows, title: "Explain machine-free flows in one post",
      bodyMd: [
        "The machine-free flows docs page gets a lot of visits but few visitors go on to create a flow. One plain post that builds a single daily digest could close that gap.",
        "",
        "# Your flow doesn't need a computer to check your inbox",
        "",
        "Most automations people ask for are small: read something, decide, post a summary. They don't need a sandbox.",
        "",
        "## Build one in five minutes",
        "",
        "1. Create a flow with a schedule of every weekday at 08:00.",
        "2. Add a mail search step and a chat post step.",
        "3. Run it once and check the post.",
        "",
        "TODO (agent): add the config once the claims list has a verified line for each step.",
      ].join("\n"),
      evidence: [
        evidence("Docs page views, machine-free flows, last 7 days (sample)", "**1,240** views", at(-2, 7)),
        evidence("Visitors who then created a flow (sample)", "**4%**", at(-2, 7)),
        evidence("Median time on page (sample)", "3m 10s", at(-2, 7)),
      ],
    }, at(-2, 7, 2)),
    idea({
      id: "gtm-idea-sites-launch-post", kind: GtmIdeaKind.GTM, status: GtmIdeaStatus.ACCEPTED, ...human, campaignId: GTM_CAMPAIGN_IDS.sites, revision: 2,
      title: "Launch post: free static hosting for every project",
      bodyMd: [
        "Hosting with a CDN and custom domains is live, and one site per organization is free. That is the clearest reason yet to try Brightwell, so it should lead the launch.",
        "",
        "# Free static hosting for every Brightwell project",
        "",
        "Push a static export and one deploy command puts it on a CDN with your domain.",
        "",
        "One static site per organization is free.",
      ].join("\n"),
      evidence: [
        evidence("Sites created in the beta (sample)", "**312**", at(-18, 11)),
        evidence("Beta users who added a custom domain (sample)", "**41%**", at(-18, 11)),
      ],
    }, at(-18, 11), at(-15, 16)),
    idea({
      id: "gtm-idea-activity-email", kind: GtmIdeaKind.GTM, status: GtmIdeaStatus.PROPOSED, ...agent, sourceChatId: "sample-chat-analytics-review",
      campaignId: GTM_CAMPAIGN_IDS.flows, title: "Email workspace users about the activity feed",
      bodyMd: [
        "Many approval requests wait more than a day for an answer. Users may not know the activity feed collects everything that is waiting on them.",
        "",
        "Subject: Everything waiting on you, in one place",
        "",
        "Hi {first_name},",
        "",
        "When a flow needs your approval or an answer, it now shows up in your activity feed. You will never miss an approval again.",
      ].join("\n"),
      evidence: [
        evidence("Approval requests answered after more than 24h (sample)", "**38%**", at(-1, 15)),
        evidence("Weekly users who opened the activity feed (sample)", "**22%**", at(-1, 15)),
      ],
    }, at(-1, 15, 40)),
    idea({
      id: "gtm-idea-tour-approval", kind: GtmIdeaKind.PRODUCT, status: GtmIdeaStatus.PROPOSED, ...agent, sourceChatId: "sample-chat-weekly-ideas",
      title: "Show an approval request in the first-run tour",
      bodyMd: "New users first meet an approval request mid-task, which reads like an error. A tour step with a sample request, Approve and Deny, and one sentence on why flows ask first would set the expectation.",
      evidence: [evidence("First-week runs that ended at an approval request (sample)", "**17%**", at(-2, 7))],
    }, at(-2, 7, 2)),
    idea({
      id: "gtm-idea-vs-cron", kind: GtmIdeaKind.CONTENT, status: GtmIdeaStatus.REJECTED, ...human, revision: 2,
      title: "Comparison page: Flows vs. CI cron jobs",
      bodyMd: "Outline: what a CI cron gives you, what a flow with approvals adds, and when each fits.\n\nRejected: the claims about other tools had no sources we could verify.",
      evidence: [],
    }, at(-20, 10), at(-12, 9)),
    idea({
      id: "gtm-idea-case-study", kind: GtmIdeaKind.CONTENT, status: GtmIdeaStatus.DONE, ...agent, sourceChatId: "sample-chat-launch-planning", campaignId: GTM_CAMPAIGN_IDS.sites, revision: 3,
      title: "Case study: our own site runs on Brightwell",
      bodyMd: [
        "We run our own marketing site on Brightwell. A first-party case study is easy to source and hard to argue with.",
        "",
        "# How we run our own site on Brightwell",
        "",
        "Our marketing site, its API and its scheduled jobs are one project.",
        "",
        "Releases are built once and promoted by digest from staging to prod.",
      ].join("\n"),
      evidence: [evidence("Deploys to our own site last month (sample)", "**64**", at(-16, 14))],
    }, at(-16, 14), at(-4, 12)),
    idea({
      id: "gtm-idea-show-hn", kind: GtmIdeaKind.GTM, status: GtmIdeaStatus.ACCEPTED, ...human, campaignId: GTM_CAMPAIGN_IDS.sites, revision: 2,
      title: "Show HN: deploy a static site with one command",
      bodyMd: [
        "A short, technical Show HN fits the audience that already tries Brightwell from the command line.",
        "",
        "Show HN: Deploy a static site with one command (free for one site per organization)",
        "",
        "We built Brightwell to take a repo to prod with environments, releases and a CDN. Static sites are the simplest case, so we made them free.",
      ].join("\n"),
      evidence: [evidence("Signups from the last HN launch (sample)", "**1 in 5**", at(-10, 9), "note")],
    }, at(-10, 9), at(-6, 17)),
    idea({
      id: "gtm-idea-pricing-faq", kind: GtmIdeaKind.CONTENT, status: GtmIdeaStatus.PROPOSED, ...agent, sourceChatId: "sample-chat-analytics-review",
      title: "A plain-language pricing FAQ",
      bodyMd: "Pricing-page visitors ask the same four questions in support. A short FAQ (what Starter costs, what is free, what changes at the limit) would answer them before they write in.",
      evidence: [
        evidence("Support threads about pricing, last 30 days (sample)", "**46**", at(-3, 8)),
        evidence("Pricing page exits without a click (sample)", "**61%**", at(-3, 8)),
      ],
    }, at(-3, 8, 15)),
    idea({
      id: "gtm-idea-live-demo", kind: GtmIdeaKind.GTM, status: GtmIdeaStatus.PROPOSED, ...human, campaignId: GTM_CAMPAIGN_IDS.flows,
      title: "Live demo: build your first scheduled flow",
      bodyMd: "A 30-minute live session where we build a daily digest from nothing, take questions, and share the config afterwards.",
      evidence: [],
    }, at(-4, 13)),
    idea({
      id: "gtm-idea-checklist", kind: GtmIdeaKind.PRODUCT, status: GtmIdeaStatus.ACCEPTED, ...human, revision: 2,
      title: "Onboarding checklist on the dashboard",
      bodyMd: "Three steps (connect a repo, deploy a site, schedule a flow) with a tick as each is done.",
      evidence: [evidence("New orgs that deploy within 7 days (sample)", "**34%**", at(-9, 10))],
    }, at(-12, 10), at(-9, 10)),
    idea({
      id: "gtm-idea-referral", kind: GtmIdeaKind.GTM, status: GtmIdeaStatus.REJECTED, ...agent, sourceChatId: "sample-chat-weekly-ideas", revision: 2,
      title: "Offer referral credits",
      bodyMd: "Give both sides a month of Starter when a referral signs up.\n\nRejected: credits need a finance review first.",
      evidence: [],
    }, at(-26, 9), at(-24, 11)),
    idea({
      id: "gtm-idea-changelog-digest", kind: GtmIdeaKind.CONTENT, status: GtmIdeaStatus.DONE, ...agent, sourceChatId: "sample-chat-launch-planning", revision: 3,
      title: "Monthly changelog digest",
      bodyMd: "Roll the month's changelog entries into one email with a line on why each matters.",
      evidence: [evidence("Changelog page views per month (sample)", "**2,900**", at(-30, 9))],
    }, at(-60, 9), at(-32, 9)),
  ];

  // ── Items: fixed days of the current month ────────────────────────────────
  const S = GtmItemStatus;
  interface ItemSpec {
    id: string;
    day: number; // 0: not on the calendar
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
      id: "gtm-item-launch-blog", day: 2, hours: 10, format: F.LONG_POST, channelId: CH.blog, title: "Free static hosting for every Brightwell project",
      bodyMd: "# Free static hosting for every Brightwell project\n\nPush a static export and one deploy command puts it on a CDN with your domain.\n\nOne static site per organization is free.",
      campaignId: GTM_CAMPAIGN_IDS.sites, ideaId: "gtm-idea-sites-launch-post", past: S.PUBLISHED, future: S.APPROVED,
    },
    {
      id: "gtm-item-launch-thread", day: 4, hours: 16, format: F.SHORT_POST, channelId: CH.x, title: "Thread: what a one-command deploy looks like",
      bodyMd: "1/ You can now host a static site on Brightwell for free (one per organization).\n2/ One deploy command and it is on a CDN.",
      campaignId: GTM_CAMPAIGN_IDS.sites, ideaId: "gtm-idea-sites-launch-post", past: S.PUBLISHED, future: S.SCHEDULED,
    },
    {
      id: "gtm-item-launch-email", day: 6, hours: 14, format: F.EMAIL, channelId: CH.email, title: "Your site can be live today",
      bodyMd: "Hi {first_name},\n\nStatic hosting is live, with a CDN and custom domains. One site per organization is free.",
      campaignId: GTM_CAMPAIGN_IDS.sites, ideaId: "gtm-idea-sites-launch-post", past: S.SENT, future: S.APPROVED,
      email: { to: ["site-owners@lists.example.com"], subject: "Your site can be live today" },
    },
    {
      id: "gtm-item-launch-linkedin", day: 8, hours: 11, format: F.SHORT_POST, channelId: CH.linkedin, title: "Static hosting is live",
      bodyMd: "Static hosting is live on Brightwell: a CDN, custom domains, and one free site per organization.",
      campaignId: GTM_CAMPAIGN_IDS.sites, past: S.PUBLISHED, future: S.SCHEDULED,
    },
    {
      // A manual channel: approved, posted by hand, and waiting for the person to paste the URL and mark it published.
      id: "gtm-item-show-hn", day: 10, hours: 16, format: F.SHORT_POST, channelId: CH.hackerNews, title: "Show HN: deploy a static site with one command",
      bodyMd: "Show HN: Deploy a static site with one command (free for one site per organization)\n\nWe built Brightwell to take a repo to prod with environments, releases and a CDN. Static sites are the simplest case, so we made them free.",
      campaignId: GTM_CAMPAIGN_IDS.sites, ideaId: "gtm-idea-show-hn", past: S.APPROVED, future: S.APPROVED,
    },
    {
      id: "gtm-item-case-study", day: 12, hours: 9, format: F.LONG_POST, channelId: CH.blog, title: "Case study: our own site runs on Brightwell",
      bodyMd: "# How we run our own site on Brightwell\n\nOur marketing site, its API and its scheduled jobs are one project.\n\nReleases are built once and promoted by digest from staging to prod.\n\nEvery deploy finishes in under a minute.",
      campaignId: GTM_CAMPAIGN_IDS.sites, ideaId: "gtm-idea-case-study", past: S.PAUSED, future: S.REVIEW,
    },
    {
      // Held: the send started and its outcome is unknown. Frozen until a person resolves it.
      id: "gtm-item-domains-email", day: 14, hours: 15, format: F.EMAIL, channelId: CH.email, title: "Follow-up: custom domains on static sites",
      bodyMd: "Hi {first_name},\n\nYour static site can use your own domain. Add it in the dashboard and we handle the certificate.",
      campaignId: GTM_CAMPAIGN_IDS.sites, past: S.APPROVED, future: S.APPROVED, held: true,
      email: { to: ["no-domain-yet@lists.example.com"], subject: "Put your site on your own domain" },
    },
    {
      id: "gtm-item-no-machine-post", day: 16, hours: 9, format: F.LONG_POST, channelId: CH.blog, title: "Machine-free flows, explained with one daily digest",
      bodyMd: "# Your flow doesn't need a computer to check your inbox\n\nA flow that only uses web APIs runs without a dedicated machine. It wakes on its schedule, does the work, and stops.",
      campaignId: GTM_CAMPAIGN_IDS.flows, ideaId: "gtm-idea-no-machine-post", past: S.PUBLISHED, future: S.DRAFT,
    },
    {
      id: "gtm-item-activity-email", day: 18, hours: 14, format: F.EMAIL, channelId: CH.email, title: "Meet the activity feed: everything waiting on you",
      bodyMd: "Hi {first_name},\n\nWhen a flow needs your approval or an answer, it now shows up in your activity feed. You will never miss an approval again.",
      campaignId: GTM_CAMPAIGN_IDS.flows, ideaId: "gtm-idea-activity-email", past: S.SENT, future: S.DRAFT,
      email: { to: ["weekly-active@lists.example.com"], subject: "Everything waiting on you, in one place" },
    },
    {
      id: "gtm-item-triage-post", day: 20, hours: 12, format: F.SHORT_POST, channelId: CH.x, title: "A flow that triages your inbox every morning",
      bodyMd: "A Brightwell flow can read your mail at 08:00, sort it, and post the summary to chat. No dedicated machine needed.",
      campaignId: GTM_CAMPAIGN_IDS.flows, past: S.PUBLISHED, future: S.REVIEW,
    },
    {
      // Cold outreach on an integrated channel whose Gmail connection is missing: it cannot be sent until a person connects it.
      id: "gtm-item-outreach", day: 21, hours: 10, format: F.COLD_OUTREACH, channelId: CH.outreach, title: "Intro to agencies that host client sites",
      bodyMd: "Hi {first_name},\n\nWe help agencies put client sites on a CDN with one command. Would a short call be useful?",
      campaignId: GTM_CAMPAIGN_IDS.sites, past: S.DRAFT, future: S.DRAFT,
      email: { to: ["agency-leads@lists.example.com"], subject: "Hosting client sites with one command" },
    },
    {
      id: "gtm-item-changelog", day: 22, hours: 10, format: F.OTHER, channelId: CH.blog, title: "Changelog: scheduled flows on every plan",
      bodyMd: "Scheduled flows now run on every plan. Flows that only use web APIs need no dedicated machine.",
      campaignId: GTM_CAMPAIGN_IDS.flows, past: S.PUBLISHED, future: S.DRAFT,
    },
    {
      id: "gtm-item-webinar", day: 24, hours: 17, format: F.VIDEO, channelId: CH.youtube, title: "Live demo: your first scheduled flow",
      bodyMd: "A 30-minute live session: build a daily digest from nothing, then questions.",
      campaignId: GTM_CAMPAIGN_IDS.flows, ideaId: "gtm-idea-live-demo", past: S.PAUSED, future: S.DRAFT,
    },
    {
      id: "gtm-item-customer-quote", day: 25, hours: 11, format: F.SHORT_POST, channelId: CH.linkedin, title: "Customer quote: from cron to flows",
      bodyMd: "\"We replaced three cron jobs with one flow and an approval step.\" (sample quote, not a real customer)",
      campaignId: GTM_CAMPAIGN_IDS.flows, past: S.PUBLISHED, future: S.DRAFT,
    },
    {
      id: "gtm-item-pricing-explained", day: 26, hours: 9, format: F.LONG_POST, channelId: CH.blog, title: "Pricing explained: what Starter costs",
      bodyMd: "# Pricing explained\n\nStarter is $19 per month, billed monthly. It used to be $15 per month, but that price is retired.\n\nOne static site per organization is free.",
      ideaId: "gtm-idea-pricing-faq", past: S.PAUSED, future: S.REVIEW,
    },
    {
      id: "gtm-item-seo-page", day: 0, hours: 9, format: F.LONG_POST, channelId: CH.blog, title: "Backlog: a page for \"static hosting with a CDN\"",
      bodyMd: "Not scheduled yet. Outline: what a CDN does for a static site, and how to add one.",
      past: S.DRAFT, future: S.DRAFT,
    },
  ];

  const channelKey = (id?: string) => channels.find((c) => c.id === id)?.key ?? "site";
  const items: GtmItem[] = specs.map((spec) => {
    const scheduled = spec.day > 0 ? onDay(spec.day, spec.hours) : null;
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
      id: "gtm-asset-hn-hero", orgId: DEMO_ORG, itemId: "gtm-item-show-hn", filename: "sites-launch-hero.png", contentType: "image/png", sizeBytes: 184_320n,
      sha256: "0".repeat(64), uploadedBy: "Chat: launch planning (sample)", source: GtmAssetSource.CHAT, createdAt: ts(at(-2, 13)), status: GtmAssetStatus.READY,
    }),
    create(GtmAssetSchema, {
      id: "gtm-asset-brand-sheet", orgId: DEMO_ORG, campaignId: GTM_CAMPAIGN_IDS.sites, filename: "sites-brand-sheet.png", contentType: "image/png", sizeBytes: 96_512n,
      sha256: "1".repeat(64), uploadedBy: GTM_SAMPLE_PERSON, source: GtmAssetSource.PERSON, createdAt: ts(at(-13, 10)), status: GtmAssetStatus.READY,
    }),
  ];

  // ── The strategy library: PLACEHOLDERS, written for this pack ─────────────
  const entry = (fields: MessageInitShape<typeof GtmStrategyLibraryEntrySchema> & Pick<GtmStrategyLibraryEntry, "key" | "name">): GtmStrategyLibraryEntry =>
    create(GtmStrategyLibraryEntrySchema, { sourceRevision: "sample-rev-2", importedAt: ts(at(-1, 9)), importedBy: GTM_SAMPLE_PERSON, ...fields });
  const SC = GtmStrategyCategory;
  const library: GtmStrategyLibraryEntry[] = [
    entry({
      key: "sample-launch-week", category: SC.LAUNCHES_PR, depth: GtmLibraryDepth.PLAY, name: "Launch week (placeholder)",
      summaryMd: "Placeholder entry: ship one small thing a day for five days, each with its own post, then a recap.",
      stage: "EARLY", cost: "LOW", difficulty: "MEDIUM", fits: ["DEVELOPER_TOOLS", "B2B"], hours: 10, signalDays: 7, northStar: "Signups during launch week", objective: "Awareness",
      hypothesisMd: "A week of small launches draws more attention than one big one (placeholder).", channelTypes: ["blog", "x", "hacker-news"], kpis: ["Signups", "Post views"],
      steps: [create(GtmLibraryStepSchema, { who: "you", title: "Pick five small things to ship" }), create(GtmLibraryStepSchema, { who: "agent", title: "Draft a post for each day" })],
    }),
    entry({
      key: "sample-template-gallery", category: SC.PRODUCT_LED_GROWTH, depth: GtmLibraryDepth.PLAY, name: "Starter template gallery (placeholder)",
      summaryMd: "Placeholder entry: a page of ready-to-deploy starters, each one click from a running copy.",
      stage: "GROWTH", cost: "MEDIUM", difficulty: "MEDIUM", fits: ["DEVELOPER_TOOLS", "OPEN_SOURCE"], hours: 6, northStar: "Projects started from a template", channelTypes: ["blog", "youtube"], kpis: ["Projects started"],
    }),
    entry({
      key: "sample-office-hours", category: SC.COMMUNITY_EVENTS, depth: GtmLibraryDepth.TACTIC, name: "Monthly office hours (placeholder)",
      summaryMd: "Placeholder entry: an open call each month where users bring a problem and leave with a fix.",
      stage: "EARLY", cost: "FREE", difficulty: "EASY", fits: ["B2B", "DEVELOPER_TOOLS"], hours: 2, northStar: "Returning attendees", channelTypes: ["email", "linkedin"], kpis: ["Attendees"],
    }),
    entry({
      key: "sample-faq-pages", category: SC.ORGANIC_CONTENT_SEO, depth: GtmLibraryDepth.TACTIC, name: "Answer pages for common questions (placeholder)",
      summaryMd: "Placeholder entry: one short page per question support hears every week, linked from the docs.",
      stage: "PRE_LAUNCH", cost: "FREE", difficulty: "EASY", fits: ["B2B", "B2C"], hours: 3, northStar: "Visits from search to answer pages", channelTypes: ["blog"], kpis: ["Organic visits"],
    }),
    entry({
      key: "sample-annual-nudge", category: SC.PRICING_OFFERS_INCENTIVES, depth: GtmLibraryDepth.TACTIC, name: "Annual plan reminder (placeholder)",
      summaryMd: "Placeholder entry: a single reminder of the annual option after a team's third month.",
      stage: "GROWTH", cost: "FREE", difficulty: "EASY", fits: ["B2B", "MARKETPLACE"], hours: 1, northStar: "Teams on an annual plan", channelTypes: ["email"], kpis: ["Annual conversions"],
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
      "Brightwell is where small teams ship and run their software, with a person in the loop for every action that matters.",
      "",
      "For founders and small teams who ship alone or nearly alone: Sites puts a project on a CDN from the first commit, and Flows keeps it running on a schedule. Anything that sends, publishes or spends waits for your approval.",
      "",
      "Unlike a chat assistant, Flows runs the work end to end. Unlike CI cron jobs, its runs can pause, ask, and resume.",
      "",
      "(Sample positioning for a fictional company.)",
    ].join("\n"),
    audiences: ["Solo founders", "Small product teams", "Platform engineers", "Agencies"],
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
      id: "gtm-claim-starter-price", text: "Starter costs $19 per month, billed monthly.", category: C.PRICING, status: GtmClaimStatus.VERIFIED,
      sourceUrl: "https://example.com/sample/pricing", sourceKind: SK.PRICING_CATALOG, ...verified(12), notes: "Sample price from the sample catalog.",
    }, at(-30)),
    claim({
      id: "gtm-claim-free-site", text: "One static site per organization is free.", category: C.LIMIT, status: GtmClaimStatus.VERIFIED,
      sourceUrl: "https://example.com/sample/docs/hosting", sourceKind: SK.DOCS, ...verified(12),
    }, at(-30)),
    claim({
      id: "gtm-claim-machine-free", text: "A flow that only uses web APIs runs without a dedicated machine.", category: C.CAPABILITY, status: GtmClaimStatus.VERIFIED,
      sourceUrl: "https://example.com/sample/docs/machine-free-flows", sourceKind: SK.DOCS, ...verified(12),
    }, at(-30)),
    claim({
      id: "gtm-claim-digest", text: "Releases are built once and promoted by digest from staging to prod.", category: C.CAPABILITY, status: GtmClaimStatus.VERIFIED,
      sourceUrl: "https://example.com/sample/docs/releases", sourceKind: SK.CODE, ...verified(7, 11),
    }, at(-30)),
    claim({
      id: "gtm-claim-deploy-time", text: "A typical static deploy finishes in under 90 seconds.", category: C.TIMING, status: GtmClaimStatus.VERIFIED,
      sourceUrl: "https://example.com/sample/benchmarks", sourceKind: SK.CODE, ...verified(60), expiresOrRecheckAt: ts(at(-10)),
      notes: "Measured on the sample benchmark; recheck was due ten days ago.",
    }, at(-70)),
    claim({
      id: "gtm-claim-credentials", text: "An integration step uses a saved credential the flow never sees.", category: C.CAPABILITY, status: GtmClaimStatus.UNVERIFIED,
      notes: "Proposed by an agent; needs a docs link.",
    }, at(-6)),
    claim({
      id: "gtm-claim-per-seat", text: "Other schedulers charge per seat for scheduled jobs.", category: C.COMPARISON, status: GtmClaimStatus.UNVERIFIED,
      notes: "A claim about other companies needs a dated source of theirs before it can be verified.",
    }, at(-9)),
    claim({
      id: "gtm-claim-old-price", text: "Starter costs $15 per month.", category: C.PRICING, status: GtmClaimStatus.RETIRED,
      sourceUrl: "https://example.com/sample/pricing", sourceKind: SK.PRICING_CATALOG, notes: "Retired: replaced by the $19 claim.",
    }, at(-90)),
  ];

  return { strategies, channels, ideas, campaigns, items, assets, library, libraryImports, pruneRequests, positioning, claims };
}
