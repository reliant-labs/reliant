// Copyright (c) 2026 Reliant Labs

/**
 * A campaign's coverage (GetCampaign.coverage): each (channel, format) pair
 * it planned or its items use, with the items counted by status. A planned
 * pair with no item reads plainly: "Reddit · Short post: planned, nothing
 * drafted".
 *
 * One-sided plans (GtmCoverageRow): a campaign that planned only channels
 * has planned rows with no format ("any format"); one that planned only
 * formats has planned rows with no channel ("any channel"). An unplanned row
 * with no channel is items that have none yet.
 *
 * The planned pairs with nothing drafted are gathered per channel, so a
 * broad plan does not read as a wall of identical rows.
 */

import { useState } from "react";

import Tag from "@/components/forge-ui/tag";
import { GtmItemFormat, GtmItemStatus, type GtmChannel, type GtmCoverageRow } from "@/gen/reliant/v1/gtm_pb";

import { channelColor, formatLabel } from "../shared/ui";

/** "2 out · 1 approved · 1 in progress", or "planned, nothing drafted". */
export function coverageSummary(row: GtmCoverageRow): string {
  const n = (statuses: GtmItemStatus[]) => row.counts.filter((c) => statuses.includes(c.status)).reduce((sum, c) => sum + c.count, 0);
  const outCount = n([GtmItemStatus.SENT, GtmItemStatus.PUBLISHED]);
  const approved = n([GtmItemStatus.APPROVED, GtmItemStatus.SCHEDULED]);
  const progress = n([GtmItemStatus.DRAFT, GtmItemStatus.REVIEW, GtmItemStatus.PAUSED]);
  if (row.total === 0) return row.planned ? "planned, nothing drafted" : "nothing yet";
  return [outCount ? `${outCount} out` : "", approved ? `${approved} approved` : "", progress ? `${progress} in progress` : ""].filter(Boolean).join(" · ");
}

/** The pair's name: "Reddit · Short post", "Reddit · any format", "Any channel · Video", "No channel · Long post". */
export function coverageLabel(row: GtmCoverageRow, channels: GtmChannel[]): string {
  const channel = row.channelId ? (channels.find((c) => c.id === row.channelId)?.name ?? "Unknown channel") : row.planned ? "Any channel" : "No channel";
  const format = row.format === GtmItemFormat.UNSPECIFIED ? (row.planned ? "any format" : "no format") : formatLabel(row.format);
  return `${channel} · ${format}`;
}

const isGap = (row: GtmCoverageRow) => row.planned && row.total === 0;

/** One channel's planned pairs with nothing drafted: "Hacker News: Long post, Email, Video". */
export interface CoverageGapGroup {
  channelId: string;
  /** "Hacker News", or "Any channel" for a formats-only plan. */
  channel: string;
  /** The formats with nothing drafted, as labels; "any format" for a channels-only plan. */
  formats: string[];
  rows: GtmCoverageRow[];
}

/** The gaps, one group per channel, in the coverage's own order. */
export function gapGroups(rows: readonly GtmCoverageRow[], channels: readonly GtmChannel[]): CoverageGapGroup[] {
  const groups = new Map<string, CoverageGapGroup>();
  for (const row of rows.filter(isGap)) {
    let g = groups.get(row.channelId);
    if (!g) {
      const channel = row.channelId ? (channels.find((c) => c.id === row.channelId)?.name ?? "Unknown channel") : "Any channel";
      groups.set(row.channelId, (g = { channelId: row.channelId, channel, formats: [], rows: [] }));
    }
    g.formats.push(row.format === GtmItemFormat.UNSPECIFIED ? "any format" : formatLabel(row.format));
    g.rows.push(row);
  }
  return [...groups.values()];
}

function ChannelDot({ channel, channels }: { channel: GtmChannel | undefined; channels: GtmChannel[] }) {
  return channel ? (
    <span aria-hidden="true" className="size-2 shrink-0 rounded-full" style={{ background: channelColor(channel, channels) }} />
  ) : (
    <span aria-hidden="true" className="size-2 shrink-0" />
  );
}

/**
 * What the campaign has drafted, pair by pair, then what it planned and has
 * nothing for: one line per channel ("Hacker News: Long post, Email, Video"),
 * which expands to the pairs themselves.
 */
