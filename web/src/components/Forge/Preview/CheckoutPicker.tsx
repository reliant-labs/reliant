// Copyright (c) 2025 Reliant Labs

/**
 * WHICH CODE THE CHANGES TAB IS ABOUT — a dropdown of this project's
 * checkouts.
 *
 * It was a wrap of clickable tags, one per worktree. A project with a dozen
 * branches turned that into a wall of chips above the thing the user actually
 * came to read, and nothing about a chip said "this is a choice of one". A
 * select is the control for picking one of N, it costs one row however many
 * branches there are, and the trigger states the current answer.
 *
 * TWO THINGS HERE ARE DELIBERATELY NOT DECORATION.
 *
 * The DIRTY marker: a checkout with uncommitted changes builds from those
 * changes, so what would be deployed exists on nobody else's machine and in no
 * commit. Legitimate for a preview and worth knowing before approving a deploy,
 * so it is stated on the trigger as well as in the list.
 *
 * The distance from main is OMITTED, not zeroed, when it is unknown. Forge
 * leaves the counts out when it could not compare (no remote, or main never
 * fetched), and rendering that as "in step with main" would be a confident lie
 * about how stale the code is — to the one person about to ship it.
 */

import { useEffect, useRef, useState } from "react";
import { Check, ChevronDown, GitBranch } from "lucide-react";

import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
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

function labelOf(checkout: ForgeCheckout): string {
  return checkout.label?.trim() || checkout.branch?.trim() || "this branch";
}

export function CheckoutPicker({ report, selected, onSelect, isLoading }: CheckoutPickerProps) {
  const checkouts = selectableCheckouts(report);
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  // DROPDOWN_STANDARDS: mousedown click-outside, no backdrop.
  useEffect(() => {
    if (!open) return;
    const handleClickOutside = (event: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(event.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, [open]);

  if (isLoading && checkouts.length === 0) {
    return (
      <div data-testid="checkout-picker-loading" aria-label="Looking for your branches" className="w-64">
        <SkeletonLoader variant="form-field" />
      </div>
    );
  }

  // Nothing to choose between. A picker with one option is a question with one
  // answer.
  if (checkouts.length <= 1) return null;

  // Empty `selected` means the main checkout: whichever one forge marks
  // selected, else the first.
  const current =
    checkouts.find((checkout) => (checkout.path ?? "") === selected) ??
    checkouts.find((checkout) => checkout.selected === true) ??
    checkouts[0]!;

  return (
    <div className="relative inline-block" ref={containerRef} data-testid="checkout-picker">
      <span id="checkout-picker-label" className="mb-1 block text-xs font-medium text-muted-foreground">
        Branch
      </span>
      <button
        type="button"
        onClick={() => setOpen((previous) => !previous)}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-labelledby="checkout-picker-label checkout-picker-trigger"
        id="checkout-picker-trigger"
        data-testid="checkout-picker-trigger"
        className="flex h-9 min-w-[16rem] max-w-md items-center gap-2 rounded-md border border-border bg-background px-3 text-left text-sm text-foreground transition-colors hover:border-border-strong focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <GitBranch className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="truncate font-mono">{labelOf(current)}</span>
        {current.dirty === true && (
          <span className="shrink-0 text-2xs text-warning-ink" data-testid="checkout-trigger-dirty">
            uncommitted
          </span>
        )}
        <ChevronDown className="ml-auto h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
      </button>

      {open && (
        <ul
          role="listbox"
          aria-labelledby="checkout-picker-label"
          className="absolute left-0 top-full z-50 mt-1 max-h-72 w-full min-w-[20rem] overflow-y-auto rounded-lg border border-border bg-card p-1 shadow-md"
        >
          {checkouts.map((checkout) => (
            <CheckoutOption
              key={checkout.path}
              checkout={checkout}
              isSelected={checkout === current}
              onSelect={() => {
                setOpen(false);
                onSelect(checkout.path ?? "");
              }}
            />
          ))}
        </ul>
      )}
    </div>
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
  return (
    <li
      role="option"
      aria-selected={isSelected}
      tabIndex={0}
      onClick={onSelect}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          onSelect();
        }
      }}
      data-testid="checkout-option"
      data-selected={isSelected}
      data-dirty={checkout.dirty === true}
      className={cn(
        "flex cursor-pointer items-start gap-2 rounded-md px-2.5 py-1.5 text-sm transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        isSelected ? "bg-primary/10" : "hover:bg-muted/70"
      )}
    >
      <Check className={cn("mt-0.5 h-3.5 w-3.5 shrink-0 text-primary", !isSelected && "invisible")} aria-hidden="true" />
      <span className="flex min-w-0 flex-col">
        <span className="truncate font-mono text-foreground">{labelOf(checkout)}</span>
        <span className="flex flex-wrap gap-x-2 text-2xs text-muted-foreground">
          {/* Uncommitted changes: this builds from code only on this machine. */}
          {checkout.dirty === true && (
            <span data-testid="checkout-dirty" className="text-warning-ink">
              uncommitted changes
            </span>
          )}
          {/* Present only when forge could actually compare. */}
          {distance && <span>{distance}</span>}
        </span>
      </span>
    </li>
  );
}
