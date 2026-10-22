// Copyright (c) 2025 Reliant Labs

/**
 * The Workflows area's filter controls: one compact button per filter that
 * says its current value ("State: Failed, Live"), opening a menu of choices.
 * Built on ui/Dropdown (portalled, click-outside, Escape) so it behaves like
 * every other menu in the app.
 *
 * They replaced rows of pill chips. Chips spelled out every option all the
 * time, which on Runs meant four rows of buttons above the list; a menu puts
 * the choices one click away while the button still shows what is applied,
 * so an active filter never hides (the reason chips were chosen originally).
 */

import { useState } from "react";
import { Check, ChevronDown, Search } from "lucide-react";

import { cn } from "@/lib/utils";
import { Dropdown } from "../ui/Dropdown";

export interface FilterOption<T extends string> {
  value: T;
  label: string;
}

/**
 * What a filter's button says: "Any" when nothing narrows it, the one label
 * when one is picked, "A, B" for two, and "A +2" beyond that so the button
 * keeps its width.
 */
export function filterSummary(labels: string[], emptyLabel = "Any"): string {
  if (labels.length === 0) return emptyLabel;
  if (labels.length <= 2) return labels.join(", ");
  return `${labels[0]} +${labels.length - 1}`;
}

function FilterTrigger({
  label,
  summary,
  active,
  open,
  onToggle,
}: {
  label: string;
  summary: string;
  active: boolean;
  open: boolean;
  onToggle: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onToggle}
      aria-haspopup="menu"
      aria-expanded={open}
      aria-label={`${label}: ${summary}`}
      className={cn(
        "inline-flex h-8 max-w-[16rem] items-center gap-1.5 rounded-md border px-2.5 text-sm transition-colors",
        "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40",
        active
          ? "border-primary/50 bg-primary/10 text-foreground"
          : "border-border bg-background text-foreground hover:border-muted-foreground/50",
      )}
    >
      <span className="shrink-0 text-muted-foreground">{label}</span>
      <span className="truncate font-medium">{summary}</span>
      <ChevronDown
        className={cn("h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform", open && "rotate-180")}
        aria-hidden="true"
      />
    </button>
  );
}

function MenuItem({
  role,
  checked,
  onSelect,
  children,
}: {
  role: "menuitemradio" | "menuitemcheckbox";
  checked: boolean;
  onSelect: () => void;
  children: string;
}) {
  return (
    <button
      type="button"
      role={role}
      aria-checked={checked}
      onClick={onSelect}
      className="flex w-full items-center gap-2 rounded px-2.5 py-1.5 text-left text-sm text-foreground transition-colors hover:bg-muted/60 focus:bg-muted/60 focus:outline-none"
    >
      <Check className={cn("h-3.5 w-3.5 shrink-0 text-primary", !checked && "invisible")} aria-hidden="true" />
      <span className="truncate">{children}</span>
    </button>
  );
}

/** One choice from a list. Closes on pick. */
export function SelectFilter<T extends string>({
  label,
  options,
  value,
  defaultValue,
  onChange,
}: {
  label: string;
  options: FilterOption<T>[];
  value: T;
  /** The value that means "not narrowed"; the button is not highlighted for it. */
  defaultValue: T;
  onChange: (value: T) => void;
}) {
  const [open, setOpen] = useState(false);
  const summary = options.find((option) => option.value === value)?.label ?? value;
  return (
    <Dropdown
      isOpen={open}
      onOpenChange={setOpen}
      variant="form"
      contentClassName="min-w-44 border p-1"
      trigger={
        <FilterTrigger
          label={label}
          summary={summary}
          active={value !== defaultValue}
          open={open}
          onToggle={() => setOpen(!open)}
        />
      }
    >
      <div role="menu" aria-label={label}>
        {options.map((option) => (
          <MenuItem
            key={option.value}
            role="menuitemradio"
            checked={option.value === value}
            onSelect={() => {
              setOpen(false);
              onChange(option.value);
            }}
          >
            {option.label}
          </MenuItem>
        ))}
      </div>
    </Dropdown>
  );
}

/** Any number of choices; empty means "not narrowed". Stays open while picking. */
export function MultiSelectFilter<T extends string>({
  label,
  options,
  value,
  onChange,
  emptyLabel = "Any",
}: {
  label: string;
  options: FilterOption<T>[];
  value: T[];
  onChange: (value: T[]) => void;
  emptyLabel?: string;
}) {
  const [open, setOpen] = useState(false);
  // In the menu's order, not the order they were picked, so the button reads the same way twice.
  const picked = options.filter((option) => value.includes(option.value));
  const toggle = (choice: T) =>
    onChange(value.includes(choice) ? value.filter((v) => v !== choice) : [...value, choice]);
  return (
    <Dropdown
      isOpen={open}
      onOpenChange={setOpen}
      variant="form"
      contentClassName="min-w-48 border p-1"
      trigger={
        <FilterTrigger
          label={label}
          summary={filterSummary(picked.map((option) => option.label), emptyLabel)}
          active={picked.length > 0}
          open={open}
          onToggle={() => setOpen(!open)}
        />
      }
    >
      <div role="menu" aria-label={label}>
        {options.map((option) => (
          <MenuItem
            key={option.value}
            role="menuitemcheckbox"
            checked={value.includes(option.value)}
            onSelect={() => toggle(option.value)}
          >
            {option.label}
          </MenuItem>
        ))}
        {picked.length > 0 && (
          <div className="mt-1 border-t border-border/60 pt-1">
            <button
              type="button"
              onClick={() => {
                setOpen(false);
                onChange([]);
              }}
              className="w-full rounded px-2.5 py-1.5 text-left text-xs text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground focus:bg-muted/60 focus:outline-none"
            >
              Clear {label.toLowerCase()}
            </button>
          </div>
        )}
      </div>
    </Dropdown>
  );
}

/** The search box every filter bar starts with. */
export function FilterSearch({
  value,
  onChange,
  placeholder,
  label,
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
  /** Accessible name. */
  label: string;
}) {
  return (
    <label className="relative flex w-full min-w-[12rem] items-center sm:w-64">
      <span className="sr-only">{label}</span>
      <Search className="pointer-events-none absolute left-2.5 h-4 w-4 text-muted-foreground" aria-hidden="true" />
      <input
        type="search"
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder={placeholder}
        className="h-8 w-full rounded-md border border-border bg-background pl-8 pr-2 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      />
    </label>
  );
}
