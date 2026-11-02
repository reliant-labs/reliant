// Copyright (c) 2025 Reliant Labs

/**
 * "I could not read it" is not "there is nothing".
 *
 * The console's pages learn what an app has deployed from one control-plane
 * read (the roster, DeployService.GetLiveView). When that read fails, its
 * environment list is empty because nothing was read, and a page that answered
 * "Nothing deployed yet" over it told the owner of a live app to deploy it.
 * Such a page renders this instead: what could not be loaded, that it says
 * nothing about the app, and Retry when retrying can help (an unreachable read;
 * a refused one says why in the CloudNotice above it and will refuse again).
 *
 * `no-control-plane` is not a failed read: a local build has no deploy state to
 * read, and its pages keep their getting-started steps.
 */

import { AlertTriangle } from "lucide-react";

import Button from "@/components/forge-ui/button";
import type { LiveAvailability } from "@/services/forge/live";

export interface DeployStatusUnreadState {
  /** Present when retrying can help. */
  onRetry?: () => void;
  retrying?: boolean;
}

/** The roster's read failed (anything but an answer, or a build with no control plane). */
export function deployStatusUnread(
  live: { data?: { availability: LiveAvailability }; refetch: () => unknown; isFetching: boolean }
): DeployStatusUnreadState | undefined {
  const availability = live.data?.availability;
  if (availability === undefined || availability === "available" || availability === "no-control-plane") return undefined;
  return availability === "unreachable" ? { onRetry: () => void live.refetch(), retrying: live.isFetching } : {};
}

export function DeployStatusUnread({ state, testId = "deploy-status-unread" }: { state: DeployStatusUnreadState; testId?: string }) {
  return (
    <div role="alert" data-testid={testId} className="flex items-start gap-3 rounded-lg border border-border bg-surface px-4 py-4">
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warning-ink" aria-hidden="true" />
      <div className="min-w-0 flex-1 space-y-2">
        <p className="text-sm font-medium text-ink">Couldn&apos;t load deployment status</p>
        <p className="text-xs text-ink-muted">Reliant could not read what this app has deployed, so this page can&apos;t say. It doesn&apos;t mean nothing is deployed.</p>
        {state.onRetry && (
          <Button variant="outline" size="sm" onClick={state.onRetry} loading={state.retrying} data-testid={`${testId}-retry`}>
            Retry
          </Button>
        )}
      </div>
    </div>
  );
}
