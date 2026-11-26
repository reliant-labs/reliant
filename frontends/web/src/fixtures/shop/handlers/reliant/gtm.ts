// Copyright (c) 2026 Reliant Labs

/**
 * GtmService, answered in the page: ideas, campaigns, items (the calendar),
 * positioning and the claims registry, from the sample pack (../../data/gtm.ts)
 * held IN MEMORY. The reads honour their filters; the writes (GTM_ANSWERS, the
 * pack's `answers`) change that memory, so accept, edit, approve, verify and
 * drag-to-reschedule visibly work and reset on reload. Nothing here sends,
 * publishes or reaches a server.
 *
 * It follows the service's contract where the UI can see it:
 *   - a row is read and written by its org only; any other org is NotFound,
 *     and GetGtmAccess is enabled for the shop user's org alone;
 *   - every write bumps the row's revision, and one that names a stale
 *     expected_revision is Aborted with a GtmRevisionConflict detail (edit a
 *     row, then edit it again from the old copy to see the conflict path);
 *   - a SENT item is not edited, SENT is never set here, and a claim is
 *     verified only with its evidence (a source, and for a comparison claim a
 *     third party's own);
 *   - CheckClaims is a small deterministic stand-in for the Go claim checker:
 *     it flags prices, percentages, durations, "free" and absolutes, and
 *     matches each against the registry.
 *
 * App layer only (../../app.ts): the demo pack never imports this.
 */
import { create } from "@bufbuild/protobuf";
import { timestampFromMs, timestampMs, type Timestamp } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  GtmCampaignSchema,
  GtmCampaignStatus,
  GtmClaimCategory,
  GtmClaimFindingKind,
  GtmClaimFindingSchema,
  GtmClaimSchema,
  GtmClaimSourceKind,
  GtmClaimStatus,
  GtmIdeaSchema,
  GtmIdeaSource,
  GtmIdeaStatus,
  GtmItemSchema,
  GtmItemStatus,
  GtmPositioningSchema,
  GtmRevisionConflictSchema,
  GtmService,
  type GtmCampaign,
  type GtmClaim,
  type GtmClaimFinding,
  type GtmIdea,
  type GtmItem,
  type GtmPositioning,
} from "@/gen/reliant/v1/gtm_pb";

import { gtmSampleData, GTM_SAMPLE_PERSON } from "../../data/gtm";
import { DEMO_ORG, demoNow } from "../../shopStory";
import { serve } from "../serve";

/** The writes the pack answers although its transport is read-only: every GtmService rpc not declared NO_SIDE_EFFECTS. */
export const GTM_ANSWERS = [
  "reliant.v1.GtmService/CreateIdea",
  "reliant.v1.GtmService/UpdateIdea",
  "reliant.v1.GtmService/SetIdeaStatus",
  "reliant.v1.GtmService/CreateCampaign",
  "reliant.v1.GtmService/UpdateCampaign",
  "reliant.v1.GtmService/CreateItem",
  "reliant.v1.GtmService/UpdateItem",
  "reliant.v1.GtmService/UpdatePositioning",
  "reliant.v1.GtmService/UpsertClaim",
] as const;

const notFound = (what: string) => new ConnectError(`${what} not found`, Code.NotFound);

function conflict(id: string, expected: number, current: number): ConnectError {
  return new ConnectError(`${id} changed since it was read (revision ${current}, not ${expected})`, Code.Aborted, undefined, [
    { desc: GtmRevisionConflictSchema, value: { id, expectedRevision: expected, currentRevision: current } },
  ]);
}

const ms = (t: Timestamp | undefined): number => (t ? timestampMs(t) : 0);

/** A page of `all`, by an offset page token. */
function page<T>(all: T[], pageSize: number, defaultSize: number, token: string): { rows: T[]; next: string } {
  const size = pageSize > 0 ? pageSize : defaultSize;
  const start = token ? Number.parseInt(token, 10) || 0 : 0;
  return { rows: all.slice(start, start + size), next: start + size < all.length ? String(start + size) : "" };
}

/** Newest first. */
const byCreated = <T extends { createdAt?: Timestamp }>(a: T, b: T) => ms(b.createdAt) - ms(a.createdAt);

