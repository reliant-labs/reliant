/**
 * Route component for `/onboarding`.
 *
 * Owns the URL-side concerns that used to live in ModernApp's render branch:
 *  - reset-onboarding signal: clears the URL plan so the flow restarts.
 *  - GitHub OAuth return at the onboarding context: shows the toast, refreshes
 *    the credential, strips the github_* params, and stays put.
 *  - "Onboarding completed" detection: navigates to `/` so workspace restore /
 *    project picker take over.
 *
 * The presentational OnboardingPage stays focused on rendering the current
 * step; this wrapper bridges it to the router. The redirect "should I even be
 * on /onboarding?" is handled here too — if the user lands at /onboarding but
 * has already completed, we just leave.
 */
import { useEffect, useRef } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { LoadingSpinner } from "../Layout/LoadingSpinner";
import { OnboardingPage } from "./OnboardingPage";
import {
  useCurrentUser,
  useDaemonList,
  useCompleteOnboarding,
} from "@/hooks/useOnboardingQueries";
import { hasUsableControlPlaneDaemonForOnboarding } from "./steps/ComputeStep";
import { GITHUB_CREDENTIAL_QUERY_KEY } from "@/hooks/useGitHubCredential";

/**
 * Daemons created at or after `userCreatedAtMs` — i.e. ones this user could
 * plausibly have produced.
 *
 * Returns nothing when the user's creation time is unknown. That is the
 * deliberate choice: the heal is an optimisation for a stranded returning user,
 * while wrongly firing it silently skips onboarding for a new account. Given no
 * evidence, do the recoverable thing and let the user onboard.
 */
function daemonsPostdating<T extends { createdAt?: { seconds: bigint } }>(
  daemons: ReadonlyArray<T>,
  userCreatedAtMs: number | undefined,
): T[] {
  if (userCreatedAtMs === undefined) return [];
  return daemons.filter((d) => {
    if (!d.createdAt) return false;
    return Number(d.createdAt.seconds) * 1000 >= userCreatedAtMs;
  });
}

export function OnboardingRoute() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const search = useSearch({ from: "/_authenticated/onboarding" });
  const { data: currentUser, isLoading: isUserLoading } = useCurrentUser();

  const isComplete = !isUserLoading && !!currentUser?.onboardingCompleted;

  // ── Returning users must not be re-onboarded ────────────────────────────
  //
  // `onboarding_completed_at` is written ONLY by the final project step, so a
  // user who set up a working daemon and then closed the tab is still flagged
  // incomplete — and ModernApp bounces every landing on / back here forever.
  //
  // That trap was survivable while every signup was auto-granted compute (they
  // could always click through again). Now that entitlement requires a coupon
  // or a plan, such a user can be permanently stuck: blocked at step 1, unable
  // to reach the step that would mark them complete, and unable to reach the
  // app they already have a machine for.
  //
  // A usable daemon IS the thing onboarding exists to produce, so treat it as
  // proof of completion and record it, which also repairs the flag for good.
  // The daemon must PREDATE this user, or the heal fires for brand-new accounts.
  //
  // The desktop app's bundled daemon re-registers under whoever signs in — the
  // post-sign-in restart hands it the new principal, and the control-plane row
  // is reassigned to that owner. Verified in dev: daemon 42906ac5, created
  // 2026-07-22, was owned by whoever last signed in. So "this user has a usable
  // daemon" is true within a second of ANY sign-in, including a first-ever one,
  // and the heal below then wrote onboarding_completed_at for users who had
  // never seen a single onboarding step.
  //
  // A daemon created BEFORE the user's account cannot have been produced by
  // that user's onboarding, so it is not evidence they completed it. Comparing
  // against the user's own createdAt keeps the genuine case working — someone
  // who really did set up a daemon and closed the tab has one created after
  // their account — while excluding the reassigned bundled daemon.
  const { data: daemons, isLoading: daemonsLoading } = useDaemonList();
  const completeOnboarding = useCompleteOnboarding();
  const alreadySetUp =
    !daemonsLoading &&
    hasUsableControlPlaneDaemonForOnboarding(
      daemonsPostdating(daemons ?? [], currentUser?.createdAtMs),
    );
  const healingRef = useRef(false);

  useEffect(() => {
    if (isUserLoading || daemonsLoading) return;
    if (isComplete || !alreadySetUp) return;
    if (healingRef.current) return;
    healingRef.current = true;
    // Best-effort: if the write fails the user still lands in the app, and
    // the next visit retries.
    completeOnboarding.mutate(
      { source: "returning_user_with_daemon" },
      { onSettled: () => navigate({ to: "/", search: {} }) },
    );
  }, [
    isUserLoading,
    daemonsLoading,
    isComplete,
    alreadySetUp,
    completeOnboarding,
    navigate,
  ]);
  const resetOnboarding = search["reset-onboarding"];
  const { github_connected, github_error, github_error_msg } = search;

  // If the user has already completed onboarding (e.g. they bookmarked
  // /onboarding, or finished elsewhere), leave the route. We deliberately
  // wait for isUserLoading to settle — without that gate a one-frame
  // `currentUser === undefined` would read as incomplete and we'd flash the
  // onboarding UI before navigating away.
  useEffect(() => {
    if (isUserLoading) return;
    if (isComplete) {
      navigate({ to: "/", search: {} });
    }
  }, [isUserLoading, isComplete, navigate]);

  // reset-onboarding signal: drop plan + the signal, restart from step 0.
  useEffect(() => {
    if (!resetOnboarding) return;
    navigate({
      to: "/onboarding",
      search: { "reset-onboarding": undefined, plan: undefined },
      replace: true,
    });
  }, [resetOnboarding, navigate]);

  // GitHub OAuth return. The control-plane OAuth handler redirects back to
  // `returnTo` (set by ProjectChoiceStep to the current /onboarding URL) with
  // these params appended. We surface a toast, refresh the credential cache so
  // the picker step sees hasToken=true without waiting for staleTime, and
  // strip the params so a reload doesn't replay the toast.
  useEffect(() => {
    if (!github_connected && !github_error) return;
    if (github_connected) {
      toast.success("GitHub connected successfully");
      void queryClient.invalidateQueries({ queryKey: GITHUB_CREDENTIAL_QUERY_KEY });
    } else if (github_error) {
      toast.error(github_error_msg || github_error || "GitHub connection failed");
    }
    navigate({
      to: "/onboarding",
      search: (prev) => {
        const { github_connected: _c, github_error: _e, github_error_msg: _m, ...rest } = prev;
        return rest;
      },
      replace: true,
    });
  }, [github_connected, github_error, github_error_msg, navigate, queryClient]);

  if (isUserLoading) return <LoadingSpinner />;
  // Already-complete users will be navigated away by the effect above; render
  // nothing in the interim to avoid a flash of the step UI.
  if (isComplete) return null;

  return (
    <div className="flex h-screen flex-col overflow-hidden bg-background">
      <OnboardingPage />
    </div>
  );
}
