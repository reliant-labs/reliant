// Copyright (c) 2025 Reliant Labs

/**
 * The grid's filters: one chip per condition (ANDed, as BrowseTable ANDs
 * them), and a small form that adds one — a column, an op that fits it
 * (LIKE and ILIKE only on text, IS NULL only where NULL is possible), and the
 * value the op takes: none, one, or a list for IN. Values go to the server as
 * text and Postgres converts them to the column's type, so "42",
 * "2026-01-31" and "true" all work; a value it cannot convert comes back as
 * the server's own error, next to these chips.
 */

import { useId, useMemo, useState } from "react";
import { Filter, Plus, X } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Popover from "@/components/forge-ui/popover";
import type { DbColumn } from "@/services/controlPlane/observability/appDatabaseApi";
import { cn } from "@/lib/utils";

import { CONTROL_FIELD } from "../controlStyles";
import { FILTER_OPS, isTextType, opSpec, type DataFilter } from "./dataUrl-model";
import { typeLabel } from "./dataCells-model";

export function filterLabel(filter: DataFilter): string {
  const spec = opSpec(filter.op);
  if (spec.arity === "none") return `${filter.column} ${spec.symbol}`;
  if (spec.arity === "many") return `${filter.column} in (${filter.values.join(", ")})`;
  return `${filter.column} ${spec.symbol} ${filter.values[0] === "" ? "''" : filter.values[0]}`;
}

/** The ops a column takes: LIKE/ILIKE on text only, IS NULL / NOT NULL only when the column can be NULL. */
export function opsFor(column: Pick<DbColumn, "pgType" | "nullable">) {
  return FILTER_OPS.filter((spec) => (!spec.textOnly || isTextType(column.pgType)) && (spec.arity !== "none" || column.nullable));
}

export function FilterBar({
  columns,
  filters,
  onChange,
}: {
  columns: DbColumn[];
  filters: DataFilter[];
  onChange: (filters: DataFilter[]) => void;
}) {
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-1.5" data-testid="data-filters">
      {filters.map((filter, index) => (
        <span
          key={`${filter.column}.${filter.op}.${index}`}
          className="inline-flex max-w-full items-center gap-1 rounded-md border border-primary bg-primary/12 py-0.5 pl-2 pr-0.5 text-xs text-primary-ink"
          data-testid="data-filter-chip"
        >
          <span className="truncate font-mono">{filterLabel(filter)}</span>
          <button
            type="button"
            aria-label={`Remove filter ${filterLabel(filter)}`}
            onClick={() => onChange(filters.filter((_, i) => i !== index))}
            className="rounded p-0.5 hover:bg-primary/15 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <X className="h-3 w-3" aria-hidden="true" />
          </button>
        </span>
      ))}
      <AddFilter columns={columns} onAdd={(filter) => onChange([...filters, filter])} />
      {filters.length > 1 && (
        <Button variant="ghost" size="xs" onClick={() => onChange([])} data-testid="data-filters-clear">
          Clear all
        </Button>
      )}
    </div>
  );
}

function AddFilter({ columns, onAdd }: { columns: DbColumn[]; onAdd: (filter: DataFilter) => void }) {
  const [open, setOpen] = useState(false);
  return (
    <Popover
      open={open}
      onOpenChange={setOpen}
      label="Add a filter"
      panelClassName="w-80 p-3"
      trigger={(props) => (
        <button
          {...props}
          type="button"
          className={cn(CONTROL_FIELD, "inline-flex h-7 items-center gap-1.5 bg-surface px-2 text-xs")}
          data-testid="data-filter-add"
        >
          <Filter className="h-3.5 w-3.5" aria-hidden="true" />
          Filter
        </button>
      )}
    >
      {open && (
        <FilterForm
          columns={columns}
          onSubmit={(filter) => {
            onAdd(filter);
            setOpen(false);
          }}
        />
      )}
    </Popover>
  );
}

export function FilterForm({ columns, onSubmit }: { columns: DbColumn[]; onSubmit: (filter: DataFilter) => void }) {
  const ids = { column: useId(), op: useId(), value: useId() };
  const [columnName, setColumnName] = useState(columns[0]?.name ?? "");
  const column = columns.find((c) => c.name === columnName) ?? columns[0];
  const ops = useMemo(() => (column ? opsFor(column) : []), [column]);
  const [op, setOp] = useState(ops[0]?.op ?? "eq");
  const spec = opSpec(ops.some((o) => o.op === op) ? op : (ops[0]?.op ?? "eq"));
  const [value, setValue] = useState("");

  const values = spec.arity === "none" ? [] : spec.arity === "one" ? [value] : value.split("\n").filter((line) => line !== "");
  const ready = !!column && (spec.arity !== "many" || values.length > 0);
  const field = cn(CONTROL_FIELD, "w-full px-2 text-sm");

  return (
    <form
      className="space-y-2.5"
      data-testid="data-filter-form"
      onSubmit={(event) => {
        event.preventDefault();
        if (ready) onSubmit({ column: column.name, op: spec.op, values });
      }}
    >
      <div className="space-y-1">
        <label htmlFor={ids.column} className="text-xs font-medium text-ink-muted">
          Column
        </label>
        <select
          id={ids.column}
          value={column?.name ?? ""}
          onChange={(event) => setColumnName(event.target.value)}
          className={cn(field, "h-8")}
          data-testid="data-filter-column"
        >
          {columns.map((c) => (
            <option key={c.name} value={c.name}>
              {c.name} ({typeLabel(c.pgType)})
            </option>
          ))}
        </select>
      </div>
      <div className="space-y-1">
        <label htmlFor={ids.op} className="text-xs font-medium text-ink-muted">
          Condition
        </label>
        <select id={ids.op} value={spec.op} onChange={(event) => setOp(event.target.value as DataFilter["op"])} className={cn(field, "h-8")} data-testid="data-filter-op">
          {ops.map((o) => (
            <option key={o.op} value={o.op}>
              {o.label}
            </option>
          ))}
        </select>
      </div>
      {spec.arity !== "none" && (
        <div className="space-y-1">
          <label htmlFor={ids.value} className="text-xs font-medium text-ink-muted">
            {spec.arity === "many" ? "Values, one per line" : "Value"}
          </label>
          {spec.arity === "many" ? (
            <textarea id={ids.value} rows={4} value={value} onChange={(event) => setValue(event.target.value)} className={cn(field, "py-1.5 font-mono")} data-testid="data-filter-value" />
          ) : (
            <input
              id={ids.value}
              value={value}
              onChange={(event) => setValue(event.target.value)}
              placeholder={spec.textOnly ? "%pending%" : ""}
              className={cn(field, "h-8 font-mono")}
              data-testid="data-filter-value"
            />
          )}
          {spec.textOnly && <p className="text-2xs text-ink-muted">% matches any run of characters, _ any one.</p>}
        </div>
      )}
      <div className="flex justify-end">
        <Button type="submit" variant="primary" size="sm" disabled={!ready} leftIcon={<Plus className="h-3.5 w-3.5" />} data-testid="data-filter-apply">
          Add filter
        </Button>
      </div>
    </form>
  );
}
