// Copyright (c) 2025 Reliant Labs

/**
 * What a finished claim did, said where its result is: the app is yours now,
 * and, when the claimer chose between two free static sites, which one is
 * paused and that billing undoes it. Set by components/Claim/useClaimFlow.ts
 * (`?claimed=<project>[&pausedSite=<name>&kept=claimed|existing]`), read by
 * the page the claim lands on: Projects (`?show=apps`), and the apps list.
 */

import { useEffect, useState } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { CircleCheck } from "lucide-react";

import { useGoToBilling } from "@/hooks/useGoToBilling";

export interface PausedSite {
  /** The site that is paused. */
  name: string;
  /** Which one the person kept live. */
  kept: "claimed" | "existing";
}

/**
 * The site is already in the user's own org by the time this shows; the list
 * below may still be catching up, which the banner says rather than leaving
 * the user to wonder.
 */
export function ClaimedBanner({
  name,
  listed,
  loading,
  paused,
}: {
  name: string;
  listed: boolean;
  loading: boolean;
  paused: PausedSite | null;
}) {
  const goToBilling = useGoToBilling("upgrade");
  return (
    <p
      className="flex items-start gap-2 rounded-xl border border-border bg-card px-4 py-3 text-ui text-foreground"
      role="status"
      data-testid="apps-claimed"
    >
      <CircleCheck className="mt-0.5 h-4 w-4 shrink-0 text-success-ink" aria-hidden="true" />
      <span>
        <strong className="font-semibold">{name}</strong> is yours now.
        {!loading && !listed && " It can take a moment to appear in this list."}
        {paused && (
          <span data-testid="apps-claimed-paused">
            {" "}
            {paused.kept === "claimed" ? "You chose free hosting for the site you claimed. " : "You kept free hosting where it was. "}
            <strong className="font-semibold">{paused.name}</strong> is paused.{" "}
            <button type="button" onClick={goToBilling} className="font-medium underline underline-offset-2">
              Review billing
            </button>
          </span>
        )}
      </span>
    </p>
  );
}

/**
 * The app a finished claim just made the user's own. Remembered, then
 * dropped from the URL (the page's other search, its filter, stays), so a
 * reload or a shared link does not highlight it again. `found` holds off
 * until the list has loaded: the claim moved the site into the user's org,
 * and the list is what shows it.
 */
export function useClaimedApp(names: string[], loading: boolean): { name: string | null; found: boolean; paused: PausedSite | null } {
  const navigate = useNavigate();
  const { claimed, pausedSite, kept } = useSearch({ strict: false }) as {
    claimed?: string;
    pausedSite?: string;
    kept?: "claimed" | "existing";
  };
  const [name, setName] = useState<string | null>(null);
  const [paused, setPaused] = useState<PausedSite | null>(null);

  useEffect(() => {
    if (!claimed) return;
    setName(claimed);
    setPaused(pausedSite && kept ? { name: pausedSite, kept } : null);
    void navigate({
      to: ".",
      search: (prev: Record<string, unknown>) => ({ ...prev, claimed: undefined, pausedSite: undefined, kept: undefined }),
      replace: true,
    } as never);
  }, [claimed, pausedSite, kept, navigate]);

  return { name, found: !loading && name !== null && names.includes(name), paused };
}
