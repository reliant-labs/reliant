// Copyright (c) 2025 Reliant Labs

/**
 * The transcript footer while the run is held until a provider's usage limit
 * resets (ChatActivity.WAITING_FOR_USAGE_LIMIT): "Waiting for Anthropic usage
 * limit · resumes 3:42 PM (in 1h 12m)", with Retry now and Stop.
 *
 * The run is live and carries on by itself at the reset, so this is a calm
 * status with a live countdown, not an error. Retry now wakes the server's timer
 * at once (if the limit still holds, the step is refused once and the run waits
 * again on the new reset); Stop ends the run like any other.
 *
 * When the chat's model has a stand-in on another connected provider, "Use
 * <model> · <provider> now" answers the wait with it (use_model): the run
 * continues there until the limited provider's earliest reset, and the chat's
 * policy is untouched. With several accounts on the limited provider the card
 * also names the one that comes back first.
 */

import { useEffect, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import Status from "../forge-ui/status";
import { api } from "@/api/client";
import { useChatModelFallback } from "@/hooks/useChatAccountUsage";
import { useProviderAccounts } from "@/hooks/provider-account-queries";
import { earliestResettingAccount } from "@/lib/usageLimitFallback";
import {
  usageLimitHeadline,
  formatResumeTime,
  usageLimitResumeLine,
  type UsageLimitWait,
} from "@/lib/usageLimitWait";

interface UsageLimitWaitNoteProps {
  chatId: string;
  wait: UsageLimitWait;
  /** "Stop": stop the run. The user's next message starts it again. */
  onStop?: () => void;
}

/** Ticks once a second so the countdown moves. */
function useNow(): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);
  return now;
}

export function UsageLimitWaitNote({ chatId, wait, onStop }: UsageLimitWaitNoteProps) {
  const now = useNow();
  const retry = useMutation({
    mutationFn: (useModel?: string) =>
      useModel ? api.chatsV2.retryUsageLimitWait(chatId, useModel) : api.chatsV2.retryUsageLimitWait(chatId),
  });
  const fallback = useChatModelFallback(chatId);
  const accounts = useProviderAccounts().data;
  const earliest = earliestResettingAccount(
    accounts?.kind === "ready" ? accounts.accounts : [],
    fallback.limitedDriver,
    now,
  );
  const offer = fallback.offer;

  return (
    <div
      role="status"
      aria-live="polite"
      data-testid="usage-limit-wait-note"
      className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm"
    >
      <Status tone="neutral" pulse size="sm" />
      <span className="text-foreground">{usageLimitHeadline(wait)}</span>
      <span className="text-xs tabular-nums text-muted-foreground" data-testid="usage-limit-resume-line">
        · {usageLimitResumeLine(wait, now)}
      </span>
      {earliest && (
        <span className="text-xs text-muted-foreground" data-testid="usage-limit-earliest-account">
          · {earliest.account.label} is back {formatResumeTime(earliest.resetsAt, now)}
        </span>
      )}
      <button
        type="button"
        onClick={() => retry.mutate(undefined)}
        disabled={retry.isPending}
        className="text-xs font-medium text-primary hover:underline disabled:no-underline disabled:opacity-60"
      >
        {retry.isPending ? "Retrying…" : "Retry now"}
      </button>
      {offer && (
        <button
          type="button"
          onClick={() => retry.mutate(offer.modelAtDriver)}
          disabled={retry.isPending}
          className="text-xs font-medium text-primary hover:underline disabled:no-underline disabled:opacity-60"
        >
          Use {offer.label} now
        </button>
      )}
      {onStop && (
        <button
          type="button"
          onClick={onStop}
          className="text-xs font-medium text-muted-foreground hover:text-foreground hover:underline"
        >
          Stop
        </button>
      )}
      {retry.isError && (
        <span role="alert" className="text-xs text-destructive">
          Couldn&apos;t retry — try again.
        </span>
      )}
    </div>
  );
}
