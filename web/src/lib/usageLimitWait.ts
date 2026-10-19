// Copyright (c) 2025 Reliant Labs

/**
 * What the chat says while its run is held until a provider's usage limit
 * resets (ChatActivity.WAITING_FOR_USAGE_LIMIT): "Waiting for Anthropic usage
 * limit · resumes 3:42 PM (in 1h 12m)".
 *
 * The server holds the run on a durable timer and retries by itself, so this is
 * a status line with a countdown, not a failure. When the provider named no
 * reset (`probing`), the time is only the next probe, and the line says so
 * instead of promising a time.
 *
 * Pure: no React, no RPCs.
 */

import type { Chat } from "@/gen/reliant/v1/chat_pb";

export interface UsageLimitWait {
  /** Who imposed the limit; empty when unknown. */
  provider: string;
  /** When the run retries, epoch ms. */
  resumesAt: number;
  /** The provider named no reset: resumesAt is the next probe, not a promise. */
  probing: boolean;
}

/** The wait a chat carries, or undefined when it is not waiting on one. */
export function usageLimitWaitOf(chat: Pick<Chat, "usageLimitResumesAt" | "usageLimitProvider" | "usageLimitProbing"> | undefined): UsageLimitWait | undefined {
  const at = chat?.usageLimitResumesAt ? Date.parse(chat.usageLimitResumesAt) : NaN;
  if (!chat || !Number.isFinite(at)) return undefined;
  return { provider: chat.usageLimitProvider ?? "", resumesAt: at, probing: chat.usageLimitProbing };
}

/** "1h 12m", "12m 5s", "40s". Whole seconds under a minute, minutes above. */
export function formatCountdown(ms: number): string {
  const total = Math.max(0, Math.round(ms / 1000));
  const days = Math.floor(total / 86400);
  const hours = Math.floor((total % 86400) / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const seconds = total % 60;
  if (days > 0) return `${days}d ${hours}h`;
  if (hours > 0) return `${hours}h ${minutes}m`;
  if (minutes > 0) return minutes >= 10 ? `${minutes}m` : `${minutes}m ${seconds}s`;
  return `${seconds}s`;
}

/** "3:42 PM", with the weekday once it is not today. */
export function formatResumeTime(at: number, now: number): string {
  const when = new Date(at);
  const time = when.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  return when.toDateString() === new Date(now).toDateString()
    ? time
    : `${when.toLocaleDateString([], { weekday: "short" })} ${time}`;
}

/** The headline: who the run is waiting on. */
export function usageLimitHeadline(wait: UsageLimitWait): string {
  return wait.provider ? `Waiting for ${wait.provider} usage limit` : "Waiting for usage limit";
}

/** The tail: when it carries on, with the live countdown. */
export function usageLimitResumeLine(wait: UsageLimitWait, now: number): string {
  const remaining = wait.resumesAt - now;
  if (remaining <= 0) return wait.probing ? "retrying now" : "resuming now";
  const when = formatResumeTime(wait.resumesAt, now);
  const countdown = formatCountdown(remaining);
  return wait.probing
    ? `no reset time given · trying again ${when} (in ${countdown})`
    : `resumes ${when} (in ${countdown})`;
}
