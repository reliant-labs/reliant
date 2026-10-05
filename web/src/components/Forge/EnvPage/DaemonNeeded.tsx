// Copyright (c) 2025 Reliant Labs

/**
 * What a daemon-backed tab shows when the daemon is not answering.
 *
 * Says WHAT this tab needs and that the rest of the page is unaffected — the
 * old one-liner ("Daemon offline. Preview needs it to render your code.") gave
 * no retry and read like the whole environment was in question. The backend
 * tabs (Overview, Releases, Secrets) are fully rendered regardless.
 */

import { RefreshCw, Unplug } from "lucide-react";

import { Button } from "@/components/ui/Button";

/** The one sentence, kept as a constant so tests and copy cannot drift. */
export const DAEMON_OFFLINE_COPY = "Your daemon isn't answering, so this tab can't read your checkout.";

export function DaemonNeeded({
  what,
  detail,
  onRetry,
  retrying = false,
}: {
  /** What this tab would show, e.g. "what this branch would change in prod". */
  what: string;
  detail?: string;
  onRetry?: () => void;
  retrying?: boolean;
}) {
  return (
    <div
      data-testid="daemon-needed"
      className="flex items-start gap-3 rounded-lg border border-dashed border-border bg-background px-4 py-4"
    >
      <Unplug className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
      <div className="min-w-0 flex-1 space-y-1">
        <p className="text-sm text-foreground">{DAEMON_OFFLINE_COPY}</p>
        <p className="text-xs text-muted-foreground">
          It shows {what}. Overview, Releases and Secrets come from Reliant and don&apos;t need it.
        </p>
        {detail && <p className="font-mono text-2xs text-muted-foreground">{detail}</p>}
      </div>
      {onRetry && (
        <Button
          variant="outline"
          size="sm"
          onClick={onRetry}
          loading={retrying}
          disabled={retrying}
          leftIcon={<RefreshCw className="h-3 w-3" />}
          data-testid="daemon-retry"
        >
          Retry
        </Button>
      )}
    </div>
  );
}
