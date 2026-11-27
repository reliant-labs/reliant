// Copyright (c) 2026 Reliant Labs

/**
 * GtmService, answered in the page: strategies and their campaigns, channels,
 * items (the calendar), assets, the strategy library and its import audit,
 * ideas, positioning and the claims registry, from the sample pack
 * (../../data/gtm.ts) held IN MEMORY. Every rpc is answered. The reads honour
 * their filters; the writes (GTM_ANSWERS, the pack's `answers`) change that
 * memory, so accept, edit, approve, record a URL, resolve a held send, use a
 * library strategy, confirm a prune and drag-to-reschedule visibly work and
 * reset on reload. Nothing here sends, publishes or reaches a server.
 *
 * It follows the service's contract where the UI can see it:
 *   - a row is read and written by its org only; any other org is NotFound,
 *     and GetGtmAccess is enabled for the shop user's org alone;
 *   - every write bumps the row's revision, and one that names a stale
 *     expected_revision is Aborted with a GtmRevisionConflict detail (edit a
 *     row, then edit it again from the old copy to see the conflict path);
 *   - the caller is the web user, a person, so the agent-only rules (an agent
 *     may not approve, set an integrated channel, ...) do not apply; the
 *     lifecycle rules a person meets do: an email-type item (EMAIL or
 *     COLD_OUTREACH) is never SCHEDULED or PUBLISHED and is SENT only by the
 *     email path (never here), a SENT item is not edited, a HELD email (its
 *     approval_hash set, not SENT) refuses every edit until ResolveItemSend,
 *     a content edit of an APPROVED or SCHEDULED item returns it to REVIEW,
 *     and PUBLISHED on a MANUAL channel needs published_url;
 *   - there is no bucket in the preview: CreateAssetUpload and
 *     ConfirmAssetUpload are Unavailable, and GetAssetDownload answers an
 *     inline image;
 *   - CheckClaims is a small deterministic stand-in for the Go claim checker:
 *     it flags prices, percentages, durations, "free" and absolutes, and
 *     matches each against the registry.
 *
 * App layer only (../../app.ts): the demo pack never imports this.
 */
import { create, equals } from "@bufbuild/protobuf";
import { timestampFromMs, timestampMs, type Timestamp } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  GtmCampaignSchema,
  GtmCampaignStatus,
  GtmChannelMode,
  GtmChannelSchema,
  GtmChannelSource,
  GtmChannelStatus,
  GtmClaimCategory,
  GtmClaimFindingKind,
  GtmClaimFindingSchema,
  GtmClaimSchema,
  GtmClaimSourceKind,
  GtmClaimStatus,
  GtmConnectionState,
  GtmCoverageRowSchema,
  GtmCredentialKind,
  GtmIdeaSchema,
  GtmIdeaSource,
  GtmIdeaStatus,
  GtmItemFormat,
  GtmItemSchema,
  GtmItemStatus,
  GtmItemStatusCountSchema,
  GtmLibraryImportSchema,
  GtmLibraryPruneRequestSchema,
  GtmPositioningSchema,
  GtmPruneRequestStatus,
  GtmRevisionConflictSchema,
  GtmService,
  GtmStrategySchema,
  GtmStrategyStatus,
  GtmStrategyLibraryEntrySchema,
  type GtmCampaign,
  type GtmChannel,
  type GtmClaim,
  type GtmClaimFinding,
  type GtmCoverageRow,
  type GtmItem,
  type GtmLibraryPruneRequest,
  type GtmStrategyLibraryEntry,
} from "@/gen/reliant/v1/gtm_pb";

import { gtmAssetImageUrl, gtmSampleData, GTM_SAMPLE_PERSON } from "../../data/gtm";
import { DAY, DEMO_ORG, demoNow, MINUTE } from "../../shopStory";
import { serve } from "../serve";

/**
 * The GtmService rpcs the pack answers although its transport is read-only:
 * every one not declared NO_SIDE_EFFECTS (the writes, and the two channel
 * reads that may seed the defaults). Each is checked against the proto.
 */
export const GTM_ANSWERS = [
  "reliant.v1.GtmService/ListChannels",
  "reliant.v1.GtmService/GetChannel",
  "reliant.v1.GtmService/CreateStrategy",
  "reliant.v1.GtmService/UpdateStrategy",
  "reliant.v1.GtmService/CreateChannel",
  "reliant.v1.GtmService/UpdateChannel",
  "reliant.v1.GtmService/CreateAssetUpload",
  "reliant.v1.GtmService/ConfirmAssetUpload",
  "reliant.v1.GtmService/DeleteAsset",
  "reliant.v1.GtmService/ImportStrategyLibrary",
  "reliant.v1.GtmService/ConfirmLibraryPrune",
  "reliant.v1.GtmService/CancelLibraryPrune",
  "reliant.v1.GtmService/UseLibraryStrategy",
  "reliant.v1.GtmService/CreateIdea",
  "reliant.v1.GtmService/UpdateIdea",
  "reliant.v1.GtmService/SetIdeaStatus",
  "reliant.v1.GtmService/CreateCampaign",
  "reliant.v1.GtmService/UpdateCampaign",
  "reliant.v1.GtmService/CreateItem",
  "reliant.v1.GtmService/UpdateItem",
  "reliant.v1.GtmService/ResolveItemSend",
  "reliant.v1.GtmService/UpdatePositioning",
  "reliant.v1.GtmService/UpsertClaim",
] as const;

