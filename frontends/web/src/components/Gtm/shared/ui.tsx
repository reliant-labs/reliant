// Copyright (c) 2026 Reliant Labs

/**
 * GTM's labels and small pieces: status and source badges, kind names, the
 * calendar's channels, date formats. One place, so every page names a status
 * the same way. Keyed on the generated GtmService enums (gen/reliant/v1/gtm_pb).
 */

import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { Bot, UserRound } from "lucide-react";
import type { ReactNode } from "react";

import Badge from "@/components/forge-ui/badge";
import Link from "@/components/forge-ui/link";
import Tag from "@/components/forge-ui/tag";
import {
  GtmBusinessModel,
  GtmBusinessType,
  GtmCampaignStatus,
  GtmChannelMode,
  GtmChannelSource,
  GtmClaimStatus,
  GtmConnectionState,
  GtmIdeaKind,
  GtmIdeaSource,
  GtmIdeaStatus,
  GtmItemFormat,
  GtmItemStatus,
  GtmStrategyCategory,
  GtmStrategyStatus,
  type GtmChannel,
} from "@/gen/reliant/v1/gtm_pb";

// No "info": the forge-ui info variant is accent-on-accent-surface, and
// Reliant's light accent is a near-white grey, so it does not read.
type BadgeVariant = "success" | "warning" | "error" | "neutral";

interface Label {
  label: string;
  variant: BadgeVariant;
}

const UNKNOWN: Label = { label: "Unknown", variant: "neutral" };

export const IDEA_STATUS: Partial<Record<GtmIdeaStatus, Label>> = {
  [GtmIdeaStatus.PROPOSED]: { label: "Proposed", variant: "warning" },
  [GtmIdeaStatus.ACCEPTED]: { label: "Accepted", variant: "success" },
  [GtmIdeaStatus.DONE]: { label: "Done", variant: "neutral" },
  [GtmIdeaStatus.REJECTED]: { label: "Rejected", variant: "error" },
};

export const IDEA_KIND: Partial<Record<GtmIdeaKind, string>> = {
  [GtmIdeaKind.PRODUCT]: "Product",
  [GtmIdeaKind.GTM]: "GTM",
  [GtmIdeaKind.CONTENT]: "Content",
};

export const ITEM_STATUS: Partial<Record<GtmItemStatus, Label>> = {
  [GtmItemStatus.DRAFT]: { label: "Draft", variant: "neutral" },
  [GtmItemStatus.REVIEW]: { label: "In review", variant: "warning" },
  [GtmItemStatus.APPROVED]: { label: "Approved", variant: "success" },
  [GtmItemStatus.SCHEDULED]: { label: "Scheduled", variant: "success" },
  [GtmItemStatus.SENT]: { label: "Sent", variant: "neutral" },
  [GtmItemStatus.PUBLISHED]: { label: "Published", variant: "neutral" },
  [GtmItemStatus.PAUSED]: { label: "Paused", variant: "neutral" },
};

export const ITEM_FORMAT: Partial<Record<GtmItemFormat, string>> = {
  [GtmItemFormat.SHORT_POST]: "Short post",
  [GtmItemFormat.LONG_POST]: "Long post",
  [GtmItemFormat.EMAIL]: "Email",
  [GtmItemFormat.COLD_OUTREACH]: "Cold outreach",
  [GtmItemFormat.VIDEO]: "Video",
  [GtmItemFormat.MEDIA_PITCH]: "Media pitch",
  [GtmItemFormat.AD]: "Ad",
  [GtmItemFormat.OTHER]: "Other",
};

/** Every format, in the order a picker lists them. */
export const ITEM_FORMATS: GtmItemFormat[] = [
  GtmItemFormat.SHORT_POST,
  GtmItemFormat.LONG_POST,
  GtmItemFormat.EMAIL,
  GtmItemFormat.COLD_OUTREACH,
  GtmItemFormat.VIDEO,
  GtmItemFormat.MEDIA_PITCH,
  GtmItemFormat.AD,
  GtmItemFormat.OTHER,
];

export const formatLabel = (format: GtmItemFormat) => ITEM_FORMAT[format] ?? "Any format";

/** Email-type formats (EMAIL, COLD_OUTREACH): sent only through the agent's approved send, never scheduled or published. */
export const isEmailFormat = (format: GtmItemFormat) => format === GtmItemFormat.EMAIL || format === GtmItemFormat.COLD_OUTREACH;

export const STRATEGY_STATUS: Partial<Record<GtmStrategyStatus, Label>> = {
  [GtmStrategyStatus.DRAFT]: { label: "Draft", variant: "neutral" },
  [GtmStrategyStatus.ACTIVE]: { label: "Active", variant: "success" },
  [GtmStrategyStatus.DONE]: { label: "Done", variant: "neutral" },
  [GtmStrategyStatus.ARCHIVED]: { label: "Archived", variant: "neutral" },
};

