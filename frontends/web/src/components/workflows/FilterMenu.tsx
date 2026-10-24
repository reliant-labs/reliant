// Copyright (c) 2025 Reliant Labs

/**
 * The Workflows area's filter controls: one compact button per filter that
 * says its current value ("State: Failed, Live"), opening a menu of choices.
 * Built on forge's dropdown_menu (portalled, a real menu button: arrow keys,
 * Escape, focus back on the trigger) so it behaves like every other menu.
 *
 * They replaced rows of pill chips. Chips spelled out every option all the
 * time, which on Runs meant four rows of buttons above the list; a menu puts
 * the choices one click away while the button still shows what is applied,
 * so an active filter never hides (the reason chips were chosen originally).
 */

import { ChevronDown, Search } from "lucide-react";

import { cn } from "@/lib/utils";
import DropdownMenu from "../forge-ui/dropdown_menu";
import type { PopoverTriggerProps } from "../forge-ui/popover";

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

/** The filter's button: its label, what is applied, and the menu's chevron. */
function FilterTrigger({
  label,
  summary,
  active,
  triggerProps,
}: {
  label: string;
  summary: string;
  active: boolean;
  triggerProps: PopoverTriggerProps;
}) {
  return (
    <button
      type="button"
      {...triggerProps}
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
        className={cn(
          "h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform",
          triggerProps["aria-expanded"] && "rotate-180",
        )}
        aria-hidden="true"
      />
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
  const summary = options.find((option) => option.value === value)?.label ?? value;
  return (
    <DropdownMenu
      align="left"
      label={label}
      menuClassName="min-w-44"
      trigger={(props) => (
        <FilterTrigger label={label} summary={summary} active={value !== defaultValue} triggerProps={props} />
      )}
      groups={[
        {
          selection: "single",
          items: options.map((option) => ({
            label: option.label,
            checked: option.value === value,
            onClick: () => onChange(option.value),
          })),
        },
      ]}
    />
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
  // In the menu's order, not the order they were picked, so the button reads the same way twice.
  const picked = options.filter((option) => value.includes(option.value));
  const toggle = (choice: T) =>
    onChange(value.includes(choice) ? value.filter((v) => v !== choice) : [...value, choice]);
  return (
    <DropdownMenu
      align="left"
      label={label}
      menuClassName="min-w-48"
      trigger={(props) => (
        <FilterTrigger
          label={label}
          summary={filterSummary(picked.map((option) => option.label), emptyLabel)}
          active={picked.length > 0}
          triggerProps={props}
        />
      )}
      groups={[
        {
          selection: "multiple",
          items: options.map((option) => ({
            label: option.label,
            checked: value.includes(option.value),
            keepOpen: true,
            onClick: () => toggle(option.value),
          })),
        },
        ...(picked.length > 0
          ? [{ items: [{ label: `Clear ${label.toLowerCase()}`, onClick: () => onChange([]) }] }]
          : []),
      ]}
    />
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
