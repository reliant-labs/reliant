// Copyright (c) 2025 Reliant Labs

/**
 * The schedule sentence's zone: "… at 09:00 Pacific Time ▾". A button showing
 * the zone as a person names it, opening a searchable list of friendly names
 * ("Pacific Time — Los Angeles"). The browser's zone is the default, so most
 * people never open it (report 03 §5).
 */

import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { ChevronDown } from "lucide-react";

import { cn } from "@/lib/utils";
import { fieldClass } from "../../workflow/run/runFormStyles";
import { friendlyZoneName, zoneOptions } from "./schedulePreview";

const MAX_SHOWN = 60;

export interface TimezonePickerProps {
  id: string;
  value: string;
  onChange: (zone: string) => void;
  invalid?: boolean;
}

export function TimezonePicker({ id, value, onChange, invalid }: TimezonePickerProps) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const listId = useId();
  const rootRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);

  const matches = useMemo(() => {
    if (!open) return [];
    const words = query.toLowerCase().split(/\s+/).filter(Boolean);
    const all = zoneOptions();
    const hits = words.length === 0 ? all : all.filter((o) => words.every((w) => `${o.label} ${o.zone}`.toLowerCase().includes(w)));
    return hits.slice(0, MAX_SHOWN);
  }, [open, query]);

  useEffect(() => {
    if (!open) return;
    searchRef.current?.focus();
    const onDown = (event: MouseEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

  const choose = (zone: string) => {
    onChange(zone);
    setOpen(false);
    setQuery("");
  };

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Escape") {
      // Close the list, not the dialog around it.
      event.stopPropagation();
      setOpen(false);
    } else if (event.key === "ArrowDown") {
      event.preventDefault();
      setActive((a) => Math.min(a + 1, matches.length - 1));
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      setActive((a) => Math.max(a - 1, 0));
    } else if (event.key === "Enter") {
      event.preventDefault();
      const pick = matches[active];
      if (pick) choose(pick.zone);
    }
  };

  const label = friendlyZoneName(value || "UTC");

  return (
    <div ref={rootRef} className="relative inline-block">
      <button
        id={id}
        type="button"
        className={cn(fieldClass, "inline-flex w-auto items-center gap-1")}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={`Time zone, ${label}`}
        aria-invalid={invalid}
        onClick={() => {
          setActive(0);
          setOpen((o) => !o);
        }}
      >
        <span className="max-w-[14rem] truncate">{label}</span>
        <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden />
      </button>
      {open && (
        <div className="absolute left-0 z-50 mt-1 w-72 rounded-md border border-border bg-card p-2 shadow-lg">
          <label htmlFor={`${id}-search`} className="sr-only">
            Search time zones
          </label>
          <input
            ref={searchRef}
            id={`${id}-search`}
            className={fieldClass}
            value={query}
            placeholder="Search: Pacific, London, Tokyo…"
            autoComplete="off"
            role="combobox"
            aria-expanded
            aria-controls={listId}
            aria-activedescendant={matches[active] ? `${listId}-${active}` : undefined}
            onChange={(e) => {
              setQuery(e.target.value);
              setActive(0);
            }}
            onKeyDown={onKeyDown}
          />
          <ul id={listId} role="listbox" aria-label="Time zones" className="mt-2 max-h-60 overflow-y-auto text-sm">
            {matches.length === 0 && <li className="px-2 py-1.5 text-xs text-muted-foreground">No zone matches.</li>}
            {matches.map((option, index) => (
              <li
                key={option.zone}
                id={`${listId}-${index}`}
                role="option"
                aria-selected={option.zone === value}
                className={cn(
                  "cursor-pointer rounded-sm px-2 py-1.5 text-foreground",
                  index === active && "bg-muted",
                  option.zone === value && "font-medium",
                )}
                onMouseEnter={() => setActive(index)}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => choose(option.zone)}
              >
                {option.label}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
