import { useEffect, useState } from "react";

import Button from "@/components/forge-ui/button";
import { formatCentsAsDollars } from "./billingUtils";
import { centsDecimalToDollars, hourlyPriceShort, storageCostLabel, MACHINE_BILLING_POLICY, type DaemonPricingLike } from "@/components/Billing/daemonUsage";
import { cn } from "@/lib/utils";

/**
 * The compute overage control: may machines run past included hours, and what
 * is the ceiling on what that costs.
 *
 * Kept in its own file, and self-contained, because the billing page's layout
 * is being reworked in parallel — this renders inside whatever card it is given
 * and owns no page structure.
 *
 * Three things this deliberately does:
 *
 *  - It asks for the ceiling and the permission in ONE submit. The server
 *    replaces both on every call, and a two-step UI would make "overage on,
 *    no ceiling" a state a user passes through on the way to setting a limit.
 *  - It says plainly what the limit does: it gates STARTING a machine, and the
 *    billing sweep pauses a running one shortly after the cap is met. It is
 *    still not an instantaneous hard ceiling, and the copy must not imply one —
 *    a cap the user reads as absolute, which then bills them past it, is worse
 *    than no cap at all. (Until the sweep learned to enforce this cap it could
 *    only gate starts, and a running machine billed until it went idle; the
 *    copy promised exactly that and now would be wrong the other way.)
 *  - It never submits from an effect. This authorizes spend, so it moves only
 *    on an explicit click.
 */

/** No cap: unset and 0 are both uncapped, matching the server's `> 0` test. */
export const NO_CAP = 0n;

export type OverageChoice = "off" | "capped" | "uncapped";

export interface ComputeOverageControlProps {
  /** Current server state. */
  enabled: boolean;
  /** Current stored cap in cents; undefined or 0 both mean uncapped. */
  budgetCents?: bigint;
  /** Plan's overage rate, server-supplied. 0 means the plan has no overage. */
  pricing?: DaemonPricingLike;
  /**
   * Overage spend so far this period, in cents, or null when the server did
   * not measure usage.
   *
   * Null rather than 0 on purpose. This is drawn as spend against a cap the
   * user authorized, so an unmeasured zero tells them their whole limit is
   * still available — the reassuring-and-wrong reading, in the currency they
   * care about most.
   */
  overageSpentCents: number | null;
  /** Plan's monthly price in cents, used only to suggest a starting limit. */
  monthlyPriceCents: number | null;
  /**
   * The org's storage against its plan's included amount, from the usage
   * response. Storage is NOT limited by this control — a cap can pause
   * machines but cannot stop us holding their disks — so it is shown as its
   * own always-on line rather than folded into the cap. Absent when unmeasured.
   */
  storage?: { includedGib: number; extraGib: number };
  disabled?: boolean;
  saving?: boolean;
  disabledReason?: string;
  onSave: (settings: { enabled: boolean; budgetCents?: bigint }) => void;
}

/**
 * Which of the three options the stored state corresponds to. A stored cap of
 * 0 is uncapped, not "capped at zero" — the server reads it that way and the
 * UI must not disagree.
 */
export function choiceFromState(
  enabled: boolean,
  budgetCents?: bigint,
): OverageChoice {
  if (!enabled) return "off";
  return budgetCents !== undefined && budgetCents > NO_CAP
    ? "capped"
    : "uncapped";
}

/**
 * A starting limit to offer someone turning overage on for the first time:
 * half the plan's monthly price. A blank field beside "allow charges" is not a
 * neutral default, it is homework — but the suggestion is editable and is never
 * submitted on the user's behalf.
 */
export function suggestedLimitCents(monthlyPriceCents: number | null): number {
  if (monthlyPriceCents === null || monthlyPriceCents <= 0) return 2000;
  return Math.round(monthlyPriceCents / 2);
}

function parseDollarsToCents(input: string): number | null {
  const trimmed = input.trim();
  const match = /^(\d+)(?:\.(\d{1,2}))?$/.exec(trimmed);
  if (!match) return null;
  const cents = BigInt(match[1]) * 100n + BigInt((match[2] ?? "").padEnd(2, "0"));
  return cents <= BigInt(Number.MAX_SAFE_INTEGER) ? Number(cents) : null;
}

