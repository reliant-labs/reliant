/**
 * Which globally-mounted modals a given route will tolerate.
 *
 * `ModalLayer` is mounted on the root route so modals survive navigation, but
 * that also means it has no idea what is underneath it. Some routes own the
 * whole screen for the duration of a flow, and a modal that lands on top of
 * one is wrong regardless of how it got opened.
 *
 * `/onboarding` is the clear case. It renders a `fixed inset-0 z-40` overlay
 * and every modal renders at `z-50`, so a modal drawn while onboarding is
 * active covers the setup flow — the user is asked to configure a credential
 * before they have been told what the product is, and dismissing it returns
 * them to a step they had not yet seen.
 *
 * This is a policy layer, not the fix for any particular trigger. It is
 * expressed as a pure function of the pathname so it can be tested without a
 * router and so adding a route means editing one list.
 */

import type { ModalId } from "@/store/modalStore";

/**
 * Modals that may never render over `/onboarding`.
 *
 * `api-key-setup` is here because onboarding's own ModelStep already collects
 * a provider credential — the modal duplicates a step the user is in the
 * middle of, using different copy.
 *
 * The billing modals are deliberately NOT listed. They are raised in direct
 * response to a request the user just made (a backend 402 / billing-email
 * error) and explain why that request failed, so suppressing them would
 * replace a clear explanation with a silent no-op.
 */
const SUPPRESSED_ON_ONBOARDING: readonly ModalId[] = ["api-key-setup"];

/**
 * True when `modalId` is allowed to render on `pathname`.
 *
 * Unknown routes allow everything: this is a narrow deny-list, so a route
 * that has not opted in behaves exactly as it did before.
 */
export function isModalAllowedOnRoute(
  modalId: ModalId,
  pathname: string,
): boolean {
  if (isOnboardingRoute(pathname) && SUPPRESSED_ON_ONBOARDING.includes(modalId)) {
    return false;
  }
  return true;
}

/**
 * Matches `/onboarding` and any nested path, but not sibling routes that
 * merely share the prefix (`/onboarding-help`). Trailing slashes are
 * tolerated because the router appends one on some navigations.
 */
function isOnboardingRoute(pathname: string): boolean {
  return pathname === "/onboarding" || pathname.startsWith("/onboarding/");
}
