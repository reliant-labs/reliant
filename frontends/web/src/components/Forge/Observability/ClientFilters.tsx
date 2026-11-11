// Copyright (c) 2025 Reliant Labs

/**
 * The client and build filters of Sessions, Traces and Errors: browser, OS and
 * the builds (frontend, backend, or an issue's build), as exact values. They
 * apply together on Enter (or Apply) and live in the URL (`browser`, `os`,
 * `fe`, `be`, `build`), so a narrowed list is a link. The browser and OS boxes
 * suggest the names the platform derives (the User-Agent parser's), since a
 * filter matches only the exact name.
 */

import { useState, type FormEvent } from "react";
import { X } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { cn } from "@/lib/utils";
import { CONTROL_FIELD } from "../controlStyles";

export interface ClientFilterValues {
  browser?: string;
  os?: string;
  /** The frontend build (a browser span's service.version). */
  fe?: string;
  /** The backend build (a server span's service.version). */
  be?: string;
  /** An issue event's build, on either platform. */
  build?: string;
}

export type ClientFilterField = keyof ClientFilterValues;

const LABEL: Record<ClientFilterField, string> = {
  browser: "Browser",
  os: "OS",
  fe: "Frontend version",
  be: "Backend version",
  build: "Build",
};

/** What the parser names most clients (uap-core families), offered as suggestions. */
const SUGGESTIONS: Partial<Record<ClientFilterField, readonly string[]>> = {
  browser: ["Chrome", "Electron", "Safari", "Mobile Safari", "Firefox", "Edge", "Chrome Mobile", "Chrome Mobile iOS", "Samsung Internet", "Opera"],
  os: ["macOS", "Windows", "Linux", "iOS", "Android", "ChromeOS"],
};

/** The empty value of every field, for a "clear filters" that clears these too. */
export const NO_CLIENT_FILTERS: Required<{ [K in ClientFilterField]: undefined }> = {
  browser: undefined,
  os: undefined,
  fe: undefined,
  be: undefined,
  build: undefined,
};

/** Whether any of the fields is set. */
export function hasClientFilters(values: ClientFilterValues): boolean {
  return Object.values(values).some(Boolean);
}

export function ClientFilters({
  id,
  fields,
  values,
  onApply,
  className,
}: {
  id: string;
  fields: readonly ClientFilterField[];
  values: ClientFilterValues;
  /** The whole new set: a cleared field is undefined. */
  onApply: (next: ClientFilterValues) => void;
  className?: string;
}) {
  const [draft, setDraft] = useState<ClientFilterValues>(values);
  // Re-sync when the URL changes the applied values (back, a link, a clear),
  // during render rather than in an effect, so the boxes never show stale text.
  const key = fields.map((f) => values[f] ?? "").join("\u0000");
  const [synced, setSynced] = useState(key);
  if (synced !== key) {
    setSynced(key);
    setDraft(values);
  }
  const applied = fields.some((f) => values[f]);
  const pick = (from: ClientFilterValues) => Object.fromEntries(fields.map((f) => [f, from[f]?.trim() || undefined])) as ClientFilterValues;
  const submit = (event: FormEvent) => {
    event.preventDefault();
    onApply(pick(draft));
  };
  return (
    <form role="search" aria-label="Client and build" onSubmit={submit} className={cn("flex flex-wrap items-center gap-2", className)} data-testid={`${id}-form`}>
      {fields.map((field) => {
        const inputId = `${id}-${field}`;
        const suggestions = SUGGESTIONS[field];
        return (
          <div key={field} className="w-36">
            <label htmlFor={inputId} className="sr-only">
              {LABEL[field]}
            </label>
            <input
              id={inputId}
              type="search"
              list={suggestions ? `${inputId}-options` : undefined}
              value={draft[field] ?? ""}
              onChange={(event) => setDraft((d) => ({ ...d, [field]: event.target.value }))}
              placeholder={LABEL[field]}
              className={cn(
                `${CONTROL_FIELD} h-8 w-full px-2.5 text-sm`,
                (field === "fe" || field === "be" || field === "build") && "font-mono placeholder:font-sans"
              )}
              data-testid={inputId}
            />
            {suggestions && (
              <datalist id={`${inputId}-options`}>
                {suggestions.map((s) => (
                  <option key={s} value={s} />
                ))}
              </datalist>
            )}
          </div>
        );
      })}
      <Button type="submit" variant="outline" size="sm" data-testid={`${id}-apply`}>
        Apply
      </Button>
      {applied && (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => onApply(pick({}))}
          leftIcon={<X className="h-3.5 w-3.5" />}
          aria-label="Clear the client and build filters"
          data-testid={`${id}-clear`}
        >
          Clear
        </Button>
      )}
    </form>
  );
}