/** The 13 strategy categories, in the catalog's order, with their display labels (GtmStrategyCategory). */
export const STRATEGY_CATEGORIES: { id: GtmStrategyCategory; label: string }[] = [
  { id: GtmStrategyCategory.PRICING_OFFERS_INCENTIVES, label: "Pricing, offers & incentives" },
  { id: GtmStrategyCategory.REFERRAL_VIRAL_GROWTH, label: "Referral & viral growth" },
  { id: GtmStrategyCategory.PAID_ACQUISITION, label: "Paid acquisition" },
  { id: GtmStrategyCategory.ORGANIC_CONTENT_SEO, label: "Organic content & SEO" },
  { id: GtmStrategyCategory.OUTBOUND_SALES_PROSPECTING, label: "Outbound sales & prospecting" },
  { id: GtmStrategyCategory.PARTNERSHIPS_DISTRIBUTION, label: "Partnerships & distribution" },
  { id: GtmStrategyCategory.LAUNCHES_PR, label: "Launches & PR" },
  { id: GtmStrategyCategory.COMMUNITY_EVENTS, label: "Community & events" },
  { id: GtmStrategyCategory.PRODUCT_LED_GROWTH, label: "Product-led growth" },
  { id: GtmStrategyCategory.CONVERSION_OPTIMIZATION, label: "Conversion optimization" },
  { id: GtmStrategyCategory.RETENTION_EXPANSION, label: "Retention & expansion" },
  { id: GtmStrategyCategory.TRUST_REPUTATION, label: "Trust & reputation" },
  { id: GtmStrategyCategory.MARKET_RESEARCH_POSITIONING, label: "Market research & positioning" },
];

export const categoryLabel = (id: GtmStrategyCategory) => STRATEGY_CATEGORIES.find((c) => c.id === id)?.label ?? "No category";

/** What kind of business an org is (GtmBusinessType), with display labels, in the proto's order. */
export const BUSINESS_TYPES: { id: GtmBusinessType; label: string }[] = [
  { id: GtmBusinessType.B2B_SAAS, label: "B2B SaaS" },
  { id: GtmBusinessType.B2C_APP, label: "Consumer app" },
  { id: GtmBusinessType.DEVELOPER_TOOLS, label: "Developer tools" },
  { id: GtmBusinessType.ECOMMERCE, label: "Ecommerce" },
  { id: GtmBusinessType.MARKETPLACE, label: "Marketplace" },
  { id: GtmBusinessType.AGENCY_SERVICES, label: "Agency & services" },
  { id: GtmBusinessType.CREATOR_MEDIA, label: "Creator & media" },
  { id: GtmBusinessType.LOCAL_BUSINESS, label: "Local business" },
  { id: GtmBusinessType.OPEN_SOURCE, label: "Open source" },
  { id: GtmBusinessType.HARDWARE, label: "Hardware" },
];

/** How an org makes money (GtmBusinessModel), with display labels, in the proto's order. */
export const BUSINESS_MODELS: { id: GtmBusinessModel; label: string }[] = [
  { id: GtmBusinessModel.SUBSCRIPTION, label: "Subscription" },
  { id: GtmBusinessModel.USAGE_BASED, label: "Usage-based" },
  { id: GtmBusinessModel.ONE_TIME, label: "One-time purchase" },
  { id: GtmBusinessModel.FREEMIUM, label: "Freemium" },
  { id: GtmBusinessModel.FREE_OSS, label: "Free / open source" },
  { id: GtmBusinessModel.MARKETPLACE, label: "Marketplace fees" },
  { id: GtmBusinessModel.SERVICES, label: "Services" },
  { id: GtmBusinessModel.AD_SUPPORTED, label: "Ad-supported" },
];

export const businessTypeLabel = (id: GtmBusinessType) => BUSINESS_TYPES.find((t) => t.id === id)?.label ?? "Not set";
export const businessModelLabel = (id: GtmBusinessModel) => BUSINESS_MODELS.find((m) => m.id === id)?.label ?? "Unknown";

