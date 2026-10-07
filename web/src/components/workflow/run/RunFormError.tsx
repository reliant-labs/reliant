// Copyright (c) 2025 Reliant Labs

/**
 * Why a run did not start, at the top of the Run… dialog and the builder's
 * Test run panel. The server's own wording, plus a way out when the fix lives
 * elsewhere: a run with no model provider stops at "no API keys configured",
 * and the place to add one is Settings → AI → Your providers
 * (research/WORKFLOW_EDITOR_UX_REVIEW.md §3 quick win 13).
 */

import { Link } from "@tanstack/react-router";

/**
 * Whether a start failure means the user has no model provider to run on.
 * Matches the server's ErrNoAPIKeysConfigured and model selection's
 * "no API keys configured - please add an API key in Settings"
 * (internal/llm/drivers).
 */
export function isMissingProviderKeyError(message: string): boolean {
  return /no api keys? configured/i.test(message);
}

export function RunFormError({ message, onNavigate }: { message: string; onNavigate?: () => void }) {
  return (
    <div
      role="alert"
      className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive-ink"
    >
      {message}
      {isMissingProviderKeyError(message) && (
        <p className="mt-1.5">
          <Link
            to="/settings/$section"
            params={{ section: "general" }}
            onClick={onNavigate}
            className="font-medium underline underline-offset-2 hover:no-underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            Add a provider in Settings → AI → Your providers
          </Link>
        </p>
      )}
    </div>
  );
}
