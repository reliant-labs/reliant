// Copyright (c) 2025 Reliant Labs

/**
 * The scope bar: WHICH telemetry every view reads — environments, attribute
 * filters — and how the aggregating views split it (group-by). Everything
 * here writes the URL (obsSearch.ts), so a narrowed, grouped view is a link.
 *
 * Environment is a filter like any other (owner decision 1). On the project
 * area it is a multi-select over the environments the caller can see, empty
 * meaning all of them; on an env page it is pinned and shown as a fact, with
 * the way out to the same view across every environment.
 *
 * Attribute values autocomplete from ListAttributeValues, most frequent first.
 * The server ignores filters on the key being completed, so picking a second
 * version to compare is never narrowed to the first.
 */

import { useCallback, useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { Check, ChevronDown, Layers, Plus, Server, X } from "lucide-react";

import { useAttributeValues } from "@/hooks/observability-queries";
import { useEscapeLayer } from "@/hooks/useEscapeLayer";
import { useDebounce } from "@/hooks/useDebounce";
import { cn } from "@/lib/utils";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";

import {
  ATTRIBUTE_KEYS,
  attributeLabel,
  CLASS_LABELS,
  ENV_KEY,
  GROUP_BY_SUGGESTIONS,
  isAttributeKeyShaped,
  MAX_FILTERS,
  MAX_GROUP_BY,
  signalForKey,
  type AttributeClass,
} from "./attributes";
import { FILTER_OP_LABELS, formatFilterToken, type FilterToken, type FilterTokenOp } from "./obsFilters";
import { formatCount } from "./format";
import type { ObsEnvironment } from "./obsPlace";

export interface ObsScopeBarProps {
  scope: ObsScope;
  range: RangePreset;
  /** Project area: every environment, for the picker. Env page: undefined. */
  environments?: ObsEnvironment[];
  /** Selected environment names; empty = all. */
  selectedEnvs: string[];
  onEnvsChange: (names: string[]) => void;
  /** Env page: the one environment in view, and the way to widen. */
  pinnedEnv?: string;
  onWiden?: () => void;
  filters: FilterToken[];
  onFiltersChange: (filters: FilterToken[]) => void;
  groupBy: string[];
  onGroupByChange: (keys: string[]) => void;
  /** False on the views that list rows rather than aggregate them. */
  groupable: boolean;
  /**
   * False where attribute filters mean nothing (Alerts: rules, silences and
   * receivers are not telemetry rows). The environment picker stays. Default true.
   */
  filterable?: boolean;
}

const CONTROL =
  "inline-flex h-7 items-center gap-1.5 rounded-md border border-border bg-background px-2 text-xs text-foreground transition-colors hover:bg-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-ring";

export function ObsScopeBar(props: ObsScopeBarProps) {
  const { filters, onFiltersChange, groupBy, onGroupByChange } = props;
  const removeFilter = (index: number) => onFiltersChange(filters.filter((_, i) => i !== index));

  return (
    <div className="flex flex-wrap items-center gap-2" data-testid="obs-scope-bar" role="group" aria-label="What to show">
      {props.environments ? (
        <EnvPicker environments={props.environments} selected={props.selectedEnvs} onChange={props.onEnvsChange} />
      ) : props.pinnedEnv ? (
        <span className="inline-flex h-7 items-center gap-1.5 rounded-md border border-border/60 bg-background px-2 text-xs" data-testid="obs-pinned-env">
          <Server className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
          <span className="text-muted-foreground">Environment</span>
          <span className="font-mono text-foreground">{props.pinnedEnv}</span>
          {props.onWiden && (
            <button
              type="button"
              onClick={props.onWiden}
              className="ml-1 rounded-sm text-primary underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              data-testid="obs-widen-envs"
            >
              Compare across environments
            </button>
          )}
        </span>
      ) : null}

      {props.filterable !== false && filters.map((filter, index) => (
        <span
          key={formatFilterToken(filter)}
          className="inline-flex h-7 items-center gap-1 rounded-md border border-border bg-card pl-2 pr-1 text-xs"
          data-testid="obs-filter-chip"
        >
          <span className="text-muted-foreground">{attributeLabel(filter.key)}</span>{" "}
          <span className="text-muted-foreground">{FILTER_OP_LABELS[filter.op]}</span>
          {filter.op !== "exists" && (
            <>
              {" "}
              <span className="max-w-[16rem] truncate font-mono text-foreground" title={filter.value}>
                {filter.value}
              </span>
            </>
          )}
          <button
            type="button"
            onClick={() => removeFilter(index)}
            aria-label={`Remove filter ${attributeLabel(filter.key)} ${FILTER_OP_LABELS[filter.op]} ${filter.value}`}
            className="rounded-sm p-0.5 text-muted-foreground hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <X className="h-3 w-3" aria-hidden="true" />
          </button>
        </span>
      ))}

      {props.filterable !== false && filters.length < MAX_FILTERS && (
        <AddFilter scope={props.scope} range={props.range} onAdd={(token) => onFiltersChange([...filters, token])} />
      )}

      {props.groupable && (
        <div className="flex flex-wrap items-center gap-1.5 sm:ml-auto" data-testid="obs-group-by">
          <span className="inline-flex items-center gap-1 text-xs text-muted-foreground">
            <Layers className="h-3.5 w-3.5" aria-hidden="true" />
            Group by
          </span>
          {groupBy.map((key) => (
            <span key={key} className="inline-flex h-7 items-center gap-1 rounded-md border border-border bg-card pl-2 pr-1 text-xs" data-testid="obs-group-chip">
              <span className="text-foreground">{attributeLabel(key)}</span>
              <button
                type="button"
                onClick={() => onGroupByChange(groupBy.filter((k) => k !== key))}
                aria-label={`Stop grouping by ${attributeLabel(key)}`}
                className="rounded-sm p-0.5 text-muted-foreground hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <X className="h-3 w-3" aria-hidden="true" />
              </button>
            </span>
          ))}
          {groupBy.length === 0 && <span className="text-xs text-muted-foreground">nothing</span>}
          {groupBy.length < MAX_GROUP_BY && <AddGroupBy used={groupBy} onAdd={(key) => onGroupByChange([...groupBy, key])} />}
        </div>
      )}
    </div>
  );
}

// ── A small popover, the dropdown standard (click outside, Escape) ───────────

export function Popover({
  label,
  trigger,
  triggerClassName,
  triggerLabel,
  panelClassName,
  testId,
  children,
  open,
  onOpenChange,
}: {
  label: string;
  trigger: ReactNode;
  triggerClassName?: string;
  /** An accessible name for a trigger whose content is only an icon. */
  triggerLabel?: string;
  /** Replaces the panel's placement and width (default: below, left-aligned, w-72). */
  panelClassName?: string;
  testId: string;
  children: ReactNode;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const close = useCallback(() => onOpenChange(false), [onOpenChange]);
  const escapeLayer = useEscapeLayer(open, close);
  useEffect(() => {
    if (!open) return;
    const handleClickOutside = (event: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(event.target as Node)) onOpenChange(false);
    };
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, [open, onOpenChange]);

  return (
    <div className="relative" ref={containerRef}>
      <button
        type="button"
        className={triggerClassName ?? CONTROL}
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-label={triggerLabel}
        onClick={() => onOpenChange(!open)}
        data-testid={testId}
      >
        {trigger}
      </button>
      {open && (
        <div
          {...escapeLayer}
          role="dialog"
          aria-label={label}
          className={cn("absolute top-full z-30 mt-1 rounded-lg border border-border bg-card p-2 shadow-lg", panelClassName ?? "left-0 w-72")}
          data-testid={`${testId}-popover`}
        >
          {children}
        </div>
      )}
    </div>
  );
}

// ── Environments ─────────────────────────────────────────────────────────────

function EnvPicker({ environments, selected, onChange }: { environments: ObsEnvironment[]; selected: string[]; onChange: (names: string[]) => void }) {
  const [open, setOpen] = useState(false);
  const known = new Set(environments.map((e) => e.name));
  const active = selected.filter((name) => known.has(name));
  const summary =
    active.length === 0 ? "All environments" : active.length <= 2 ? active.join(", ") : `${active.length} environments`;
  const toggle = (name: string) => onChange(active.includes(name) ? active.filter((n) => n !== name) : [...active, name]);

  return (
    <Popover
      label="Environments"
      open={open}
      onOpenChange={setOpen}
      testId="obs-env-picker"
      trigger={
        <>
          <Server className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
          <span className="text-muted-foreground">Environment</span>
          <span className="font-medium">{summary}</span>
          <ChevronDown className="h-3 w-3 text-muted-foreground" aria-hidden="true" />
        </>
      }
    >
      <fieldset>
        <legend className="px-1 pb-1 text-2xs font-medium uppercase tracking-wide text-muted-foreground">Environments</legend>
        <button
          type="button"
          onClick={() => onChange([])}
          className="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm hover:bg-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          data-testid="obs-env-all"
        >
          <Check className={cn("h-3.5 w-3.5", active.length === 0 ? "text-primary" : "invisible")} aria-hidden="true" />
          Every environment
        </button>
        {environments.map((env) => (
          <label key={env.id} className="flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm hover:bg-muted">
            <input
              type="checkbox"
              checked={active.includes(env.name)}
              onChange={() => toggle(env.name)}
              className="h-3.5 w-3.5 accent-[hsl(var(--primary))]"
              data-testid={`obs-env-option-${env.name}`}
            />
            <span className="font-mono">{env.name}</span>
          </label>
        ))}
      </fieldset>
      <p className="px-2 pt-2 text-2xs text-muted-foreground">
        To compare environments side by side, group by Environment.
      </p>
    </Popover>
  );
}

// ── Add a filter ─────────────────────────────────────────────────────────────

function KeyList({ query, used, onPick, suggestions }: { query: string; used?: string[]; onPick: (key: string) => void; suggestions?: readonly string[] }) {
  const q = query.trim().toLowerCase();
  const matches = ATTRIBUTE_KEYS.filter(
    (a) => !used?.includes(a.key) && (!q || a.key.toLowerCase().includes(q) || a.label.toLowerCase().includes(q))
  );
  const first = suggestions && !q ? matches.filter((a) => suggestions.includes(a.key)) : [];
  const rest = matches.filter((a) => !first.includes(a));
  const sections = (Object.keys(CLASS_LABELS) as AttributeClass[])
    .map((cls) => ({ cls, keys: rest.filter((a) => a.class === cls) }))
    .filter((section) => section.keys.length > 0);
  const typed = query.trim();
  const custom = typed && isAttributeKeyShaped(typed) && !ATTRIBUTE_KEYS.some((a) => a.key === typed) && !used?.includes(typed);

  const option = (key: string, label: string) => (
    <li key={key}>
      <button
        type="button"
        onClick={() => onPick(key)}
        className="flex w-full items-baseline justify-between gap-2 rounded px-2 py-1 text-left text-sm hover:bg-muted focus:outline-none focus-visible:bg-muted"
        data-testid={`obs-key-${key}`}
      >
        <span className="text-foreground">{label}</span>
        <span className="truncate font-mono text-2xs text-muted-foreground">{key}</span>
      </button>
    </li>
  );

  return (
    <div className="max-h-64 overflow-y-auto">
      {custom && <ul className="pb-1">{option(typed, `Use "${typed}"`)}</ul>}
      {first.length > 0 && (
        <section>
          <h4 className="px-2 pb-0.5 pt-1 text-2xs font-medium uppercase tracking-wide text-muted-foreground">Common</h4>
          <ul>{first.map((a) => option(a.key, a.label))}</ul>
        </section>
      )}
      {sections.map(({ cls, keys }) => (
        <section key={cls}>
          <h4 className="px-2 pb-0.5 pt-1 text-2xs font-medium uppercase tracking-wide text-muted-foreground">{CLASS_LABELS[cls]}</h4>
          <ul>{keys.map((a) => option(a.key, a.label))}</ul>
        </section>
      ))}
      {!custom && first.length === 0 && sections.length === 0 && (
        <p className="px-2 py-2 text-xs text-muted-foreground">No attribute matches. Type a full attribute key to use it.</p>
      )}
    </div>
  );
}

function AddFilter({ scope, range, onAdd }: { scope: ObsScope; range: RangePreset; onAdd: (token: FilterToken) => void }) {
  const [open, setOpen] = useState(false);
  const [key, setKey] = useState<string | null>(null);
  const [keyQuery, setKeyQuery] = useState("");
  const [op, setOp] = useState<FilterTokenOp>("eq");
  const [value, setValue] = useState("");
  const inputId = useId();

  const reset = useCallback(() => {
    setKey(null);
    setKeyQuery("");
    setOp("eq");
    setValue("");
  }, []);
  const onOpenChange = useCallback(
    (next: boolean) => {
      setOpen(next);
      if (!next) reset();
    },
    [reset]
  );

  const apply = (chosen: string) => {
    if (!key) return;
    if (op !== "exists" && !chosen.trim()) return;
    onAdd({ key, op, value: op === "exists" ? "" : chosen.trim() });
    onOpenChange(false);
  };

  return (
    <Popover
      label="Add a filter"
      open={open}
      onOpenChange={onOpenChange}
      testId="obs-add-filter"
      triggerClassName={cn(CONTROL, "border-dashed text-muted-foreground")}
      trigger={
        <>
          <Plus className="h-3.5 w-3.5" aria-hidden="true" />
          Filter
        </>
      }
    >
      {!key ? (
        <div className="space-y-1">
          <label htmlFor={inputId} className="sr-only">
            Attribute
          </label>
          <input
            id={inputId}
            autoFocus
            value={keyQuery}
            onChange={(event) => setKeyQuery(event.target.value)}
            onKeyDown={(event) => {
              const typed = keyQuery.trim();
              if (event.key === "Enter" && isAttributeKeyShaped(typed)) setKey(typed);
            }}
            placeholder="Filter by… (service, version, route)"
            className="h-8 w-full rounded-md border border-border bg-background px-2 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            data-testid="obs-filter-key-input"
          />
          <KeyList query={keyQuery} onPick={setKey} />
        </div>
      ) : (
        <ValueStep
          scope={scope}
          range={range}
          attributeKey={key}
          op={op}
          onOpChange={setOp}
          value={value}
          onValueChange={setValue}
          onBack={() => setKey(null)}
          onApply={apply}
        />
      )}
    </Popover>
  );
}

function ValueStep({
  scope,
  range,
  attributeKey,
  op,
  onOpChange,
  value,
  onValueChange,
  onBack,
  onApply,
}: {
  scope: ObsScope;
  range: RangePreset;
  attributeKey: string;
  op: FilterTokenOp;
  onOpChange: (op: FilterTokenOp) => void;
  value: string;
  onValueChange: (value: string) => void;
  onBack: () => void;
  onApply: (value: string) => void;
}) {
  const valueId = useId();
  const opId = useId();
  const prefix = useDebounce(value, 200);
  // `contains` filters the most frequent values client-side: a prefix would miss the substring.
  const values = useAttributeValues(op === "exists" ? null : scope, range, attributeKey, op === "contains" ? "" : prefix, signalForKey(attributeKey));
  const suggestions = useMemo(() => {
    const all = values.data?.values ?? [];
    if (op !== "contains" || !value) return all;
    return all.filter((v) => v.value.toLowerCase().includes(value.toLowerCase()));
  }, [values.data, op, value]);

  return (
    <form
      className="space-y-2"
      onSubmit={(event) => {
        event.preventDefault();
        onApply(value);
      }}
    >
      <div className="flex items-center justify-between gap-2">
        <button type="button" onClick={onBack} className="rounded-sm text-xs text-muted-foreground hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring">
          ← Attributes
        </button>
        <span className="truncate font-mono text-2xs text-muted-foreground">{attributeKey}</span>
      </div>
      <p className="text-sm font-medium text-foreground">{attributeLabel(attributeKey)}</p>
      <div className="flex gap-2">
        <label htmlFor={opId} className="sr-only">
          Operator
        </label>
        <select
          id={opId}
          value={op}
          onChange={(event) => onOpChange(event.target.value as FilterTokenOp)}
          className="h-8 rounded-md border border-border bg-background px-1.5 text-sm text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          data-testid="obs-filter-op"
        >
          {(Object.keys(FILTER_OP_LABELS) as FilterTokenOp[]).map((candidate) => (
            <option key={candidate} value={candidate}>
              {FILTER_OP_LABELS[candidate]}
            </option>
          ))}
        </select>
        {op !== "exists" && (
          <>
            <label htmlFor={valueId} className="sr-only">
              Value
            </label>
            <input
              id={valueId}
              autoFocus
              value={value}
              onChange={(event) => onValueChange(event.target.value)}
              placeholder={attributeKey === ENV_KEY ? "prod" : "value"}
              className="h-8 min-w-0 flex-1 rounded-md border border-border bg-background px-2 font-mono text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              data-testid="obs-filter-value-input"
            />
          </>
        )}
      </div>
      {op !== "exists" && (
        <div className="max-h-48 overflow-y-auto" aria-live="polite">
          {values.isLoading ? (
            <p className="px-2 py-1 text-xs text-muted-foreground">Looking up values…</p>
          ) : values.error ? (
            <p className="px-2 py-1 text-xs text-muted-foreground">Values could not be listed; type one.</p>
          ) : suggestions.length === 0 ? (
            <p className="px-2 py-1 text-xs text-muted-foreground">No values seen in this window. Type one to filter anyway.</p>
          ) : (
            <ul aria-label={`Values of ${attributeLabel(attributeKey)}`}>
              {suggestions.map((v) => (
                <li key={v.value}>
                  <button
                    type="button"
                    onClick={() => onApply(v.value)}
                    className="flex w-full items-baseline justify-between gap-2 rounded px-2 py-1 text-left text-sm hover:bg-muted focus:outline-none focus-visible:bg-muted"
                    data-testid={`obs-value-${v.value}`}
                  >
                    <span className="truncate font-mono text-foreground">{v.value || "(empty)"}</span>
                    <span className="shrink-0 text-2xs tabular-nums text-muted-foreground">{formatCount(v.count)}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
          {values.data?.truncated && <p className="px-2 pt-1 text-2xs text-muted-foreground">Showing the most frequent; type to narrow.</p>}
        </div>
      )}
      <div className="flex justify-end">
        <button
          type="submit"
          disabled={op !== "exists" && !value.trim()}
          className="h-7 rounded-md bg-primary px-3 text-xs font-medium text-primary-foreground disabled:opacity-50 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          data-testid="obs-filter-apply"
        >
          Add filter
        </button>
      </div>
    </form>
  );
}

// ── Add a group-by ───────────────────────────────────────────────────────────

function AddGroupBy({ used, onAdd }: { used: string[]; onAdd: (key: string) => void }) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const inputId = useId();
  const onOpenChange = useCallback((next: boolean) => {
    setOpen(next);
    if (!next) setQuery("");
  }, []);
  const pick = (key: string) => {
    onAdd(key);
    onOpenChange(false);
  };
  return (
    <Popover
      label="Group by"
      open={open}
      onOpenChange={onOpenChange}
      testId="obs-add-group-by"
      triggerClassName={cn(CONTROL, "border-dashed text-muted-foreground")}
      trigger={
        <>
          <Plus className="h-3.5 w-3.5" aria-hidden="true" />
          <span className="sr-only">Add a group-by</span>
        </>
      }
    >
      <label htmlFor={inputId} className="sr-only">
        Attribute to group by
      </label>
      <input
        id={inputId}
        autoFocus
        value={query}
        onChange={(event) => setQuery(event.target.value)}
        onKeyDown={(event) => {
          const typed = query.trim();
          if (event.key === "Enter" && isAttributeKeyShaped(typed) && !used.includes(typed)) pick(typed);
        }}
        placeholder="Group by…"
        className="mb-1 h-8 w-full rounded-md border border-border bg-background px-2 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        data-testid="obs-group-by-input"
      />
      <KeyList query={query} used={used} onPick={pick} suggestions={GROUP_BY_SUGGESTIONS} />
    </Popover>
  );
}