/** The library's string vocabularies (GtmStrategyLibraryEntry stages, objective, effort, cost), with labels. */
export const LIBRARY_STAGES = [
  { id: "pre_launch", label: "Pre-launch" },
  { id: "launch", label: "Launch" },
  { id: "early_traction", label: "Early traction" },
  { id: "growth", label: "Growth" },
  { id: "scale", label: "Scale" },
] as const;
export const LIBRARY_OBJECTIVES = [
  { id: "awareness", label: "Awareness" },
  { id: "acquisition", label: "Acquisition" },
  { id: "activation", label: "Activation" },
  { id: "conversion", label: "Conversion" },
  { id: "retention", label: "Retention" },
  { id: "expansion", label: "Expansion" },
  { id: "referral", label: "Referral" },
  { id: "research", label: "Research" },
] as const;
export const LIBRARY_EFFORTS = [
  { id: "low", label: "Low" },
  { id: "medium", label: "Medium" },
  { id: "high", label: "High" },
] as const;
export const LIBRARY_COSTS = [
  { id: "free", label: "Free" },
  { id: "low", label: "Low" },
  { id: "medium", label: "Medium" },
  { id: "high", label: "High" },
] as const;

/** A library vocabulary value's label; an unknown value as written. */
export const vocabLabel = (list: readonly { id: string; label: string }[], value: string) => list.find((v) => v.id === value)?.label ?? value;

export const CONNECTION_STATE: Partial<Record<GtmConnectionState, Label>> = {
  [GtmConnectionState.CONNECTED]: { label: "Connected", variant: "success" },
  [GtmConnectionState.NOT_CONNECTED]: { label: "Not connected", variant: "warning" },
  [GtmConnectionState.UNKNOWN]: { label: "Status unknown", variant: "neutral" },
};

export const CAMPAIGN_STATUS: Partial<Record<GtmCampaignStatus, Label>> = {
  [GtmCampaignStatus.DRAFT]: { label: "Draft", variant: "neutral" },
  [GtmCampaignStatus.ACTIVE]: { label: "Active", variant: "success" },
  [GtmCampaignStatus.DONE]: { label: "Done", variant: "neutral" },
  [GtmCampaignStatus.ARCHIVED]: { label: "Archived", variant: "neutral" },
};

export const CLAIM_STATUS: Partial<Record<GtmClaimStatus, Label>> = {
  [GtmClaimStatus.VERIFIED]: { label: "Verified by a person", variant: "success" },
  [GtmClaimStatus.UNVERIFIED]: { label: "Not verified", variant: "warning" },
  [GtmClaimStatus.RETIRED]: { label: "Retired", variant: "neutral" },
};

/** Whether an item is out (sent or published): what is out is not edited, moved or approved. */
export function isItemOut(status: GtmItemStatus): boolean {
  return status === GtmItemStatus.SENT || status === GtmItemStatus.PUBLISHED;
}

/** Each default channel's colour, by key (the calendar's categorical palette). */
const CHANNEL_COLORS: Record<string, string> = {
  blog: "#6366f1",
  email: "#0ea5e9",
  x: "#64748b",
  linkedin: "#10b981",
  "hacker-news": "#f59e0b",
  reddit: "#f97316",
  youtube: "#ef4444",
};

/** The colours for every other channel: none of the defaults' hues. */
const EXTRA_CHANNEL_COLORS = ["#8b5cf6", "#ec4899", "#14b8a6", "#84cc16", "#a16207", "#06b6d4", "#d946ef", "#78716c", "#e11d48", "#22c55e"];

/**
 * A channel's colour. A default channel keeps its own; any other takes the
 * next extra colour in the order of its key among the org's other channels
 * (`channels`), so the same channels always get the same colours and no two
 * share one until the palette runs out. Without the org's channels, the key
 * picks one.
 */
export function channelColor(channel: Pick<GtmChannel, "key">, channels?: readonly Pick<GtmChannel, "key">[]): string {
  const fixed = CHANNEL_COLORS[channel.key];
  if (fixed) return fixed;
  const others = channels ? [...new Set(channels.map((c) => c.key).filter((k) => !CHANNEL_COLORS[k]))].sort() : [];
  let index = others.indexOf(channel.key);
  if (index < 0) index = [...channel.key].reduce((h, ch) => (h * 31 + ch.charCodeAt(0)) >>> 0, 7);
  return EXTRA_CHANNEL_COLORS[index % EXTRA_CHANNEL_COLORS.length] ?? "#8b5cf6";
}

export const isManualChannel = (channel: GtmChannel | undefined) => channel?.mode === GtmChannelMode.MANUAL;

/**
 * A URL an agent or a person wrote (where an item went out, evidence, a
 * fact's source), as a link target: only http(s). Anything else is shown as
 * text, never followed.
 */
export function externalHref(url: string): string | null {
  return /^https?:\/\/\S+$/i.test(url.trim()) ? url.trim() : null;
}

/** A link to a URL someone wrote: a new tab, no opener, no referrer; plain text when it is not http(s). */
export function OutboundLink({ url, children, className }: { url: string; children?: ReactNode; className?: string }) {
  const href = externalHref(url);
  if (!href) return <span className={className}>{children ?? url}</span>;
  return (
    <Link href={href} target="_blank" rel="noopener noreferrer" className={className}>
      {children ?? url}
    </Link>
  );
}

