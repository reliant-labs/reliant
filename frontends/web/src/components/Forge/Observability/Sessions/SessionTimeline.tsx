// Copyright (c) 2025 Reliant Labs

/**
 * What the user did and what went wrong, in order: navigations, clicks,
 * failed requests, errors and the app's own events. An error links to its
 * issue in the Errors view (once the issues worker has grouped it); anything
 * that came from a trace links to that trace; and with a recording, a line's
 * time seeks the player there, while the player's playhead marks the line it
 * has reached.
 *
 * Nothing here is raw page content: the server cuts query strings and
 * fragments from paths, names a click's element by tag and xpath (never its
 * text), and masks the volatile parts of an error message.
 */

import { useMemo, useState } from "react";
import { AlertTriangle, Compass, ExternalLink, MousePointerClick, ServerCrash, Sparkles } from "lucide-react";

import Button from "@/components/forge-ui/button";
import type { TimelineEntry, TimelineKind } from "@/services/controlPlane/observability/rumTypes";
import { cn } from "@/lib/utils";

import { formatDuration } from "../format";
import { shortId } from "../LogsView";
import { currentEntryIndex, filterTimeline, formatOffset, navigationLabel, TIMELINE_FILTERS, TIMELINE_KIND_LABEL, type TimelineFilter } from "./sessionModel";

export interface SessionTimelineProps {
  entries: readonly TimelineEntry[];
  /** The session's start (epoch ms): offsets are measured from it. */
  startMs: number;
  /** The player's playhead (epoch ms), or null without a recording. */
  currentMs: number | null;
  hasMore: boolean;
  loadingMore: boolean;
  onLoadMore: () => void;
  /** Seek the player to this moment. Absent without a playable recording. */
  onSeek?: (atMs: number) => void;
  onIssue: (issueId: string) => void;
  onTrace: (traceId: string, spanId?: string) => void;
}

const KIND_ICON: Record<TimelineKind, typeof Compass> = {
  navigation: Compass,
  click: MousePointerClick,
  request_failed: ServerCrash,
  error: AlertTriangle,
  event: Sparkles,
};

export function SessionTimeline({ entries, startMs, currentMs, hasMore, loadingMore, onLoadMore, onSeek, onIssue, onTrace }: SessionTimelineProps) {
  const [filter, setFilter] = useState<TimelineFilter>("all");
  const shown = useMemo(() => filterTimeline(entries, filter), [entries, filter]);
  const current = currentEntryIndex(shown, currentMs);

  return (
    <div className="space-y-2" data-testid="session-timeline">
      <div role="group" aria-label="Show" className="inline-flex flex-wrap rounded-lg border border-border bg-surface-sunken p-0.5">
        {TIMELINE_FILTERS.map((option) => (
          <button
            key={option.id}
            type="button"
            aria-pressed={filter === option.id}
            onClick={() => setFilter(option.id)}
            className={cn(
              "rounded-md px-2.5 py-1 text-xs transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
              filter === option.id ? "bg-surface font-medium text-ink shadow-sm" : "text-ink-muted hover:text-ink"
            )}
            data-testid={`session-timeline-filter-${option.id}`}
          >
            {option.label}
          </button>
        ))}
      </div>
      {shown.length === 0 ? (
        <p className="px-1 py-3 text-xs text-ink-muted" data-testid="session-timeline-empty">
          {entries.length === 0 ? "Nothing was recorded for this session's timeline." : "Nothing of this kind in the session."}
        </p>
      ) : (
        <ol className="divide-y divide-border/40" aria-label="Session timeline">
          {shown.map((entry, index) => (
            <TimelineRow
              key={`${entry.timeMs}-${entry.kind}-${index}`}
              entry={entry}
              startMs={startMs}
              current={index === current}
              onSeek={onSeek}
              onIssue={onIssue}
              onTrace={onTrace}
            />
          ))}
        </ol>
      )}
      {hasMore && (
        <Button variant="ghost" size="sm" onClick={onLoadMore} loading={loadingMore} data-testid="session-timeline-more">
          Show later events
        </Button>
      )}
    </div>
  );
}

