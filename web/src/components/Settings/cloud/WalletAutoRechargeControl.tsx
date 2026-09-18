import { useEffect, useState } from "react";
import { AlertCircle, CreditCard } from "lucide-react";

import { Button } from "./ui";
import { formatCentsAsDollars, suggestRecharge } from "./billingUtils";
import {
  useSetWalletAutoRecharge,
  useWalletAutoRecharge,
} from "@/hooks/useCloudBillingQueries";
import { cn } from "@/lib/utils";

/**
 * The AI-credit budget control: may we top up automatically, how much, and
 * what is the ceiling on what that costs in a month.
 *
 * This is the surface the owner remembers building and could not find. The
 * RPCs (`GetCurrentUserWalletAutoRecharge` / `SetCurrentUserWalletAutoRecharge`)
 * and their hooks have existed the whole time, but the only thing that ever
 * called them was `OutOfCreditModal` — so the feature was offered exclusively
 * at the moment the user had ALREADY run out, which is the one moment it can
 * no longer help them.
 *
 * Modelled on `ComputeOverageControl`, deliberately, so the two spend controls
 * on this page behave identically: radio options rather than a toggle plus
 * hidden fields, permission and ceiling in ONE submit, spend drawn against the
 * cap, an explicit Save, and nothing that fires from an effect.
 *
 * ── The one place it must NOT copy compute ────────────────────────────
 *
 * Compute's cap of 0 means UNCAPPED. The wallet's ceiling of 0 is REJECTED —
 * there is no uncapped state for a charger that fires with nobody watching, so
 * the proto makes `max_per_month_cents` mandatory whenever the rule is on.
 * That is why there is no third "no limit" option here: it is not a choice the
 * server offers, and rendering it would produce an error the user cannot fix.
 *
 * ── Why failure is read from failure_state and never from `enabled` ───
 *
 * A declined card does NOT disarm the rule. The server keeps it enabled on
 * every rung of the ladder — "declined", "requires_action", "ceiling_reached"
 * — because a rule that silently switched itself off would leave the user
 * believing they were protected. So a degraded rule renders as ON AND BROKEN,
 * from `failureState`. Inferring failure from `enabled === false` would report
 * a state that never occurs and would miss every state that does.
 */

/**
 * The smallest automatic top-up worth making. Below this the processing fee is
 * a large fraction of the charge, and the rule would fire constantly.
 */
export const MIN_AUTO_RECHARGE_AMOUNT_CENTS = 500;

export type AutoRechargeChoice = "off" | "on";

export interface AutoRechargeRule {
  enabled: boolean;
  thresholdCents: number;
  amountCents: number;
  maxPerMonthCents: number;
}

export interface WalletAutoRechargeControlProps {
  /** Current server state. Null when no rule has ever been stored. */
  rule: AutoRechargeRule | null;
  /**
   * Where the rule sits on the failure ladder: "ok", "declined",
   * "requires_action" or "ceiling_reached". Empty is treated as healthy.
   */
  failureState: string;
  /** The server's own words about the last failure, when it has any. */
  lastError?: string;
  /**
   * Cents auto-recharge has already committed this billing month.
   *
   * Null when there is no stored rule to attribute spend to. This is a LEDGER
   * SUM of charges we made, not a meter reading, so when a rule exists the
   * number is authoritative and a zero here is a real zero.
   */
  spentThisMonthCents: number | null;
  /** The card the rule would charge, or null when none is on file. */
  card: { brand: string; last4: string } | null;
  /** Observed daily AI spend, used ONLY to size the first suggestion. */
  dailySpendUsd: number | null;
  saving?: boolean;
  /** A failed save, in the user's terms. */
  error?: string;
  onSave: (rule: AutoRechargeRule) => void;
  /** Send the user somewhere they can put a card on file. */
  onAddCard?: () => void;
}

/**
 * How a degraded rule should read, or null when it is healthy.
 *
 * Exported because this mapping is the whole of the "never infer failure from
 * enabled" rule, and it should be assertable without rendering a form.
 */
