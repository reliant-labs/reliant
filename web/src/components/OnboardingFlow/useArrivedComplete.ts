/**
 * Did this user ARRIVE at /onboarding already finished?
 *
 * ── The two facts the route used to conflate ──────────────────────────
 *
 * "The server has recorded completion" and "this flow is done with the user"
 * are different facts, and `OnboardingRoute` read the first as though it were
 * the second. It derived `isComplete` from `['onboarding','currentUser']`, and
 * on any render where that was true it both navigated away and returned
 * `null`.
 *
 * But the terminal steps make that query true *in the middle of their own
 * work*: they `await completeOnboardingMutation.mutateAsync(...)` and then
 * `await runCommit(...)`, and `useCompleteOnboarding.onSuccess` optimistically
 * writes `onboardingCompleted: true` into that exact cache entry. So resolving
 * the completion was itself what unmounted the subtree that was about to render
 * `ProvisioningGate`: the commit was awaited by a dying component, `setCommit`
 * landed on nothing, and the user was dropped on `/` with
 * `onboarding_completed=true` and no ACTIVE daemon — the failure
 * `finalizeOnboarding.cloudGate.test.ts` describes, live again through a
 * different door.
 *
 * ── What this separates, and why it is not a flag ─────────────────────
 *
 * The redirect this route legitimately owns answers one question: "should you
 * even be on /onboarding?" That is a question about ARRIVAL, so it is answered
 * from the FIRST settled read of the user, and never re-asked. A completion
 * that happens while the flow is mounted is this flow's own work; it cannot be
 * evidence that the user should never have been here.
 *
 * The rejected alternative was suppressing the redirect with a "mid-commit"
 * flag threaded from the steps. That is how the PREVIOUS version of this bug
 * was fixed, and `leaveOnboarding.ts` records why it failed: a flag has to be
 * passed correctly at every call site, so the bug sits one forgotten argument
 * away on any new path. A latch needs no cooperation from the steps at all —
 * a terminal step added tomorrow is covered without knowing this exists.
 *
 * Leaving is then exactly what it claims to be: `leaveOnboarding`, called by
 * the gate's Continue, by the escape hatch, by the returning-user heal, or by
 * this arrival redirect. Nothing exits by side effect.
 *
 * ── Why it is keyed on the user ───────────────────────────────────────
 *
 * Sign-out clears the user cache and the next sign-in resolves a different
 * account (`resetUserCache`). A latch that outlived that would answer for the
 * previous user — the same class of bug `authStore.signOutClearsCache` exists
 * to prevent — so the answer resets whenever the identity does.
 */
import { useRef } from "react";

export interface ArrivedCompleteInput {
  /**
   * Whose arrival this is. Undefined until the user query resolves, which is
   * also how this hook knows the answer is not settled yet.
   */
  userId: string | undefined;
  /** The server's current answer, including any optimistic write. */
  isComplete: boolean;
}

/**
 * True only if this user's FIRST observed state on this route was "complete".
 *
 * The identity IS the loading guard — a separate `isUserLoading` term was
 * tried and deleted, because it could never independently change the answer.
 * While the query is in flight `userId` is undefined, so the pair latched is
 * `(undefined, false)`; when the user resolves the identity changes and the
 * question is asked again against real data. An unsettled read therefore
 * reports false, keeping the user on the flow for a frame rather than
 * redirecting on a guess — the same pessimism `useOnboardingFacts` applies,
 * and for the same reason: a wrong redirect is unrecoverable from the user's
 * side, a wrong extra frame costs nothing.
 */
export function useArrivedComplete({
  userId,
  isComplete,
}: ArrivedCompleteInput): boolean {
  const latch = useRef<{ userId: string | undefined; complete: boolean } | null>(
    null,
  );

  if (!latch.current || latch.current.userId !== userId) {
    latch.current = { userId, complete: isComplete };
  }

  return latch.current.complete;
}
