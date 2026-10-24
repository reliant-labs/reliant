// Copyright (c) 2025 Reliant Labs

/**
 * The SQL tab: what the query really runs, and the way into raw SQL.
 *
 *   explain    QueryPanel{explain} — the statements the panel compiles to,
 *              with their bound parameters. It runs nothing, so it costs no
 *              query quota.
 *   SQL panel  "Open as SQL panel" rewrites one statement as a `kind: "sql"`
 *              panel over the scope's own parameters (sqlPanel.ts), and the tab
 *              then edits and runs it — the starting point for anything the
 *              builder cannot say. A pivot can open the tab on a statement of
 *              its own (an alert rule's), which is ExplorerState.sqlPanel.
 */

import { useEffect, useId, useState } from "react";
import { ArrowLeft, Play, TerminalSquare } from "lucide-react";

import { usePanelQuery } from "@/hooks/observability-queries";
import type { ObsScope, Panel } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";

import { BUTTON_PRIMARY, BUTTON_SMALL } from "../Dashboards/parts";
import { ObsError, ObsLoadingRows } from "../ObsStates";
import { useExplainPanel } from "./explorerQueries";
import { ExplorerChart } from "./ExplorerChart";
import { sqlPanelStatement, unboundParams } from "./sqlPanel";

export interface SqlTabProps {
  /** The builder's (debounced) query; null when it cannot run yet. */
  panel: Panel | null;
  sqlPanel?: Panel;
  scope: ObsScope | null;
  range: RangePreset;
  onOpenSql: (sql: string) => void;
  onBack: () => void;
  subject: string;
}

export function SqlTab(props: SqlTabProps) {
  return props.sqlPanel ? <SqlPanelEditor {...props} sqlPanel={props.sqlPanel} /> : <Explain {...props} />;
}

function Explain({ panel, scope, range, onOpenSql, subject }: SqlTabProps) {
  const explained = useExplainPanel(scope, range, panel, true);
  if (!panel) {
    return <p className="py-6 text-center text-xs text-ink-muted">Pick a metric to see the SQL its query compiles to.</p>;
  }
  if (explained.isLoading) return <ObsLoadingRows rows={4} testId="metrics-sql-loading" />;
  if (explained.error) return <ObsError error={explained.error} subject={subject} onRetry={() => void explained.refetch()} retrying={explained.isFetching} />;
  const statements = explained.data?.result.compiledSql ?? [];
  if (statements.length === 0) {
    return <p className="py-6 text-center text-xs text-ink-muted">The server returned no statements for this query.</p>;
  }
  return (
    <div className="space-y-3" data-testid="metrics-sql-explain">
      <p className="text-xs text-ink-muted">
        What this query runs, compiled by the same code the CLI and dashboards use. Parameters in braces are bound by the server, never spliced in.
      </p>
      <ol className="space-y-3">
        {statements.map((statement, index) => (
          <li key={index} className="rounded-md border border-border/60 bg-background" data-testid="metrics-sql-statement">
            <div className="flex items-center justify-between gap-2 border-b border-border/60 px-3 py-1.5">
              <span className="text-xs text-ink-muted">
                Statement {index + 1} of {statements.length}
              </span>
              <button
                type="button"
                className={BUTTON_SMALL}
                onClick={() =>
                  onOpenSql(
                    sqlPanelStatement(statement, {
                      window: explained.data!.window,
                      stepSeconds: explained.data!.result.stepSeconds,
                      envIds: scope?.envIds ?? [],
                    })
                  )
                }
                data-testid="metrics-sql-open-panel"
              >
                <TerminalSquare className="h-3 w-3" aria-hidden="true" />
                Open as SQL panel
              </button>
            </div>
            <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words px-3 py-2 font-mono text-2xs text-ink">{statement.sql}</pre>
            {Object.keys(statement.params).length > 0 && (
              <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-0.5 border-t border-border/60 px-3 py-2 font-mono text-2xs">
                {Object.entries(statement.params)
                  .sort(([a], [b]) => a.localeCompare(b, undefined, { numeric: true }))
                  .map(([name, value]) => (
                    <div key={name} className="contents">
                      <dt className="text-ink-muted">{name}</dt>
                      <dd className="truncate text-ink" title={value}>
                        {value}
                      </dd>
                    </div>
                  ))}
              </dl>
            )}
          </li>
        ))}
      </ol>
    </div>
  );
}

function SqlPanelEditor({ sqlPanel, scope, range, onOpenSql, onBack, subject }: SqlTabProps & { sqlPanel: Panel }) {
  const [draft, setDraft] = useState(sqlPanel.sql ?? "");
  useEffect(() => setDraft(sqlPanel.sql ?? ""), [sqlPanel.sql]);
  const id = useId();
  const query = usePanelQuery(scope, range, sqlPanel, []);
  const unbound = unboundParams(draft);
  const data = query.data;
  const timeSeries = !!data && data.series.some((s) => s.timestamps.length > 0);
  const display: Panel = timeSeries
    ? { ...sqlPanel, kind: "line" }
    : { ...sqlPanel, kind: "table", groupBy: Object.keys(data?.rows[0]?.group ?? {}) };
  return (
    <div className="space-y-3" data-testid="metrics-sql-panel">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <button type="button" className={BUTTON_SMALL} onClick={onBack} data-testid="metrics-sql-back">
          <ArrowLeft className="h-3 w-3" aria-hidden="true" />
          Back to the builder
        </button>
        <p className="text-xs text-ink-muted">
          Bound from the scope: <span className="font-mono">{"{from} {to} {step} {env_ids} {envs} {services}"}</span>
        </p>
      </div>
      <label htmlFor={id} className="sr-only">
        SQL statement
      </label>
      <textarea
        id={id}
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
            event.preventDefault();
            onOpenSql(draft);
          }
        }}
        spellCheck={false}
        rows={Math.min(18, Math.max(6, draft.split("\n").length + 1))}
        className="w-full rounded-md border border-border bg-background px-3 py-2 font-mono text-xs text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-accent"
        data-testid="metrics-sql-editor"
      />
      <div className="flex flex-wrap items-center gap-2">
        <button type="button" className={BUTTON_PRIMARY} onClick={() => onOpenSql(draft)} disabled={!draft.trim() || draft === sqlPanel.sql} data-testid="metrics-sql-run">
          <Play className="h-3.5 w-3.5" aria-hidden="true" />
          Run
        </button>
        <span className="text-xs text-ink-muted">One read-only SELECT, under the same guard and limits as an alert rule. ⌘/Ctrl+Enter runs it.</span>
      </div>
      {unbound.length > 0 && (
        <p className="text-xs text-warning-ink" data-testid="metrics-sql-unbound">
          The statement names parameters the panel does not bind: {unbound.map((p) => `{${p}}`).join(", ")}.
        </p>
      )}
      {query.error ? (
        <ObsError error={query.error} subject={subject} onRetry={() => void query.refetch()} retrying={query.isFetching} />
      ) : query.isLoading ? (
        <ObsLoadingRows rows={4} testId="metrics-sql-running" />
      ) : (
        <ExplorerChart panel={display} query={query} unit="count" hidden={[]} attributes={[]} />
      )}
    </div>
  );
}