export function describeFailure(
  failureState: string,
  maxPerMonthCents: number,
): { tone: "error" | "warning"; title: string; message: string } | null {
  switch (failureState) {
    case "declined":
      return {
        tone: "error",
        title: "Your last automatic top-up was declined",
        message:
          "Automatic top-ups are still on, but they can't go through until the card is updated. Your balance will run out if nothing changes.",
      };
    case "requires_action":
      return {
        tone: "warning",
        title: "Your bank needs to verify the last automatic top-up",
        message:
          "The charge is waiting on a check your bank can only ask you in person. Adding credit manually will clear it, or update the card to use one that doesn't need verification.",
      };
    case "ceiling_reached":
      return {
        tone: "warning",
        title: `Automatic top-ups have hit their ${formatCentsAsDollars(maxPerMonthCents)} monthly limit`,
        message:
          "The rule is still on and resumes next month. To keep topping up before then, raise the monthly limit below.",
      };
    default:
      // "ok", "", and anything a newer server invents. An unrecognised rung is
      // reported as healthy rather than as an alarming unknown: the rule is
      // still doing its job, and a scary banner naming a state we cannot
      // explain is worse than no banner.
      return null;
  }
}

function parseDollarsToCents(input: string): number | null {
  const trimmed = input.trim();
  if (trimmed === "") return null;
  const dollars = Number(trimmed);
  if (!Number.isFinite(dollars) || dollars < 0) return null;
  return Math.round(dollars * 100);
}

