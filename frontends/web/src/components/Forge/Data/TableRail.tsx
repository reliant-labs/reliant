// Copyright (c) 2025 Reliant Labs

/**
 * The table list: every table and view the read-only role may SELECT,
 * grouped by schema (DescribeDatabase sorts them by schema, then name),
 * searchable, each with the planner's row estimate and a badge for anything
 * that is not a plain table. Each entry is a link, so a table opens in a new
 * tab like any other page.
 */

import { useMemo, useState } from "react";
import { Search } from "lucide-react";

import Link from "@/components/forge-ui/link";
import type { DbTable, DbTableKind } from "@/services/controlPlane/observability/appDatabaseApi";
import { cn } from "@/lib/utils";

import { CONTROL_FIELD } from "../controlStyles";
import { estimatedRowsLabel, qualifiedName } from "./dataLinks-model";

const KIND_BADGE: Partial<Record<DbTableKind, string>> = {
  view: "view",
  materialized_view: "mat. view",
  foreign_table: "foreign",
};

export function TableRail({
  tables,
  selected,
  tableHref,
}: {
  tables: DbTable[];
  selected: string | null;
  tableHref: (qualified: string) => string;
}) {
  const [query, setQuery] = useState("");
  const groups = useMemo(() => {
    const needle = query.trim().toLowerCase();
    const bySchema = new Map<string, DbTable[]>();
    for (const table of tables) {
      if (needle && !qualifiedName(table).toLowerCase().includes(needle)) continue;
      const list = bySchema.get(table.schema) ?? [];
      list.push(table);
      bySchema.set(table.schema, list);
    }
    return [...bySchema.entries()];
  }, [tables, query]);

  return (
    <nav aria-label="Tables" className="flex min-h-0 flex-col gap-2" data-testid="data-tables">
      <label className="relative block">
        <span className="sr-only">Find a table</span>
        <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-muted" aria-hidden="true" />
        <input
          type="search"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder={`Find among ${tables.length} ${tables.length === 1 ? "table" : "tables"}`}
          className={cn(CONTROL_FIELD, "h-8 w-full pl-8 pr-2 text-sm")}
          data-testid="data-table-search"
        />
      </label>
      <div className="min-h-0 flex-1 overflow-y-auto pr-1">
        {groups.length === 0 ? (
          <p className="px-2 py-3 text-xs text-ink-muted" data-testid="data-tables-none">
            No table matches &ldquo;{query}&rdquo;.
          </p>
        ) : (
          groups.map(([schema, list]) => (
            <section key={schema} className="mb-3" aria-label={`Schema ${schema}`}>
              <h3 className="px-2 pb-1 text-xs font-medium text-ink-muted">{schema}</h3>
              <ul>
                {list.map((table) => {
                  const name = qualifiedName(table);
                  const active = name === selected;
                  const rows = estimatedRowsLabel(table.estimatedRows);
                  const badge = KIND_BADGE[table.kind];
                  return (
                    <li key={name}>
                      <Link
                        href={tableHref(name)}
                        aria-current={active ? "page" : undefined}
                        title={table.comment || name}
                        data-testid={`data-table-${name}`}
                        className={cn(
                          "relative flex items-center gap-2 rounded-md px-2 py-1 text-sm transition-colors hover:bg-ink/5",
                          active ? "bg-ink/5 font-medium text-ink" : "text-ink-muted hover:text-ink"
                        )}
                      >
                        {active && <span aria-hidden="true" className="absolute inset-y-1 left-0 w-0.5 rounded-full bg-primary" />}
                        <span className="min-w-0 flex-1 truncate font-mono">{table.name}</span>
                        {badge && <span className="shrink-0 rounded border border-border px-1 text-2xs text-ink-muted">{badge}</span>}
                        {rows && (
                          <span className="shrink-0 font-mono text-2xs tabular-nums text-ink-muted" title="The planner's estimate">
                            {rows}
                          </span>
                        )}
                      </Link>
                    </li>
                  );
                })}
              </ul>
            </section>
          ))
        )}
      </div>
    </nav>
  );
}