function TimelineRow({
  entry,
  startMs,
  current,
  onSeek,
  onIssue,
  onTrace,
}: {
  entry: TimelineEntry;
  startMs: number;
  current: boolean;
  onSeek?: (atMs: number) => void;
  onIssue: (issueId: string) => void;
  onTrace: (traceId: string, spanId?: string) => void;
}) {
  const Icon = entry.kind ? KIND_ICON[entry.kind] : Sparkles;
  const tone = entry.kind === "error" ? "text-danger-ink" : entry.kind === "request_failed" ? "text-warning-ink" : "text-ink-muted";
  const offset = formatOffset(entry.timeMs - startMs);
  return (
    <li
      className={cn("flex gap-2 px-1 py-2 text-xs", current && "bg-surface-sunken")}
      aria-current={current ? "true" : undefined}
      data-testid={`session-entry-${entry.kind ?? "unknown"}`}
      data-time={entry.timeMs}
    >
      {onSeek ? (
        <button
          type="button"
          onClick={() => onSeek(entry.timeMs)}
          className="w-12 shrink-0 text-left tabular-nums text-ink-muted underline-offset-2 hover:text-ink hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          title="Play from here"
          data-testid="session-entry-seek"
        >
          {offset}
        </button>
      ) : (
        <span className="w-12 shrink-0 tabular-nums text-ink-muted">{offset}</span>
      )}
      <Icon className={cn("mt-0.5 h-3.5 w-3.5 shrink-0", tone)} aria-hidden="true" />
      <div className="min-w-0 flex-1 space-y-0.5">
        <p className="flex flex-wrap items-baseline gap-x-2">
          <span className="sr-only">{entry.kind ? TIMELINE_KIND_LABEL[entry.kind] : "Entry"}: </span>
          <EntryHeadline entry={entry} />
        </p>
        <EntryDetail entry={entry} />
        {(entry.issueId || entry.traceId || (entry.kind === "error" && !entry.issueId)) && (
          <p className="flex flex-wrap items-center gap-x-3">
            {entry.kind === "error" &&
              (entry.issueId ? (
                <button
                  type="button"
                  onClick={() => onIssue(entry.issueId)}
                  className="inline-flex items-center gap-1 text-ink underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  data-testid={`session-entry-issue-${entry.issueId}`}
                >
                  <ExternalLink className="h-3 w-3" aria-hidden="true" />
                  Open issue
                </button>
              ) : (
                <span className="text-ink-muted" data-testid="session-entry-ungrouped">
                  Not grouped into an issue yet
                </span>
              ))}
            {entry.traceId && (
              <button
                type="button"
                onClick={() => onTrace(entry.traceId, entry.spanId || undefined)}
                className="inline-flex items-center gap-1 text-ink underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                data-testid={`session-entry-trace-${entry.traceId}`}
              >
                <ExternalLink className="h-3 w-3" aria-hidden="true" />
                Trace <span className="font-mono">{shortId(entry.traceId)}</span>
              </button>
            )}
          </p>
        )}
      </div>
    </li>
  );
}

function EntryHeadline({ entry }: { entry: TimelineEntry }) {
  switch (entry.kind) {
    case "navigation":
      return (
        <>
          <span className="font-medium text-ink">{navigationLabel(entry.name)}</span>
          <span className="break-all font-mono text-ink">{entry.path || "/"}</span>
        </>
      );
    case "click":
      return (
        <>
          <span className="font-medium text-ink">Click</span>
          {entry.element && <span className="font-mono text-ink">&lt;{entry.element.toLowerCase()}&gt;</span>}
          {entry.path && <span className="break-all font-mono text-ink-muted">on {entry.path}</span>}
        </>
      );
    case "request_failed":
      return (
        <>
          <span className="font-medium text-ink">{entry.status > 0 ? entry.status : "Network error"}</span>
          <span className="break-all font-mono text-ink">
            {entry.method || entry.name} {entry.path}
          </span>
          {entry.durationMs > 0 && <span className="text-ink-muted">{formatDuration(entry.durationMs)}</span>}
        </>
      );
    case "error":
      return (
        <>
          <span className="font-medium text-danger-ink">{entry.errorType || "Error"}</span>
          {entry.path && <span className="break-all font-mono text-ink-muted">on {entry.path}</span>}
        </>
      );
    case "event":
      return <span className="font-medium text-ink">{entry.name || "Event"}</span>;
    default:
      return <span className="text-ink">{entry.name || "Recorded entry"}</span>;
  }
}

function EntryDetail({ entry }: { entry: TimelineEntry }) {
  if (entry.kind === "navigation" && entry.props.from) {
    return <p className="break-all font-mono text-ink-muted">from {entry.props.from}</p>;
  }
  if (entry.kind === "click" && entry.selector) {
    return (
      <p className="truncate font-mono text-ink-muted" title={entry.selector}>
        {entry.selector}
      </p>
    );
  }
  if ((entry.kind === "error" || entry.kind === "request_failed") && entry.message) {
    return <p className="whitespace-pre-wrap break-words font-mono text-ink">{entry.message}</p>;
  }
  if (entry.kind === "event") {
    const props = Object.entries(entry.props);
    if (props.length === 0) return null;
    return (
      <p className="flex flex-wrap gap-x-2 font-mono text-ink-muted">
        {props.map(([key, value]) => (
          <span key={key}>
            {key}={value}
          </span>
        ))}
      </p>
    );
  }
  return null;
}