export function CoveragePanel({ name, rows, channels }: { name: string; rows: GtmCoverageRow[]; channels: GtmChannel[] }) {
  const covered = rows.filter((r) => !isGap(r));
  const groups = gapGroups(rows, channels);
  const gapCount = groups.reduce((n, g) => n + g.rows.length, 0);
  const [showPairs, setShowPairs] = useState(false);
  return (
    <section aria-labelledby="gtm-coverage" className="rounded-lg border border-border bg-surface-muted p-4" data-testid="gtm-campaign-coverage">
      <div className="mb-3 flex flex-wrap items-baseline justify-between gap-2">
        <h2 id="gtm-coverage" className="text-sm font-semibold text-ink">
          Coverage: {name}
        </h2>
        {rows.length > 0 && (
          <span className={`text-sm ${gapCount ? "text-warning-ink" : "text-ink-muted"}`}>
            {gapCount === 0 ? "Everything planned has something drafted." : `${gapCount} planned ${gapCount === 1 ? "pair has" : "pairs have"} nothing drafted.`}
          </span>
        )}
      </div>
      {rows.length === 0 ? (
        <p className="text-sm text-ink-muted">No channels or formats planned, and nothing drafted yet. Edit the campaign to plan where it shows up.</p>
      ) : (
        <div className="space-y-3">
          {covered.length > 0 && (
            <ul className="divide-y divide-border rounded-lg border border-border bg-surface" aria-label="Drafted">
              {covered.map((row) => (
                <li key={`${row.channelId}|${row.format}|${row.planned}`} className="flex items-center justify-between gap-3 px-3 py-2 text-sm">
                  <span className="flex min-w-0 items-center gap-2">
                    <ChannelDot channel={channels.find((c) => c.id === row.channelId)} channels={channels} />
                    <span className="truncate font-medium text-ink">{coverageLabel(row, channels)}</span>
                    {!row.planned && <Tag>Not planned</Tag>}
                  </span>
                  <span className="shrink-0 text-right tabular-nums text-ink-muted">{coverageSummary(row)}</span>
                </li>
              ))}
            </ul>
          )}
          {groups.length > 0 && (
            <div className="rounded-lg border border-border bg-surface" data-testid="gtm-coverage-gaps">
              <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-3 py-2">
                <h3 className="text-sm font-medium text-warning-ink">Planned, nothing drafted</h3>
                <button
                  type="button"
                  className="text-xs text-ink-muted underline underline-offset-2 hover:text-ink"
                  aria-expanded={showPairs}
                  aria-controls="gtm-coverage-gap-list"
                  onClick={() => setShowPairs((v) => !v)}
                >
                  {showPairs ? "Show by channel" : "Show each pair"}
                </button>
              </div>
              <ul id="gtm-coverage-gap-list" className="divide-y divide-border">
                {showPairs
                  ? groups.flatMap((g) =>
                      g.rows.map((row) => (
                        <li key={`${row.channelId}|${row.format}`} className="flex items-center justify-between gap-3 px-3 py-2 text-sm" data-coverage-gap>
                          <span className="flex min-w-0 items-center gap-2">
                            <ChannelDot channel={channels.find((c) => c.id === row.channelId)} channels={channels} />
                            <span className="truncate font-medium text-ink">{coverageLabel(row, channels)}</span>
                          </span>
                          <span className="shrink-0 text-right font-medium text-warning-ink">{coverageSummary(row)}</span>
                        </li>
                      )),
                    )
                  : groups.map((g) => (
                      <li key={g.channelId || "any"} className="flex items-start gap-2 px-3 py-2 text-sm" data-coverage-gap-channel>
                        <span className="mt-1.5 flex">
                          <ChannelDot channel={channels.find((c) => c.id === g.channelId)} channels={channels} />
                        </span>
                        <span className="min-w-0">
                          <span className="font-medium text-ink">{g.channel}:</span> <span className="text-ink-muted">{g.formats.join(", ")}</span>
                        </span>
                      </li>
                    ))}
              </ul>
            </div>
          )}
        </div>
      )}
    </section>
  );
}
