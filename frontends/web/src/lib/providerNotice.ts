// Copyright (c) 2025 Reliant Labs

/**
 * The transcript notices the account router and the usage-limit failover post
 * (internal/workflow/runtime/activities/handlers/usage_limit_notice.go). They
 * are ordinary warning-styled system messages whose text marks the names it
 * means with **bold**, so the web reads the words, not a side channel:
 *
 *   Claude account **a@x** reached its 5-hour limit (resets <time:2026-10-19T15:40:00Z>). Continuing on **b@x**.
 *   Claude is rate-limited on all your accounts until <time:2026-10-19T15:40:00Z>. Continuing on **GPT-5.6 Sol · Codex** as this chat allows; returning to Claude after the reset.
 *   Claude is rate-limited on all your accounts; no reset time was given, checking again at <time:...>. Continuing on **...**
 *   Claude's limit has reset. Back on **Claude 5.5 Opus · Claude**.
 *
 * A time is a `<time:RFC3339 UTC>` token, not a clock string: the worker runs in
 * UTC and does not know the reader's zone, so it stores the instant and
 * localizeNoticeTimes renders it in the reader's own zone. The stored text stays
 * unambiguous for anything that reads it raw (the model, an export).
 */

import { formatResumeTime } from "@/lib/usageLimitWait";

export type ProviderNoticeKind = "account_switch" | "provider_switch" | "return";

export function providerNoticeKind(text: string): ProviderNoticeKind | undefined {
  if (/^.+ account \*\*[^*]+\*\* /.test(text)) return "account_switch";
  if (/ is rate-limited on all your accounts\b.*Continuing on \*\*/.test(text)) {
    return "provider_switch";
  }
  if (/'s limit has reset\. Back on \*\*/.test(text)) return "return";
  return undefined;
}

const TIME_TOKEN = /<time:([^>\s]+)>/g;

/**
 * Replace each <time:...> token with the instant in the reader's time zone
 * ("3:40 PM", with the weekday once it is not today). A token that does not
 * parse as a time is left as written.
 */
export function localizeNoticeTimes(text: string, now: number = Date.now()): string {
  return text.replace(TIME_TOKEN, (token, iso: string) => {
    const at = Date.parse(iso);
    return Number.isFinite(at) ? formatResumeTime(at, now) : token;
  });
}

export interface TextSegment {
  text: string;
  bold: boolean;
}

/** Split on **bold** markers; an unmatched marker stays literal. */
export function boldSegments(text: string): TextSegment[] {
  const out: TextSegment[] = [];
  const re = /\*\*([^*]+)\*\*/g;
  let last = 0;
  for (let m = re.exec(text); m; m = re.exec(text)) {
    if (m.index > last) out.push({ text: text.slice(last, m.index), bold: false });
    out.push({ text: m[1], bold: true });
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push({ text: text.slice(last), bold: false });
  return out;
}
