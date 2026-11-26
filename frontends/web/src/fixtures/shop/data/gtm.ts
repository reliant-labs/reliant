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
 */

import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate, type Timestamp } from "@bufbuild/protobuf/wkt";

import {
  GtmCampaignSchema,
  GtmCampaignStatus,
  GtmClaimCategory,
  GtmClaimSchema,
  GtmClaimSourceKind,
  GtmClaimStatus,
  GtmEvidenceSchema,
  GtmIdeaKind,
  GtmIdeaSchema,
  GtmIdeaSource,
  GtmIdeaStatus,
  GtmItemKind,
  GtmItemSchema,
  GtmItemStatus,
  GtmPositioningSchema,
  type GtmCampaign,
  type GtmClaim,
  type GtmEvidence,
  type GtmIdea,
  type GtmItem,
  type GtmPositioning,
} from "@/gen/reliant/v1/gtm_pb";

import { DEMO_ORG } from "../shopStory";
import { APP_USER } from "../user";

/** The person every "by a person" field names (a display name, so the pages read well). */
export const GTM_SAMPLE_PERSON = APP_USER.name;

export const GTM_CAMPAIGN_IDS = {
  sites: "gtm-cmp-sites-launch",
  flows: "gtm-cmp-flows-quarter",
  newsletter: "gtm-cmp-newsletter",
} as const;