export function WalletAutoRechargeControl({
  rule,
  failureState,
  lastError,
  spentThisMonthCents,
  card,
  dailySpendUsd,
  saving = false,
  error,
  onSave,
  onAddCard,
}: WalletAutoRechargeControlProps) {
  // The suggestion is sized from the user's OWN burn rate — the same rule the
  // out-of-credit modal uses, so the two surfaces cannot recommend different
  // numbers for the same account. A blank field beside "charge my card
  // automatically" is not a neutral default, it is homework.
  const suggestion = suggestRecharge(dailySpendUsd);

  const storedChoice: AutoRechargeChoice = rule?.enabled ? "on" : "off";
  const [choice, setChoice] = useState<AutoRechargeChoice>(storedChoice);
  const [amountInput, setAmountInput] = useState(() =>
    ((rule?.amountCents || suggestion.amountCents) / 100).toFixed(2),
  );
  const [thresholdInput, setThresholdInput] = useState(() =>
    ((rule?.thresholdCents || suggestion.thresholdCents) / 100).toFixed(2),
  );
  const [ceilingInput, setCeilingInput] = useState(() =>
    ((rule?.maxPerMonthCents || suggestion.maxPerMonthCents) / 100).toFixed(2),
  );

  // Mirror the server's answer back into the form when it changes underneath —
  // a save landing, or another tab. This ONLY copies state into inputs; it
  // never calls onSave. Nothing here may authorize a recurring charge without
  // a click.
  useEffect(() => {
    setChoice(rule?.enabled ? "on" : "off");
    if (rule) {
      if (rule.amountCents > 0)
        setAmountInput((rule.amountCents / 100).toFixed(2));
      if (rule.thresholdCents > 0)
        setThresholdInput((rule.thresholdCents / 100).toFixed(2));
      if (rule.maxPerMonthCents > 0)
        setCeilingInput((rule.maxPerMonthCents / 100).toFixed(2));
    }
  }, [rule]);

  const amountCents = parseDollarsToCents(amountInput);
  const thresholdCents = parseDollarsToCents(thresholdInput);
  const ceilingCents = parseDollarsToCents(ceilingInput);

  const turningOn = choice === "on";
  const amountInvalid =
    turningOn &&
    (amountCents === null || amountCents < MIN_AUTO_RECHARGE_AMOUNT_CENTS);
  const thresholdInvalid =
    turningOn && (thresholdCents === null || thresholdCents <= 0);
  // A ceiling below one top-up is a rule that can never fire — the user would
  // arm it, see nothing happen, and have no way to tell why. Rejected here
  // rather than stored.
  const ceilingInvalid =
    turningOn &&
    (ceilingCents === null ||
      ceilingCents <= 0 ||
      (amountCents !== null && ceilingCents < amountCents));

  const invalid = amountInvalid || thresholdInvalid || ceilingInvalid;

  const dirty =
    choice !== storedChoice ||
    (turningOn &&
      (amountCents !== (rule?.amountCents ?? -1) ||
        thresholdCents !== (rule?.thresholdCents ?? -1) ||
        ceilingCents !== (rule?.maxPerMonthCents ?? -1)));

  const failure = rule?.enabled
    ? describeFailure(failureState, rule.maxPerMonthCents)
    : null;

  const handleSave = () => {
    if (choice === "off") {
      // The server replaces the whole rule on every call, so the amounts
      // travel even when disarming — they are what the form will show if the
      // user turns it back on, and sending zeroes would erase their choices.
      onSave({
        enabled: false,
        thresholdCents: thresholdCents ?? suggestion.thresholdCents,
        amountCents: amountCents ?? suggestion.amountCents,
        maxPerMonthCents: ceilingCents ?? suggestion.maxPerMonthCents,
      });
      return;
    }
    if (
      invalid ||
      amountCents === null ||
      thresholdCents === null ||
      ceilingCents === null
    )
      return;
    onSave({
      enabled: true,
      thresholdCents,
      amountCents,
      maxPerMonthCents: ceilingCents,
    });
  };

  const spentAgainstCeiling =
    rule?.enabled && rule.maxPerMonthCents > 0 && spentThisMonthCents !== null
      ? {
          spent: formatCentsAsDollars(spentThisMonthCents),
          cap: formatCentsAsDollars(rule.maxPerMonthCents),
          pct: Math.min(
            (spentThisMonthCents / Math.max(rule.maxPerMonthCents, 1)) * 100,
            100,
          ),
        }
      : null;

  return (
    <div className="flex flex-col gap-4" data-testid="wallet-auto-recharge">
      <div>
        <p className="text-sm font-medium text-foreground">
          Automatic top-ups
        </p>
        <p className="text-xs text-muted-foreground">
          Keep a working balance without watching it, with a hard limit on what
          that can cost in a month.
        </p>
      </div>

      {/* The degraded rungs. Rendered from failureState and ABOVE the options,
          because the options below will correctly show the rule as ON — the
          server never disarms it — and a user seeing "on" with no explanation
          would have no way to learn their card is failing. */}
      {failure && (
        <div
          role="alert"
          data-testid="auto-recharge-failure"
          data-failure-state={failureState}
          className={cn(
            "rounded-md border px-3 py-2",
            failure.tone === "error"
              ? "border-destructive/40 bg-destructive/10"
              : "border-warning/30 bg-warning/10",
          )}
        >
          <p
            className={cn(
              "text-xs font-semibold",
              failure.tone === "error" ? "text-destructive" : "text-warning",
            )}
          >
            {failure.title}
          </p>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {failure.message}
          </p>
          {lastError && (
            <p className="mt-1 text-xs text-muted-foreground">{lastError}</p>
          )}
        </div>
      )}

      <fieldset
        disabled={saving}
        className="flex flex-col gap-3 disabled:opacity-60"
      >
        <legend className="sr-only">Automatic top-ups</legend>

        <RechargeOption
          name="auto-recharge-choice"
          value="off"
          checked={choice === "off"}
          onSelect={() => setChoice("off")}
          label="Only top up when I say so"
          description="Nothing is charged automatically. Requests fail once your balance runs out."
        />

        <RechargeOption
          name="auto-recharge-choice"
          value="on"
          checked={choice === "on"}
          onSelect={() => setChoice("on")}
          label="Top up automatically, up to a monthly limit"
        >
          {card ? (
            <p className="text-xs text-muted-foreground">
              Charged to your {card.brand} ···· {card.last4}.
            </p>
          ) : (
            /* No card, no rule. The server would reject this anyway, and a
               Save button that fails on press is worse than one that says
               what is missing. */
            <div className="flex flex-wrap items-center gap-2">
              <p className="text-xs text-muted-foreground">
                You&apos;ll need a card on file before this can be turned on.
              </p>
              {onAddCard && (
                <Button size="sm" variant="outline" onClick={onAddCard}>
                  <CreditCard className="mr-1 h-3.5 w-3.5" />
                  Add a card
                </Button>
              )}
            </div>
          )}

          <div className="flex flex-col gap-2">
            <MoneyField
              id="auto-recharge-threshold"
              label="When my balance drops below"
              value={thresholdInput}
              onChange={setThresholdInput}
              onFocus={() => setChoice("on")}
              invalid={thresholdInvalid}
            />
            <MoneyField
              id="auto-recharge-amount"
              label="Add this much credit"
              value={amountInput}
              onChange={setAmountInput}
              onFocus={() => setChoice("on")}
              invalid={amountInvalid}
            />
            <MoneyField
              id="auto-recharge-ceiling"
              label="Never spend more than"
              suffix="/mo"
              value={ceilingInput}
              onChange={setCeilingInput}
              onFocus={() => setChoice("on")}
              invalid={ceilingInvalid}
            />
          </div>

          {thresholdInvalid && (
            <p className="text-xs text-destructive">
              Enter a balance above $0 to top up from.
            </p>
          )}
          {amountInvalid && (
            <p className="text-xs text-destructive">
              Top-ups start at{" "}
              {formatCentsAsDollars(MIN_AUTO_RECHARGE_AMOUNT_CENTS)}.
            </p>
          )}
          {ceilingInvalid && (
            <p className="text-xs text-destructive">
              The monthly limit has to be at least one top-up, and it can&apos;t
              be $0 — an automatic charger must have a wall.
            </p>
          )}

          <p className="text-xs text-muted-foreground">
            The monthly limit is a hard stop: once automatic top-ups have spent
            it, they pause until the next billing month rather than carrying on.
          </p>
        </RechargeOption>
      </fieldset>

      {spentAgainstCeiling && (
        <div className="flex flex-col gap-1">
          <div className="flex items-center justify-between text-xs text-muted-foreground">
            <span>Topped up automatically this month</span>
            <span>
              {spentAgainstCeiling.spent} of {spentAgainstCeiling.cap}
            </span>
          </div>
          <div className="h-2 rounded-full bg-background">
            <div
              className={cn(
                "h-2 rounded-full",
                spentAgainstCeiling.pct >= 75 ? "bg-warning" : "bg-primary",
              )}
              style={{ width: `${spentAgainstCeiling.pct}%` }}
            />
          </div>
        </div>
      )}

      {error && (
        <p className="text-xs text-destructive" role="alert">
          {error}
        </p>
      )}

      <div>
        <Button
          size="sm"
          onClick={handleSave}
          disabled={saving || !dirty || invalid || (turningOn && !card)}
        >
          {saving ? "Saving…" : "Save top-up settings"}
        </Button>
      </div>
    </div>
  );
}

