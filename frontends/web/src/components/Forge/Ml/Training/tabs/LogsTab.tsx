// Copyright (c) 2026 Reliant Labs

/**
 * A run's Logs tab (02-views §3.6): every pod of the scope's attempts,
 * merged and rank-tagged. Highlights (NCCL, watchdog, Xid, tracebacks, bus
 * errors, NaN, loss spikes, the straggler) are chips with counts that filter
 * to them; each attempt's first error is pinned above the log; a highlighted
 * line carries an icon at its left, never a tinted row; a line from a traced
 * step opens that trace.
 */

import { useMemo, useState } from "react";
import { AlertOctagon, AlertTriangle, Pin } from "lucide-react";

import { FilterBar, FilterChip, FilterMenu, FilterSearch } from "@/components/forge-ui/filter_bar";
import { MlSubjectKind, type MlLogLine } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { SeverityLabel } from "../../../Observability/LogsView";
import { MlRead } from "../../MlParts";
import { EmptyNote, mlHref, MlLink, Panel, SubjectLink, tsMs, useMl, useMlSearch } from "../../kit";
import { filterLogs, firstErrors, highlightCounts, severityOf } from "../obs/obsModel";
import type { RunScope } from "../runScope";

const time = (ms: number | null) =>
  ms ? new Date(ms).toLocaleString("en-US", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }) : "—";

export function LogsTab({ scope }: { scope: RunScope }) {
  const logs = useMl("listMlLogs", { subject: scope.subject, limit: 2000 });
  return (
    <div className="space-y-4" data-testid="ml-run-logs">
      <MlRead query={logs} what="logs">
        {(data) => <LogViewer scope={scope} lines={data.lines} truncated={data.truncated} />}
      </MlRead>
    </div>
  );
}

function LogViewer({ scope, lines, truncated }: { scope: RunScope; lines: readonly MlLogLine[]; truncated: boolean }) {
  const [search, setSearch] = useMlSearch();
  const [errorsOnly, setErrorsOnly] = useState(false);
  const [highlight, setHighlight] = useState<string | null>(null);
  const q = search.q ?? "";
  const rank = search.rank ?? null;
  const chips = useMemo(() => highlightCounts(lines), [lines]);
  const pinned = useMemo(() => firstErrors(lines), [lines]);
  const ranks = useMemo(() => [...new Set(lines.map((l) => l.rank))].filter((r) => r >= 0).sort((a, b) => a - b), [lines]);
  const shown = useMemo(() => filterLogs(lines, { rank, errorsOnly, highlight, q }), [lines, rank, errorsOnly, highlight, q]);
  const active = (q ? 1 : 0) + (rank !== null ? 1 : 0) + (errorsOnly ? 1 : 0) + (highlight ? 1 : 0);
  const errors = lines.filter((l) => l.level === "error").length;
  const warnings = lines.filter((l) => l.level === "warn").length;

  return (
    <>
      <FilterBar
        label="Filter logs"
        active={active}
        onClear={() => {
          setErrorsOnly(false);
          setHighlight(null);
          setSearch({ q: undefined, rank: undefined });
        }}
        testId="ml-logs-filters"
        trailing={
          <span className="text-meta tabular-nums text-ink-muted">
            {shown.length.toLocaleString()} of {lines.length.toLocaleString()} lines{truncated ? " (newest kept)" : ""} · {errors} errors · {warnings} warnings
          </span>
        }
      >
        <FilterSearch value={q} onChange={(v) => setSearch({ q: v || undefined })} placeholder="Search messages and pods" testId="ml-logs-search" />
        <FilterMenu label="Rank" value={rank !== null ? String(rank) : undefined} options={ranks.map((r) => ({ value: String(r), label: `rank ${r}`, count: lines.filter((l) => l.rank === r).length }))} onChange={(v) => setSearch({ rank: v === undefined ? undefined : Number(v) })} testId="ml-logs-rank" />
        <FilterChip on={errorsOnly} onClick={() => setErrorsOnly((v) => !v)} count={errors} testId="ml-logs-errors">
          Errors only
        </FilterChip>
        <FilterChip on={highlight === "*"} onClick={() => setHighlight((h) => (h === "*" ? null : "*"))} count={lines.filter((l) => l.highlight).length} testId="ml-logs-highlights">
          Highlights only
        </FilterChip>
      </FilterBar>
      {chips.length > 0 && (
        <div className="flex flex-wrap items-center gap-2" data-testid="ml-logs-chips">
          <span className="text-meta text-ink-muted">Highlights</span>
          {chips.map((c) => (
            <FilterChip key={c.highlight} on={highlight === c.highlight} onClick={() => setHighlight((h) => (h === c.highlight ? null : c.highlight))} count={c.count} testId={`ml-logs-chip-${c.highlight}`}>
              {c.label}
            </FilterChip>
          ))}
        </div>
      )}
      {pinned.length > 0 && (
        <Panel title="First error of each attempt" note="pinned" flush testId="ml-logs-pinned">
          <ul className="border-t border-border-subtle">
            {pinned.map((l) => (
              <li key={`${l.attempt}-${tsMs(l.time)}`} className="flex items-start gap-3 border-b border-border-subtle px-4 py-1.5 last:border-0">
                <Pin className="mt-0.5 h-3.5 w-3.5 shrink-0 text-ink-muted" aria-hidden="true" />
                <span className="w-20 shrink-0 text-meta text-ink-muted">
                  {scope.phase ? <SubjectLink subject={{ kind: MlSubjectKind.ATTEMPT, id: `${l.runId}/${l.phaseKey}/${l.attempt}` }}>attempt {l.attempt}</SubjectLink> : `attempt ${l.attempt}`}
                </span>
                <span className="w-16 shrink-0 text-meta tabular-nums text-ink-muted">{l.rank >= 0 ? `rank ${l.rank}` : "—"}</span>
                <span className="min-w-0 flex-1 break-words font-mono text-code text-danger-ink">{l.message}</span>
              </li>
            ))}
          </ul>
        </Panel>
      )}
      <Panel title="Log" note="all pods of the scope, merged by time" flush testId="ml-logs-list">
        {shown.length === 0 ? (
          <div className="px-4">
            <EmptyNote>No line matches these filters.</EmptyNote>
          </div>
        ) : (
          <ol className="overflow-y-auto border-t border-border-subtle" style={{ maxHeight: "70vh" }} data-testid="ml-logs-lines">
            {shown.map((l, i) => (
              <LogRow key={i} line={l} scope={scope} />
            ))}
          </ol>
        )}
      </Panel>
    </>
  );
}