export function ComputeOverageControl({
  enabled,
  budgetCents,
  pricing,
  overageSpentCents,
  monthlyPriceCents,
  storage,
  disabled = false,
  saving = false,
  disabledReason,
  onSave,
}: ComputeOverageControlProps) {
  const storedChoice = choiceFromState(enabled, budgetCents);

  const [choice, setChoice] = useState<OverageChoice>(storedChoice);
  const [limitInput, setLimitInput] = useState(() =>
    budgetCents !== undefined && budgetCents > NO_CAP
      ? (Number(budgetCents) / 100).toFixed(2)
      : (suggestedLimitCents(monthlyPriceCents) / 100).toFixed(2),
  );

  // Re-sync the form when the server's answer changes underneath it (a save
  // landing, or another tab). This only mirrors state into the form; it never
  // saves — nothing here may authorize spend without a click.
  useEffect(() => {
    setChoice(storedChoice);
    if (budgetCents !== undefined && budgetCents > NO_CAP) {
      setLimitInput((Number(budgetCents) / 100).toFixed(2));
    }
  }, [storedChoice, budgetCents]);

  const parsedLimitCents = parseDollarsToCents(limitInput);
  const rateLabel = pricing?.sizes.map((s) => `${s.size}: ${hourlyPriceShort(pricing, s.size)}`).join(" · ") || "prices unavailable";

  // A capped choice needs a real ceiling. 0 is rejected HERE, in the form,
  // rather than sent: the server would store it and read it back as uncapped,
  // which would silently give the user the opposite of what they picked.
  const limitInvalid =
    choice === "capped" && (parsedLimitCents === null || parsedLimitCents <= 0);

  const dirty =
    choice !== storedChoice ||
    (choice === "capped" &&
      BigInt(parsedLimitCents ?? 0) !== (budgetCents ?? NO_CAP));

  const handleSave = () => {
    if (choice === "off") {
      onSave({ enabled: false });
      return;
    }
    if (choice === "uncapped") {
      onSave({ enabled: true });
      return;
    }
    if (parsedLimitCents === null || parsedLimitCents <= 0) return;
    onSave({ enabled: true, budgetCents: BigInt(parsedLimitCents) });
  };

  const spentAgainstCap =
    storedChoice === "capped" && budgetCents !== undefined
      ? {
          // The cap is a stored setting and is known regardless; only the
          // spend depends on metering, so an unmeasured period still shows
          // the user what they set — it just refuses to claim how much of it
          // is left.
          spent:
            overageSpentCents === null
              ? null
              : formatCentsAsDollars(overageSpentCents),
          cap: formatCentsAsDollars(Number(budgetCents)),
          pct:
            overageSpentCents === null
              ? null
              : Math.min(
                  (overageSpentCents / Math.max(Number(budgetCents), 1)) * 100,
                  100,
                ),
        }
      : null;

  return (
    <div className="flex flex-col gap-4" title={disabledReason}>
      <div>
        <p className="text-sm font-medium text-foreground">
          Beyond your included hours
        </p>
        <p className="text-xs text-muted-foreground">
          Extra machine time is charged at {rateLabel}.
        </p>
        <StorageLine pricing={pricing} storage={storage} />
      </div>

      <fieldset
        disabled={disabled || saving}
        className="flex flex-col gap-3 disabled:opacity-60"
      >
        <legend className="sr-only">Extra machine time</legend>

        <OverageOption
          name="overage-choice"
          value="off"
          checked={choice === "off"}
          onSelect={() => setChoice("off")}
          label="Stop at my included hours"
          description={`Machines pause when included hours run out. ${MACHINE_BILLING_POLICY}`}
        />

        <OverageOption
          name="overage-choice"
          value="capped"
          checked={choice === "capped"}
          onSelect={() => setChoice("capped")}
          label="Allow extra time, up to a monthly limit"
        >
          <div className="flex flex-wrap items-center gap-2">
            <label
              htmlFor="overage-limit"
              className="text-xs text-muted-foreground"
            >
              Limit
            </label>
            <div className="flex items-center gap-1">
              <span className="text-sm text-muted-foreground">$</span>
              <input
                id="overage-limit"
                type="text"
                inputMode="decimal"
                value={limitInput}
                onChange={(e) => setLimitInput(e.target.value)}
                onFocus={() => setChoice("capped")}
                aria-invalid={limitInvalid}
                aria-describedby="overage-limit-caveat"
                className={cn(
                  "w-24 rounded-md border bg-background px-2 py-1 text-sm text-foreground",
                  limitInvalid ? "border-destructive" : "border-border",
                )}
              />
              <span className="text-sm text-muted-foreground">/mo</span>
            </div>

          </div>
          {limitInvalid && (
            <p className="text-xs text-destructive-ink">
              Enter a limit above $0, or choose one of the other options.
            </p>
          )}
          {/* The load-bearing sentence, and it changed when the cap became
              real. The server checks it when a machine STARTS (CreateDaemon /
              ResumeDaemon) AND on the billing sweep, which suspends a running
              machine once the cap is met. The sweep runs about once a minute,
              so the overshoot is now bounded by that interval rather than by
              whenever the machine happened to go idle. Still not a hard
              ceiling — promising one would be false — but a much smaller
              caveat than the one this replaced. */}
          <p id="overage-limit-caveat" className="text-xs text-muted-foreground">
            This limit stops new machines from starting, and pauses running ones
            shortly after the limit is reached. Your final bill can pass the
            limit slightly, by whatever a machine spends before it&apos;s paused.
          </p>
        </OverageOption>

        <OverageOption
          name="overage-choice"
          value="uncapped"
          checked={choice === "uncapped"}
          onSelect={() => setChoice("uncapped")}
          label="Allow extra time with no limit"
          description={`Not recommended. Machine time past your included hours is charged at ${rateLabel} with no ceiling.`}
        />
      </fieldset>

      {spentAgainstCap && (
        <div className="flex flex-col gap-1">
          <div className="flex items-center justify-between text-xs text-muted-foreground">
            <span>Extra time used this period</span>
            <span>
              {spentAgainstCap.spent === null
                ? `Usage unavailable · limit ${spentAgainstCap.cap}`
                : `${spentAgainstCap.spent} of ${spentAgainstCap.cap}`}
            </span>
          </div>
          {/* No bar when there is no measurement: an empty track reads as
              "nothing spent", which is the claim we are refusing to make. */}
          {spentAgainstCap.pct !== null && (
            <div className="h-2 rounded-full bg-background">
              <div
                className={cn(
                  "h-2 rounded-full",
                  spentAgainstCap.pct >= 75 ? "bg-warning" : "bg-primary",
                )}
                style={{ width: `${spentAgainstCap.pct}%` }}
              />
            </div>
          )}
        </div>
      )}

      <div>
        <Button
          size="md"
          onClick={handleSave}
          disabled={disabled || saving || !dirty || limitInvalid}
        >
          {saving ? "Saving…" : "Save extra-time settings"}
        </Button>
      </div>
    </div>
  );
}

