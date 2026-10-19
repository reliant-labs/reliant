// Copyright (c) 2025 Reliant Labs

/**
 * The transcript notices the account router and the usage-limit failover post
 * (internal/workflow/runtime/activities/handlers/usage_limit_notice.go). They
 * are ordinary warning-styled system messages whose text marks the names it
 * means with **bold**, so the web reads the words, not a side channel:
 *
 *   Claude account **a@x** reached its 5-hour limit (resets 3:40 PM). Continuing on **b@x**.
 *   Claude is rate-limited on all your accounts until 3:40 PM. Continuing on **GPT-5.6 Sol · Codex** as this chat allows; returning to Claude after the reset.
 *   Claude's limit has reset. Back on **Claude 5.5 Opus · Claude**.
 */

export type ProviderNoticeKind = "account_switch" | "provider_switch" | "return";

export function providerNoticeKind(text: string): ProviderNoticeKind | undefined {
  if (/^.+ account \*\*[^*]+\*\* /.test(text)) return "account_switch";
  if (/ is rate-limited on all your accounts\b.*Continuing on \*\*/.test(text)) {
    return "provider_switch";
  }
  if (/'s limit has reset\. Back on \*\*/.test(text)) return "return";
  return undefined;
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
