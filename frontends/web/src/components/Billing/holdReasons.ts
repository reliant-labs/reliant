/**
 * Server hold reasons, said the way a customer can act on them.
 *
 * The control plane explains a held checkout or usage estimate in its own
 * vocabulary ("machine route machine-pooled-v1/l has not been commissioned",
 * "proposed route economics do not clear the complete cost guard"). Those
 * strings are for the operator log, not for the person deciding whether to
 * buy: shown verbatim they read as an error in our wiring and say nothing
 * about what to do next. Each reason maps to one plain sentence; several
 * reasons with the same meaning collapse to one.
 */

const CHECKOUT_HOLDS: { match: RegExp; message: string }[] = [
  { match: /commission|route/i, message: "This machine size isn't available to buy yet." },
  { match: /open for sale|publish|price verification|prices have not/i, message: "Machine prices are being updated. Checkout reopens shortly." },
  { match: /existing subscription period|billing period/i, message: "We're still confirming your current billing period." },
  { match: /cost guard|economics/i, message: "This machine size isn't available to buy right now." },
];

const CHECKOUT_FALLBACK = "Machine checkout is paused right now.";

/** One customer sentence per distinct meaning, in the server's order. */
export function friendlyCheckoutHolds(reasons: string[]): string[] {
  const out: string[] = [];
  for (const reason of reasons) {
    const message = CHECKOUT_HOLDS.find((hold) => hold.match.test(reason))?.message ?? CHECKOUT_FALLBACK;
    if (!out.includes(message)) out.push(message);
  }
  return out.length > 0 ? out : [CHECKOUT_FALLBACK];
}

/**
 * The usage tab's hold: the server could not settle an exact estimate. Never
 * an alarm — conversations, machines and stored data are unaffected.
 */
export function friendlyUsageHold(reason: string | undefined): string | null {
  if (!reason) return null;
  return "We're still reconciling this period's machine usage, so some figures aren't shown yet. Your machines, conversations and data aren't affected.";
}