function OverageOption({
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
    <label
      className={cn(
        "flex cursor-pointer gap-3 rounded-md border px-3 py-3",
        checked ? "border-primary bg-muted/40" : "border-border",
      )}
    >
      <input
        type="radio"
        name={name}
        value={value}
        checked={checked}
        onChange={onSelect}
        className="mt-1 h-4 w-4 shrink-0 accent-primary"
      />
      <span className="flex flex-col gap-2">
        <span className="text-sm font-medium text-foreground">{label}</span>
        {description && (
          <span className="text-xs text-muted-foreground">{description}</span>
        )}
        {children}
      </span>
    </label>
  );
}

/**
 * Storage, always on and outside the cap: "Storage: $5.00/mo for 20 GB beyond
 * your included 60 GB — billed separately, not limited by this cap."
 */
export function StorageLine({
  pricing,
  storage,
}: {
  pricing?: DaemonPricingLike;
  storage?: { includedGib: number; extraGib: number };
}) {
  if (!storage || !pricing) return null;
  const rate = centsDecimalToDollars(pricing.storageCentsPerGibMonth);
  const extra = storageCostLabel(pricing, storage.extraGib);
  const text =
    storage.extraGib > 0
      ? `Storage: ${extra ?? "price unavailable"} for ${storage.extraGib} GB beyond your included ${storage.includedGib} GB`
      : `Storage: ${storage.includedGib} GB included, then ${rate}/GB-month`;
  return (
    <p className="text-xs text-muted-foreground" data-testid="overage-storage-line">
      {text} — billed separately, not limited by this cap.
    </p>
  );
}
