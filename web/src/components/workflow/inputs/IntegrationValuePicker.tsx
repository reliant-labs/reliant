// Copyright (c) 2025 Reliant Labs

/**
 * IntegrationValuePicker — one control for every typed input
 * (`github.repository`, `github.label`, `github.user`, and every type an
 * integration adds later). It renders from CatalogService.ListInputOptions
 * and nothing else, so nothing in it is per integration
 * (research/autopilot/automation-ux-spec.md §3.5).
 *
 * A WAI-ARIA 1.2 combobox with a listbox popup: the field shows the selected
 * values as chips (multi) or one value (single); opening it moves focus to a
 * search box that drives the listbox through aria-activedescendant. Below
 * `sm`, or on a coarse pointer, the popup is a full-screen sheet instead
 * (PickerSheet): one sheet per level, never a popover inside a sheet.
 *
 * The nine states the spec names, and where each is decided:
 *   1 Loading            first page in flight
 *   2 Not connected      the server answers NEEDS_CONNECTION
 *   3 Waiting on scope   a sibling input it is scoped by is empty (host says)
 *   4 No options         listed fine, nothing there
 *   5 Options failed     listing failed; "Use {query}" still lets a value in
 *   6 No match           the query matches nothing
 *   7 Unreachable value  a stored value the full list no longer has
 *   8 Free text          the type accepts values that do not exist yet
 *   9 Over the cap       more pages: scroll or type to reach them
 *
 * Validation display is the host's: `invalid` only sets aria-invalid here.
 */

import { useCallback, useEffect, useId, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { AlertTriangle, Check, ChevronDown, Loader2, Plus, RefreshCw, X } from "lucide-react";

import type { InputOption } from "@/api/input-options-grpc";
import { inputOptionsErrorReason, useInputOptions, useResolvedInputOptions } from "@/hooks/input-options-queries";
import { cn } from "@/lib/utils";
import { inputTypeNouns } from "./inputTypeNouns";
import { PickerSheet, usePickerSheet } from "./PickerSheet";

export interface IntegrationValuePickerProps {
  /** The qualified type ref, e.g. "github.repository". */
  type: string;
  multi: boolean;
  minItems?: number;
  maxItems?: number;
  required?: boolean;
  value: string[] | string | null | undefined;
  onChange: (next: string[] | string) => void;
  /** The connection the host's integration section chose; empty = the default. */
  connectionId?: string;
  /** Scope key → value, resolved by the host from sibling inputs. */
  scope?: Record<string, unknown>;
  /** A scope input is empty: the list cannot be asked for yet (state 3). */
  scopeMissing?: boolean;
  /** The scope input's label ("Repositories"), for state 3's hint. */
  scopeLabel?: string;
  /** The scope type's plural ("repositories"), for state 3's hint. */
  scopePlural?: string;
  actingIdentity?: "owner" | "app";
  /** The type accepts values not among its options (a label created on first use). */
  freeText?: boolean;
  /** Error text from the host; sets aria-invalid. */
  invalid?: string;
  /** "GitHub", from the host's catalog entry. */
  integrationName?: string;
  /** Opens the host's connect flow (state 2). */
  onConnect?: () => void;
  disabled?: boolean;
  id: string;
  labelId: string;
  /** The label's text, for the mobile sheet's title. */
  label?: string;
  describedBy?: string;
}

type Row =
  | { kind: "option"; option: InputOption; key: string }
  | { kind: "select_all"; group: string; values: string[]; key: string }
  | { kind: "use_query"; value: string; note: string; key: string }
  | { kind: "unreachable"; value: string; key: string };

interface Section {
  heading?: string;
  rows: Row[];
}

const DEBOUNCE_MS = 300;
const PAGE_JUMP = 10;

function asValues(value: IntegrationValuePickerProps["value"], multi: boolean): string[] {
  if (Array.isArray(value)) return value.map(String).filter(Boolean);
  if (typeof value === "string" && value) return multi ? [value] : [value];
  return [];
}

function matchesQuery(option: InputOption, query: string): boolean {
  if (!query) return true;
  const q = query.toLowerCase();
  return [option.label, option.value, option.description, option.group].some((field) => field?.toLowerCase().includes(q));
}

function useDebounced<T>(value: T, ms: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), ms);
    return () => clearTimeout(timer);
  }, [value, ms]);
  return debounced;
}

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

