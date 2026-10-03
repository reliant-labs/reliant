// Copyright (c) 2025 Reliant Labs

/**
 * WHICH CODE PREVIEW IS ABOUT.
 *
 * Preview answers "what would this code do to that environment", and until
 * there was a picker the answer was always "whatever the daemon's main checkout
 * happens to be" — which is rarely the branch anyone is actually working on.
 * The selected checkout drives every Preview call below it.
 *
 * TWO THINGS HERE ARE DELIBERATELY NOT DECORATION.
 *
 * The DIRTY badge: a checkout with uncommitted changes builds from those
 * changes, so the thing deployed would exist on nobody else's machine and in no
 * commit. That is legitimate for a preview and worth knowing before approving a
 * deploy, so it is stated rather than implied.
 *
 * The distance from main is OMITTED, not zeroed, when it is unknown. Forge
 * leaves the counts out when it could not compare (no remote, or main never
 * fetched), and rendering that as "in step with main" would be a confident lie
 * about how stale the code is — to the one person about to ship it.
 */

import { Check, GitBranch } from "lucide-react";

import { cn } from "@/lib/utils";
import {
  distanceFromMain,
  selectableCheckouts,
  type ForgeCheckout,
  type ForgeCheckoutsReport,
} from "@/services/forge/checkouts";

export interface CheckoutPickerProps {
  report: ForgeCheckoutsReport | null | undefined;
  /** The selected checkout's path. Empty means the project's main checkout. */
  selected: string;
  onSelect: (path: string) => void;
  isLoading?: boolean;
}

export function CheckoutPicker({
  report,
  selected,
  onSelect,
  isLoading,
}: CheckoutPickerProps) {
  const checkouts = selectableCheckouts(report);

  if (isLoading && checkouts.length === 0) {
    return (
      <p
        data-testid="checkout-picker-loading"
        className="rounded-lg border border-dashed border-border px-3 py-2 text-xs text-muted-foreground"
      >
        Looking for your branches…
      </p>
    );
  }

  // Nothing to choose between. Rendering a picker with one option would be
  // asking a question with one answer.
  if (checkouts.length <= 1) return null;

  return (
    <section className="space-y-1.5" data-testid="checkout-picker">
      <p className="text-xs font-medium text-foreground">Preview which branch</p>
      <div className="flex flex-wrap gap-1.5">
        {checkouts.map((checkout) => (
          <CheckoutOption
            key={checkout.path}
            checkout={checkout}
            isSelected={(checkout.path ?? "") === selected}
            onSelect={() => onSelect(checkout.path ?? "")}
          />
        ))}
      </div>
    </section>
  );
}

function CheckoutOption({
  checkout,
  isSelected,
  onSelect,
}: {
  checkout: ForgeCheckout;
  isSelected: boolean;
  onSelect: () => void;
}) {
  const distance = distanceFromMain(checkout);
  const label = checkout.label?.trim() || checkout.branch?.trim() || "this branch";

  return (
    <button
      type="button"
      onClick={onSelect}
      aria-pressed={isSelected}
      data-testid="checkout-option"
      data-selected={isSelected}
      data-dirty={checkout.dirty === true}
      className={cn(
        "flex items-center gap-1.5 rounded-md border px-2 py-1 text-left text-2xs transition-colors",
        isSelected
          ? "border-primary bg-primary/10 text-foreground"
          : "border-border text-muted-foreground hover:bg-muted hover:text-foreground"
      )}
    >
      {isSelected ? (
        <Check className="h-3 w-3 shrink-0 text-primary" aria-hidden="true" />
      ) : (
        <GitBranch className="h-3 w-3 shrink-0" aria-hidden="true" />
      )}
      <span className="font-mono text-foreground">{label}</span>

      {/* Uncommitted changes: this builds from code that is only on this
          machine. */}
      {checkout.dirty === true && (
        <span data-testid="checkout-dirty" className="text-warning">
          uncommitted changes
        </span>
      )}

      {/* Present only when forge could actually compare. */}
      {distance && <span className="text-muted-foreground">{distance}</span>}
    </button>
  );
}
