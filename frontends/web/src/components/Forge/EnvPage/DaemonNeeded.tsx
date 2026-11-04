// Copyright (c) 2025 Reliant Labs

/**
 * What a daemon-backed tab shows when the daemon is not answering.
 *
 * Says WHAT this tab needs and that the rest of the page is unaffected — the
 * old one-liner ("Daemon offline. Preview needs it to render your code.") gave
 * no retry and read like the whole environment was in question. The backend
 * tabs (Overview, Releases, Secrets) are fully rendered regardless. Only a local
 * env's Running tab needs the daemon.
 *
 * `forgeDisabled` is the API server refusing the forge API (its operator's
 * kill switch): no daemon was asked, so the daemon is not blamed, and there is
 * no retry, because asking again cannot change the answer.
 */

import { PowerOff, RefreshCw, Unplug } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { FORGE_API_DISABLED_COPY } from "@/lib/forgeApiDisabled";

/** The one sentence, kept as a constant so tests and copy cannot drift. */
export const DAEMON_OFFLINE_COPY = "Your daemon isn't answering, so this tab can't read your machine.";

export function DaemonNeeded({
  what,
  detail,
  onRetry,
  retrying = false,
  forgeDisabled = false,
}: {
  /** What this tab would show, e.g. "what forge env up is running for dev on your machine". */
  what: string;
  detail?: string;
  onRetry?: () => void;
  retrying?: boolean;
  /** The server has the forge API turned off (DaemonSide "disabled"). */
  forgeDisabled?: boolean;
}) {
  const Icon = forgeDisabled ? PowerOff : Unplug;
  return (
    <div
      data-testid={forgeDisabled ? "forge-api-disabled" : "daemon-needed"}
      className="flex items-start gap-3 rounded-lg border border-dashed border-border bg-surface-sunken px-4 py-4"
    >
      <Icon className="mt-0.5 h-4 w-4 shrink-0 text-ink-muted" aria-hidden="true" />
      <div className="min-w-0 flex-1 space-y-1">
        <p className="text-sm text-ink">{forgeDisabled ? FORGE_API_DISABLED_COPY : DAEMON_OFFLINE_COPY}</p>
        <p className="text-xs text-ink-muted">
          {forgeDisabled
            ? `Its operator turned the forge API off, so this tab can't show ${what}.`
            : `It shows ${what}.`}{" "}
          Overview, Releases and Secrets come from Reliant and don&apos;t need it.
        </p>
        {detail && !forgeDisabled && <p className="font-mono text-2xs text-ink-muted">{detail}</p>}
      </div>
      {onRetry && !forgeDisabled && (
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
