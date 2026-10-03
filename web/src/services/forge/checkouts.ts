// Copyright (c) 2025 Reliant Labs

/**
 * THE CHECKOUTS A PREVIEW MAY RENDER.
 *
 * Preview answers "what would this code do to that environment", so something
 * has to choose WHICH code. This is that list: the project's remote main plus
 * every git worktree on the daemon's disk.
 *
 * THE LIST IS A CONVENIENCE HERE AND AN ALLOWLIST ON THE SERVER. The daemon
 * honours a checkout only if it re-derived the same list and found it there, so
 * nothing in this file is load-bearing for safety — a request that named
 * something else would be refused regardless of what this module believes.
 * That ordering is deliberate: a check that lives in the browser is a check an
 * attacker simply does not run.
 */

/** Where a checkout came from. */
export type CheckoutKind = "remote" | "main" | "worktree" | "unknown";

/** Decoded conservatively: an unrecognised kind is `unknown`, never `main`. */
export function checkoutKindOf(value: string | undefined): CheckoutKind {
  switch (value) {
    case "remote":
      return "remote";
    case "main":
      return "main";
    case "worktree":
      return "worktree";
    default:
      return "unknown";
  }
}

export interface ForgeCheckout {
  /** What to show. Forge's own label for this checkout. */
  label?: string;
  kind?: string;
  /** For the remote entry: the ref, e.g. "origin/main". */
  ref?: string;
  /**
   * The directory on the daemon's disk. ABSENT for the remote entry — there is
   * nothing checked out to render, which is why that entry is not selectable.
   */
  path?: string;
  branch?: string;
  head?: string;
  /** This checkout has uncommitted changes. */
  dirty?: boolean;
  devstack_key?: string;
  /**
   * Commits ahead of / behind main. OMITTED rather than zero when the
   * comparison could not be made — see aheadBehindKnown.
   */
  ahead_of_main?: number;
  behind_main?: number;
  tree?: string;
  selected?: boolean;
}

export interface ForgeCheckoutsReport {
  project?: string;
  /** What "main" means here: the REMOTE main ref, not a local branch. */
  main_ref?: string;
  checkouts?: ForgeCheckout[];
}

/**
 * The checkouts that can actually be rendered: the ones with a path.
 *
 * The remote entry is filtered out rather than shown-and-disabled. It is not a
 * thing that failed to be selectable; there is simply no working tree for it
 * until somebody checks it out, so offering it would promise something the
 * daemon cannot do.
 */
export function selectableCheckouts(
  report: ForgeCheckoutsReport | null | undefined
): ForgeCheckout[] {
  return (report?.checkouts ?? []).filter((checkout) => !!checkout.path?.trim());
}

/**
 * Whether this checkout's distance from main is KNOWN.
 *
 * "In step with main" and "I could not tell" are different answers, and forge
 * distinguishes them by omitting the counts rather than sending zero. A UI that
 * rendered a missing count as "0 ahead, 0 behind" would state confidently that
 * a checkout is current when the truth is that main was never fetched — which
 * is the one thing someone choosing a checkout to deploy needs to not be lied
 * to about.
 */
export function aheadBehindKnown(checkout: ForgeCheckout): boolean {
  return typeof checkout.ahead_of_main === "number" && typeof checkout.behind_main === "number";
}

/**
 * A short human description of where this checkout sits relative to main.
 *
 * Returns null when the comparison is unknown, so a caller renders nothing
 * rather than a misleading zero.
 */
export function distanceFromMain(checkout: ForgeCheckout): string | null {
  if (!aheadBehindKnown(checkout)) return null;
  const ahead = checkout.ahead_of_main ?? 0;
  const behind = checkout.behind_main ?? 0;
  if (ahead === 0 && behind === 0) return "In step with main";
  const parts: string[] = [];
  if (ahead > 0) parts.push(`${ahead} ahead`);
  if (behind > 0) parts.push(`${behind} behind`);
  return parts.join(", ");
}

/** The checkout forge marked as the current one, if any. */
export function currentCheckout(
  report: ForgeCheckoutsReport | null | undefined
): ForgeCheckout | null {
  return selectableCheckouts(report).find((checkout) => checkout.selected === true) ?? null;
}

/**
 * Whether a path is one this project offers — the browser-side mirror of the
 * server's allowlist.
 *
 * Present so the UI can avoid sending a request it knows will be refused (a
 * worktree deleted since the list was fetched, say). It is NOT the guarantee:
 * the daemon re-derives the list and checks again, which is what holds when
 * this module is bypassed entirely.
 */
export function isOfferedCheckout(
  report: ForgeCheckoutsReport | null | undefined,
  path: string
): boolean {
  const wanted = path.trim();
  if (!wanted) return true; // empty means the project's main checkout
  return selectableCheckouts(report).some((checkout) => checkout.path?.trim() === wanted);
}