function MoneyField({
  id,
  label,
  suffix,
  value,
  onChange,
  onFocus,
  invalid,
}: {
  id: string;
  label: string;
  suffix?: string;
  value: string;
  onChange: (next: string) => void;
  onFocus: () => void;
  invalid: boolean;
}) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-2">
      <label htmlFor={id} className="text-xs text-muted-foreground">
        {label}
      </label>
      <div className="flex items-center gap-1">
        <span className="text-sm text-muted-foreground">$</span>
        <input
          id={id}
          type="text"
          inputMode="decimal"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          onFocus={onFocus}
          aria-invalid={invalid}
          className={cn(
            "w-24 rounded-md border bg-background px-2 py-1 text-sm text-foreground",
            invalid ? "border-destructive" : "border-border",
          )}
        />
        {suffix && (
          <span className="text-sm text-muted-foreground">{suffix}</span>
        )}
      </div>
    </div>
  );
}

/**
 * One option, with its detail panel OUTSIDE the `<label>`.
 *
 * `ComputeOverageControl` nests its detail inside the label and gets away with
 * it because the only thing in there is one input and some prose. This option
 * carries THREE labelled money fields, and a `<label>` that wraps them owns
 * every one of their label elements too — so the radio's accessible name
 * becomes the whole panel, and each field's own label resolves ambiguously
 * between the field and the radio. Assistive tech reads a radio called "Top up
 * automatically, up to a monthly limit When my balance drops below Add this
 * much credit Never spend more than", and `getByLabelText` finds two elements
 * for each field — which is how this was caught.
 *
 * So the label covers exactly the clickable title, and the fields sit beside
 * it inside the bordered container.
 */
