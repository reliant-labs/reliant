import { useNavigate } from "@tanstack/react-router";
import { settingsPath } from "@/lib/paths";
import type { BillingTab } from "@/routeSchemas";

/**
 * Where a trip to billing started. Required at every call site: billing
 * renders a way back for each one, so no visit to it is a dead end. Payment
 * itself happens in place (the Add machine sheet, the upgrade and
 * out-of-credit modals); billing is where plans are compared and settings
 * changed, and the user always has the route home.
 */
export type BillingOrigin =
  | "onboarding"
  | "forge"
  | "machines"
  | "chat"
  | "runs"
  | "inbox"
  | "reliant-ai"
  | "credit"
  | "upgrade"
  | "claim";

/** Where on billing the trip lands, and for which org. Every field is optional. */
export interface GoToBillingOptions {
  /**
   * The org the trip is about (the org a queued environment was read in).
   * Carried as `?org=`: billing acts in the ACTIVE org, and BillingOrgGate
   * asks before switching when this names another one, so a link opened
   * later, or in another tab, never authorizes spend in the wrong org.
   */
  orgId?: string | null;
  /** The tab to open; by default the origin's (hosting for forge, plans otherwise). */
  tab?: BillingTab;
  /** An anchor on that tab; by default `app-hosting` (the agreement card) on the hosting tab. */
  hash?: string;
}

/** The hosting tab's agreement card (UsageBillingPanel's anchor). */
const APP_HOSTING_ANCHOR = "app-hosting";

/**
 * Send the user to billing, carrying where they came from. Hosted apps go to
 * the hosting tab's agreement card; machine origins go to Plans.
 *
 * WHY THERE IS NO LONGER A DETOUR: this hook used to fork on anonymity and
 * route anonymous sessions through /upgrade first, because a subscription
 * bought against a browser-session identity belongs to nobody reachable and
 * losing the session loses the purchase. That rule is right and still enforced
 * — it just moved to where it binds. `useCreateCheckoutSession` refuses to mint
 * a Stripe session for an anonymous user (CheckoutIdentityRequiredError), so
 * the guarantee now sits on the one call that spends money rather than on five
 * navigation call sites, any one of which could have been added without it.
 *
 * What that buys the user: an anonymous visitor can reach billing, read the
 * plans and pick one before being asked for anything. The identity ask arrives
 * at the moment of purchase, with the reason named and the chosen plan carried
 * through — instead of a screen that looks like a sign-in wall, demanded
 * before they have seen a price.
 *
 * `?tab=plans` is the other half: the user asked for a plan, so they land on
 * plans, not on a wallet dashboard they then have to navigate out of.
 *
 * @param from Originating surface, so billing can offer a route back. Passing
 *   "onboarding" is what closes the dead end where a user who detoured from the
 *   compute step had no way back into the wizard. "forge" is an environment
 *   whose deploy is queued on billing: the way back is that environment's
 *   page, where the deploy can be watched going out. Every other origin gets
 *   a generic "Back to where you were".
 * @param options The org the trip is for, and the tab and anchor to open.
 */
export function useGoToBilling(from: BillingOrigin, options: GoToBillingOptions = {}): () => void {
  const navigate = useNavigate();
  const { orgId, tab: askedTab, hash: askedHash } = options;

  return () => {
    // Capture the FULL current URL, not just the route name. The onboarding
    // plan lives entirely in the `plan` search param (useOnboardingPlan), so a
    // return that navigates to a bare "/onboarding" lands on an empty plan and
    // deriveStep sends the user back to step one — they lose every answer they
    // gave before the detour. Round-tripping pathname+search is the same thing
    // ProjectChoiceStep and GitHubConnectStep already do for the GitHub OAuth
    // hop, which is the other place onboarding leaves the app and comes back.
    // An environment page needs its search too: it names the project.
    const returnTo = `${window.location.pathname}${window.location.search}`;

    const tab: BillingTab = askedTab ?? (from === "forge" ? "hosting" : "plans");
    const hash = askedHash ?? (tab === "hosting" ? APP_HOSTING_ANCHOR : undefined);

    void navigate({
      to: settingsPath("billing"),
      search: { tab, from, returnTo, ...(orgId ? { org: orgId } : {}) },
      ...(hash ? { hash } : {}),
    });
  };
}
