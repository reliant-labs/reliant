// Copyright (c) 2025 Reliant Labs

/**
 * The transcript footer's word on the chat's machine (lib/chatMachineNotice):
 * "Waking your machine — your message will send when it connects · 1m 12s",
 * "Your machine failed to start" with Try again, "Your machine is asleep" with
 * Start it.
 *
 * ChatPresenter renders it in place of the thinking indicator whenever the
 * chat has work waiting on a machine that is down, so it does not depend on
 * the run having reached the machine wait — a run that is retrying, resuming
 * or still replaying shows it too.
 */

import { useEffect, useState } from "react";
import { AlertTriangle } from "lucide-react";
import StatusDot from "../forge-ui/status_dot";
import { useResumeDaemon } from "@/hooks/useOnboardingQueries";
import { markWaking } from "@/lib/machineWake";
import { resumeErrorMessage } from "@/lib/daemon-resume";
import { formatWaitElapsed, type ChatMachineNotice } from "@/lib/chatMachineNotice";

/** Ticks once a second while there is a wait to time. */
function useElapsedSince(since: number | undefined): string | null {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (since === undefined) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [since]);
  return since === undefined ? null : formatWaitElapsed(now - since);
}

export function ChatMachineNoticeLine({ notice }: { notice: ChatMachineNotice }) {
  const elapsed = useElapsedSince(notice.since);
  const [error, setError] = useState<string | null>(null);
  // Resume is the one action for both: it wakes an asleep machine, and the
  // control plane treats it as a retry that rebuilds a failed one.
  const resume = useResumeDaemon({
    onSuccess: (daemonId) => {
      setError(null);
      markWaking(daemonId);
    },
    onError: (err) => setError(resumeErrorMessage(err)),
  });
  const act = () => {
    setError(null);
    resume.mutate(notice.daemonId);
  };

  return (
    <div
      role="status"
      aria-live="polite"
      data-testid="chat-machine-notice"
      data-kind={notice.kind}
      className="flex flex-col gap-0.5"
    >
      <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-sm">
        {notice.kind === "failed" ? (
          <AlertTriangle className="h-3.5 w-3.5 flex-shrink-0 text-warning" aria-hidden="true" />
        ) : (
          <StatusDot variant={notice.offerStart ? "paused" : "pending"} pulse={!notice.offerStart} size="sm" />
        )}
        <span className="text-foreground">{notice.title}</span>
        {elapsed && (
          <span className="text-xs tabular-nums text-muted-foreground" data-testid="chat-machine-notice-elapsed">
            · {elapsed}
          </span>
        )}
        {(notice.offerRetry || notice.offerStart) && (
          <button
            type="button"
            onClick={act}
            disabled={resume.isPending}
            className="text-xs font-medium text-primary hover:underline disabled:opacity-60 disabled:no-underline"
          >
            {resume.isPending ? "Starting…" : notice.offerRetry ? "Try again" : "Start it"}
          </button>
        )}
      </div>
      {notice.detail && <p className="pl-5 text-xs text-muted-foreground">{notice.detail}</p>}
      {error && (
        <p role="alert" className="pl-5 text-xs text-destructive">
          {error}
        </p>
      )}
    </div>
  );
}
