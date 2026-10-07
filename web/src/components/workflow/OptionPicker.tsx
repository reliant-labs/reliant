// Copyright (c) 2025 Reliant Labs

/**
 * A searchable picker for a reference-shaped field (a tool name, a workflow
 * ref): pick from what exists, with each option's description, instead of
 * typing a name from memory into a free-text box.
 *
 * The list is a suggestion, not a wall. "Enter manually" switches to a plain
 * text box (for a name the list does not know yet), and a value that is not
 * in the list opens in that box rather than being shown as nothing. A field
 * that can take an expression keeps its own Fixed / Expression toggle beside
 * this, so the picker never has to.
 */

import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { Check, ChevronDown, Pencil, Search } from "lucide-react";

import { cn } from "../../lib/utils";
import { usePopoverPlacement } from "./usePopoverPlacement";

export interface PickerOption {
  value: string;
  /** How the option is named; the value when empty. */
  label?: string;
  description?: string;
  /** Options sharing a group are listed under its heading, in first-seen order. */
  group?: string;
}

export interface OptionPickerProps {
  /** DOM id of the trigger button (or of the manual text box), for `<label htmlFor>`. */
  id: string;
  value: string;
  onChange: (value: string) => void;
  options: readonly PickerOption[];
  /** Shown when nothing is picked. */
  placeholder?: string;
  /** Placeholder of the manual text box. */
  manualPlaceholder?: string;
  searchPlaceholder?: string;
  /** Shown in the list when there are no options at all. */
  emptyMessage?: string;
  loading?: boolean;
  disabled?: boolean;
  /** Describes the field (its hint's id), for the button and the text box. */
  describedBy?: string;
}

/** The "Enter manually" row's key in the keyboard order (not a value anyone stores). */
const MANUAL_ROW = "\u0000manual";

/** The open list's height at most: search, a max-h-64 list, and the manual row. */
const POPOVER_HEIGHT = 330;

function optionLabel(option: PickerOption): string {
  return option.label || option.value;
}

function matches(option: PickerOption, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [option.value, option.label ?? "", option.description ?? "", option.group ?? ""].some((text) => text.toLowerCase().includes(q));
}

