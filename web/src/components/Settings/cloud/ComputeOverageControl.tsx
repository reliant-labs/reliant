import { useEffect, useMemo, useState } from "react";

import { Button } from "./ui";
import { formatCentsAsDollars, formatOverageRate } from "./billingUtils";
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
 *  - It says plainly that the limit gates STARTING a machine and does not stop
 *    one already running. That is the actual server behaviour, and a cap the
 *    user reads as a hard ceiling — which then bills them for a weekend — is
 *    worse than no cap at all.
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
  overageCentsPerMinute: number;
  /** Overage spend so far this period, in cents. */
  overageSpentCents: number;
  /** Plan's monthly price in cents, used only to suggest a starting limit. */
  monthlyPriceCents: number | null;
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

/** How many extra minutes a limit buys at the plan's rate. */
export function limitAsMinutes(
  limitCents: number,
  overageCentsPerMinute: number,
): number | null {
  if (!(overageCentsPerMinute > 0) || !(limitCents > 0)) return null;
  return Math.floor(limitCents / overageCentsPerMinute);
}

function parseDollarsToCents(input: string): number | null {
  const trimmed = input.trim();
  if (trimmed === "") return null;
  const dollars = Number(trimmed);
  if (!Number.isFinite(dollars) || dollars < 0) return null;
  return Math.round(dollars * 100);
}

export function ComputeOverageControl({
  enabled,
  budgetCents,
  overageCentsPerMinute,
  overageSpentCents,
  monthlyPriceCents,
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
  const rateLabel = formatOverageRate(overageCentsPerMinute);
  const planHasOverage = overageCentsPerMinute > 0;

  const extraMinutes = useMemo(
    () =>
      parsedLimitCents === null
        ? null
        : limitAsMinutes(parsedLimitCents, overageCentsPerMinute),
    [parsedLimitCents, overageCentsPerMinute],
  );

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

  if (!planHasOverage) {
    return (
      <p className="text-sm text-muted-foreground">
        This plan doesn&apos;t offer extra time beyond its included hours.
      </p>
    );
  }

  const spentAgainstCap =
    storedChoice === "capped" && budgetCents !== undefined
      ? {
          spent: formatCentsAsDollars(overageSpentCents),
          cap: formatCentsAsDollars(Number(budgetCents)),
          pct: Math.min(
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
          description="Machines won't start once your included hours are used. Nothing extra is charged."
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
            {extraMinutes !== null && (
              <span className="text-xs text-muted-foreground">
                ≈ {extraMinutes} extra minutes at {rateLabel}
              </span>
            )}
          </div>
          {limitInvalid && (
            <p className="text-xs text-destructive">
              Enter a limit above $0, or choose one of the other options.
            </p>
          )}
          {/* The load-bearing sentence. The server's cap is checked when a
              machine STARTS (CreateDaemon / ResumeDaemon) and nowhere else, so
              promising a hard ceiling here would be false. */}
          <p id="overage-limit-caveat" className="text-xs text-muted-foreground">
            This limit stops new machines from starting. A machine that&apos;s
            already running keeps running until it goes idle, so your final bill
            can pass the limit by the cost of finishing what&apos;s in flight.
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
              {spentAgainstCap.spent} of {spentAgainstCap.cap}
            </span>
          </div>
          <div className="h-2 rounded-full bg-muted">
            <div
              className={cn(
                "h-2 rounded-full",
                spentAgainstCap.pct >= 75 ? "bg-warning" : "bg-primary",
              )}
              style={{ width: `${spentAgainstCap.pct}%` }}
            />
          </div>
        </div>
      )}

      <div>
        <Button
          size="sm"
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