const SLUG = /^[a-z0-9-]{1,32}$/;
const LIBRARY_KEY = /^[a-z0-9][a-z0-9_-]{0,127}$/;
const DATE = /^(\d{4})-(\d{2})-(\d{2})$/;
const MAX_IMPORT_BATCH = 500;
/** GetCampaign's cap on each of its ideas and items lists (the domain's MaxCampaignIdeas and MaxCampaignItems). */
const MAX_CAMPAIGN_ROWS = 200;
const PRUNE_EXPIRY_MS = 7 * DAY;
const DOWNLOAD_TTL_MS = 15 * MINUTE;

const notFound = (what: string) => new ConnectError(`${what} not found`, Code.NotFound);
const invalid = (field: string, reason: string) => new ConnectError(`${field}: ${reason}`, Code.InvalidArgument);
const precondition = (message: string) => new ConnectError(message, Code.FailedPrecondition);

function conflict(id: string, expected: number, current: number): ConnectError {
  return new ConnectError(`${id} changed since it was read (revision ${current}, not ${expected})`, Code.Aborted, undefined, [
    { desc: GtmRevisionConflictSchema, value: { id, expectedRevision: expected, currentRevision: current } },
  ]);
}

const ms = (t: Timestamp | undefined): number => (t ? timestampMs(t) : 0);

/** A real calendar date, YYYY-MM-DD; empty is open. */
function checkDate(field: string, value: string | undefined) {
  if (!value) return;
  const m = DATE.exec(value);
  const d = m ? new Date(Date.UTC(+m[1]!, +m[2]! - 1, +m[3]!)) : null;
  if (!m || !d || d.getUTCFullYear() !== +m[1]! || d.getUTCMonth() !== +m[2]! - 1 || d.getUTCDate() !== +m[3]!) throw invalid(field, "a real date, YYYY-MM-DD");
}

function checkPeriod(start: string, end: string) {
  checkDate("period_start", start);
  checkDate("period_end", end);
  if (start && end && end < start) throw invalid("period_end", "before period_start");
}

/** A page of `all`, by an offset page token. */
function page<T>(all: T[], pageSize: number, defaultSize: number, token: string): { rows: T[]; next: string } {
  const size = pageSize > 0 ? pageSize : defaultSize;
  const start = token ? Number.parseInt(token, 10) || 0 : 0;
  return { rows: all.slice(start, start + size), next: start + size < all.length ? String(start + size) : "" };
}

/** Newest first. */
const byCreated = <T extends { createdAt?: Timestamp }>(a: T, b: T) => ms(b.createdAt) - ms(a.createdAt);

const isEmailType = (format: GtmItemFormat) => format === GtmItemFormat.EMAIL || format === GtmItemFormat.COLD_OUTREACH;
/** A send started and its outcome is unknown: the item is frozen until a person resolves it. */
const isHeld = (item: GtmItem) => item.approvalHash !== "" && item.status !== GtmItemStatus.SENT;
const draftish = (s: GtmItemStatus) => s === GtmItemStatus.DRAFT || s === GtmItemStatus.REVIEW;
const statusWord = (s: GtmItemStatus) => (GtmItemStatus[s] ?? "unknown").toLowerCase();

/** The lifecycle a person meets (GtmItemStatus's comment); an email is never scheduled or published. */
function checkTransition(format: GtmItemFormat, from: GtmItemStatus, to: GtmItemStatus) {
  if (draftish(from) && draftish(to)) return;
  const email = isEmailType(format);
  const S = GtmItemStatus;
  const allowed =
    (to === S.APPROVED && draftish(from)) ||
    (to === S.SCHEDULED && from === S.APPROVED && !email) ||
    (to === S.PUBLISHED && (from === S.APPROVED || from === S.SCHEDULED) && !email) ||
    (to === S.PAUSED && from !== S.SENT) ||
    (to === S.DRAFT && from === S.PAUSED);
  if (allowed) return;
  const reason = email && (to === S.SCHEDULED || to === S.PUBLISHED) ? ": an email is never scheduled or published; it stays APPROVED until it is sent" : "";
  throw precondition(`an item cannot go from ${statusWord(from)} to ${statusWord(to)}${reason}`);
}

