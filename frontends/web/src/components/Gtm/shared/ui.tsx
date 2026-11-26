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
import Tag from "@/components/forge-ui/tag";
import {
  GtmCampaignStatus,
  GtmClaimStatus,
  GtmIdeaKind,
  GtmIdeaSource,
  GtmIdeaStatus,
  GtmItemKind,
  GtmItemStatus,
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

export const ITEM_KIND: Partial<Record<GtmItemKind, string>> = {
  [GtmItemKind.BLOG_POST]: "Blog post",
  [GtmItemKind.SOCIAL_POST]: "Social post",
  [GtmItemKind.EMAIL]: "Email",
  [GtmItemKind.LAUNCH]: "Launch",
  [GtmItemKind.OTHER]: "Other",
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

/** The calendar's channels; the colours are the calendar's categorical palette. A channel is free-form, so others fall back. */
export const CHANNELS: { id: string; label: string; color: string }[] = [
  { id: "blog", label: "Blog", color: "#6366f1" },
  { id: "email", label: "Email", color: "#0ea5e9" },
  { id: "newsletter", label: "Newsletter", color: "#0ea5e9" },
  { id: "x", label: "X", color: "#64748b" },
  { id: "linkedin", label: "LinkedIn", color: "#10b981" },
  { id: "hn", label: "Hacker News", color: "#f59e0b" },
  { id: "changelog", label: "Changelog", color: "#8b5cf6" },
];

export const channelLabel = (id: string) => CHANNELS.find((c) => c.id === id)?.label ?? (id || "No channel");

/** "Email", or "Blog post · Blog": the kind, and the channel when it says more. */
export const kindAndChannel = (kind: GtmItemKind, channel: string) =>
  kind === GtmItemKind.EMAIL ? ITEM_KIND[kind]! : `${ITEM_KIND[kind] ?? "Item"} · ${channelLabel(channel)}`;

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
