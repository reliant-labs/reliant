// Copyright (c) 2025 Reliant Labs

/**
 * Who an automation's integration writes (comments, reviews, pull requests)
 * speak as, as two radio cards side by side (stacked on a phone). The cards
 * are labels around real radios, so arrow keys move between them and the
 * whole card is the click target.
 *
 * The bot is every new automation's default: its GitHub writes are the bot's
 * unless the owner explicitly chooses their own account. Integrations with no
 * bot (Slack, Gmail, Twilio, HTTP) always write through the owner's connection,
 * so the choice is only ever about GitHub.
 *
 * The bot needs a control plane to mint its installation tokens. Without one,
 * its option stays visible but unavailable, and the person can explicitly
 * choose their own account. Never hide a source default or silently fall back.
 */

import { useId, type ReactNode } from "react";

import type { ActingIdentityKey } from "@/hooks/trigger-model";
import { cn } from "@/lib/utils";
import { hasControlPlane } from "@/services/controlPlane/config";

export interface ActingIdentityFieldProps {
  value: ActingIdentityKey | undefined;
  onChange: (next: ActingIdentityKey) => void;
  /** Mixed activations keep their stored choices. */
  defaultHint?: string;
  /** The owner's login, to say where their writes appear. */
  ownerLogin?: string;
  /** A non-blocking note under the cards (the bot cannot write somewhere). */
  warning?: ReactNode;
  disabled?: boolean;
  /** Whether this deployment can write as the bot. Default: it has a control plane. */
  botAvailable?: boolean;
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
      hint: "Comments and reviews appear as reliant-labs[bot]. Works in repositories you can push to.",
    },
  ];
}

/** Whether this deployment can write as the bot (it has a control plane). */
export function reliantBotAvailable(): boolean {
  return hasControlPlane;
}

/**
 * The identity a new automation gets when nobody chose one, as the server
 * decides it: the bot wherever the deployment has one. Without a control plane
 * a GitHub-event source still means the bot (and is refused on save until the
 * person explicitly chooses themselves); any other source is the owner's.
 */
export function defaultActingIdentity(githubSource: boolean, botAvailable = hasControlPlane): ActingIdentityKey {
  return botAvailable || githubSource ? "app" : "owner";
}

export function ActingIdentityField({ value, onChange, ownerLogin, warning, defaultHint, disabled = false, botAvailable = hasControlPlane }: ActingIdentityFieldProps) {
  const ids = useId();
  return (
    <fieldset className="min-w-0">
      <legend className="mb-1.5 text-sm font-medium text-foreground">GitHub writes as</legend>
      {defaultHint && <p className="mb-1.5 text-xs text-muted-foreground">{defaultHint}</p>}
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
                disabled={disabled || (choice.value === "app" && !botAvailable)}
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
      {!botAvailable && (
        <p className="mt-1.5 text-xs text-warning-ink">Reliant bot requires a control plane. Choose You explicitly to write as your account; bot writes never fall back automatically.</p>
      )}
      {warning && <div className="mt-1.5 text-xs text-warning-ink">{warning}</div>}
    </fieldset>
  );
}