const contentOf = (i: GtmItem) => JSON.stringify([i.format, i.title, i.bodyMd, i.channelId, i.emailSubject, i.emailTo]);

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
  const find = <T extends { id: string }>(rows: T[], id: string, what: string): T => rows.find((r) => r.id === id) ?? (() => { throw notFound(what); })();

  const strategy = (orgId: string, id: string) => find(org(orgId).strategies, id, "strategy");
  const channel = (orgId: string, id: string) => find(org(orgId).channels, id, "channel");
  const idea = (orgId: string, id: string) => find(org(orgId).ideas, id, "idea");
  const campaign = (orgId: string, id: string) => find(org(orgId).campaigns, id, "campaign");
  const item = (orgId: string, id: string) => find(org(orgId).items, id, "item");
  const asset = (orgId: string, id: string) => find(org(orgId).assets, id, "asset");
  const pruneRequest = (orgId: string, id: string) => find(org(orgId).pruneRequests, id, "prune request");

  /** A link names one of the org's own rows. Empty is none. */
  const link = (orgId: string, id: string | undefined, kind: "campaign" | "idea" | "channel") => {
    if (!id) return;
    if (kind === "campaign") campaign(orgId, id);
    else if (kind === "idea") idea(orgId, id);
    else channel(orgId, id);
  };

  const checkRevision = (id: string, expected: number, current: number) => {
    if (expected !== current) throw conflict(id, expected, current);
  };

  /** Replace a row in its list, or add it first. */
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

  /** A pending request past its seven days reads as EXPIRED. */
  const withExpiry = (req: GtmLibraryPruneRequest): GtmLibraryPruneRequest =>
    req.status === GtmPruneRequestStatus.PENDING && demoNow() >= ms(req.createdAt) + PRUNE_EXPIRY_MS ? create(GtmLibraryPruneRequestSchema, { ...req, status: GtmPruneRequestStatus.EXPIRED }) : req;

  /** One row per planned (channel, format) pair and per pair an item uses, with the items counted by status. */
  const coverageOf = (c: GtmCampaign, items: GtmItem[], channels: GtmChannel[]): GtmCoverageRow[] => {
    const rows = new Map<string, { channelId: string; format: GtmItemFormat; planned: boolean; counts: Map<GtmItemStatus, number> }>();
    const row = (channelId: string, format: GtmItemFormat) => {
      const key = `${channelId}|${format}`;
      let r = rows.get(key);
      if (!r) rows.set(key, (r = { channelId, format, planned: false, counts: new Map() }));
      return r;
    };
    for (const channelId of c.plannedChannelIds) for (const format of c.plannedFormats) row(channelId, format).planned = true;
    for (const i of items) {
      const r = row(i.channelId, i.format);
      r.counts.set(i.status, (r.counts.get(i.status) ?? 0) + 1);
    }
    const keyOf = (id: string) => channels.find((ch) => ch.id === id)?.key ?? "";
    return [...rows.values()]
      .sort((a, b) => keyOf(a.channelId).localeCompare(keyOf(b.channelId)) || a.format - b.format)
      .map((r) =>
        create(GtmCoverageRowSchema, {
          channelId: r.channelId, format: r.format, planned: r.planned,
          counts: [...r.counts].sort(([a], [b]) => a - b).map(([status, count]) => create(GtmItemStatusCountSchema, { status, count })),
          total: [...r.counts.values()].reduce((a, b) => a + b, 0),
        }),
      );
  };

  const checkPlanned = (orgId: string, ids: readonly string[]) => {
    if (ids.length > 50 || new Set(ids).size !== ids.length) throw invalid("planned_channel_ids", "at most 50, each once");
    for (const id of ids) channel(orgId, id);
  };
  const checkFormats = (formats: readonly GtmItemFormat[]) => {
    if (new Set(formats).size !== formats.length || formats.some((f) => f === GtmItemFormat.UNSPECIFIED)) throw invalid("planned_formats", "each a known format, once");
  };
  const checkChannelTypes = (types: readonly string[]) => {
    if (types.length > 32 || new Set(types).size !== types.length || types.some((t) => !SLUG.test(t))) throw invalid("channel_types", "each a slug, once, at most 32");
  };
  const checkChannelShape = (mode: GtmChannelMode, integrationKind: string, connectionId: string) => {
    if (mode === GtmChannelMode.INTEGRATED && !integrationKind) throw invalid("integration_kind", "an integrated channel needs its integration (a catalog id)");
    if (mode === GtmChannelMode.MANUAL && (integrationKind || connectionId)) throw invalid("integration_kind", "a manual channel has no integration or connection");
  };
  /** Display only: an integrated channel on the default connection is connected when another channel of the same integration is. */
  const connectionFor = (channels: GtmChannel[], integrationKind: string, connectionId: string): GtmConnectionState =>
    connectionId ? GtmConnectionState.UNKNOWN : channels.some((c) => c.integrationKind === integrationKind && c.connectionState === GtmConnectionState.CONNECTED) ? GtmConnectionState.CONNECTED : GtmConnectionState.NOT_CONNECTED;

  const checkEmailFields = (format: GtmItemFormat, to: readonly string[], subject: string) => {
    if (!isEmailType(format) && (to.length > 0 || subject !== "")) throw invalid("email_to", "only an email-type item has recipients and a subject");
  };

  return serve(GtmService, {
    getGtmAccess: (req) => ({ enabled: req.orgId === DEMO_ORG }),

    // ── Strategies ───────────────────────────────────────────────────────────
    listStrategies: (req) => {
      const rows = org(req.orgId).strategies.filter((s) => req.status === undefined || s.status === req.status).sort(byCreated);
      const { rows: strategies, next } = page(rows, req.pageSize, 50, req.pageToken);
      return { strategies, nextPageToken: next };
    },
    getStrategy: (req) => {
      const found = strategy(req.orgId, req.id);
      const campaigns = org(req.orgId).campaigns.filter((c) => c.strategyId === found.id).sort(byCreated);
      return { strategy: found, campaigns: campaigns.slice(0, 200), truncated: campaigns.length > 200 };
    },
    createStrategy: (req) => {
      org(req.orgId);
      if (!req.name) throw invalid("name", "required");
      checkPeriod(req.periodStart, req.periodEnd);
      const created = create(GtmStrategySchema, {
        id: nextId("gtm-str"), orgId: req.orgId, name: req.name, goalMd: req.goalMd, targetMetric: req.targetMetric, periodStart: req.periodStart, periodEnd: req.periodEnd,
        status: req.status ?? GtmStrategyStatus.DRAFT, category: req.category ?? 0, revision: 1, createdBy: GTM_SAMPLE_PERSON, updatedBy: GTM_SAMPLE_PERSON, createdAt: nowTs(), updatedAt: nowTs(),
      });
      return { strategy: put(state().strategies, created) };
    },
    updateStrategy: (req) => {
      const current = strategy(req.orgId, req.id);
      checkRevision(req.id, req.expectedRevision, current.revision);
      const next = create(GtmStrategySchema, {
        ...current,
        name: req.name ?? current.name,
        goalMd: req.goalMd ?? current.goalMd,
        targetMetric: req.targetMetric ?? current.targetMetric,
        periodStart: req.periodStart ?? current.periodStart,
        periodEnd: req.periodEnd ?? current.periodEnd,
        status: req.status ?? current.status,
        category: req.category ?? current.category,
        revision: current.revision + 1, updatedBy: GTM_SAMPLE_PERSON, updatedAt: nowTs(),
      });
      checkPeriod(next.periodStart, next.periodEnd);
      return { strategy: put(state().strategies, next) };
    },

    // ── Channels ─────────────────────────────────────────────────────────────
    listChannels: (req) => ({
      channels: org(req.orgId).channels
        .filter((c) => (req.status === undefined || c.status === req.status) && (req.mode === undefined || c.mode === req.mode))
        .sort((a, b) => a.key.localeCompare(b.key)),
    }),
    getChannel: (req) => ({ channel: channel(req.orgId, req.id) }),
    createChannel: (req) => {
      const all = org(req.orgId);
      if (!SLUG.test(req.key)) throw invalid("key", "a slug: lowercase letters, digits and '-', 1 to 32");
      if (!req.name) throw invalid("name", "required");
      if (req.mode === GtmChannelMode.UNSPECIFIED) throw invalid("mode", "required");
      checkChannelShape(req.mode, req.integrationKind, req.connectionId);
      checkChannelTypes(req.channelTypes);
      if (all.channels.some((c) => c.key === req.key)) throw new ConnectError("a channel with this key already exists", Code.AlreadyExists);
      const created = create(GtmChannelSchema, {
        id: nextId("gtm-ch"), orgId: req.orgId, key: req.key, name: req.name, mode: req.mode, integrationKind: req.integrationKind, connectionId: req.connectionId,
        manualInstructionsMd: req.manualInstructionsMd, status: req.status ?? GtmChannelStatus.ACTIVE, source: GtmChannelSource.PERSON, createdBy: GTM_SAMPLE_PERSON,
        channelTypes: req.channelTypes, connectionState: req.mode === GtmChannelMode.INTEGRATED ? connectionFor(all.channels, req.integrationKind, req.connectionId) : GtmConnectionState.UNSPECIFIED,
        revision: 1, createdAt: nowTs(), updatedAt: nowTs(),
      });
      return { channel: put(all.channels, created) };
    },
    updateChannel: (req) => {
      const all = org(req.orgId);
      const current = channel(req.orgId, req.id);
      checkRevision(req.id, req.expectedRevision, current.revision);
      if (req.key !== undefined) {
        if (!SLUG.test(req.key)) throw invalid("key", "a slug: lowercase letters, digits and '-', 1 to 32");
        if (all.channels.some((c) => c.id !== current.id && c.key === req.key)) throw new ConnectError("a channel with this key already exists", Code.AlreadyExists);
      }
      if (req.name === "") throw invalid("name", "required");
      if (req.channelTypes) checkChannelTypes(req.channelTypes.types);
      const mode = req.mode ?? current.mode;
      const integrationKind = mode === GtmChannelMode.MANUAL ? (req.integrationKind ?? "") : (req.integrationKind ?? current.integrationKind);
      const connectionId = mode === GtmChannelMode.MANUAL ? (req.connectionId ?? "") : (req.connectionId ?? current.connectionId);
      checkChannelShape(mode, integrationKind, connectionId);
      const next = create(GtmChannelSchema, {
        ...current,
        key: req.key ?? current.key,
        name: req.name ?? current.name,
        mode, integrationKind, connectionId,
        manualInstructionsMd: req.manualInstructionsMd ?? current.manualInstructionsMd,
        status: req.status ?? current.status,
        channelTypes: req.channelTypes ? req.channelTypes.types : current.channelTypes,
        connectionState: mode === GtmChannelMode.INTEGRATED ? (current.mode === GtmChannelMode.INTEGRATED && !req.integrationKind && req.connectionId === undefined ? current.connectionState : connectionFor(all.channels, integrationKind, connectionId)) : GtmConnectionState.UNSPECIFIED,
        revision: current.revision + 1, updatedAt: nowTs(),
      });
      return { channel: put(all.channels, next) };
    },

    // ── Assets ───────────────────────────────────────────────────────────────
    listAssets: (req) => {
      if ((req.itemId === undefined) === (req.campaignId === undefined)) throw invalid("item_id", "exactly one of item_id and campaign_id");
      const rows = org(req.orgId).assets.filter((a) => (req.itemId !== undefined ? a.itemId === req.itemId : a.campaignId === req.campaignId)).sort(byCreated);
      const { rows: assets, next } = page(rows, req.pageSize, 50, req.pageToken);
      return { assets, nextPageToken: next };
    },
    getAsset: (req) => ({ asset: asset(req.orgId, req.id) }),
    createAssetUpload: (req) => {
      org(req.orgId);
      throw new ConnectError("asset storage is not configured in the preview", Code.Unavailable);
    },
    confirmAssetUpload: (req) => {
      org(req.orgId);
      throw new ConnectError("asset storage is not configured in the preview", Code.Unavailable);
    },
    getAssetDownload: (req) => {
      const found = asset(req.orgId, req.assetId);
      if (found.status !== 2 /* READY */) throw precondition("the asset is not ready: its upload has not been confirmed");
      return { url: gtmAssetImageUrl(found.id), expiresAt: timestampFromMs(demoNow() + DOWNLOAD_TTL_MS) };
    },
    deleteAsset: (req) => {
      const all = org(req.orgId);
      const found = asset(req.orgId, req.assetId);
      all.assets = all.assets.filter((a) => a.id !== found.id);
      return {};
    },

    // ── Strategy library ─────────────────────────────────────────────────────
    listStrategyLibrary: (req) => {
      const query = req.query?.toLowerCase();
      const rows = org(req.orgId).library
        .filter((e) =>
          (req.category === undefined || e.category === req.category) && (req.depth === undefined || e.depth === req.depth) && (req.stage === undefined || e.stage === req.stage) &&
          (req.cost === undefined || e.cost === req.cost) && (req.difficulty === undefined || e.difficulty === req.difficulty) && (req.fit === undefined || e.fits.includes(req.fit)) &&
          (!query || e.name.toLowerCase().includes(query) || e.summaryMd.toLowerCase().includes(query)),
        )
        .sort((a, b) => a.category - b.category || a.name.localeCompare(b.name));
      const { rows: entries, next } = page(rows, req.pageSize, 50, req.pageToken);
      return { entries, nextPageToken: next };
    },
    getStrategyLibraryEntry: (req) => ({ entry: org(req.orgId).library.find((e) => e.key === req.key) ?? (() => { throw notFound("library entry"); })() }),
    // The web user is a person-class credential, so a prune here removes at once (a token's prune is staged by the service, which the sample's pending request stands in for).
    importStrategyLibrary: (req) => {
      const all = org(req.orgId);
      if (req.entries.length < 1 || req.entries.length > MAX_IMPORT_BATCH) throw invalid("entries", `1 to ${MAX_IMPORT_BATCH} per call`);
      const keys = new Set<string>();
      req.entries.forEach((e, i) => {
        if (!LIBRARY_KEY.test(e.key)) throw invalid(`entries[${i}].key`, "a slug: [a-z0-9][a-z0-9_-]{0,127}");
        if (keys.has(e.key)) throw invalid(`entries[${i}].key`, `"${e.key}" twice in the batch`);
        keys.add(e.key);
        if (!e.name) throw invalid(`entries[${i}].name`, "required");
        if (e.category === 0) throw invalid(`entries[${i}].category`, "required, one of the 13");
        if (e.depth === 0) throw invalid(`entries[${i}].depth`, "required: PLAY or TACTIC");
      });
      if (req.prune) {
        if (req.allKeys.length === 0) throw invalid("all_keys", "required with prune: every key of the library");
        const complete = new Set(req.allKeys);
        for (const k of keys) if (!complete.has(k)) throw invalid("all_keys", `"${k}" is not in all_keys`);
      }
      const stamp = { sourceRevision: req.sourceRevision, importedAt: nowTs(), importedBy: GTM_SAMPLE_PERSON };
      let created = 0, updated = 0, unchanged = 0;
      for (const incoming of req.entries) {
        const at = all.library.findIndex((e) => e.key === incoming.key);
        const next = create(GtmStrategyLibraryEntrySchema, { ...incoming, ...stamp });
        if (at < 0) created++;
        else if (equals(GtmStrategyLibraryEntrySchema, { ...all.library[at]!, ...stamp }, next)) unchanged++;
        else updated++;
        if (at < 0) all.library.push(next);
        else all.library[at] = next;
      }
      let pruned = 0;
      if (req.prune) {
        const complete = new Set(req.allKeys);
        const before = all.library.length;
        all.library = all.library.filter((e) => complete.has(e.key));
        pruned = before - all.library.length;
      }
      all.libraryImports.unshift(create(GtmLibraryImportSchema, {
        id: nextId("gtm-import"), orgId: req.orgId, actorUserId: GTM_SAMPLE_PERSON, credentialKind: GtmCredentialKind.PERSON, sourceRevision: req.sourceRevision,
        created, updated, unchanged, pruned, createdAt: nowTs(),
      }));
      return { created, updated, unchanged, pruned, pruneRequestId: "", wouldRemove: [] };
    },
    listLibraryImports: (req) => {
      const { rows: imports, next } = page([...org(req.orgId).libraryImports].sort(byCreated), req.pageSize, 50, req.pageToken);
      return { imports, nextPageToken: next };
    },
    listLibraryPruneRequests: (req) => {
      const rows = org(req.orgId).pruneRequests.map(withExpiry).filter((r) => req.status === undefined || r.status === req.status).sort(byCreated);
      const { rows: requests, next } = page(rows, req.pageSize, 50, req.pageToken);
      return { requests, nextPageToken: next };
    },
    confirmLibraryPrune: (req) => {
      const all = org(req.orgId);
      const current = withExpiry(pruneRequest(req.orgId, req.requestId));
      if (current.status === GtmPruneRequestStatus.EXPIRED) throw precondition("the prune request expired; ask for a new one");
      if (current.status !== GtmPruneRequestStatus.PENDING) throw precondition("the prune request was already decided");
      // Only keys still present and not imported again since the request was made.
      const before = all.library.length;
      all.library = all.library.filter((e) => !current.removeKeys.includes(e.key) || ms(e.importedAt) > ms(current.createdAt));
      const next = create(GtmLibraryPruneRequestSchema, { ...current, status: GtmPruneRequestStatus.CONFIRMED, decidedBy: GTM_SAMPLE_PERSON, decidedAt: nowTs() });
      put(all.pruneRequests, next);
      return { removed: before - all.library.length, request: next };
    },
    cancelLibraryPrune: (req) => {
      const all = org(req.orgId);
      const current = withExpiry(pruneRequest(req.orgId, req.requestId));
      if (current.status === GtmPruneRequestStatus.EXPIRED) throw precondition("the prune request expired");
      if (current.status !== GtmPruneRequestStatus.PENDING) throw precondition("the prune request was already decided");
      const next = create(GtmLibraryPruneRequestSchema, { ...current, status: GtmPruneRequestStatus.CANCELLED, decidedBy: GTM_SAMPLE_PERSON, decidedAt: nowTs() });
      return { request: put(all.pruneRequests, next) };
    },
    useLibraryStrategy: (req) => {
      const all = org(req.orgId);
      const entry: GtmStrategyLibraryEntry = all.library.find((e) => e.key === req.libraryKey) ?? (() => { throw notFound("library entry"); })();
      if (req.campaignName !== undefined && !req.createFirstCampaign) throw invalid("campaign_name", "only with create_first_campaign");
      const created = put(all.strategies, create(GtmStrategySchema, {
        id: nextId("gtm-str"), orgId: req.orgId, name: entry.name, goalMd: [entry.northStar, entry.objective].filter((p) => p.trim()).join("\n\n"), targetMetric: entry.kpis.join(", "),
        status: GtmStrategyStatus.DRAFT, category: entry.category, libraryKey: entry.key, revision: 1, createdBy: GTM_SAMPLE_PERSON, updatedBy: GTM_SAMPLE_PERSON, createdAt: nowTs(), updatedAt: nowTs(),
      }));
      if (!req.createFirstCampaign) return { strategy: created };
      // The org's ACTIVE channels that fit: a channel type in common, or a key that is one of the entry's types.
      const planned: string[] = [];
      for (const type of entry.channelTypes) {
        for (const ch of all.channels) {
          if (ch.status === GtmChannelStatus.ACTIVE && !planned.includes(ch.id) && (ch.key === type || ch.channelTypes.includes(type))) planned.push(ch.id);
        }
      }
      const first = put(all.campaigns, create(GtmCampaignSchema, {
        id: nextId("gtm-cmp"), orgId: req.orgId, strategyId: created.id, name: req.campaignName ?? entry.name, status: GtmCampaignStatus.DRAFT,
        plannedChannelIds: planned.slice(0, 50), plannedFormats: [], revision: 1, createdAt: nowTs(), updatedAt: nowTs(),
      }));
      return { strategy: created, campaign: first };
    },

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
      org(req.orgId);
      link(req.orgId, req.campaignId, "campaign");
      const created = create(GtmIdeaSchema, {
        id: nextId("gtm-idea"), orgId: req.orgId, kind: req.kind, title: req.title, bodyMd: req.bodyMd, evidence: req.evidence, status: GtmIdeaStatus.PROPOSED,
        source: GtmIdeaSource.HUMAN, sourceChatId: req.sourceChatId, createdBy: GTM_SAMPLE_PERSON, campaignId: req.campaignId, revision: 1, createdAt: nowTs(), updatedAt: nowTs(),
      });
      return { idea: put(state().ideas, created) };
    },
    updateIdea: (req) => {
      const current = idea(req.orgId, req.id);
      checkRevision(req.id, req.expectedRevision, current.revision);
      link(req.orgId, req.campaignId, "campaign");
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
      // The status it already has is no write: no revision bump, no new updated_at.
      if (current.status === req.status) return { idea: current };
      const next = create(GtmIdeaSchema, { ...current, status: req.status, revision: current.revision + 1, updatedAt: nowTs() });
      return { idea: put(state().ideas, next) };
    },

    // ── Campaigns ────────────────────────────────────────────────────────────
    listCampaigns: (req) => {
      const rows = org(req.orgId).campaigns
        .filter((c) => (req.status === undefined || c.status === req.status) && (req.strategyId === undefined || c.strategyId === req.strategyId))
        .sort(byCreated);
      const { rows: campaigns, next } = page(rows, req.pageSize, 50, req.pageToken);
      return { campaigns, nextPageToken: next };
    },
    getCampaign: (req) => {
      const found = campaign(req.orgId, req.id);
      const all = state();
      const items = all.items.filter((i) => i.campaignId === found.id);
      const ideas = all.ideas.filter((i) => i.campaignId === found.id).sort(byCreated);
      const sorted = sortedItems(items);
      // Each list is capped; truncated says either was cut. Coverage counts every item.
      return {
        campaign: found,
        ideas: ideas.slice(0, MAX_CAMPAIGN_ROWS),
        items: sorted.slice(0, MAX_CAMPAIGN_ROWS),
        truncated: ideas.length > MAX_CAMPAIGN_ROWS || sorted.length > MAX_CAMPAIGN_ROWS,
        coverage: coverageOf(found, items, all.channels),
      };
    },
    createCampaign: (req) => {
      org(req.orgId);
      if (!req.name) throw invalid("name", "required");
      if (!req.strategyId) throw invalid("strategy_id", "required");
      strategy(req.orgId, req.strategyId);
      if (req.periodStart && req.periodEnd && ms(req.periodEnd) < ms(req.periodStart)) throw invalid("period_end", "before period_start");
      checkPlanned(req.orgId, req.plannedChannelIds);
      checkFormats(req.plannedFormats);
      const created = create(GtmCampaignSchema, {
        id: nextId("gtm-cmp"), orgId: req.orgId, strategyId: req.strategyId, name: req.name, goal: req.goal, periodStart: req.periodStart, periodEnd: req.periodEnd,
        status: req.status ?? GtmCampaignStatus.DRAFT, plannedChannelIds: req.plannedChannelIds, plannedFormats: req.plannedFormats, revision: 1, createdAt: nowTs(), updatedAt: nowTs(),
      });
      return { campaign: put(state().campaigns, created) };
    },
    updateCampaign: (req) => {
      const current = campaign(req.orgId, req.id);
      checkRevision(req.id, req.expectedRevision, current.revision);
      if (req.strategyId !== undefined) {
        if (!req.strategyId) throw invalid("strategy_id", "a campaign always has a strategy");
        strategy(req.orgId, req.strategyId);
      }
      if (req.plannedChannelIds) checkPlanned(req.orgId, req.plannedChannelIds.ids);
      if (req.plannedFormats) checkFormats(req.plannedFormats.formats);
      const next = create(GtmCampaignSchema, {
        ...current,
        name: req.name ?? current.name,
        goal: req.goal ?? current.goal,
        periodStart: req.clearPeriodStart ? undefined : (req.periodStart ?? current.periodStart),
        periodEnd: req.clearPeriodEnd ? undefined : (req.periodEnd ?? current.periodEnd),
        status: req.status ?? current.status,
        strategyId: req.strategyId ?? current.strategyId,
        plannedChannelIds: req.plannedChannelIds ? req.plannedChannelIds.ids : current.plannedChannelIds,
        plannedFormats: req.plannedFormats ? req.plannedFormats.formats : current.plannedFormats,
        revision: current.revision + 1,
        updatedAt: nowTs(),
      });
      if (next.periodStart && next.periodEnd && ms(next.periodEnd) < ms(next.periodStart)) throw invalid("period_end", "before period_start");
      return { campaign: put(state().campaigns, next) };
    },

    // ── Items ────────────────────────────────────────────────────────────────
    listItems: (req) => {
      const from = req.scheduledFrom ? ms(req.scheduledFrom) : null;
      const to = req.scheduledTo ? ms(req.scheduledTo) : null;
      if (from !== null && to !== null && to <= from) throw invalid("scheduled_to", "must be after scheduled_from");
      const ranged = from !== null || to !== null;
      const rows = org(req.orgId).items.filter((i) => {
        if (req.campaignId !== undefined && i.campaignId !== req.campaignId) return false;
        if (req.channelId !== undefined && i.channelId !== req.channelId) return false;
        if (req.format !== undefined && i.format !== req.format) return false;
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
      org(req.orgId);
      if (req.format === GtmItemFormat.UNSPECIFIED) throw invalid("format", "required");
      if (req.status !== undefined && req.status !== GtmItemStatus.DRAFT && req.status !== GtmItemStatus.REVIEW) throw invalid("status", "an item is created DRAFT or REVIEW");
      if (!req.title) throw invalid("title", "required");
      checkEmailFields(req.format, req.emailTo, req.emailSubject);
      link(req.orgId, req.campaignId, "campaign");
      link(req.orgId, req.ideaId, "idea");
      link(req.orgId, req.channelId, "channel");
      const created = create(GtmItemSchema, {
        id: nextId("gtm-item"), orgId: req.orgId, campaignId: req.campaignId, ideaId: req.ideaId, format: req.format, channelId: req.channelId, title: req.title, bodyMd: req.bodyMd,
        status: req.status ?? GtmItemStatus.DRAFT, scheduledAt: req.scheduledAt, emailTo: req.emailTo, emailSubject: req.emailSubject, revision: 1, createdAt: nowTs(), updatedAt: nowTs(),
      });
      return { item: put(state().items, created) };
    },
    updateItem: (req) => {
      const current = item(req.orgId, req.id);
      if (req.status === GtmItemStatus.SENT) throw invalid("status", "SENT is written only when the approved email is sent");
      if (req.title === "") throw invalid("title", "required");
      if (req.publishedUrl && !/^https?:\/\//.test(req.publishedUrl)) throw invalid("published_url", "an http(s) URL");
      if (current.status === GtmItemStatus.SENT) throw precondition("a sent item cannot be changed");
      if (isHeld(current)) throw precondition("a send of this item was started and not recorded; check the sent mail in Gmail, then resolve the send");
      checkRevision(req.id, req.expectedRevision, current.revision);
      link(req.orgId, req.campaignId, "campaign");
      link(req.orgId, req.ideaId, "idea");
      if (req.channelId !== undefined) link(req.orgId, req.channelId, "channel");

      const draft = create(GtmItemSchema, {
        ...current,
        format: req.format ?? current.format,
        title: req.title ?? current.title,
        bodyMd: req.bodyMd ?? current.bodyMd,
        channelId: req.channelId ?? current.channelId,
        campaignId: req.campaignId ?? current.campaignId,
        ideaId: req.ideaId ?? current.ideaId,
        publishedUrl: req.publishedUrl ?? current.publishedUrl,
        scheduledAt: req.clearScheduledAt ? undefined : (req.scheduledAt ?? current.scheduledAt),
        emailTo: req.emailTo ? req.emailTo.addresses : current.emailTo,
        emailSubject: req.emailSubject ?? current.emailSubject,
      });
      // A content edit of an APPROVED or SCHEDULED item returns it to REVIEW.
      let from = current.status;
      if (contentOf(draft) !== contentOf(current) && (from === GtmItemStatus.APPROVED || from === GtmItemStatus.SCHEDULED)) from = GtmItemStatus.REVIEW;
      let status = from;
      if (req.status !== undefined && req.status !== from) {
        checkTransition(draft.format, from, req.status);
        status = req.status;
      }
      if (isEmailType(draft.format) && (status === GtmItemStatus.SCHEDULED || status === GtmItemStatus.PUBLISHED)) {
        throw precondition("an email is never scheduled or published; it stays APPROVED until it is sent");
      }
      checkEmailFields(draft.format, draft.emailTo, draft.emailSubject);
      if (status === GtmItemStatus.PUBLISHED && !draft.publishedUrl && draft.channelId && channel(req.orgId, draft.channelId).mode === GtmChannelMode.MANUAL) {
        throw precondition("an item on a manual channel needs its published URL: record where it went out");
      }
      if (status === GtmItemStatus.SCHEDULED && !draft.scheduledAt) {
        if (current.status === GtmItemStatus.SCHEDULED) throw precondition("a scheduled item keeps its date; to take it off the calendar, pause it, return it to draft and approve it again");
        throw invalid("scheduled_at", "a scheduled item needs a date");
      }
      const next = create(GtmItemSchema, { ...draft, status, revision: current.revision + 1, updatedAt: nowTs() });
      return { item: put(state().items, next) };
    },
    resolveItemSend: (req) => {
      const current = item(req.orgId, req.id);
      if (current.status === GtmItemStatus.SENT) throw precondition("the item was already sent");
      if (!isHeld(current)) throw precondition("the item has no send in doubt");
      if (current.approvalHash !== req.approvalHash) throw precondition("the approval does not match the item as it is now; approve it again");
      // With a Gmail id the person found the email: SENT. Without, it did not go out: the hold is released and the item stays APPROVED.
      const next = req.gmailMessageId !== undefined
        ? create(GtmItemSchema, { ...current, status: GtmItemStatus.SENT, gmailMessageId: req.gmailMessageId, sentAt: nowTs(), sentBy: GTM_SAMPLE_PERSON, revision: current.revision + 1, updatedAt: nowTs() })
        : create(GtmItemSchema, { ...current, approvalHash: "", revision: current.revision + 1, updatedAt: nowTs() });
      return { item: put(state().items, next) };
    },

    // ── Positioning ──────────────────────────────────────────────────────────
    getPositioning: (req) => ({ positioning: org(req.orgId).positioning }),
    updatePositioning: (req) => {
      const current = org(req.orgId).positioning;
      checkRevision("positioning", req.expectedRevision, current.revision);
      const next = create(GtmPositioningSchema, {
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
