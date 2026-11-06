// Copyright (c) 2025 Reliant Labs

/**
 * Which columns the grid shows. Kept in the URL (`cols`) in table order, so a
 * narrowed grid is a link; showing every column again drops the key. The
 * primary key is still read when hidden (dataUrl-model requestedColumns), so
 * a row stays openable.
 */

import { useMemo, useState } from "react";
import { Columns3 } from "lucide-react";

import Popover from "@/components/forge-ui/popover";
import type { DbColumn } from "@/services/controlPlane/observability/appDatabaseApi";
import { cn } from "@/lib/utils";

import { CONTROL_FIELD } from "../controlStyles";
import { typeLabel } from "./dataCells-model";

export function ColumnPicker({ columns, visible, onChange }: { columns: DbColumn[]; visible: string[]; onChange: (cols: string[] | undefined) => void }) {
  const [query, setQuery] = useState("");
  const shown = useMemo(() => new Set(visible), [visible]);
  const listed = columns.filter((c) => c.name.toLowerCase().includes(query.trim().toLowerCase()));
  const all = shown.size === columns.length;

  const toggle = (name: string) => {
    const next = new Set(shown);
    if (next.has(name)) next.delete(name);
    else next.add(name);
    if (next.size === 0) return;
    const ordered = columns.map((c) => c.name).filter((n) => next.has(n));
    onChange(ordered.length === columns.length ? undefined : ordered);
  };

  return (
    <Popover
      label="Columns"
      align="right"
      panelClassName="w-72 p-2"
      trigger={(props) => (
        <button {...props} type="button" className={cn(CONTROL_FIELD, "inline-flex h-7 items-center gap-1.5 bg-surface px-2 text-xs")} data-testid="data-columns">
          <Columns3 className="h-3.5 w-3.5" aria-hidden="true" />
          Columns{all ? "" : ` ${shown.size}/${columns.length}`}
        </button>
      )}
    >
      <div className="space-y-2" data-testid="data-columns-panel">
        {columns.length > 8 && (
          <input
            type="search"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Find a column"
            aria-label="Find a column"
            className={cn(CONTROL_FIELD, "h-8 w-full px-2 text-sm")}
          />
        )}
        <ul className="max-h-72 overflow-y-auto">
          {listed.map((c) => (
            <li key={c.name}>
              <label className="flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 text-sm hover:bg-ink/5">
                <input
                  type="checkbox"
                  checked={shown.has(c.name)}
                  disabled={shown.has(c.name) && shown.size === 1}
                  onChange={() => toggle(c.name)}
                  className="accent-primary"
                  data-testid={`data-column-${c.name}`}
                />
                <span className="min-w-0 flex-1 truncate font-mono text-xs text-ink">{c.name}</span>
                <span className="shrink-0 font-mono text-2xs text-ink-muted">{typeLabel(c.pgType)}</span>
              </label>
            </li>
          ))}
        </ul>
        <div className="flex justify-between border-t border-border pt-2">
          <button type="button" className="text-xs text-primary-ink hover:underline disabled:text-ink-subtle disabled:no-underline" disabled={all} onClick={() => onChange(undefined)} data-testid="data-columns-all">
            Show all
          </button>
          <span className="text-xs text-ink-muted">
            {shown.size} of {columns.length}
          </span>
        </div>
      </div>
    </Popover>
  );
}