export interface GtmSampleData {
  ideas: GtmIdea[];
  campaigns: GtmCampaign[];
  items: GtmItem[];
  positioning: GtmPositioning;
  claims: GtmClaim[];
}

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

  const evidence = (title: string, summaryMd: string, observed: Date, kind = "analytics"): GtmEvidence =>
    create(GtmEvidenceSchema, { kind, title, summaryMd, observedAt: ts(observed) });

  // ── Campaigns ──────────────────────────────────────────────────────────────
  const campaigns: GtmCampaign[] = [
    create(GtmCampaignSchema, {
      id: GTM_CAMPAIGN_IDS.sites, orgId: DEMO_ORG, name: "Sites launch",
      goal: "200 new sites deployed in the first 30 days (sample target)",
      periodStart: ts(at(-14)), periodEnd: ts(at(16)), status: GtmCampaignStatus.ACTIVE, revision: 2, createdAt: ts(at(-20)), updatedAt: ts(at(-14)),
    }),
    create(GtmCampaignSchema, {
      id: GTM_CAMPAIGN_IDS.flows, orgId: DEMO_ORG, name: "Flows: automations that run without you",
      goal: "50 orgs with a scheduled flow by the end of the quarter (sample target)",
      periodStart: ts(at(3)), periodEnd: ts(at(40)), status: GtmCampaignStatus.DRAFT, revision: 1, createdAt: ts(at(-8)), updatedAt: ts(at(-8)),
    }),
    create(GtmCampaignSchema, {
      id: GTM_CAMPAIGN_IDS.newsletter, orgId: DEMO_ORG, name: "Spring developer newsletter",
      goal: "A newsletter every two weeks, 30% open rate (sample target)",
      periodStart: ts(at(-75)), periodEnd: ts(at(-20)), status: GtmCampaignStatus.DONE, revision: 4, createdAt: ts(at(-80)), updatedAt: ts(at(-20)),
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
      id: "gtm-idea-changelog-digest", kind: GtmIdeaKind.CONTENT, status: GtmIdeaStatus.DONE, ...agent, sourceChatId: "sample-chat-launch-planning", campaignId: GTM_CAMPAIGN_IDS.newsletter, revision: 3,
      title: "Monthly changelog digest",
      bodyMd: "Roll the month's changelog entries into one email with a line on why each matters.",
      evidence: [evidence("Changelog page views per month (sample)", "**2,900**", at(-30, 9))],
    }, at(-60, 9), at(-32, 9)),
  ];

  // ── Items: fixed days of the current month ────────────────────────────────
  interface ItemSpec {
    id: string;
    day: number; // 0: not on the calendar
    hours: number;
    kind: GtmItemKind;
    channel: string;
    title: string;
    bodyMd: string;
    campaignId?: string;
    ideaId?: string;
    /** The status once its day has gone by, and before. */
    past: GtmItemStatus;
    future: GtmItemStatus;
    email?: { to: string[]; subject: string };
  }
  const S = GtmItemStatus;
  const K = GtmItemKind;
  const specs: ItemSpec[] = [
    {
      id: "gtm-item-launch-blog", day: 2, hours: 10, kind: K.BLOG_POST, channel: "blog", title: "Free static hosting for every Brightwell project",
      bodyMd: "# Free static hosting for every Brightwell project\n\nPush a static export and one deploy command puts it on a CDN with your domain.\n\nOne static site per organization is free.",
      campaignId: GTM_CAMPAIGN_IDS.sites, ideaId: "gtm-idea-sites-launch-post", past: S.PUBLISHED, future: S.APPROVED,
    },
    {
      id: "gtm-item-launch-thread", day: 4, hours: 16, kind: K.SOCIAL_POST, channel: "x", title: "Thread: what a one-command deploy looks like",
      bodyMd: "1/ You can now host a static site on Brightwell for free (one per organization).\n2/ One deploy command and it is on a CDN.",
      campaignId: GTM_CAMPAIGN_IDS.sites, ideaId: "gtm-idea-sites-launch-post", past: S.PUBLISHED, future: S.SCHEDULED,
    },
    {
      id: "gtm-item-launch-email", day: 6, hours: 14, kind: K.EMAIL, channel: "email", title: "Your site can be live today",
      bodyMd: "Hi {first_name},\n\nStatic hosting is live, with a CDN and custom domains. One site per organization is free.",
      campaignId: GTM_CAMPAIGN_IDS.sites, ideaId: "gtm-idea-sites-launch-post", past: S.SENT, future: S.APPROVED,
      email: { to: ["site-owners@lists.example.com"], subject: "Your site can be live today" },
    },
    {
      id: "gtm-item-launch-linkedin", day: 8, hours: 11, kind: K.SOCIAL_POST, channel: "linkedin", title: "Static hosting is live",
      bodyMd: "Static hosting is live on Brightwell: a CDN, custom domains, and one free site per organization.",
      campaignId: GTM_CAMPAIGN_IDS.sites, past: S.PUBLISHED, future: S.SCHEDULED,
    },
    {
      id: "gtm-item-show-hn", day: 10, hours: 16, kind: K.LAUNCH, channel: "hn", title: "Show HN: deploy a static site with one command",
      bodyMd: "Show HN: Deploy a static site with one command (free for one site per organization)\n\nWe built Brightwell to take a repo to prod with environments, releases and a CDN. Static sites are the simplest case, so we made them free.",
      campaignId: GTM_CAMPAIGN_IDS.sites, ideaId: "gtm-idea-show-hn", past: S.PUBLISHED, future: S.APPROVED,
    },
    {
      id: "gtm-item-case-study", day: 12, hours: 9, kind: K.BLOG_POST, channel: "blog", title: "Case study: our own site runs on Brightwell",
      bodyMd: "# How we run our own site on Brightwell\n\nOur marketing site, its API and its scheduled jobs are one project.\n\nReleases are built once and promoted by digest from staging to prod.\n\nEvery deploy finishes in under a minute.",
      campaignId: GTM_CAMPAIGN_IDS.sites, ideaId: "gtm-idea-case-study", past: S.PAUSED, future: S.REVIEW,
    },
    {
      id: "gtm-item-domains-email", day: 14, hours: 15, kind: K.EMAIL, channel: "email", title: "Follow-up: custom domains on static sites",
      bodyMd: "Hi {first_name},\n\nYour static site can use your own domain. Add it in the dashboard and we handle the certificate.",
      campaignId: GTM_CAMPAIGN_IDS.sites, past: S.SENT, future: S.DRAFT,
      email: { to: ["no-domain-yet@lists.example.com"], subject: "Put your site on your own domain" },
    },
    {
      id: "gtm-item-no-machine-post", day: 16, hours: 9, kind: K.BLOG_POST, channel: "blog", title: "Machine-free flows, explained with one daily digest",
      bodyMd: "# Your flow doesn't need a computer to check your inbox\n\nA flow that only uses web APIs runs without a dedicated machine. It wakes on its schedule, does the work, and stops.",
      campaignId: GTM_CAMPAIGN_IDS.flows, ideaId: "gtm-idea-no-machine-post", past: S.PUBLISHED, future: S.DRAFT,
    },
    {
      id: "gtm-item-activity-email", day: 18, hours: 14, kind: K.EMAIL, channel: "email", title: "Meet the activity feed: everything waiting on you",
      bodyMd: "Hi {first_name},\n\nWhen a flow needs your approval or an answer, it now shows up in your activity feed. You will never miss an approval again.",
      campaignId: GTM_CAMPAIGN_IDS.flows, ideaId: "gtm-idea-activity-email", past: S.SENT, future: S.DRAFT,
      email: { to: ["weekly-active@lists.example.com"], subject: "Everything waiting on you, in one place" },
    },
    {
      id: "gtm-item-triage-post", day: 20, hours: 12, kind: K.SOCIAL_POST, channel: "x", title: "A flow that triages your inbox every morning",
      bodyMd: "A Brightwell flow can read your mail at 08:00, sort it, and post the summary to chat. No dedicated machine needed.",
      campaignId: GTM_CAMPAIGN_IDS.flows, past: S.PUBLISHED, future: S.REVIEW,
    },
    {
      id: "gtm-item-changelog", day: 22, hours: 10, kind: K.LAUNCH, channel: "changelog", title: "Changelog: scheduled flows on every plan",
      bodyMd: "Scheduled flows now run on every plan. Flows that only use web APIs need no dedicated machine.",
      campaignId: GTM_CAMPAIGN_IDS.flows, past: S.PUBLISHED, future: S.DRAFT,
    },
    {
      id: "gtm-item-webinar", day: 24, hours: 17, kind: K.OTHER, channel: "webinar", title: "Live demo: your first scheduled flow",
      bodyMd: "A 30-minute live session: build a daily digest from nothing, then questions.",
      campaignId: GTM_CAMPAIGN_IDS.flows, ideaId: "gtm-idea-live-demo", past: S.PAUSED, future: S.DRAFT,
    },
    {
      id: "gtm-item-customer-quote", day: 25, hours: 11, kind: K.SOCIAL_POST, channel: "linkedin", title: "Customer quote: from cron to flows",
      bodyMd: "\"We replaced three cron jobs with one flow and an approval step.\" (sample quote, not a real customer)",
      campaignId: GTM_CAMPAIGN_IDS.flows, past: S.PUBLISHED, future: S.DRAFT,
    },
    {
      id: "gtm-item-pricing-explained", day: 26, hours: 9, kind: K.BLOG_POST, channel: "blog", title: "Pricing explained: what Starter costs",
      bodyMd: "# Pricing explained\n\nStarter is $19 per month, billed monthly. It used to be $15 per month, but that price is retired.\n\nOne static site per organization is free.",
      ideaId: "gtm-idea-pricing-faq", past: S.PAUSED, future: S.REVIEW,
    },
    {
      id: "gtm-item-month-review", day: 28, hours: 14, kind: K.EMAIL, channel: "email", title: "The month in review",
      bodyMd: "Hi {first_name},\n\nHere is what shipped this month, in four lines.",
      past: S.SENT, future: S.DRAFT,
      email: { to: ["newsletter@lists.example.com"], subject: "The month in review" },
    },
    {
      id: "gtm-item-seo-page", day: 0, hours: 9, kind: K.BLOG_POST, channel: "blog", title: "Backlog: a page for \"static hosting with a CDN\"",
      bodyMd: "Not scheduled yet. Outline: what a CDN does for a static site, and how to add one.",
      past: S.DRAFT, future: S.DRAFT,
    },
  ];

  const items: GtmItem[] = specs.map((spec) => {
    const scheduled = spec.day > 0 ? onDay(spec.day, spec.hours) : null;
    const status = scheduled && scheduled.getTime() < nowMs ? spec.past : spec.future;
    const created = scheduled ? new Date(scheduled.getTime() - 6 * 24 * 3600 * 1000) : at(-5, 11);
    const sent = status === S.SENT;
    return create(GtmItemSchema, {
      id: spec.id, orgId: DEMO_ORG, campaignId: spec.campaignId ?? "", ideaId: spec.ideaId ?? "", kind: spec.kind, channel: spec.channel,
      title: spec.title, bodyMd: spec.bodyMd, status, scheduledAt: scheduled ? ts(scheduled) : undefined,
      emailTo: spec.email?.to ?? [], emailSubject: spec.email?.subject ?? "",
      ...(sent && scheduled ? { gmailMessageId: `sample-gmail-${spec.id}`, sentAt: ts(scheduled), sentBy: GTM_SAMPLE_PERSON, approvalHash: "sample-approval-hash" } : {}),
      revision: 1, createdAt: ts(created), updatedAt: ts(created),
    });
  });

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

  return { ideas, campaigns, items, positioning, claims };
}