export function gtmHandlers() {
  let data: ReturnType<typeof gtmSampleData> | null = null;
  let seq = 0;
  const state = () => (data ??= gtmSampleData(demoNow()));
  const nowTs = () => timestampFromMs(demoNow());
  const nextId = (prefix: string) => `${prefix}-new-${++seq}`;

  /** The org the call names: the shop user's, or NotFound (never PermissionDenied: GTM cannot be probed). */
  const org = (orgId: string) => {
    if (orgId !== DEMO_ORG) throw notFound("org");
    return state();
  };

  const idea = (orgId: string, id: string): GtmIdea => org(orgId).ideas.find((i) => i.id === id) ?? (() => { throw notFound("idea"); })();
  const campaign = (orgId: string, id: string): GtmCampaign => org(orgId).campaigns.find((c) => c.id === id) ?? (() => { throw notFound("campaign"); })();
  const item = (orgId: string, id: string): GtmItem => org(orgId).items.find((i) => i.id === id) ?? (() => { throw notFound("item"); })();

  /** A link to a campaign or idea must name one of the org's own. Empty is none. */
  const checkCampaignLink = (orgId: string, id: string | undefined) => {
    if (id) campaign(orgId, id);
  };
  const checkIdeaLink = (orgId: string, id: string | undefined) => {
    if (id) idea(orgId, id);
  };

  const checkRevision = (id: string, expected: number, current: number) => {
    if (expected !== current) throw conflict(id, expected, current);
  };

  /** Replace a row in its list. */
  const put = <T extends { id: string }>(rows: T[], next: T) => {
    const at = rows.findIndex((r) => r.id === next.id);
    if (at >= 0) rows[at] = next;
    else rows.unshift(next);
    return next;
  };

  const sortedItems = (items: GtmItem[]) => {
    const scheduled = items.filter((i) => i.scheduledAt).sort((a, b) => ms(a.scheduledAt) - ms(b.scheduledAt));
    const unscheduled = items.filter((i) => !i.scheduledAt).sort(byCreated);
    return [...scheduled, ...unscheduled];
  };

  return serve(GtmService, {
    getGtmAccess: (req) => ({ enabled: req.orgId === DEMO_ORG }),

    // ── Ideas ────────────────────────────────────────────────────────────────
    listIdeas: (req) => {
      const rows = org(req.orgId).ideas
        .filter((i) => (req.kind === undefined || i.kind === req.kind) && (req.status === undefined || i.status === req.status) && (req.campaignId === undefined || i.campaignId === req.campaignId))
        .sort(byCreated);
      const { rows: ideas, next } = page(rows, req.pageSize, 50, req.pageToken);
      return { ideas, nextPageToken: next };
    },
    getIdea: (req) => ({ idea: idea(req.orgId, req.id) }),
    createIdea: (req) => {
      checkCampaignLink(req.orgId, req.campaignId);
      const created = create(GtmIdeaSchema, {
        id: nextId("gtm-idea"), orgId: req.orgId, kind: req.kind, title: req.title, bodyMd: req.bodyMd, evidence: req.evidence, status: GtmIdeaStatus.PROPOSED,
        source: GtmIdeaSource.HUMAN, sourceChatId: req.sourceChatId, createdBy: GTM_SAMPLE_PERSON, campaignId: req.campaignId, revision: 1, createdAt: nowTs(), updatedAt: nowTs(),
      });
      return { idea: put(state().ideas, created) };
    },
    updateIdea: (req) => {
      const current = idea(req.orgId, req.id);
      checkRevision(req.id, req.expectedRevision, current.revision);
      checkCampaignLink(req.orgId, req.campaignId);
      const next = create(GtmIdeaSchema, {
        ...current,
        kind: req.kind ?? current.kind,
        title: req.title ?? current.title,
        bodyMd: req.bodyMd ?? current.bodyMd,
        evidence: req.evidence ? req.evidence.evidence : current.evidence,
        campaignId: req.campaignId ?? current.campaignId,
        revision: current.revision + 1,
        updatedAt: nowTs(),
      });
      return { idea: put(state().ideas, next) };
    },
    setIdeaStatus: (req) => {
      const current = idea(req.orgId, req.id);
      checkRevision(req.id, req.expectedRevision, current.revision);
      const next = create(GtmIdeaSchema, { ...current, status: req.status, revision: current.revision + 1, updatedAt: nowTs() });
      return { idea: put(state().ideas, next) };
    },

    // ── Campaigns ────────────────────────────────────────────────────────────
    listCampaigns: (req) => {
      const rows = org(req.orgId).campaigns.filter((c) => req.status === undefined || c.status === req.status).sort(byCreated);
      const { rows: campaigns, next } = page(rows, req.pageSize, 50, req.pageToken);
      return { campaigns, nextPageToken: next };
    },
    getCampaign: (req) => {
      const found = campaign(req.orgId, req.id);
      const all = state();
      return {
        campaign: found,
        ideas: all.ideas.filter((i) => i.campaignId === found.id).sort(byCreated),
        items: sortedItems(all.items.filter((i) => i.campaignId === found.id)),
      };
    },
    createCampaign: (req) => {
      const created = create(GtmCampaignSchema, {
        id: nextId("gtm-cmp"), orgId: req.orgId, name: req.name, goal: req.goal, periodStart: req.periodStart, periodEnd: req.periodEnd,
        status: req.status ?? GtmCampaignStatus.DRAFT, revision: 1, createdAt: nowTs(), updatedAt: nowTs(),
      });
      org(req.orgId);
      return { campaign: put(state().campaigns, created) };
    },
    updateCampaign: (req) => {
      const current = campaign(req.orgId, req.id);
      checkRevision(req.id, req.expectedRevision, current.revision);
      const next = create(GtmCampaignSchema, {
        ...current,
        name: req.name ?? current.name,
        goal: req.goal ?? current.goal,
        periodStart: req.clearPeriodStart ? undefined : (req.periodStart ?? current.periodStart),
        periodEnd: req.clearPeriodEnd ? undefined : (req.periodEnd ?? current.periodEnd),
        status: req.status ?? current.status,
        revision: current.revision + 1,
        updatedAt: nowTs(),
      });
      return { campaign: put(state().campaigns, next) };
    },

    // ── Items ────────────────────────────────────────────────────────────────
    listItems: (req) => {
      const from = req.scheduledFrom ? ms(req.scheduledFrom) : null;
      const to = req.scheduledTo ? ms(req.scheduledTo) : null;
      const ranged = from !== null || to !== null;
      const rows = org(req.orgId).items.filter((i) => {
        if (req.campaignId !== undefined && i.campaignId !== req.campaignId) return false;
        if (req.kind !== undefined && i.kind !== req.kind) return false;
        if (req.status !== undefined && i.status !== req.status) return false;
        if (!ranged) return true;
        if (!i.scheduledAt) return false;
        const at = ms(i.scheduledAt);
        return (from === null || at >= from) && (to === null || at < to);
      });
      const ordered = ranged ? rows.sort((a, b) => ms(a.scheduledAt) - ms(b.scheduledAt)) : rows.sort(byCreated);
      const { rows: items, next } = page(ordered, req.pageSize, 100, req.pageToken);
      return { items, nextPageToken: next };
    },
    getItem: (req) => ({ item: item(req.orgId, req.id) }),
    createItem: (req) => {
      checkCampaignLink(req.orgId, req.campaignId);
      checkIdeaLink(req.orgId, req.ideaId);
      const created = create(GtmItemSchema, {
        id: nextId("gtm-item"), orgId: req.orgId, campaignId: req.campaignId, ideaId: req.ideaId, kind: req.kind, channel: req.channel, title: req.title, bodyMd: req.bodyMd,
        status: GtmItemStatus.DRAFT, scheduledAt: req.scheduledAt, emailTo: req.emailTo, emailSubject: req.emailSubject, revision: 1, createdAt: nowTs(), updatedAt: nowTs(),
      });
      return { item: put(state().items, created) };
    },
    updateItem: (req) => {
      const current = item(req.orgId, req.id);
      checkRevision(req.id, req.expectedRevision, current.revision);
      if (current.status === GtmItemStatus.SENT) throw new ConnectError("a sent item is not edited", Code.FailedPrecondition);
      if (req.status === GtmItemStatus.SENT) throw new ConnectError("an item becomes SENT only through the approved email path", Code.FailedPrecondition);
      checkCampaignLink(req.orgId, req.campaignId);
      checkIdeaLink(req.orgId, req.ideaId);
      const next = create(GtmItemSchema, {
        ...current,
        kind: req.kind ?? current.kind,
        title: req.title ?? current.title,
        bodyMd: req.bodyMd ?? current.bodyMd,
        channel: req.channel ?? current.channel,
        campaignId: req.campaignId ?? current.campaignId,
        ideaId: req.ideaId ?? current.ideaId,
        scheduledAt: req.clearScheduledAt ? undefined : (req.scheduledAt ?? current.scheduledAt),
        emailTo: req.emailTo ? req.emailTo.addresses : current.emailTo,
        emailSubject: req.emailSubject ?? current.emailSubject,
        status: req.status ?? current.status,
        revision: current.revision + 1,
        updatedAt: nowTs(),
      });
      return { item: put(state().items, next) };
    },

    // ── Positioning ──────────────────────────────────────────────────────────
    getPositioning: (req) => ({ positioning: org(req.orgId).positioning }),
    updatePositioning: (req) => {
      const current = org(req.orgId).positioning;
      checkRevision("positioning", req.expectedRevision, current.revision);
      const next: GtmPositioning = create(GtmPositioningSchema, {
        orgId: req.orgId, positioningMd: req.positioningMd, audiences: req.audiences, updatedBy: GTM_SAMPLE_PERSON, revision: current.revision + 1, updatedAt: nowTs(),
      });
      state().positioning = next;
      return { positioning: next };
    },

    // ── Claims ───────────────────────────────────────────────────────────────
    listClaims: (req) => {
      const search = req.search?.toLowerCase();
      const rows = org(req.orgId).claims
        .filter((c) => (req.status === undefined || c.status === req.status) && (req.category === undefined || c.category === req.category) && (!search || c.text.toLowerCase().includes(search)))
        .sort(byCreated);
      const { rows: claims, next } = page(rows, req.pageSize, 200, req.pageToken);
      return { claims, nextPageToken: next };
    },
    upsertClaim: (req) => {
      const all = org(req.orgId);
      const existing = req.id ? all.claims.find((c) => c.id === req.id) : undefined;
      if (req.id && !existing) throw notFound("claim");
      if (existing) checkRevision(req.id, req.expectedRevision, existing.revision);
      if (existing && ((req.text !== undefined && req.text !== existing.text) || (req.category !== undefined && req.category !== existing.category))) {
        throw new ConnectError("a claim's text and category are fixed; retire it and add a new one", Code.InvalidArgument);
      }
      if (!existing && (!req.text || req.category === undefined)) throw new ConnectError("a new claim needs its text and category", Code.InvalidArgument);

      const text = existing?.text ?? req.text ?? "";
      const category = existing?.category ?? req.category ?? GtmClaimCategory.CAPABILITY;
      const status = req.status ?? existing?.status ?? GtmClaimStatus.UNVERIFIED;
      const sourceUrl = req.sourceUrl ?? existing?.sourceUrl ?? "";
      const sourceKind = req.sourceKind ?? existing?.sourceKind ?? GtmClaimSourceKind.UNSPECIFIED;

      const textNorm = text.toLowerCase().replace(/\s+/g, " ").trim();
      if (all.claims.some((c) => c.id !== existing?.id && c.textNorm === textNorm)) throw new ConnectError("the org already holds this claim", Code.AlreadyExists);

      const verifying = status === GtmClaimStatus.VERIFIED && existing?.status !== GtmClaimStatus.VERIFIED;
      if (status === GtmClaimStatus.VERIFIED) {
        if (!sourceUrl || sourceKind === GtmClaimSourceKind.UNSPECIFIED) throw new ConnectError("a verified claim needs its evidence: a source and its kind", Code.InvalidArgument);
        if (category === GtmClaimCategory.COMPARISON && sourceKind !== GtmClaimSourceKind.PRIMARY_THIRD_PARTY) {
          throw new ConnectError("a claim about another company is verified only by a source of theirs", Code.InvalidArgument);
        }
      }

      const next = create(GtmClaimSchema, {
        ...(existing ?? { id: nextId("gtm-claim"), orgId: req.orgId, createdAt: nowTs() }),
        text, textNorm, category, status, sourceUrl, sourceKind,
        verifiedAt: status !== GtmClaimStatus.VERIFIED ? undefined : verifying ? nowTs() : existing?.verifiedAt,
        verifiedBy: status !== GtmClaimStatus.VERIFIED ? "" : verifying ? GTM_SAMPLE_PERSON : (existing?.verifiedBy ?? ""),
        expiresOrRecheckAt: req.clearExpiresOrRecheckAt ? undefined : (req.expiresOrRecheckAt ?? existing?.expiresOrRecheckAt),
        notes: req.notes ?? existing?.notes ?? "",
        revision: (existing?.revision ?? 0) + 1,
        updatedAt: nowTs(),
      });
      put(all.claims, next);
      return { claim: next, created: !existing };
    },
    checkClaims: (req) => {
      const findings = checkClaims(req.text, org(req.orgId).claims, demoNow());
      return { findings, blocking: findings.some((f) => f.blocking) };
    },
  });
}

