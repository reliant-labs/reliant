// Copyright (c) 2025 Reliant Labs

/**
 * The builder for the panels that carry their own statement:
 *
 *   sql       one read-only ClickHouse SELECT over the tenant's telemetry
 *             tables, run by QueryPanel under the alert-rule guard. Its
 *             parameters are ClickHouse's own, `{from:DateTime64(9)}`, bound
 *             from the dashboard's scope. The schema browser lists the tables,
 *             the attribute keys the console knows and the metrics the scope
 *             carries (ListAttributeValues, ListMetrics).
 *   postgres  one read-only statement against one of the environment's
 *             databases, run by QueryDatabasePanel as its dashboard role. Its
 *             parameters are positional, $1 $2 $3. The schema browser lists
 *             what that role may SELECT (ListDashboardDatabases).
 *
 * Neither runs as you type: a statement can take seconds, so "Run" (or
 * ⌘/Ctrl+Enter) reads it into the preview. When the guard or the database
 * refuses it, the refusal is shown here in the server's own words.
 */

import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { ChevronRight, Play } from "lucide-react";

import { useAttributeValues, useDashboardDatabases, useMetricCatalog } from "@/hooks/observability-queries";
import { obsErrorKind, obsErrorMessage } from "@/services/controlPlane/observability/errors";
import type { ObsScope, Panel, PanelPostgres } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { ATTRIBUTE_KEYS } from "../attributes";
import { unboundParams } from "../Metrics/sqlPanel";
import { BUTTON_PRIMARY, BUTTON_SMALL } from "./parts";
import { POSTGRES_DRAWS } from "./panelBuilder";
import { QueryEditor, type QueryEditorHandle } from "./QueryEditor";

const INPUT =
  "h-8 w-full min-w-0 rounded-md border border-border bg-surface px-2.5 text-sm text-ink placeholder:text-ink-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-60";

export interface StatementRun {
  /** Reads the current statement into the preview. */
  onRun: () => void;
  /** The statement can run: it is complete by the panel's own rules. */
  canRun: boolean;
  running: boolean;
  /** The current statement differs from the one the preview shows. */
  stale: boolean;
  /** The preview's failure, if it failed. */
  error: unknown;
}

// ── The parameter contracts ──────────────────────────────────────────────────

const CLICKHOUSE_PARAMS: readonly [string, string][] = [
  ["{from:DateTime64(9)}", "the start of the dashboard's range, inclusive"],
  ["{to:DateTime64(9)}", "its end, exclusive"],
  ["{step:UInt32}", "a bucket width in seconds suited to the range"],
  ["{env_ids:Array(String)}", "the reliant.env.id values in scope; empty is every environment"],
  ["{envs:Array(String)}", "their deployment.environment.name values, in the same order"],
  ["{services:Array(String)}", "the service.name values in scope; empty is every service"],
  ["{reliant_env_id:String}", "the one environment, when exactly one is in scope"],
];

const POSTGRES_PARAMS: readonly [string, string][] = [
  ["$1", "timestamptz: the start of the dashboard's range, inclusive"],
  ["$2", "timestamptz: its end, exclusive. A statement that uses $2 uses $1 too"],
  ["$3", "int4: a bucket width in seconds suited to the range (write $3::int4 where Postgres cannot tell)"],
];

