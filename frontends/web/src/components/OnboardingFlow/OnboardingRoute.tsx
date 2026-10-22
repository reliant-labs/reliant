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
 * on /onboarding?" is handled here too — if the user ARRIVED at /onboarding
 * already complete, we just leave. See {@link useArrivedComplete} for why
 * "arrived complete" and "is complete" are different questions.
 */
import { useCallback, useEffect } from "react";
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
import { useReturningUserHeal } from "./useReturningUserHeal";
import { useArrivedComplete } from "./useArrivedComplete";
import { leaveOnboarding } from "./leaveOnboarding";
import { GITHUB_CREDENTIAL_QUERY_KEY } from "@/hooks/useGitHubCredential";

export function OnboardingRoute() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const search = useSearch({ from: "/_authenticated/onboarding" });
  const { data: currentUser, isLoading: isUserLoading } = useCurrentUser();

  const isComplete = !isUserLoading && !!currentUser?.onboardingCompleted;

  // "The server recorded completion" is NOT "this flow is finished with the
  // user". A terminal step marks completion and THEN commits the plan, and the
  // completion mutation writes `onboardingCompleted: true` into this very
  // query — so reading `isComplete` as "leave now" made resolving the
  // completion tear down the step that was about to render ProvisioningGate.
  // The redirect below asks the arrival question instead. See
  // ./useArrivedComplete.ts.
  const arrivedComplete = useArrivedComplete({
    userId: currentUser?.id,
    isComplete,
  });

  // A user who already set up a daemon in a previous session was never marked
  // complete, and would otherwise be bounced back here forever. See
  // useReturningUserHeal for what does and does not count as proof — in
  // particular, a daemon this flow just created does not.
  const { data: daemons, isLoading: daemonsLoading } = useDaemonList();
  const completeOnboarding = useCompleteOnboarding();

  const handleHeal = useCallback(() => {
    // Best-effort: if the write fails the user still lands in the app, and
    // the next visit retries.
    completeOnboarding.mutate(
      { source: "returning_user_with_daemon" },
      { onSettled: () => void leaveOnboarding("returning_user_heal", navigate) },
    );
  }, [completeOnboarding, navigate]);

  useReturningUserHeal({
    userId: currentUser?.id,
    userCreatedAtMs: currentUser?.createdAtMs,
    daemons,
    daemonsLoading,
    isComplete,
    onHeal: handleHeal,
  });

  const resetOnboarding = search["reset-onboarding"];
  const { github_connected, github_error, github_error_msg } = search;

  // The user was ALREADY complete when they got here — a bookmarked
  // /onboarding, or a flow finished in another tab. There is nothing for them
  // to do, so leave.
  //
  // Deliberately `arrivedComplete`, not `isComplete`: a completion recorded
  // while this flow is mounted is this flow's own work, and acting on it here
  // is what unmounted the terminal step mid-commit. We also wait for
  // isUserLoading to settle — a one-frame `currentUser === undefined` reads as
  // incomplete and would flash the onboarding UI before navigating away.
  useEffect(() => {
    if (isUserLoading) return;
    if (arrivedComplete) {
      void leaveOnboarding("already_complete", navigate);
    }
  }, [isUserLoading, arrivedComplete, navigate]);

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
  // Users who ARRIVED complete will be navigated away by the effect above;
  // render nothing in the interim to avoid a flash of the step UI. A user who
  // became complete while standing on a terminal step keeps their subtree —
  // that step still has a provisioning gate to show them.
  if (arrivedComplete) return null;

  return (
    <div className="flex h-screen flex-col overflow-hidden bg-background">
      <OnboardingPage />
    </div>
  );
}
