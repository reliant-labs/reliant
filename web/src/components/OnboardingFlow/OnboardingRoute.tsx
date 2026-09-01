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
import { GITHUB_CREDENTIAL_QUERY_KEY } from "@/hooks/useGitHubCredential";

/**
 * Search params to carry when leaving /onboarding for the app.
 *
 * These redirects used to pass `search: {}`, which erases everything —
 * including `?tour=<step-id>`, the ONLY thing that starts the post-onboarding
 * tour (see useTourNavigation). A redirect that raced the handoff therefore
 * ended the tour before it rendered, silently.
 *
 * `tour` is the only param worth forwarding: the rest of onboarding's search
 * (`plan`, the `github_*` return params) is flow-local and must not leak into
 * the app, so this deliberately whitelists rather than spreading `prev`.
 */
const keepTourHandoff = (prev: Record<string, unknown>) =>
  prev.tour ? { tour: prev.tour } : {};

export function OnboardingRoute() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const search = useSearch({ from: "/_authenticated/onboarding" });
  const { data: currentUser, isLoading: isUserLoading } = useCurrentUser();

  const isComplete = !isUserLoading && !!currentUser?.onboardingCompleted;

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
      { onSettled: () => navigate({ to: "/", search: keepTourHandoff }) },
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

  // If the user has already completed onboarding (e.g. they bookmarked
  // /onboarding, or finished elsewhere), leave the route. We deliberately
  // wait for isUserLoading to settle — without that gate a one-frame
  // `currentUser === undefined` would read as incomplete and we'd flash the
  // onboarding UI before navigating away.
  useEffect(() => {
    if (isUserLoading) return;
    if (isComplete) {
      navigate({ to: "/", search: keepTourHandoff });
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