function ParamContract({ params, note, onInsert }: { params: readonly [string, string][]; note: ReactNode; onInsert: (text: string) => void }) {
  return (
    <div className="space-y-1.5" data-testid="statement-params">
      <p className="text-xs text-ink-muted">{note}</p>
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs">
        {params.map(([param, meaning]) => (
          <div key={param} className="contents">
            <dt>
              <button type="button" className="font-mono text-ink hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" onClick={() => onInsert(param)} title={`Insert ${param}`}>
                {param}
              </button>
            </dt>
            <dd className="text-ink-muted">{meaning}</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

// ── Run, and what the server said ────────────────────────────────────────────

function RunBar({ run, children }: { run: StatementRun; children?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-3">
      <button type="button" className={BUTTON_PRIMARY} onClick={run.onRun} disabled={!run.canRun || run.running} data-testid="statement-run">
        <Play className="h-3.5 w-3.5" aria-hidden="true" />
        {run.running ? "Running…" : "Run"}
      </button>
      <span className="text-xs text-ink-muted">{run.stale ? "Edited since the last run: Run to see it." : "⌘/Ctrl+Enter runs it."}</span>
      {children}
    </div>
  );
}

/** The guard's or the database's refusal, verbatim: the words are the server's, so they are never paraphrased. */
function Refusal({ error }: { error: unknown }) {
  if (!error) return null;
  const refused = obsErrorKind(error) === "invalid";
  return (
    <div role="alert" className="rounded-md border border-border px-3 py-2" data-testid="statement-refusal">
      <p className="text-xs font-medium text-danger-ink">{refused ? "The statement was refused:" : "The statement could not run:"}</p>
      <pre className="mt-1 whitespace-pre-wrap break-words font-mono text-xs text-ink">{obsErrorMessage(error) || "no reason given"}</pre>
    </div>
  );
}

// ── Schema browsers ──────────────────────────────────────────────────────────

/** The telemetry tables a sql panel may read, and their columns (the OpenTelemetry ClickHouse exporter's schema, plus product events). */
const CLICKHOUSE_TABLES: readonly { name: string; about: string; columns: readonly [string, string][] }[] = [
  {
    name: "otel_traces",
    about: "one row per span",
    columns: [
      ["Timestamp", "DateTime64(9)"],
      ["ServiceName", "LowCardinality(String)"],
      ["SpanName", "LowCardinality(String)"],
      ["SpanKind", "LowCardinality(String)"],
      ["Duration", "UInt64 (ns)"],
      ["StatusCode", "LowCardinality(String)"],
      ["StatusMessage", "String"],
      ["TraceId", "String"],
      ["SpanId", "String"],
      ["ParentSpanId", "String"],
      ["SpanAttributes", "Map(String, String)"],
      ["ResourceAttributes", "Map(String, String)"],
    ],
  },
  {
    name: "otel_logs",
    about: "one row per log record",
    columns: [
      ["Timestamp", "DateTime64(9)"],
      ["ServiceName", "LowCardinality(String)"],
      ["SeverityText", "LowCardinality(String)"],
      ["SeverityNumber", "Int32"],
      ["Body", "String"],
      ["TraceId", "String"],
      ["LogAttributes", "Map(String, String)"],
      ["ResourceAttributes", "Map(String, String)"],
    ],
  },
  ...["otel_metrics_gauge", "otel_metrics_sum"].map((name) => ({
    name,
    about: name === "otel_metrics_sum" ? "counter and up-down counter points" : "gauge points",
    columns: [
      ["TimeUnix", "DateTime64(9)"],
      ["ServiceName", "LowCardinality(String)"],
      ["MetricName", "String"],
      ["Value", "Float64"],
      ["Attributes", "Map(String, String)"],
      ["ResourceAttributes", "Map(String, String)"],
    ] as [string, string][],
  })),
  {
    name: "otel_metrics_histogram",
    about: "histogram points",
    columns: [
      ["TimeUnix", "DateTime64(9)"],
      ["ServiceName", "LowCardinality(String)"],
      ["MetricName", "String"],
      ["Count", "UInt64"],
      ["Sum", "Float64"],
      ["BucketCounts", "Array(UInt64)"],
      ["ExplicitBounds", "Array(Float64)"],
      ["Attributes", "Map(String, String)"],
      ["ResourceAttributes", "Map(String, String)"],
    ],
  },
  {
    name: "product_events",
    about: "one row per track() event",
    columns: [
      ["Timestamp", "DateTime64(9)"],
      ["ServiceName", "LowCardinality(String)"],
      ["EnvId", "LowCardinality(String)"],
      ["EventName", "LowCardinality(String)"],
      ["SessionId", "String"],
      ["UserId", "String"],
      ["VisitorId", "String"],
      ["Props", "Map(String, String)"],
    ],
  },
];

/** Where an attribute key is read in SQL: resource keys from ResourceAttributes, the rest from the signal's own map. */
function attributeExpression(key: string, cls: string): string {
  if (key === "service.name") return "ServiceName";
  if (cls === "resource") return `ResourceAttributes['${key}']`;
  if (cls === "event") return `LogAttributes['${key}']`;
  if (cls === "metric") return `Attributes['${key}']`;
  return key === "status" ? "StatusCode" : `SpanAttributes['${key}']`;
}

function Disclosure({ title, count, children, testId }: { title: string; count?: number; children: ReactNode; testId?: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div data-testid={testId}>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className="flex w-full items-center gap-1 rounded py-1 text-left text-xs text-ink hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <ChevronRight className={cn("h-3 w-3 shrink-0 text-ink-muted transition-transform motion-reduce:transition-none", open && "rotate-90")} aria-hidden="true" />
        <span className="min-w-0 truncate font-mono">{title}</span>
        {count !== undefined && <span className="ml-auto pl-2 text-ink-muted tabular-nums">{count}</span>}
      </button>
      {open && <div className="pb-1 pl-4">{children}</div>}
    </div>
  );
}

function Insertable({ text, label, detail, onInsert }: { text: string; label?: string; detail?: string; onInsert: (text: string) => void }) {
  return (
    <button
      type="button"
      onClick={() => onInsert(text)}
      title={`Insert ${text}`}
      className="flex w-full items-baseline gap-2 rounded px-1 py-0.5 text-left text-xs hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <span className="min-w-0 truncate font-mono text-ink">{label ?? text}</span>
      {detail && <span className="ml-auto shrink-0 truncate pl-2 text-2xs text-ink-muted">{detail}</span>}
    </button>
  );
}

function AttributeValues({ scope, range, attribute, onInsert }: { scope: ObsScope | null; range: RangePreset; attribute: string; onInsert: (text: string) => void }) {
  const values = useAttributeValues(scope, range, attribute, "");
  if (values.isLoading) return <p className="px-1 text-2xs text-ink-muted">Reading values…</p>;
  if (values.error) return <p className="px-1 text-2xs text-danger-ink">{obsErrorMessage(values.error)}</p>;
  const list = values.data?.values ?? [];
  if (!list.length) return <p className="px-1 text-2xs text-ink-muted">No values in this range.</p>;
  return (
    <>
      {list.slice(0, 20).map((v) => (
        <Insertable key={v.value} text={`'${v.value.replace(/'/g, "\\'")}'`} label={v.value} detail={String(v.count)} onInsert={onInsert} />
      ))}
    </>
  );
}

function ClickHouseSchema({ scope, range, onInsert }: { scope: ObsScope | null; range: RangePreset; onInsert: (text: string) => void }) {
  const metrics = useMetricCatalog(scope, range);
  const [opened, setOpened] = useState<string | null>(null);
  return (
    <div className="space-y-2" data-testid="schema-clickhouse">
      <section>
        <h4 className="text-2xs font-medium uppercase tracking-wide text-ink-muted">Tables</h4>
        {CLICKHOUSE_TABLES.map((table) => (
          <Disclosure key={table.name} title={table.name} testId={`schema-table-${table.name}`}>
            <p className="px-1 text-2xs text-ink-muted">{table.about}</p>
            <Insertable text={table.name} detail="table" onInsert={onInsert} />
            {table.columns.map(([column, type]) => (
              <Insertable key={column} text={column} detail={type} onInsert={onInsert} />
            ))}
          </Disclosure>
        ))}
      </section>
      <section>
        <h4 className="text-2xs font-medium uppercase tracking-wide text-ink-muted">Attributes</h4>
        {ATTRIBUTE_KEYS.map((attribute) => (
          <div key={attribute.key}>
            <div className="flex items-center gap-1">
              <Insertable text={attributeExpression(attribute.key, attribute.class)} label={attribute.key} detail={attribute.label} onInsert={onInsert} />
              <button
                type="button"
                className="shrink-0 rounded px-1 text-2xs text-ink-muted hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                aria-expanded={opened === attribute.key}
                aria-label={`Values of ${attribute.key}`}
                onClick={() => setOpened(opened === attribute.key ? null : attribute.key)}
              >
                values
              </button>
            </div>
            {opened === attribute.key && (
              <div className="pl-3">
                <AttributeValues scope={scope} range={range} attribute={attribute.key} onInsert={onInsert} />
              </div>
            )}
          </div>
        ))}
      </section>
      <section>
        <h4 className="text-2xs font-medium uppercase tracking-wide text-ink-muted">Metrics in this range</h4>
        {metrics.isLoading ? (
          <p className="px-1 text-2xs text-ink-muted">Reading the metric catalog…</p>
        ) : metrics.error ? (
          <p className="px-1 text-2xs text-danger-ink">{obsErrorMessage(metrics.error)}</p>
        ) : (metrics.data?.metrics ?? []).length === 0 ? (
          <p className="px-1 text-2xs text-ink-muted">No metrics in this range.</p>
        ) : (
          (metrics.data?.metrics ?? []).slice(0, 100).map((metric) => (
            <Insertable key={`${metric.name}:${metric.kind}`} text={`'${metric.name}'`} label={metric.name} detail={`${metric.kind} → otel_metrics_${METRIC_TABLE[metric.kind] ?? "gauge"}`} onInsert={onInsert} />
          ))
        )}
      </section>
    </div>
  );
}

const METRIC_TABLE: Record<string, string> = { gauge: "gauge", sum: "sum", histogram: "histogram", exp_histogram: "exponential_histogram", summary: "summary" };

function PostgresSchema({ environmentId, database, onDatabase, onInsert }: { environmentId: string | null; database: string; onDatabase: (name: string) => void; onInsert: (text: string) => void }) {
  const databases = useDashboardDatabases(environmentId);
  if (!environmentId) return <p className="text-xs text-ink-muted">Choose one environment for the dashboard to see its databases.</p>;
  if (databases.isLoading) return <p className="text-xs text-ink-muted">Reading the environment&apos;s databases…</p>;
  if (databases.error) return <Refusal error={databases.error} />;
  const list = databases.data ?? [];
  if (!list.length) {
    return <p className="text-xs text-ink-muted">This environment has no database with dashboards enabled.</p>;
  }
  const chosen = list.find((db) => db.name === database);
  return (
    <div className="space-y-2" data-testid="schema-postgres">
      {list.map((db) => (
        <div key={db.name} className={cn("rounded-md", db.name === database && "bg-ink/5")}>
          <div className="flex items-center gap-2 px-1 py-1">
            <span className="font-mono text-xs text-ink">{db.name}</span>
            <span className="text-2xs text-ink-muted">
              {db.rowCap} rows · {db.timeoutMs / 1000}s · {db.replica === "ro" ? "read replica" : "primary"}
            </span>
            {db.name !== database && (
              <button type="button" className={cn(BUTTON_SMALL, "ml-auto h-6")} onClick={() => onDatabase(db.name)}>
                Use
              </button>
            )}
          </div>
          {db.tablesError ? (
            <p className="px-1 text-2xs text-danger-ink">{db.tablesError}</p>
          ) : (
            db.tables.map((table) => (
              <Disclosure key={`${table.schema}.${table.name}`} title={`${table.schema}.${table.name}`} count={table.columns.length} testId={`schema-table-${table.schema}.${table.name}`}>
                <Insertable text={table.schema === "public" ? table.name : `${table.schema}.${table.name}`} detail="table" onInsert={onInsert} />
                {table.columns.map((column) => (
                  <Insertable key={column.name} text={column.name} detail={column.type} onInsert={onInsert} />
                ))}
              </Disclosure>
            ))
          )}
        </div>
      ))}
      {database && !chosen && <p className="text-xs text-warning-ink">&ldquo;{database}&rdquo; is not a dashboards-enabled database of this environment.</p>}
    </div>
  );
}

// ── The two editors ──────────────────────────────────────────────────────────

function Row({ label, children, htmlFor }: { label: string; children: ReactNode; htmlFor?: string }) {
  return (
    <div className="grid grid-cols-[6.5rem_minmax(0,1fr)] items-start gap-3">
      {htmlFor ? (
        <label htmlFor={htmlFor} className="pt-2 text-xs font-medium text-ink-muted">
          {label}
        </label>
      ) : (
        <span className="pt-2 text-xs font-medium text-ink-muted">{label}</span>
      )}
      <div className="min-w-0">{children}</div>
    </div>
  );
}

function SchemaPane({ children }: { children: ReactNode }) {
  return <div className="max-h-64 overflow-y-auto rounded-md border border-border/60 bg-surface-sunken p-2">{children}</div>;
}

export function SqlStatementEditor({ draft, setDraft, scope, range, run }: { draft: Panel; setDraft: (panel: Panel) => void; scope: ObsScope | null; range: RangePreset; run: StatementRun }) {
  const id = useId();
  const editor = useRef<QueryEditorHandle>(null);
  const sql = draft.sql ?? "";
  const unbound = useMemo(() => unboundParams(sql), [sql]);
  const insert = (text: string) => editor.current?.insert(text);
  return (
    <div className="space-y-3" data-testid="panel-builder-sql">
      <QueryEditor ref={editor} value={sql} onChange={(next) => setDraft({ ...draft, sql: next })} onRun={() => run.canRun && run.onRun()} language="sql" label="ClickHouse statement" testId="panel-builder-sql-editor" />
      {unbound.length > 0 && (
        <p className="text-xs text-warning-ink" data-testid="statement-unbound">
          Not bound by a dashboard: {unbound.map((name) => `{${name}}`).join(", ")}. The statement is refused until it uses only the parameters below.
        </p>
      )}
      <RunBar run={run} />
      <Refusal error={run.error} />
      <ParamContract
        params={CLICKHOUSE_PARAMS}
        note="Bound from the dashboard's scope as ClickHouse parameters, never spliced in. One read-only SELECT; no writes, SETTINGS, FORMAT or system tables."
        onInsert={insert}
      />
      <Row label="Min step" htmlFor={`${id}-min-step`}>
        <input
          id={`${id}-min-step`}
          value={draft.minStep ?? ""}
          onChange={(e) => setDraft({ ...draft, minStep: e.target.value.trim() || undefined })}
          placeholder="1d, when it groups by toDate"
          className={cn(INPUT, "w-56 font-mono")}
        />
      </Row>
      <SchemaPane>
        <ClickHouseSchema scope={scope} range={range} onInsert={insert} />
      </SchemaPane>
    </div>
  );
}

const DRAW_LABELS: Record<(typeof POSTGRES_DRAWS)[number], string> = { auto: "Auto", line: "Line", number: "Number", table: "Table", bar: "Bar" };

const listOf = (text: string) => {
  const items = text.split(",").map((item) => item.trim()).filter(Boolean);
  return items.length ? items : undefined;
};

/**
 * A comma-separated list of column names. The text is kept as typed, so a
 * trailing comma or space survives until the next name; the list is parsed
 * from it on every change, and the text follows the list only when the list
 * changes from outside.
 */
function ListInput({ id, value, onChange, placeholder, testId }: { id: string; value: string[] | undefined; onChange: (list: string[] | undefined) => void; placeholder: string; testId?: string }) {
  const [text, setText] = useState(() => (value ?? []).join(", "));
  const joined = (value ?? []).join(",");
  useEffect(() => {
    setText((current) => ((listOf(current) ?? []).join(",") === joined ? current : (value ?? []).join(", ")));
    // value is compared by its joined form: a new array with the same names is the same list.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [joined]);
  return (
    <input
      id={id}
      value={text}
      onChange={(e) => {
        setText(e.target.value);
        onChange(listOf(e.target.value));
      }}
      placeholder={placeholder}
      className={cn(INPUT, "font-mono")}
      data-testid={testId}
    />
  );
}

export function PostgresStatementEditor({
  draft,
  setDraft,
  environmentId,
  run,
}: {
  draft: Panel;
  setDraft: (panel: Panel) => void;
  environmentId: string | null;
  run: StatementRun;
}) {
  const id = useId();
  const editor = useRef<QueryEditorHandle>(null);
  const query: PanelPostgres = draft.postgres ?? { database: "", sql: "" };
  const set = (next: Partial<PanelPostgres>) => setDraft({ ...draft, postgres: { ...query, ...next } });
  const insert = (text: string) => editor.current?.insert(text);
  const draw = query.draw || "auto";
  return (
    <div className="space-y-3" data-testid="panel-builder-postgres">
      <Row label="Database" htmlFor={`${id}-database`}>
        <input id={`${id}-database`} value={query.database} onChange={(e) => set({ database: e.target.value.trim() })} placeholder="app" className={cn(INPUT, "w-56 font-mono")} data-testid="panel-builder-postgres-database" />
      </Row>
      <QueryEditor ref={editor} value={query.sql} onChange={(sql) => set({ sql })} onRun={() => run.canRun && run.onRun()} language="pgsql" label="Postgres statement" testId="panel-builder-postgres-editor" />
      <RunBar run={run} />
      <Refusal error={run.error} />
      <ParamContract
        params={POSTGRES_PARAMS}
        note="Bound from the dashboard's range, never spliced in, and none past $3. One read-only SELECT, WITH, TABLE or VALUES, at read committed with a short statement timeout: keep it to an indexed range scan."
        onInsert={insert}
      />
      <Row label="Draw as">
        <div role="radiogroup" aria-label="Draw as" className="flex flex-wrap gap-1" data-testid="panel-builder-postgres-draw">
          {POSTGRES_DRAWS.map((value) => (
            <button
              key={value}
              type="button"
              role="radio"
              aria-checked={draw === value}
              onClick={() => set({ draw: value === "auto" ? undefined : value })}
              data-value={value}
              className={cn(
                "h-7 rounded-md border px-2.5 text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                draw === value ? "border-ink/40 bg-ink/8 font-medium text-ink" : "border-border bg-surface text-ink-muted hover:text-ink"
              )}
            >
              {DRAW_LABELS[value]}
            </button>
          ))}
        </div>
        <p className="mt-1 text-xs text-ink-muted">Auto draws a line when a time column is named, one figure for one row of one value, a table otherwise.</p>
      </Row>
      <Row label="Time column" htmlFor={`${id}-time`}>
        <input id={`${id}-time`} value={query.time ?? ""} onChange={(e) => set({ time: e.target.value.trim() || undefined })} placeholder={draw === "line" ? "required for a line: t" : "t"} className={cn(INPUT, "w-56 font-mono")} />
      </Row>
      <Row label="Values" htmlFor={`${id}-values`}>
        <ListInput id={`${id}-values`} value={query.values} onChange={(values) => set({ values })} placeholder="every numeric column" testId="panel-builder-postgres-values" />
      </Row>
      <Row label="Split by" htmlFor={`${id}-group`}>
        <ListInput id={`${id}-group`} value={query.group} onChange={(group) => set({ group })} placeholder="up to three text columns" testId="panel-builder-postgres-group" />
      </Row>
      <SchemaPane>
        <PostgresSchema environmentId={environmentId} database={query.database} onDatabase={(database) => set({ database })} onInsert={insert} />
      </SchemaPane>
    </div>
  );
}

// ── Text ─────────────────────────────────────────────────────────────────────

const MAX_MARKDOWN_BYTES = 10_000;

/** The Markdown of a text panel; the drawer's preview draws it as it is typed. */
export function MarkdownEditor({ draft, setDraft }: { draft: Panel; setDraft: (panel: Panel) => void }) {
  const id = useId();
  const markdown = draft.markdown ?? "";
  const size = new TextEncoder().encode(markdown).length;
  return (
    <div className="space-y-1.5" data-testid="panel-builder-text">
      <label htmlFor={id} className="text-xs font-medium text-ink-muted">
        Markdown
      </label>
      <textarea
        id={id}
        value={markdown}
        onChange={(e) => setDraft({ ...draft, markdown: e.target.value })}
        rows={10}
        spellCheck
        className="block w-full resize-y rounded-md border border-border/60 bg-surface-sunken p-3 font-mono text-xs leading-relaxed text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        data-testid="panel-builder-markdown"
      />
      <p className={cn("text-xs", size > MAX_MARKDOWN_BYTES ? "text-danger-ink" : "text-ink-muted")}>
        CommonMark with tables and links; raw HTML and remote images are not drawn. {size.toLocaleString()} of {MAX_MARKDOWN_BYTES.toLocaleString()} bytes.
      </p>
    </div>
  );
}
