// Copyright (c) 2025 Reliant Labs

/**
 * A user-id filter: a small search box that applies on Enter (the URL's
 * `user`) and offers a way out when one is set. User ids only — the console
 * never shows or searches by an email.
 */

import { useEffect, useState, type FormEvent } from "react";
import { UserRound, X } from "lucide-react";

import { cn } from "@/lib/utils";
import { CONTROL_FIELD } from "../controlStyles";

export function UserIdFilter({ id, value, onChange, className }: { id: string; value: string; onChange: (next: string | undefined) => void; className?: string }) {
  const [draft, setDraft] = useState(value);
  useEffect(() => setDraft(value), [value]);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    onChange(draft.trim() || undefined);
  };
  return (
    <form role="search" onSubmit={submit} className={cn("relative w-48", className)} data-testid={`${id}-form`}>
      <label htmlFor={id} className="sr-only">
        User id
      </label>
      <UserRound className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-muted" aria-hidden="true" />
      <input
        id={id}
        type="search"
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        placeholder="User id"
        className={`${CONTROL_FIELD} h-8 w-full pl-8 pr-7 font-mono text-sm placeholder:font-sans`}
        data-testid={id}
      />
      {value && (
        <button
          type="button"
          onClick={() => onChange(undefined)}
          className="absolute right-1.5 top-1.5 rounded-sm p-0.5 text-ink-muted hover:text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          aria-label={`Clear the user filter (${value})`}
          data-testid={`${id}-clear`}
        >
          <X className="h-3.5 w-3.5" aria-hidden="true" />
        </button>
      )}
    </form>
  );
}