// ── CheckClaims ──────────────────────────────────────────────────────────────

/** The rules, in the order findings are reported. Each finds the risky words in a text. */
const RULES: { kind: string; pattern: RegExp }[] = [
  { kind: "price", pattern: /\$\d[\d,]*(?:\.\d+)?(?:\/(?:mo|month|yr|year))?/g },
  { kind: "percent", pattern: /\b\d+(?:\.\d+)?\s?%/g },
  { kind: "duration", pattern: /\b(?:(?:under|within|less than|in)\s+(?:\d+|an?|one)\s+(?:seconds?|minutes?|hours?|days?)|\d+\s?(?:seconds?|minutes?|hours?|days?))\b/gi },
  { kind: "free", pattern: /\bfree\b/gi },
  { kind: "superlative", pattern: /\b(?:fastest|best|#1)\b/gi },
  { kind: "absolute", pattern: /\b(?:never|always)\b/gi },
];

/** The sentence of `text` that holds the character at `index`. */
function sentenceAt(text: string, index: number): string {
  let start = 0;
  for (const m of text.matchAll(/[.!?](?=\s|$)|\n/g)) {
    const end = m.index + 1;
    if (end <= index) start = end;
    else return text.slice(start, end).trim();
  }
  return text.slice(start).trim();
}

const rank = (claim: GtmClaim, now: number): number => {
  if (claim.status !== GtmClaimStatus.VERIFIED) return claim.status === GtmClaimStatus.UNVERIFIED ? 1 : 0;
  return claim.expiresOrRecheckAt && ms(claim.expiresOrRecheckAt) < now ? 2 : 3;
};

/**
 * A deterministic stand-in for the registry check: every risky mention is
 * matched against the claims whose text contains it, preferring a verified
 * claim whose recheck is not overdue. Verified matches do not block; an
 * unverified, retired or overdue match, and a mention no claim holds, do.
 */
export function checkClaims(text: string, claims: readonly GtmClaim[], now: number): GtmClaimFinding[] {
  const findings: GtmClaimFinding[] = [];
  const seen = new Set<string>();
  for (const { kind, pattern } of RULES) {
    for (const m of text.matchAll(pattern)) {
      const mention = m[0].trim();
      const key = `${kind}:${mention.toLowerCase()}`;
      if (seen.has(key)) continue;
      seen.add(key);
      const context = sentenceAt(text, m.index);
      const needle = mention.toLowerCase();
      const best = claims.filter((c) => c.textNorm.includes(needle)).sort((a, b) => rank(b, now) - rank(a, now))[0];
      if (!best) {
        findings.push(create(GtmClaimFindingSchema, { kind: GtmClaimFindingKind.UNMATCHED_RISKY, mentionKind: kind, mention, context, blocking: true }));
        continue;
      }
      const level = rank(best, now);
      const reason = level === 3 ? "" : level === 2 ? "recheck overdue" : best.status === GtmClaimStatus.RETIRED ? "retired" : "unverified";
      findings.push(
        create(GtmClaimFindingSchema, {
          kind: level === 3 ? GtmClaimFindingKind.MATCHED_VERIFIED : GtmClaimFindingKind.MATCHED_UNVERIFIED,
          mentionKind: kind, mention, context, claimId: best.id, claimText: best.text, reason, blocking: level !== 3,
        }),
      );
    }
  }
  return findings;
}
