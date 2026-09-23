// The deploy budget cap control — the INFRA twin of ../ComputeOverageControl.
//
// ── THE PARTIAL-SEND HAZARD, AND WHY THIS SHAPE ───────────────────────
//
// SetCurrentUserInfraOverage REPLACES the stored cap outright. Its proto
// says, in as many words: "A caller that omits this field is asking for
// uncapped deploy overage, so a UI must send the user's current choice on
// every call, not just when it changes."
//
// So the dangerous bug is not a validation miss — it is a form that submits
// `{ enabled }` alone after the user toggled something unrelated, silently
// removing a cap they set weeks ago. It succeeds. It returns a healthy
// subscription. Nothing surfaces it until the bill does.
//
// This component cannot express that request. It holds ONE piece of state —
// an OverageChoice, a union in which every variant names its own cap — and
// submits `buildOverageRequest(choice)`, which is total over the union and
// always emits budget_cents. There is no partial shape to build, no field to
// forget, and no code path that reaches the RPC without the cap.
//
// THERE IS NO "TURN OVERAGE OFF" OPTION, and that is honest rather than
// missing. The infra request carries no `enabled` flag because the platform
// has no such switch: a deployment that is already running accrues usage
// whether or not anyone opted in, so the only real choice is where the
// ceiling sits. Offering an off position would promise a thing that does not
// exist — and it is the deploy cap's one deliberate difference from
// ComputeOverageControl beside it, which genuinely CAN refuse to start a
// machine and therefore does offer "Stop at my included hours".
//
// The second rule this encodes: "no cap" is a NAMED, SELECTED state, never an
// empty input. Rendering uncapped as a blank field would make the most
// expensive setting in the product look like a field the user had not filled
// in yet — and would make it indistinguishable from a cap of $0.00, which
// means the exact opposite.

import { useEffect, useId, useState } from "react";

import { cn } from "@/lib/utils";

import { Button } from "../ui";
import { buildOverageRequest, formatCents } from "./usageModel";

import type { OverageChoice, OverageRequestFields } from "./usageModel";

export interface BudgetCapControlProps {
  /** The stored cap, read back from the subscription. */
  value: OverageChoice;
  /** Receives the FULL request — budget_cents, always. */
  onSubmit: (fields: OverageRequestFields) => void;
  isSubmitting?: boolean;
  /** Set when there is no plan to attach a cap to. */
  disabledReason?: string;
}

type Mode = OverageChoice["kind"];

const MODES: Array<{ mode: Mode; label: string; description: string }> = [
  {
    mode: "uncapped",
    label: "No ceiling",
    description:
      "Usage past your included allowance keeps billing, with no monthly limit. Scale-ups are still blocked at 100% of an allowance.",
  },
  {
    mode: "capped",
    label: "Set a ceiling",
    description:
      "Stop new deployments once overage reaches the amount you set. Services already running are not stopped, and storage is never released.",
  },
];

/** Dollars typed by the user → whole cents. */
function dollarsToCents(input: string): number | null {
  const parsed = Number.parseFloat(input);
  if (!Number.isFinite(parsed) || parsed <= 0) return null;
  return Math.round(parsed * 100);
}

