// Copyright (c) 2025 Reliant Labs

/**
 * Who an automation's integration writes (comments, reviews, pull requests)
 * speak as, as two radio cards side by side (stacked on a phone). The cards
 * are labels around real radios, so arrow keys move between them and the
 * whole card is the click target.
 */

import { useId, type ReactNode } from "react";

import type { ActingIdentityKey } from "@/api/trigger-grpc";
import { cn } from "@/lib/utils";

export interface ActingIdentityFieldProps {
  value: ActingIdentityKey;
  onChange: (next: ActingIdentityKey) => void;
  /** The owner's login, to say where their writes appear. */
  ownerLogin?: string;
  /** A non-blocking note under the cards (the bot cannot write somewhere). */
  warning?: ReactNode;
  disabled?: boolean;
}

function choices(ownerLogin?: string): { value: ActingIdentityKey; label: string; hint: string }[] {
  return [
    {
      value: "owner",
      label: "You",
      hint: ownerLogin ? `Reviews and comments appear under ${ownerLogin}.` : "Reviews and comments appear under your own account.",
    },
    {
      value: "app",
      label: "Reliant bot",
      hint: "They appear as reliant-labs[bot]. Only where you can push; hosted Reliant only.",
    },
  ];
}

export function ActingIdentityField({ value, onChange, ownerLogin, warning, disabled = false }: ActingIdentityFieldProps) {
  const ids = useId();
  return (
    <fieldset className="min-w-0">
      <legend className="mb-1.5 text-sm font-medium text-foreground">Writes as</legend>
      <div className="grid gap-2 sm:grid-cols-2">
        {choices(ownerLogin).map((choice) => {
          const checked = value === choice.value;
          return (
            <label
              key={choice.value}
              htmlFor={`${ids}-${choice.value}`}
              className={cn(
                "flex cursor-pointer items-start gap-2.5 rounded-md border px-3 py-2.5 text-sm transition-colors",
                checked ? "border-primary/60 bg-card ring-1 ring-primary/30" : "border-border bg-background hover:border-muted-foreground/40",
                disabled && "cursor-not-allowed opacity-60",
              )}
            >
              <input
                id={`${ids}-${choice.value}`}
                type="radio"
                name={`${ids}-identity`}
                className="mt-0.5"
                checked={checked}
                disabled={disabled}
                onChange={() => onChange(choice.value)}
                aria-describedby={`${ids}-${choice.value}-hint`}
              />
              <span className="min-w-0">
                <span className="block font-medium text-foreground">{choice.label}</span>
                <span id={`${ids}-${choice.value}-hint`} className="mt-0.5 block text-xs text-muted-foreground">
                  {choice.hint}
                </span>
              </span>
            </label>
          );
        })}
      </div>
      {warning && <div className="mt-1.5 text-xs text-warning-ink">{warning}</div>}
    </fieldset>
  );
}
