import { useNavigate } from "@tanstack/react-router";

/**
 * Send the user to billing, on the Plans tab, carrying where they came from.
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
 *   compute step had no way back into the wizard.
 */
export function useGoToBilling(from?: "onboarding"): () => void {
  const navigate = useNavigate();

  return () => {
    // Capture the FULL current URL, not just the route name. The onboarding
    // plan lives entirely in the `plan` search param (useOnboardingPlan), so a
    // return that navigates to a bare "/onboarding" lands on an empty plan and
    // deriveStep sends the user back to step one — they lose every answer they
    // gave before the detour. Round-tripping pathname+search is the same thing
    // ProjectChoiceStep and GitHubConnectStep already do for the GitHub OAuth
    // hop, which is the other place onboarding leaves the app and comes back.
    const returnTo =
      from === "onboarding"
        ? `${window.location.pathname}${window.location.search}`
        : undefined;

    void navigate({
      to: "/settings/$section",
      params: { section: "billing" },
      search: { tab: "plans", from, returnTo },
    });
  };
}