function LogRow({ line, scope }: { line: MlLogLine; scope: RunScope }) {
  const Icon = line.highlight ? (line.level === "error" ? AlertOctagon : AlertTriangle) : null;
  const traceHref = line.traceId && scope.phase ? mlHref("runs", [scope.run.name, "phases", line.phaseKey], { attempt: line.attempt, tab: "traces", sel: line.traceId }) : null;
  return (
    <li className="flex items-start gap-x-2 border-b border-border-subtle px-4 py-1 last:border-0" data-testid={line.highlight ? `ml-log-highlight-${line.highlight}` : undefined}>
      <span className="w-4 shrink-0 pt-0.5" title={line.highlight || undefined}>
        {Icon && <Icon className={line.level === "error" ? "h-3.5 w-3.5 text-danger-ink" : "h-3.5 w-3.5 text-warning-ink"} aria-label={`highlight: ${line.highlight}`} />}
      </span>
      <span className="w-28 shrink-0 text-meta tabular-nums text-ink-muted">{time(tsMs(line.time))}</span>
      <span className="w-6 shrink-0 text-meta tabular-nums text-ink-muted" title={`attempt ${line.attempt}`}>
        a{line.attempt}
      </span>
      <span className="w-16 shrink-0 truncate text-meta tabular-nums text-ink-muted" title={line.pod}>
        {line.rank >= 0 ? (
          <SubjectLink subject={{ kind: MlSubjectKind.RANK, id: `${line.runId}/${line.phaseKey}/${line.attempt}/${line.rank}` }}>rank {line.rank}</SubjectLink>
        ) : (
          line.container || "—"
        )}
      </span>
      <span className="w-10 shrink-0 text-meta">
        <SeverityLabel severity={severityOf(line.level)} text={line.level} />
      </span>
      <span className="min-w-0 break-words font-mono text-code text-ink">
        {line.message}
        {traceHref && (
          <MlLink href={traceHref} className="ml-2 font-sans text-meta text-primary-ink" testId="ml-log-trace">
            trace
          </MlLink>
        )}
      </span>
    </li>
  );
}