export function BudgetCapControl({
  value,
  onSubmit,
  isSubmitting,
  disabledReason,
}: BudgetCapControlProps) {
  const fieldId = useId();
  const [mode, setMode] = useState<Mode>(value.kind);
  // The dollar input is separate from `mode` only so a user can clear and
  // retype without the component forgetting which mode they picked.
  const [capDollars, setCapDollars] = useState<string>(
    value.kind === "capped" ? (value.budgetCents / 100).toFixed(2) : "",
  );
  const [error, setError] = useState<string | null>(null);

  // Re-sync when the server's stored value changes under us (a successful
  // save, or another tab). Without this the form would keep showing a stale
  // choice and the next submit would write it back. It only MIRRORS state
  // into the form; it never submits — nothing here may authorize spend
  // without a click.
  useEffect(() => {
    setMode(value.kind);
    setCapDollars(
      value.kind === "capped" ? (value.budgetCents / 100).toFixed(2) : "",
    );
    setError(null);
  }, [value]);

  function handleSubmit(event: React.FormEvent) {
    event.preventDefault();
    if (disabledReason) return;

    let choice: OverageChoice;
    if (mode === "capped") {
      const cents = dollarsToCents(capDollars);
      if (cents === null) {
        // A cap of zero is NOT a cap — the enforcement gate tests `> 0`, so
        // saving 0 here would be stored as "no ceiling", the exact opposite
        // of what someone typing 0 intends. Refusing forces the user to say
        // which of the two they actually meant.
        setError("Enter a ceiling above $0.00, or choose “No ceiling” instead.");
        return;
      }
      choice = { kind: "capped", budgetCents: cents };
    } else {
      choice = { kind: "uncapped" };
    }

    setError(null);
    // The ONLY call. buildOverageRequest is total over the union, so the cap
    // is always on the wire — including the explicit 0 that spells "no cap".
    onSubmit(buildOverageRequest(choice));
  }

  return (
    <form onSubmit={handleSubmit} className="flex flex-col gap-4">
      <div>
        <p className="text-sm font-medium text-foreground">
          Deployment spending cap
        </p>
        <p className="text-xs text-muted-foreground">
          Controls what happens once you pass the included allowance.
        </p>
      </div>

      <fieldset
        className="flex flex-col gap-2 disabled:opacity-60"
        disabled={Boolean(disabledReason) || isSubmitting}
      >
        <legend className="sr-only">Deployment overage billing</legend>
        {MODES.map((option) => {
          const selected = mode === option.mode;
          const optionId = `${fieldId}-${option.mode}`;
          return (
            <label
              key={option.mode}
              htmlFor={optionId}
              className={cn(
                "flex cursor-pointer items-start gap-3 rounded-md border px-3 py-3 transition-colors",
                // bg-muted here is INTERACTION state (the selected row), not
                // structure — the one use ../ui/card.tsx permits it for.
                selected ? "border-primary bg-muted/40" : "border-border",
                (disabledReason || isSubmitting) && "cursor-not-allowed",
              )}
            >
              <input
                id={optionId}
                type="radio"
                name={`${fieldId}-mode`}
                value={option.mode}
                checked={selected}
                onChange={() => {
                  setMode(option.mode);
                  setError(null);
                }}
                // The accessible NAME is the label text alone; the longer
                // description is associated separately so a screen reader
                // announces "Set a ceiling" rather than the whole paragraph,
                // and so the two options stay distinguishable.
                aria-labelledby={`${optionId}-label`}
                aria-describedby={`${optionId}-description`}
                className="mt-0.5 h-4 w-4 shrink-0 accent-primary"
              />
              <span className="flex flex-col gap-1">
                <span
                  id={`${optionId}-label`}
                  className="text-sm font-medium text-foreground"
                >
                  {option.label}
                </span>
                <span
                  id={`${optionId}-description`}
                  className="text-xs leading-relaxed text-muted-foreground"
                >
                  {option.description}
                </span>
              </span>
            </label>
          );
        })}

        {mode === "capped" ? (
          <div className="mt-1">
            <label
              htmlFor={`${fieldId}-cap`}
              className="block text-xs font-medium text-foreground"
            >
              Monthly ceiling
            </label>
            <div className="mt-1 flex flex-wrap items-center gap-2">
              <span className="text-sm text-muted-foreground">$</span>
              <input
                id={`${fieldId}-cap`}
                type="number"
                min="0.01"
                step="0.01"
                inputMode="decimal"
                value={capDollars}
                onChange={(e) => {
                  setCapDollars(e.target.value);
                  setError(null);
                }}
                aria-describedby={error ? `${fieldId}-error` : undefined}
                aria-invalid={error ? true : undefined}
                className={cn(
                  "w-32 rounded-md border bg-background px-3 py-2 text-sm text-foreground",
                  "focus:outline-none focus:ring-2 focus:ring-ring",
                  error ? "border-destructive" : "border-border",
                )}
              />
              <span className="text-xs text-muted-foreground">per month</span>
            </div>
          </div>
        ) : null}
      </fieldset>

      {/*
        The current stored state, spelled out. "No cap" is a sentence, not an
        empty field — and it is deliberately worded so it can never be read as
        a $0.00 cap, which would mean the opposite.
      */}
      <p className="text-xs text-muted-foreground">
        Currently saved:{" "}
        <span className="font-medium text-foreground">
          {value.kind === "uncapped"
            ? "no spending ceiling"
            : `ceiling of ${formatCents(value.budgetCents)} per month`}
        </span>
      </p>

      {error ? (
        <p id={`${fieldId}-error`} role="alert" className="text-xs text-destructive">
          {error}
        </p>
      ) : null}

      {disabledReason ? (
        <p className="text-xs text-muted-foreground">{disabledReason}</p>
      ) : (
        <div>
          <Button type="submit" size="sm" isLoading={isSubmitting}>
            {isSubmitting ? "Saving…" : "Save spending cap"}
          </Button>
        </div>
      )}
    </form>
  );
}