/** Focus the first focusable element after `container` in document order. */
function focusAfter(container: HTMLElement | null): void {
  if (!container) return;
  const next = Array.from(document.querySelectorAll<HTMLElement>(FOCUSABLE)).find(
    (el) => !container.contains(el) && container.compareDocumentPosition(el) & Node.DOCUMENT_POSITION_FOLLOWING,
  );
  next?.focus();
}

export function IntegrationValuePicker(props: IntegrationValuePickerProps) {
  const {
    type,
    multi,
    maxItems,
    value,
    onChange,
    connectionId,
    scope,
    scopeMissing = false,
    scopeLabel,
    scopePlural,
    actingIdentity,
    freeText = false,
    invalid,
    integrationName,
    onConnect,
    disabled = false,
    id,
    labelId,
    label,
    describedBy,
    required = false,
  } = props;
  const nouns = inputTypeNouns(type, integrationName);
  const ids = useId();
  const listboxId = `${ids}-listbox`;
  const optionId = (key: string) => `${ids}-opt-${key.replace(/[^a-zA-Z0-9_-]/g, "_")}`;

  const wrapperRef = useRef<HTMLDivElement>(null);
  const fieldRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const chipRefs = useRef<Array<HTMLButtonElement | null>>([]);

  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const debouncedQuery = useDebounced(query.trim(), DEBOUNCE_MS);
  const [active, setActive] = useState(0);
  const [announcement, setAnnouncement] = useState("");
  const sheet = usePickerSheet();

  const values = useMemo(() => asValues(value, multi), [value, multi]);
  const atCap = multi && maxItems !== undefined && values.length >= maxItems;

  const optionsQuery = useInputOptions({
    type,
    connectionId,
    scope,
    query: debouncedQuery,
    actingIdentity,
    enabled: !scopeMissing && !disabled,
  });
  const pages = useMemo(() => optionsQuery.data?.pages ?? [], [optionsQuery.data]);
  const needsConnection = pages[0]?.connectionState === "needs_connection";
  const loaded = useMemo(() => pages.flatMap((page) => page.options), [pages]);
  const byValue = useMemo(() => new Map(loaded.map((o) => [o.value, o])), [loaded]);

  // Stored values the loaded options do not label (an id stored for a login,
  // or a value beyond the loaded pages) are resolved by the server.
  const unlabelled = useMemo(() => values.filter((v) => !byValue.has(v)), [values, byValue]);
  const resolveQuery = useResolvedInputOptions(type, connectionId, unlabelled, optionsQuery.isSuccess && !needsConnection && !scopeMissing);
  const resolved = useMemo(() => new Map((resolveQuery.data ?? []).map((o) => [o.value, o])), [resolveQuery.data]);

  // A value is unreachable only when the WHOLE unfiltered list was read and
  // it was not there, nor could the server resolve it.
  const fullListRead = optionsQuery.isSuccess && !optionsQuery.hasNextPage && !debouncedQuery && !needsConnection;
  const unreachable = useMemo(
    () => new Set(fullListRead && !resolveQuery.isFetching ? unlabelled.filter((v) => !resolved.has(v)) : []),
    [fullListRead, resolveQuery.isFetching, unlabelled, resolved],
  );

  const labelOf = (v: string) => byValue.get(v)?.label ?? resolved.get(v)?.label ?? v;
  const warningsOf = (v: string) => byValue.get(v)?.warnings ?? resolved.get(v)?.warnings ?? [];

  const typedQuery = query.trim();
  const filtered = useMemo(() => loaded.filter((o) => matchesQuery(o, typedQuery)), [loaded, typedQuery]);

  const sections = useMemo<Section[]>(() => {
    const out: Section[] = [];
    if (unreachable.size > 0 && !typedQuery) {
      out.push({ heading: "No longer reachable", rows: [...unreachable].map((v) => ({ kind: "unreachable", value: v, key: `unreachable:${v}` })) });
    }
    const groups = new Map<string, InputOption[]>();
    for (const option of filtered) groups.set(option.group, [...(groups.get(option.group) ?? []), option]);
    const remaining = maxItems === undefined ? Number.POSITIVE_INFINITY : maxItems - values.length;
    for (const [group, options] of groups) {
      const rows: Row[] = [];
      const unselected = options.filter((o) => !values.includes(o.value)).map((o) => o.value);
      if (multi && group && unselected.length > 1 && unselected.length <= remaining) {
        rows.push({ kind: "select_all", group, values: unselected, key: `all:${group}` });
      }
      for (const option of options) rows.push({ kind: "option", option, key: `opt:${option.value}` });
      out.push({ heading: group || undefined, rows });
    }
    const exact = typedQuery && loaded.some((o) => o.value.toLowerCase() === typedQuery.toLowerCase());
    if (typedQuery && !exact && !values.includes(typedQuery)) {
      if (optionsQuery.isError) {
        out.push({ rows: [{ kind: "use_query", value: typedQuery, note: "Not checked yet. It is checked when you save.", key: `use:${typedQuery}` }] });
      } else if (freeText && optionsQuery.isSuccess) {
        out.push({ rows: [{ kind: "use_query", value: typedQuery, note: "created on first use", key: `use:${typedQuery}` }] });
      }
    }
    return out;
  }, [unreachable, typedQuery, filtered, maxItems, values, multi, loaded, optionsQuery.isError, optionsQuery.isSuccess, freeText]);
  const rows = useMemo(() => sections.flatMap((s) => s.rows), [sections]);
  const activeRow = rows[Math.min(active, rows.length - 1)];

  // Keep the active option in view, and the announcement current.
  useEffect(() => {
    if (!open || !activeRow) return;
    document.getElementById(optionId(activeRow.key))?.scrollIntoView?.({ block: "nearest" });
    // optionId is derived from ids, stable for the component's life.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, activeRow]);
  useEffect(() => {
    if (!open || optionsQuery.isFetching) return;
    if (optionsQuery.isError) setAnnouncement(`Couldn't load ${nouns.plural}: ${inputOptionsErrorReason(optionsQuery.error)}.`);
    else setAnnouncement(`${filtered.length} ${filtered.length === 1 ? nouns.singular : nouns.plural}`);
    // Announce when a search settles, not on every keystroke.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, optionsQuery.isFetching, debouncedQuery, optionsQuery.isError]);

  // A click outside closes the popover (the sheet covers the screen and has Done).
  useEffect(() => {
    if (!open || sheet) return;
    const onDown = (event: MouseEvent) => {
      if (!wrapperRef.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open, sheet]);

  const blocked = disabled || scopeMissing || needsConnection;

  const openAt = useCallback(
    (index: "first" | "last") => {
      if (blocked) return;
      setOpen(true);
      setActive(index === "first" ? 0 : Number.MAX_SAFE_INTEGER);
      requestAnimationFrame(() => searchRef.current?.focus());
    },
    [blocked],
  );
  const close = useCallback((refocus = true) => {
    setOpen(false);
    setQuery("");
    if (refocus) requestAnimationFrame(() => fieldRef.current?.focus());
  }, []);

  const emit = (next: string[]) => {
    if (multi) onChange(next);
    else onChange(next[0] ?? "");
  };

  const toggle = (v: string) => {
    if (!multi) {
      emit([v]);
      setAnnouncement(`${labelOf(v)} selected`);
      close();
      return;
    }
    if (values.includes(v)) {
      const next = values.filter((x) => x !== v);
      emit(next);
      setAnnouncement(`${labelOf(v)} removed, ${next.length} selected`);
    } else {
      if (atCap) return;
      const next = [...values, v];
      emit(next);
      setAnnouncement(`${labelOf(v)} selected, ${next.length} selected`);
    }
  };

  const activate = (row: Row | undefined) => {
    if (!row) return;
    switch (row.kind) {
      case "option":
      case "unreachable":
        toggle(row.kind === "option" ? row.option.value : row.value);
        break;
      case "select_all": {
        const next = [...values, ...row.values.filter((v) => !values.includes(v))];
        emit(next);
        setAnnouncement(`${row.values.length} selected in ${row.group}, ${next.length} selected`);
        break;
      }
      case "use_query":
        toggle(row.value);
        setQuery("");
        break;
    }
  };

  const removeAt = (index: number) => {
    const v = values[index];
    const next = values.filter((_, i) => i !== index);
    emit(next);
    setAnnouncement(`${labelOf(v)} removed, ${next.length} selected`);
    requestAnimationFrame(() => {
      const previous = chipRefs.current[Math.max(0, index - 1)];
      if (next.length > 0 && previous) previous.focus();
      else fieldRef.current?.focus();
    });
  };

  const loadMore = () => {
    if (optionsQuery.hasNextPage && !optionsQuery.isFetchingNextPage) {
      setAnnouncement("Loading more");
      void optionsQuery.fetchNextPage();
    }
  };

  const move = (delta: number) => {
    if (rows.length === 0) return;
    const current = Math.min(active, rows.length - 1);
    const next = Math.max(0, Math.min(rows.length - 1, current + delta));
    if (delta > 0 && current + delta >= rows.length - 1) loadMore();
    setActive(next);
  };

  const onSearchKeyDown = (event: ReactKeyboardEvent<HTMLInputElement>) => {
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        move(1);
        break;
      case "ArrowUp":
        event.preventDefault();
        move(-1);
        break;
      case "PageDown":
        event.preventDefault();
        move(PAGE_JUMP);
        break;
      case "PageUp":
        event.preventDefault();
        move(-PAGE_JUMP);
        break;
      case "Home":
        if (event.ctrlKey) return;
        event.preventDefault();
        setActive(0);
        break;
      case "End":
        if (event.ctrlKey) return;
        event.preventDefault();
        setActive(Math.max(0, rows.length - 1));
        break;
      case "Enter":
        // Never submits the form the picker sits in.
        event.preventDefault();
        activate(activeRow);
        break;
      case "Escape":
        // The popover owns this Escape: the dialog around it stays open.
        event.preventDefault();
        event.stopPropagation();
        event.nativeEvent.stopImmediatePropagation();
        close();
        break;
      case "Backspace":
        if (multi && query === "" && values.length > 0) {
          event.preventDefault();
          const v = values[values.length - 1];
          const next = values.slice(0, -1);
          emit(next);
          setAnnouncement(`${labelOf(v)} removed, ${next.length} selected`);
        }
        break;
      case "Tab":
        if (sheet) return;
        // Close, keep the selection, and go on to the next field.
        event.preventDefault();
        setOpen(false);
        setQuery("");
        if (event.shiftKey) fieldRef.current?.focus();
        else focusAfter(wrapperRef.current);
        break;
    }
  };

  const onFieldKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (event.target !== fieldRef.current) return;
    switch (event.key) {
      case "ArrowDown":
      case "Enter":
      case " ":
        event.preventDefault();
        openAt("first");
        break;
      case "ArrowUp":
        event.preventDefault();
        openAt("last");
        break;
      case "ArrowLeft":
      case "Backspace":
        if (multi && values.length > 0) {
          event.preventDefault();
          chipRefs.current[values.length - 1]?.focus();
        }
        break;
    }
  };

  const onChipKeyDown = (index: number) => (event: ReactKeyboardEvent<HTMLButtonElement>) => {
    switch (event.key) {
      case "Backspace":
      case "Delete":
        event.preventDefault();
        removeAt(index);
        break;
      case "ArrowLeft":
        event.preventDefault();
        chipRefs.current[Math.max(0, index - 1)]?.focus();
        break;
      case "ArrowRight":
        event.preventDefault();
        if (index + 1 < values.length) chipRefs.current[index + 1]?.focus();
        else fieldRef.current?.focus();
        break;
    }
  };

  // A field whose first page is still in flight says so (state 1).
  const firstLoad = !scopeMissing && !needsConnection && optionsQuery.isLoading;
  const placeholder = scopeMissing
    ? `Pick ${articleFor(props.scopeLabel ? singularOf(scopePlural) : "value")} first`
    : needsConnection
      ? `Connect ${nouns.integration} first`
      : firstLoad
        ? `Loading ${nouns.plural}…`
        : multi
          ? values.length > 0
            ? `Add ${articleFor(nouns.singular)}…`
            : `Choose ${nouns.plural}…`
          : `Choose ${articleFor(nouns.singular)}…`;

  const stateHintId = `${ids}-state-hint`;
  const countId = `${ids}-count`;
  const describedByIds = [describedBy, (scopeMissing || needsConnection) && stateHintId, multi && countId].filter(Boolean).join(" ") || undefined;

  const panel = (
    <PickerPanel
      nouns={nouns}
      searchRef={searchRef}
      listRef={listRef}
      listboxId={listboxId}
      labelId={labelId}
      multi={multi}
      query={query}
      onQuery={(q) => {
        setQuery(q);
        setActive(0);
      }}
      onSearchKeyDown={onSearchKeyDown}
      sections={sections}
      activeKey={activeRow?.key}
      optionId={optionId}
      values={values}
      atCap={atCap}
      maxItems={maxItems}
      onActivate={(row) => {
        activate(row);
        searchRef.current?.focus();
      }}
      onHover={(key) => setActive(Math.max(0, rows.findIndex((r) => r.key === key)))}
      loading={optionsQuery.isLoading}
      fetching={optionsQuery.isFetching}
      error={optionsQuery.isError ? inputOptionsErrorReason(optionsQuery.error) : null}
      onRetry={() => void optionsQuery.refetch()}
      hasNextPage={!!optionsQuery.hasNextPage}
      fetchingNextPage={optionsQuery.isFetchingNextPage}
      onLoadMore={loadMore}
      loadedCount={loaded.length}
      empty={optionsQuery.isSuccess && loaded.length === 0 && !debouncedQuery}
      noMatch={!!typedQuery && filtered.length === 0 && !optionsQuery.isError}
      sheet={sheet}
    />
  );

  return (
    <div ref={wrapperRef} className="relative min-w-0" data-testid="integration-value-picker" data-type={type}>
      <div
        className={cn(
          "flex min-h-9 w-full flex-wrap items-center gap-1.5 rounded-md border border-input bg-background px-2 py-1 text-sm",
          "focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/30",
          invalid && "border-destructive",
          blocked && "opacity-70",
        )}
      >
        {multi &&
          values.map((v, index) => (
            <Chip
              key={v}
              label={labelOf(v)}
              warnings={warningsOf(v)}
              unreachable={unreachable.has(v)}
              buttonRef={(el) => {
                chipRefs.current[index] = el;
              }}
              onRemove={() => removeAt(index)}
              onKeyDown={onChipKeyDown(index)}
              disabled={disabled}
            />
          ))}
        <div
          ref={fieldRef}
          id={id}
          role="combobox"
          tabIndex={0}
          aria-labelledby={labelId}
          aria-describedby={describedByIds}
          aria-expanded={open}
          aria-controls={listboxId}
          aria-haspopup="listbox"
          aria-required={required || undefined}
          aria-invalid={invalid ? true : undefined}
          aria-disabled={blocked || undefined}
          aria-busy={firstLoad || undefined}
          onKeyDown={onFieldKeyDown}
          onClick={() => (open ? close(false) : openAt("first"))}
          className={cn(
            "flex min-h-7 min-w-[8rem] flex-1 cursor-pointer items-center justify-between gap-2 rounded-sm px-1 text-left focus:outline-none",
            blocked && "cursor-not-allowed",
          )}
          data-testid="picker-field"
        >
          <span className={cn("truncate", !multi && values.length > 0 ? "text-foreground" : "text-muted-foreground")}>
            {!multi && values.length > 0 ? (
              <SingleValue label={labelOf(values[0])} unreachable={unreachable.has(values[0])} warnings={warningsOf(values[0])} />
            ) : (
              placeholder
            )}
          </span>
          {firstLoad ? (
            <Loader2 className="h-4 w-4 shrink-0 animate-spin text-muted-foreground motion-reduce:animate-none" aria-hidden />
          ) : (
            <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
          )}
        </div>
        {!multi && values.length > 0 && !required && !disabled && (
          <button
            type="button"
            aria-label={`Clear ${labelOf(values[0])}`}
            onClick={(e) => {
              e.stopPropagation();
              onChange("");
            }}
            className="rounded-sm p-0.5 text-muted-foreground hover:bg-muted/70 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            <X className="h-3.5 w-3.5" aria-hidden />
          </button>
        )}
      </div>

      {multi && (
        <p id={countId} className="mt-1 text-xs text-muted-foreground" data-testid="picker-count">
          {values.length} selected{maxItems !== undefined ? ` · up to ${maxItems}` : ""}
        </p>
      )}

      {scopeMissing && (
        <p id={stateHintId} className="mt-1 text-xs text-muted-foreground" data-testid="picker-scope-hint">
          {nouns.plural.charAt(0).toUpperCase() + nouns.plural.slice(1)} come from the {scopePlural ?? "values"} you choose under {scopeLabel ?? "the field above"}.
        </p>
      )}
      {needsConnection && !scopeMissing && (
        <div id={stateHintId} className="mt-1.5 flex flex-wrap items-center gap-2 text-xs text-muted-foreground" data-testid="picker-connect">
          <span>Connect {nouns.integration} to choose {nouns.plural}.</span>
          {onConnect && (
            <button
              type="button"
              onClick={onConnect}
              className="inline-flex items-center gap-1 rounded-md bg-primary px-2 py-1 text-xs font-medium text-primary-foreground hover:bg-primary/90 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
            >
              <Plus className="h-3.5 w-3.5" aria-hidden /> Connect {nouns.integration}
            </button>
          )}
        </div>
      )}

      {open &&
        (sheet ? (
          <PickerSheet
            title={label ?? nouns.plural}
            count={values.length}
            multi={multi}
            onDone={() => close()}
          >
            {panel}
          </PickerSheet>
        ) : (
          <div
            className="absolute left-0 right-0 top-full z-30 mt-1 overflow-hidden rounded-md border border-border bg-card elevation-3 motion-safe:animate-in motion-safe:fade-in-0"
            data-testid="picker-popover"
          >
            {panel}
          </div>
        ))}

      <div aria-live="polite" className="sr-only" data-testid="picker-live">
        {announcement}
      </div>
    </div>
  );
}

function articleFor(noun: string): string {
  return /^[aeiou]/i.test(noun) ? `an ${noun}` : `a ${noun}`;
}

function singularOf(plural: string | undefined): string {
  if (!plural) return "value";
  if (plural.endsWith("ies")) return `${plural.slice(0, -3)}y`;
  if (/(ses|xes|zes|ches|shes)$/.test(plural)) return plural.slice(0, -2);
  return plural.replace(/s$/, "");
}

function SingleValue({ label, unreachable, warnings }: { label: string; unreachable: boolean; warnings: string[] }) {
  return (
    <span className={cn("inline-flex items-center gap-1.5", unreachable && "text-danger-ink line-through")}>
      {warnings.length > 0 && <AlertTriangle className="h-3.5 w-3.5 text-warning-ink" aria-label="Warning" />}
      {label}
      {unreachable && <span className="no-underline"> · no longer reachable</span>}
    </span>
  );
}

function Chip({
  label,
  warnings,
  unreachable,
  buttonRef,
  onRemove,
  onKeyDown,
  disabled,
}: {
  label: string;
  warnings: string[];
  unreachable: boolean;
  buttonRef: (el: HTMLButtonElement | null) => void;
  onRemove: () => void;
  onKeyDown: (event: ReactKeyboardEvent<HTMLButtonElement>) => void;
  disabled: boolean;
}) {
  const warned = warnings.length > 0 && !unreachable;
  return (
    <span
      className={cn(
        "inline-flex max-w-full items-center gap-1 rounded-md border px-1.5 py-0.5 text-xs",
        unreachable
          ? "border-danger-border bg-danger-surface text-danger-ink"
          : warned
            ? "border-warning-border bg-warning-surface text-warning-ink"
            : "border-border/60 bg-card text-foreground",
      )}
      title={warned ? warnings.join(" ") : undefined}
      data-testid="picker-chip"
      data-unreachable={unreachable || undefined}
    >
      {warned && <AlertTriangle className="h-3 w-3 shrink-0" aria-hidden />}
      <span className={cn("truncate", unreachable && "line-through")}>{label}</span>
      {unreachable && <span className="shrink-0"> · no longer reachable</span>}
      {warned && <span className="sr-only">: {warnings.join(" ")}</span>}
      <button
        ref={buttonRef}
        type="button"
        onClick={onRemove}
        onKeyDown={onKeyDown}
        disabled={disabled}
        aria-label={`Remove ${label}`}
        className="-mr-0.5 rounded-sm p-0.5 hover:bg-muted/70 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      >
        <X className="h-3 w-3" aria-hidden />
      </button>
    </span>
  );
}

interface PickerPanelProps {
  nouns: ReturnType<typeof inputTypeNouns>;
  searchRef: React.RefObject<HTMLInputElement | null>;
  listRef: React.RefObject<HTMLDivElement | null>;
  listboxId: string;
  labelId: string;
  multi: boolean;
  query: string;
  onQuery: (q: string) => void;
  onSearchKeyDown: (event: ReactKeyboardEvent<HTMLInputElement>) => void;
  sections: Section[];
  activeKey?: string;
  optionId: (key: string) => string;
  values: string[];
  atCap: boolean;
  maxItems?: number;
  onActivate: (row: Row) => void;
  onHover: (key: string) => void;
  loading: boolean;
  fetching: boolean;
  error: string | null;
  onRetry: () => void;
  hasNextPage: boolean;
  fetchingNextPage: boolean;
  onLoadMore: () => void;
  loadedCount: number;
  empty: boolean;
  noMatch: boolean;
  sheet: boolean;
}

function PickerPanel(p: PickerPanelProps) {
  const groupIds = useId();
  const rowsCount = p.sections.reduce((n, s) => n + s.rows.length, 0);
  return (
    <div className={cn("flex min-h-0 flex-col", p.sheet && "h-full")}>
      <div className="border-b border-border/60 p-2">
        <input
          ref={p.searchRef}
          type="text"
          role="combobox"
          aria-label={`Search ${p.nouns.plural}`}
          aria-expanded
          aria-controls={p.listboxId}
          aria-autocomplete="list"
          aria-activedescendant={p.activeKey ? p.optionId(p.activeKey) : undefined}
          value={p.query}
          onChange={(e) => p.onQuery(e.target.value)}
          onKeyDown={p.onSearchKeyDown}
          placeholder={`Search ${p.nouns.plural}…`}
          className="h-8 w-full rounded-md border border-input bg-background px-2.5 text-sm text-foreground placeholder:text-muted-foreground/70 focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring/30"
          data-testid="picker-search"
        />
      </div>

      <div
        ref={p.listRef}
        id={p.listboxId}
        role="listbox"
        aria-labelledby={p.labelId}
        aria-multiselectable={p.multi || undefined}
        aria-busy={p.loading || undefined}
        className={cn("overflow-y-auto py-1", p.sheet ? "min-h-0 flex-1" : "max-h-72")}
        onScroll={(e) => {
          const el = e.currentTarget;
          if (el.scrollTop + el.clientHeight >= el.scrollHeight - 40) p.onLoadMore();
        }}
      >
        {p.loading ? (
          <div className="space-y-1 px-2 py-1" data-testid="picker-skeleton">
            {[0, 1, 2, 3].map((i) => (
              <div key={i} className="h-7 animate-pulse rounded bg-border/60 motion-reduce:animate-none" />
            ))}
          </div>
        ) : (
          p.sections.map((section, sectionIndex) => {
            const headingId = `${groupIds}-g${sectionIndex}`;
            const body = section.rows.map((row) => (
              <OptionRow
                key={row.key}
                row={row}
                id={p.optionId(row.key)}
                active={p.activeKey === row.key}
                selected={row.kind === "option" ? p.values.includes(row.option.value) : row.kind === "unreachable" ? p.values.includes(row.value) : false}
                disabledByCap={p.atCap && row.kind === "option" && !p.values.includes(row.option.value)}
                multi={p.multi}
                onActivate={() => p.onActivate(row)}
                onHover={() => p.onHover(row.key)}
              />
            ));
            if (!section.heading) return <div key={`s${sectionIndex}`}>{body}</div>;
            return (
              <div key={`s${sectionIndex}`} role="group" aria-labelledby={headingId}>
                <div id={headingId} className="px-3 pb-0.5 pt-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                  {section.heading}
                </div>
                {body}
              </div>
            );
          })
        )}

        {!p.loading && p.error && (
          <div className="px-3 py-2 text-xs" data-testid="picker-error">
            <p className="text-danger-ink">
              Couldn&apos;t load {p.nouns.plural}: {p.error}.
            </p>
            <button
              type="button"
              onClick={p.onRetry}
              className="mt-1 inline-flex items-center gap-1 rounded-sm font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
            >
              <RefreshCw className="h-3 w-3" aria-hidden /> Try again
            </button>
            {!p.query && <p className="mt-1 text-muted-foreground">You can still type a value to use it.</p>}
          </div>
        )}
        {!p.loading && !p.error && p.empty && (
          <div className="px-3 py-3 text-xs text-muted-foreground" data-testid="picker-empty">
            <p>Nothing to choose from yet.</p>
            <button
              type="button"
              onClick={p.onRetry}
              className="mt-1 inline-flex items-center gap-1 rounded-sm font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
            >
              <RefreshCw className="h-3 w-3" aria-hidden /> Refresh
            </button>
          </div>
        )}
        {!p.loading && p.noMatch && rowsCount === 0 && (
          <p className="px-3 py-3 text-xs text-muted-foreground" data-testid="picker-no-match">
            No {p.nouns.plural} match “{p.query.trim()}”.
          </p>
        )}
        {p.fetchingNextPage && (
          <p className="flex items-center gap-1.5 px-3 py-2 text-xs text-muted-foreground">
            <Loader2 className="h-3 w-3 animate-spin motion-reduce:animate-none" aria-hidden /> Loading more…
          </p>
        )}
      </div>

      <div className="flex items-center justify-between gap-2 border-t border-border/60 bg-background px-3 py-1.5 text-xs text-muted-foreground" data-testid="picker-footer">
        <span className="truncate">
          {p.hasNextPage
            ? `Showing the first ${p.loadedCount} ${p.nouns.plural}. Type to search the rest.`
            : p.atCap
              ? `Up to ${p.maxItems} ${p.nouns.plural}`
              : `From your ${p.nouns.integration} account`}
        </span>
        {p.fetching && !p.loading && !p.fetchingNextPage && <Loader2 className="h-3 w-3 shrink-0 animate-spin motion-reduce:animate-none" aria-hidden />}
      </div>
    </div>
  );
}

function OptionRow({
  row,
  id,
  active,
  selected,
  disabledByCap,
  multi,
  onActivate,
  onHover,
}: {
  row: Row;
  id: string;
  active: boolean;
  selected: boolean;
  disabledByCap: boolean;
  multi: boolean;
  onActivate: () => void;
  onHover: () => void;
}) {
  const warnId = `${id}-warn`;
  const base = cn(
    "flex min-h-9 cursor-pointer items-start gap-2 px-3 py-1.5 text-sm [@media(pointer:coarse)]:min-h-11",
    active && "bg-muted/70",
    disabledByCap && "cursor-not-allowed opacity-50",
  );
  // Options are not focusable themselves: focus stays in the search box and
  // aria-activedescendant points here. mousedown keeps it there.
  const common = {
    id,
    onMouseDown: (e: React.MouseEvent) => e.preventDefault(),
    onClick: () => !disabledByCap && onActivate(),
    onMouseMove: onHover,
  };
  if (row.kind === "select_all") {
    return (
      <div {...common} role="option" aria-selected={false} className={cn(base, "text-primary")}>
        <Plus className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden />
        Select all {row.values.length} in {row.group}
      </div>
    );
  }
  if (row.kind === "use_query") {
    return (
      <div {...common} role="option" aria-selected={false} className={base} data-testid="picker-use-query">
        <Plus className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden />
        <span>
          Use “{row.value}” <span className="text-xs text-muted-foreground">· {row.note}</span>
        </span>
      </div>
    );
  }
  const value = row.kind === "option" ? row.option : undefined;
  const label = value?.label ?? (row.kind === "unreachable" ? row.value : "");
  const warnings = value?.warnings ?? [];
  return (
    <div
      {...common}
      role="option"
      aria-selected={selected}
      aria-disabled={disabledByCap || undefined}
      aria-describedby={warnings.length > 0 ? warnId : undefined}
      className={base}
      data-value={row.kind === "option" ? row.option.value : row.value}
    >
      <span
        aria-hidden
        className={cn(
          "mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center border",
          multi ? "rounded-sm" : "rounded-full",
          selected ? "border-primary bg-primary text-primary-foreground" : "border-input bg-background",
        )}
      >
        {selected && <Check className="h-3 w-3" />}
      </span>
      <span className="min-w-0">
        <span className={cn("block truncate text-foreground", row.kind === "unreachable" && "text-danger-ink line-through")}>{label}</span>
        {value?.description && <span className="block truncate text-xs text-muted-foreground">{value.description}</span>}
        {warnings.length > 0 && (
          <span id={warnId} className="mt-0.5 flex items-center gap-1 text-xs text-warning-ink">
            <AlertTriangle className="h-3 w-3 shrink-0" aria-hidden />
            {warnings.join(" ")}
          </span>
        )}
      </span>
    </div>
  );
}