/** "Email", or "Long post · Blog": the format, and its channel when that says more. */
export function formatAndChannel(format: GtmItemFormat, channel: GtmChannel | undefined): string {
  if (!channel) return `${formatLabel(format)} · No channel`;
  return format === GtmItemFormat.EMAIL && channel.key === "email" ? formatLabel(format) : `${formatLabel(format)} · ${channel.name}`;
}

export function IdeaStatusBadge({ status }: { status: GtmIdeaStatus }) {
  const s = IDEA_STATUS[status] ?? UNKNOWN;
  return <Badge label={s.label} variant={s.variant} size="sm" />;
}

export function ItemStatusBadge({ status }: { status: GtmItemStatus }) {
  const s = ITEM_STATUS[status] ?? UNKNOWN;
  return <Badge label={s.label} variant={s.variant} size="sm" />;
}

export function CampaignStatusBadge({ status }: { status: GtmCampaignStatus }) {
  const s = CAMPAIGN_STATUS[status] ?? UNKNOWN;
  return <Badge label={s.label} variant={s.variant} size="sm" />;
}

export function ClaimStatusBadge({ status }: { status: GtmClaimStatus }) {
  const s = CLAIM_STATUS[status] ?? UNKNOWN;
  return <Badge label={s.label} variant={s.variant} size="sm" dot />;
}

export function StrategyStatusBadge({ status }: { status: GtmStrategyStatus }) {
  const s = STRATEGY_STATUS[status] ?? UNKNOWN;
  return <Badge label={s.label} variant={s.variant} size="sm" />;
}

export function ConnectionBadge({ state }: { state: GtmConnectionState }) {
  const s = CONNECTION_STATE[state];
  return s ? <Badge label={s.label} variant={s.variant} size="sm" dot /> : null;
}

/** Who added a channel: the default set, an agent (badged), or a person. */
export function ChannelSourceTag({ source }: { source: GtmChannelSource }) {
  if (source === GtmChannelSource.AGENT) return <Tag icon={<Bot className="size-3" aria-hidden="true" />}>Added by agent</Tag>;
  if (source === GtmChannelSource.PERSON) return <Tag icon={<UserRound className="size-3" aria-hidden="true" />}>Added by a person</Tag>;
  if (source === GtmChannelSource.SEED) return <Tag>Default</Tag>;
  return null;
}

/** Agent or Human, with the icon carrying the difference as well as the word. */
export function SourceTag({ source }: { source: GtmIdeaSource }) {
  return source === GtmIdeaSource.AGENT ? (
    <Tag icon={<Bot className="size-3" aria-hidden="true" />}>Agent</Tag>
  ) : (
    <Tag icon={<UserRound className="size-3" aria-hidden="true" />}>Human</Tag>
  );
}

const dateFmt = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" });
const dateTimeFmt = new Intl.DateTimeFormat(undefined, { weekday: "short", month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });

/** A timestamp as a Date; null when unset. */
export function tsDate(ts: Timestamp | undefined): Date | null {
  return ts ? timestampDate(ts) : null;
}

/** A timestamp as milliseconds, for sorting; 0 when unset. */
export function tsMs(ts: Timestamp | undefined): number {
  return ts ? timestampDate(ts).getTime() : 0;
}

export function formatDay(ts: Timestamp | Date | undefined | null): string {
  if (!ts) return "—";
  return dateFmt.format(ts instanceof Date ? ts : timestampDate(ts));
}

/** When an item goes out. */
export function formatWhen(ts: Timestamp | undefined): string {
  return ts ? dateTimeFmt.format(timestampDate(ts)) : "Not scheduled";
}

/**
 * Label and value pairs, compact: a record's facts in a dialog or a side card.
 * KeyValueList's `plain` variant spaces each pair a full row apart and
 * right-aligns values, which reads as a sparse table at dialog width.
 */
export function Fields({ fields, columns = 1 }: { fields: { label: string; value: ReactNode; mono?: boolean }[]; columns?: 1 | 2 }) {
  return (
    <dl className={`grid gap-x-6 gap-y-2.5 text-sm ${columns === 2 ? "sm:grid-cols-2" : ""}`}>
      {fields.map((f) => (
        <div key={f.label} className="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-3">
          <dt className="text-ink-muted">{f.label}</dt>
          <dd className={`min-w-0 text-ink ${f.mono ? "font-mono text-xs leading-5" : ""}`}>{f.value}</dd>
        </div>
      ))}
    </dl>
  );
}