function RechargeOption({
  name,
  value,
  checked,
  onSelect,
  label,
  description,
  children,
}: {
  name: string;
  value: string;
  checked: boolean;
  onSelect: () => void;
  label: string;
  description?: string;
  children?: React.ReactNode;
}) {
  return (
    <div
      className={cn(
        "flex flex-col gap-2 rounded-md border px-3 py-3",
        checked ? "border-primary bg-muted/40" : "border-border",
      )}
    >
      <label className="flex cursor-pointer gap-3">
        <input
          type="radio"
          name={name}
          value={value}
          checked={checked}
          onChange={onSelect}
          className="mt-1 h-4 w-4 shrink-0 accent-primary"
        />
        <span className="flex min-w-0 flex-1 flex-col gap-2">
          <span className="text-sm font-medium text-foreground">{label}</span>
          {description && (
            <span className="text-xs text-muted-foreground">{description}</span>
          )}
        </span>
      </label>
      {children && (
        <div className="flex flex-col gap-2 pl-7">{children}</div>
      )}
    </div>
  );
}

/**
 * The control, wired to the RPCs.
 *
 * Kept beside the pure component rather than in the billing page so that the
 * page composes ONE element and owns none of this domain — the same split
 * `ComputeOverageControl` has, with the query half added because the wallet
 * rule is not part of any overview the page already loads.
 */
export function WalletAutoRechargeSection({
  dailySpendUsd,
  onAddCard,
}: {
  dailySpendUsd: number | null;
  onAddCard?: () => void;
}) {
  const autoRechargeQ = useWalletAutoRecharge();
  const setAutoRecharge = useSetWalletAutoRecharge();
  const [error, setError] = useState("");

  if (autoRechargeQ.isLoading) {
    return (
      <p className="text-sm text-muted-foreground">
        Loading your top-up settings…
      </p>
    );
  }

  // A read we could not make is NOT "automatic top-ups are off". Rendering the
  // form here would show an unarmed rule to someone whose rule is armed, which
  // is the reassuring-and-wrong direction on a control about money.
  if (autoRechargeQ.error && !autoRechargeQ.data) {
    return (
      <div className="flex flex-wrap items-center gap-2">
        <AlertCircle className="h-4 w-4 shrink-0 text-muted-foreground" />
        <p className="text-sm text-muted-foreground">
          Couldn&apos;t load your automatic top-up settings.
        </p>
        <Button
          size="sm"
          variant="outline"
          onClick={() => void autoRechargeQ.refetch()}
        >
          Retry
        </Button>
      </div>
    );
  }

  const wire = autoRechargeQ.data?.autoRecharge;
  const card = autoRechargeQ.data?.paymentMethod;

  return (
    <WalletAutoRechargeControl
      rule={
        wire
          ? {
              enabled: wire.enabled,
              thresholdCents: Number(wire.thresholdCents),
              amountCents: Number(wire.amountCents),
              maxPerMonthCents: Number(wire.maxPerMonthCents),
            }
          : null
      }
      failureState={wire?.failureState ?? ""}
      lastError={wire?.lastError || undefined}
      spentThisMonthCents={wire ? Number(wire.spentThisMonthCents) : null}
      card={card ? { brand: card.brand, last4: card.last4 } : null}
      dailySpendUsd={dailySpendUsd}
      saving={setAutoRecharge.isPending}
      error={error}
      onAddCard={onAddCard}
      // The only path to a recurring charge, and it is a click. There is
      // deliberately no effect anywhere in this file that reaches it.
      onSave={(rule) => {
        setError("");
        setAutoRecharge.mutate(rule, {
          onError: (err) =>
            setError(
              err instanceof Error
                ? err.message.replace(/^\[[a-z_]+\]\s*/i, "")
                : "We couldn't save your top-up settings.",
            ),
        });
      }}
    />
  );
}
