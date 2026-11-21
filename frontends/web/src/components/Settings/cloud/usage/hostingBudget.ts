import { formatPrice } from "@/components/Billing/priceFormat";

// Mirrors control-plane's self-serve admission budget maximum (#3135).
// This limits new consent and edits, not existing agreements or their invoices.
const MAX_HOSTING_BUDGET_CENTS = 1_000_000n;
export const HOSTING_BUDGET_MAX_LABEL = formatPrice(MAX_HOSTING_BUDGET_CENTS);

export function validateHostingBudget(value: string): { cents: bigint; error?: never } | { error: string; cents?: never } {
  const dollars = /^(\d+)(?:\.(\d{1,2}))?$/.exec(value.trim());
  if (!dollars) return { error: "Enter a positive dollar amount with at most two decimal places." };

  const cents = BigInt(dollars[1]) * 100n + BigInt((dollars[2] ?? "").padEnd(2, "0"));
  if (cents <= 0n) return { error: "Enter a monthly budget above $0." };
  if (cents > MAX_HOSTING_BUDGET_CENTS) return { error: `Enter a monthly hosting admission budget of ${HOSTING_BUDGET_MAX_LABEL} or less.` };
  return { cents };
}

/** Exact decimal for a stored budget, including one above the self-serve max. */
export function hostingBudgetInputValue(cents: bigint): string {
  return `${cents / 100n}.${(cents % 100n).toString().padStart(2, "0")}`;
}
