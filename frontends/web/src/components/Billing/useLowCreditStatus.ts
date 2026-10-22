/**
 * The wallet facts the low-credit surfaces read, and the gate that decides who
 * is asked about them at all.
 *
 * ── Only people who spend Reliant credit ──────────────────────────────
 *
 * A user on their own API key has no wallet to run out of, and telling them
 * their balance is low would be both wrong and alarming. Two conditions have to
 * hold before this reports anything:
 *
 *   1. `reliantAIAvailable` — this build talks to a control plane at all. It is
 *      a build constant, so it cannot flap and gates the queries themselves,
 *      not just the render.
 *   2. `entitlement.reliantEnabled` — the user has actually turned managed
 *      models ON. This is the BYOK discriminator, and it is the user's own
 *      stored preference rather than anything inferred from their balance.
 *
 * Both queries are disabled when the first fails, so a self-hosted build makes
 * no billing requests from the header at all.
 *
 * ── Reads only what is already cached ─────────────────────────────────
 *
 * `useWalletOverview` and `useReliantOverview` are the same queries the billing
 * page mounts, on the same react-query keys, so a user who visits settings pays
 * for one fetch and both surfaces read it. The spend window is the same 30-day
 * range the credit band uses, for the same reason: it is the only denominator
 * the runway has.
 *
 * The spend query is allowed to FAIL SILENTLY. It contributes one thing — the
 * burn rate — and if it is missing the balance-based trigger still fires. A
 * header indicator must never be the thing that surfaces a spend-API outage.
 */

import { useMemo } from "react";

import {
  formatCurrencyFromWalletFields,
  nanosFromFields,
} from "@/components/Settings/cloud/billingUtils";
import {
  useLLMSpend,
  useReliantOverview,
  useWalletOverview,
} from "@/hooks/useReliantAIQueries";
import { reliantAIAvailable } from "@/services/controlPlane/reliantAI";

import { assessCredit, type CreditStatus } from "./lowCredit";

export interface LowCreditStatus {
  status: CreditStatus;
  /** Formatted balance, or null when the read failed. */
  formattedBalance: string | null;
  /**
   * Whether this user spends Reliant credit at all. False for a self-hosted
   * build and for anyone on their own API key — both must see nothing.
   */
  available: boolean;
  /** Observed daily burn, for the modal's "at your recent rate" copy. */
  dailySpendUsd: number | null;
}

/** The 30-day window the credit band already uses. */
function spendWindow() {
  const end = new Date();
  const start = new Date(end);
  start.setDate(end.getDate() - 30);
  const iso = (d: Date) => d.toISOString().slice(0, 10);
  return { startDate: iso(start), endDate: iso(end) };
}

export function useLowCreditStatus(): LowCreditStatus {
  const overviewQ = useReliantOverview();
  const walletQ = useWalletOverview();

  // The user's own preference, not an inference from their balance. Someone who
  // has turned managed models off is on their own key and has no wallet to
  // warn about.
  const reliantEnabled = Boolean(
    overviewQ.data?.entitlement?.reliantEnabled,
  );
  const available = reliantAIAvailable && reliantEnabled;

  const orgId = walletQ.data?.organization?.id ?? "";
  const window = useMemo(spendWindow, []);
  const spendQ = useLLMSpend({ orgId, ...window });

  const wallet = walletQ.data?.wallet;

  return useMemo(() => {
    if (!available) {
      return {
        status: { urgency: "healthy" as const, runwayDays: null },
        formattedBalance: null,
        available: false,
        dailySpendUsd: null,
      };
    }

    // A balance we could not read is NOT a balance of zero. Reporting "out of
    // credit" because a request failed would interrupt a user with a full
    // wallet, so a failed read reports healthy and says nothing.
    if (walletQ.error || !wallet) {
      return {
        status: { urgency: "healthy" as const, runwayDays: null },
        formattedBalance: null,
        available: true,
        dailySpendUsd: null,
      };
    }

    const nanos = nanosFromFields(
      wallet.balanceUsdNanos,
      wallet.balanceUsdMicros,
      wallet.balanceCents,
    );
    const totalSpend = spendQ.data?.totalSpend ?? 0;
    // The server's own day count. Zero means "cannot say", and assessCredit
    // withholds the runway on it rather than substituting a denominator — the
    // exact bug that made this estimate render for nobody once before.
    const sampleDays = spendQ.data?.sampleDays ?? 0;

    return {
      status: assessCredit(nanos, totalSpend, sampleDays),
      formattedBalance: formatCurrencyFromWalletFields(
        wallet.balanceUsdNanos,
        wallet.balanceUsdMicros,
        wallet.balanceCents,
      ),
      available: true,
      dailySpendUsd: sampleDays > 0 ? totalSpend / sampleDays : null,
    };
  }, [available, wallet, walletQ.error, spendQ.data]);
}
