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
 */

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

export function CoveragePanel({ name, rows, channels }: { name: string; rows: GtmCoverageRow[]; channels: GtmChannel[] }) {
  const gaps = rows.filter(isGap);
  return (
    <section aria-labelledby="gtm-coverage" className="rounded-lg border border-border bg-surface-muted p-4" data-testid="gtm-campaign-coverage">
      <div className="mb-3 flex flex-wrap items-baseline justify-between gap-2">
        <h2 id="gtm-coverage" className="text-sm font-semibold text-ink">
          Coverage: {name}
        </h2>
        {rows.length > 0 && (
          <span className={`text-sm ${gaps.length ? "text-warning-ink" : "text-ink-muted"}`}>
            {gaps.length === 0 ? "Everything planned has something drafted." : `${gaps.length} planned ${gaps.length === 1 ? "pair has" : "pairs have"} nothing drafted.`}
          </span>
        )}
      </div>
      {rows.length === 0 ? (
        <p className="text-sm text-ink-muted">No channels or formats planned, and nothing drafted yet. Edit the campaign to plan where it shows up.</p>
      ) : (
        <ul className="divide-y divide-border rounded-lg border border-border bg-surface">
          {rows.map((row) => {
            const channel = channels.find((c) => c.id === row.channelId);
            const gap = isGap(row);
            return (
              <li key={`${row.channelId}|${row.format}|${row.planned}`} className="flex items-center justify-between gap-3 px-3 py-2 text-sm" data-coverage-gap={gap || undefined}>
                <span className="flex min-w-0 items-center gap-2">
                  {channel && <span aria-hidden="true" className="size-2 shrink-0 rounded-full" style={{ background: channelColor(channel) }} />}
                  <span className="truncate font-medium text-ink">{coverageLabel(row, channels)}</span>
                  {!row.planned && <Tag>Not planned</Tag>}
                </span>
                <span className={`shrink-0 text-right tabular-nums ${gap ? "font-medium text-warning-ink" : "text-ink-muted"}`}>{coverageSummary(row)}</span>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