export function OptionPicker({
  id,
  value,
  onChange,
  options,
  placeholder = "Select…",
  manualPlaceholder,
  searchPlaceholder = "Search",
  emptyMessage = "Nothing to pick from yet.",
  loading = false,
  disabled = false,
  describedBy,
}: OptionPickerProps) {
  const selected = options.find((option) => option.value === value);
  // A value the list does not know is edited as text, never hidden.
  const [manual, setManual] = useState(() => value !== "" && !selected && !loading);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  // Highlighted row; focus stays in the search box (aria-activedescendant),
  // so Escape always reaches this list before the builder's own handler.
  const [active, setActive] = useState(0);
  const rootRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const listId = useId();
  const optionIdPrefix = useId();
  const placement = usePopoverPlacement(rootRef, open, POPOVER_HEIGHT);

  useEffect(() => {
    if (!loading && value !== "" && !selected) setManual(true);
  }, [loading, value, selected]);

  useEffect(() => {
    if (!open) return;
    searchRef.current?.focus();
    const onPointerDown = (event: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onPointerDown);
    return () => document.removeEventListener("mousedown", onPointerDown);
  }, [open]);

  const groups = useMemo(() => {
    const out: Array<{ name: string; options: PickerOption[] }> = [];
    for (const option of options) {
      if (!matches(option, query)) continue;
      const name = option.group ?? "";
      const group = out.find((g) => g.name === name);
      if (group) group.options.push(option);
      else out.push({ name, options: [option] });
    }
    return out;
  }, [options, query]);

  // Every row in keyboard order; the last is "Enter manually".
  const rows = useMemo(() => [...groups.flatMap((group) => group.options.map((option) => option.value)), MANUAL_ROW], [groups]);
  const activeRow = rows[Math.min(active, rows.length - 1)];
  const rowId = (row: string) => `${optionIdPrefix}-${rows.indexOf(row)}`;

  const close = () => {
    setOpen(false);
    setQuery("");
    setActive(0);
  };

  const enterManually = () => {
    close();
    setManual(true);
  };

  const choose = (next: string) => {
    onChange(next);
    close();
    buttonRef.current?.focus();
  };

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === "Escape") {
      // A stray Escape leaves the builder; this one only closes the list.
      event.stopPropagation();
      event.preventDefault();
      close();
      buttonRef.current?.focus();
      return;
    }
    if (event.key === "Enter") {
      event.preventDefault();
      if (activeRow === MANUAL_ROW) enterManually();
      else if (activeRow !== undefined) choose(activeRow);
      return;
    }
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    event.preventDefault();
    setActive((index) => (event.key === "ArrowDown" ? Math.min(index + 1, rows.length - 1) : Math.max(index - 1, 0)));
  };

  if (manual) {
    return (
      <div className="flex items-center gap-1.5">
        <input
          id={id}
          value={value}
          onChange={(event) => onChange(event.target.value)}
          placeholder={manualPlaceholder}
          aria-describedby={describedBy}
          disabled={disabled}
          className="cpv2-field-input flex-1 font-mono"
        />
        {options.length > 0 && (
          <button
            type="button"
            onClick={() => {
              setManual(false);
              setOpen(true);
            }}
            disabled={disabled}
            className="cpv2-mode-pill cpv2-mode-pill-compact"
          >
            Pick from list
          </button>
        )}
      </div>
    );
  }

  return (
    <div ref={rootRef} className="relative">
      <button
        ref={buttonRef}
        id={id}
        type="button"
        disabled={disabled || loading}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        aria-describedby={describedBy}
        onClick={() => (open ? close() : setOpen(true))}
        className={cn("cpv2-field-select flex w-full items-center justify-between gap-2 text-left", !selected && "text-muted-foreground")}
      >
        <span className="truncate">{loading ? "Loading…" : selected ? optionLabel(selected) : placeholder}</span>
        <ChevronDown className="h-3.5 w-3.5 flex-shrink-0 opacity-60" aria-hidden />
      </button>

      {open && (
        <div
          onKeyDown={onKeyDown}
          data-placement={placement}
          className={cn(
            "absolute left-0 right-0 z-[1000] overflow-hidden rounded-lg border border-border bg-card shadow-lg",
            placement === "top" ? "bottom-full mb-1" : "top-full mt-1",
          )}
        >
          <div className="flex items-center gap-2 border-b border-border px-2.5 py-2">
            <Search className="h-3.5 w-3.5 flex-shrink-0 text-muted-foreground" aria-hidden />
            <input
              ref={searchRef}
              type="search"
              role="combobox"
              aria-expanded
              aria-controls={listId}
              aria-activedescendant={activeRow !== undefined ? rowId(activeRow) : undefined}
              aria-label={searchPlaceholder}
              value={query}
              onChange={(event) => {
                setQuery(event.target.value);
                setActive(0);
              }}
              placeholder={searchPlaceholder}
              className="w-full bg-transparent text-xs text-foreground placeholder:text-muted-foreground focus:outline-none"
            />
          </div>
          <div id={listId} role="listbox" aria-label={searchPlaceholder} className="max-h-64 overflow-y-auto py-1">
            {groups.map((group) => (
              <div key={group.name || "_"} role="group" aria-label={group.name || undefined}>
                {group.name && (
                  <div className="px-2.5 pb-0.5 pt-2 text-xs font-semibold uppercase tracking-wider text-muted-foreground">{group.name}</div>
                )}
                {group.options.map((option) => {
                  const isSelected = option.value === value;
                  return (
                    <div
                      key={option.value}
                      id={rowId(option.value)}
                      role="option"
                      aria-selected={isSelected}
                      onMouseDown={(event) => event.preventDefault()}
                      onMouseEnter={() => setActive(rows.indexOf(option.value))}
                      onClick={() => choose(option.value)}
                      className={cn(
                        "flex w-full cursor-pointer items-start gap-2 px-2.5 py-1.5 text-left",
                        option.value === activeRow && "bg-muted",
                        isSelected && "text-primary",
                      )}
                    >
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-xs font-medium">{optionLabel(option)}</span>
                        {option.label && option.label !== option.value && (
                          <span className="block truncate font-mono text-xs text-muted-foreground">{option.value}</span>
                        )}
                        {option.description && <span className="line-clamp-2 block text-xs text-muted-foreground">{option.description}</span>}
                      </span>
                      {isSelected && <Check className="mt-0.5 h-3.5 w-3.5 flex-shrink-0" aria-hidden />}
                    </div>
                  );
                })}
              </div>
            ))}
            {options.length === 0 && <p className="px-2.5 py-3 text-xs text-muted-foreground">{emptyMessage}</p>}
            {options.length > 0 && groups.length === 0 && <p className="px-2.5 py-3 text-xs text-muted-foreground">Nothing matches “{query}”.</p>}
            <div
              id={rowId(MANUAL_ROW)}
              role="option"
              aria-selected={false}
              onMouseDown={(event) => event.preventDefault()}
              onMouseEnter={() => setActive(rows.length - 1)}
              onClick={enterManually}
              className={cn(
                "flex w-full cursor-pointer items-center gap-1.5 border-t border-border px-2.5 py-2 text-left text-xs text-muted-foreground hover:text-foreground",
                activeRow === MANUAL_ROW && "bg-muted text-foreground",
              )}
            >
              <Pencil className="h-3 w-3" aria-hidden />
              Enter manually
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
