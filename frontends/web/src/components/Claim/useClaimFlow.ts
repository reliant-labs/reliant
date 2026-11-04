// Copyright (c) 2025 Reliant Labs

/**
 * The /claim page's state machine (design: control-plane
 * docs/design/claimable-deploys.md §4).
 *
 * Path 1, a new human:
 *
 *   read the pending claim (lib/claimLink.ts)
 *     └ GetClaim ............... preview, shown BEFORE anyone signs in
 *         └ "Claim it" ......... OpenClaimSession (the session, ONCE)
 *             └ adoptSession ... this browser IS the anonymous user that deployed
 *                 └ /upgrade ... the existing flow attaches an email or provider
 *                     (same user id) and returns here
 *                     └ CompleteClaim ... the org and site are the human's
 *                         └ /apps, with the app highlighted
 *
 * Path 2, someone who already has an account (W11):
 *
 *   GetClaim (preview)
 *     └ signed in already ...... "Claim into my account" → CompleteClaim as
 *     │                          themselves: they join the site's org as its
 *     │                          owner → switch to that org → /apps
 *     │                          If they already hold their one free static site
 *     │                          a dialog asks which site stays live (or sends
 *     │                          them to subscribe and back); the claim waits.
 *     └ not signed in .......... "Sign in to claim" → /auth, which returns here
 *                                (the token stays in storage) → the line above
 *                                No session is released, so the link stays good.
 *
 * The page remounts at each hop (the upgrade flow leaves it, and a provider
 * redirect or an emailed link comes back to a fresh load), so nothing here
 * assumes continuity. What carries over is the pending claim in storage and the
 * Supabase session; `claimSituation` reads those two to find the step.
 *
 * Onboarding: this page lives outside the authenticated layout and does not
 * create a daemon or touch the onboarding plan, so OnboardingRoute's
 * returning-user heal (see the onboarding-flow skill) has nothing to fire on.
 * It lands on /apps, not on a route that would send an unonboarded user into
 * /onboarding on its own.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate } from "@tanstack/react-router";

import { trackEvent } from "@/lib/analytics";
import {
  CLAIM_RETURN_TO,
  clearPendingClaim,
  readPendingClaim,
  updatePendingClaim,
  type PendingClaim,
} from "@/lib/claimLink";
import { errorReason } from "@/lib/errorReasons";
import { logger } from "@/lib/logger";
import { queryClient } from "@/lib/query-client";
import { supabase } from "@/lib/supabase";
import { hasControlPlane } from "@/services/controlPlane/config";
import { switchToJoinedOrg } from "@/services/controlPlane/orgs";
import {
  claimRefusalOf,
  completeActionOf,
  completeClaim,
  failureReasonOf,
  freeSiteConflictOf,
  getClaim,
  getClaimFreeSite,
  isDeclined,
  openClaimSession,
  type ClaimRefusal,
  type ClaimSite,
  type FreeSiteConflict,
  type FreeSiteKeep,
  type ReleasedSession,
} from "@/services/controlPlane/claim";
import { useAuthStore } from "@/store/authStore";

import { useGoToBilling } from "@/hooks/useGoToBilling";

import { UPGRADE_STILL_REQUIRED_MESSAGE } from "./claimCopy";
import { claimSituation, type ClaimSituation } from "./claimSituation";

export type ClaimView =
  | { kind: "loading" }
  /** No claim link in this browser: the page was opened without its fragment. */
  | { kind: "no-link" }
  | { kind: "refused"; refusal: ClaimRefusal }
  /** The control plane could not be asked: nothing is known about the claim. */
  | { kind: "unavailable"; message: string }
  | {
      kind: "ready";
      site: ClaimSite;
      situation: ClaimSituation;
      working: boolean;
      error: string | null;
      /** Claiming into this account would put a second site on their one free slot. */
      freeSite: FreeSiteConflict | null;
    }
  | { kind: "finishing" }
  | { kind: "finish-failed"; message: string }
  /** CompleteClaim refused for a reason this page has no name for. */
  | { kind: "finish-declined"; reason: string | null };

export interface ClaimFlow {
  view: ClaimView;
  /** "Claim it, it's free": release the session and start the upgrade. */
  claim: () => void;
  /**
   * "Claim into my account": CompleteClaim as the signed-in person, who joins
   * the org as owner. When they already hold their free static site this opens
   * the dialog instead, so the claim never decides which site stays live.
   */
  claimIntoAccount: () => void;
  /** The free-site dialog, or null when none is open. */
  freeSiteDialog: FreeSiteConflict | null;
  /** The person's answer: keep one site live, or leave to subscribe and keep both. */
  answerFreeSite: (answer: FreeSiteKeep | "subscribe") => void;
  /** Close the dialog. Nothing was changed. */
  cancelFreeSite: () => void;
  /** CompleteClaim again, after it failed for a reason a retry can fix. */
  retryFinish: () => void;
  /** Load the preview again, after the control plane could not be reached. */
  retryPreview: () => void;
}

const UNREACHABLE = "Reliant could not be reached. Check your connection and try again.";

function messageOf(err: unknown, fallback: string): string {
  return err instanceof Error && err.message ? err.message.replace(/^\[[^\]]+\]\s*/, "") : fallback;
}

export function useClaimFlow(): ClaimFlow {
  const navigate = useNavigate();
  const user = useAuthStore((s) => s.user);
  const initialized = useAuthStore((s) => s.initialized);
  const authLoading = useAuthStore((s) => s.loading);
  const initialize = useAuthStore((s) => s.initialize);
  const adoptSession = useAuthStore((s) => s.adoptSession);
  const signOut = useAuthStore((s) => s.signOut);

  const [view, setView] = useState<ClaimView>({ kind: "loading" });
  // The released tokens, held only in memory. The server hands them out once,
  // so a failed install must be retried with these, not with a second open.
  const released = useRef<ReleasedSession | null>(null);
  const decided = useRef(false);

  useEffect(() => {
    if (!initialized) void initialize();
  }, [initialized, initialize]);

  const loadPreviewRef = useRef<(pending: PendingClaim, situation: ClaimSituation, known?: FreeSiteConflict) => Promise<void>>(async () => undefined);
  const [freeSiteDialog, setFreeSiteDialog] = useState<FreeSiteConflict | null>(null);
  // The answer the last CompleteClaim carried, so "Try again" after a failed
  // network call sends the same one instead of asking the server to guess.
  const keepRef = useRef<FreeSiteKeep | undefined>(undefined);
  const goToBilling = useGoToBilling("claim");

  const toApps = useCallback(
    (project: string | undefined, paused?: { name: string; kept: "claimed" | "existing" }) => {
      const search = {
        ...(project ? { claimed: project } : {}),
        // Said where the result is: which site was paused, and why it is not
        // a surprise. Reversible by subscribing (the /apps banner says so).
        ...(paused ? { pausedSite: paused.name, kept: paused.kept } : {}),
      };
      void navigate({ to: "/apps", search, replace: true });
    },
    [navigate],
  );

  const finish = useCallback(
    async (pending: PendingClaim, keep?: FreeSiteKeep) => {
      keepRef.current = keep;
      setView({ kind: "finishing" });
      try {
        // The server decides "upgraded" from a verified email on the bearer
        // token, not from is_anonymous. An upgrade that needed no email
        // confirmation leaves the session's JWT stale, so re-issue it from the
        // upgraded user BEFORE asking. A failed refresh is not fatal: the token
        // may already be current, and CompleteClaim's own answer is the verdict.
        try {
          const { error } = await supabase.auth.refreshSession();
          if (error) logger.warn("[Claim] refreshSession failed", { message: error.message });
        } catch (refreshErr) {
          logger.warn("[Claim] refreshSession threw", { message: messageOf(refreshErr, "unknown") });
        }
        const done = await completeClaim(pending.token, keep);
        trackEvent("claim_completed", { claimPath: done.claimPath, holds: done.holds, freeSite: done.freeSite });
        clearPendingClaim();
        // A membership claim added an org the person did not have: act in it,
        // so /apps lists the site they just claimed. A failure here must not
        // undo the claim; the org is in their switcher either way.
        if (done.claimPath === "membership" && done.orgId) {
          try {
            await switchToJoinedOrg(done.orgId);
          } catch (switchErr) {
            logger.warn("[Claim] could not switch to the claimed org", { reason: failureReasonOf(switchErr) });
          }
        }
        // The org's environments are now the user's own: read them afresh.
        await queryClient.invalidateQueries({ queryKey: ["forge"] });
        // Report what HAPPENED, not what was asked: the server re-checks inside
        // the claim, so a site deleted or a plan bought since the choice shows
        // as no pause at all.
        toApps(
          pending.project,
          done.pausedSite
            ? { name: done.pausedSite.name || done.pausedSite.project, kept: done.freeSite === "claimed-live" ? "claimed" : "existing" }
            : undefined,
        );
      } catch (err) {
        trackEvent("claim_failed", { stage: "complete", reason: failureReasonOf(err) });
        const refusal = claimRefusalOf(err);
        if (refusal) {
          setView({ kind: "refused", refusal });
          return;
        }
        const action = completeActionOf(err);
        if (action === "claimable_free_site_choice_required") {
          // The preview said no choice was needed (or could not be asked) and
          // the server disagrees. Ask now: the claim changed nothing.
          const conflict = freeSiteConflictOf(err);
          if (conflict) {
            // Put the page back to "ready" first (finish() left it on
            // "finishing"), carrying the conflict so Cancel leaves a page that
            // asks again. The dialog opens only once the page under it is there.
            await loadPreviewRef.current(pending, "existing-account", conflict);
            trackEvent("claim_free_site_prompted", { source: "claim_refusal" });
            setFreeSiteDialog(conflict);
            return;
          }
        }
        if (action === "claimable_upgrade_required") {
          // Still anonymous, or the email is not verified: back through the
          // upgrade flow, once. If it bounces straight back here with the same
          // answer, say so rather than loop.
          if (pending.upgradeSent) {
            setView({ kind: "finish-failed", message: UPGRADE_STILL_REQUIRED_MESSAGE });
            return;
          }
          updatePendingClaim({ upgradeSent: true });
          void navigate({ to: "/upgrade", search: { returnTo: CLAIM_RETURN_TO } });
          return;
        }
        if (isDeclined(err)) {
          logger.warn("[Claim] CompleteClaim declined", { reason: failureReasonOf(err) });
          setView({ kind: "finish-declined", reason: errorReason(err) });
          return;
        }
        logger.warn("[Claim] CompleteClaim failed", { reason: failureReasonOf(err) });
        setView({ kind: "finish-failed", message: messageOf(err, "We could not finish your claim.") });
      }
    },
    [toApps, navigate],
  );

  const loadPreview = useCallback(
    async (pending: PendingClaim, situation: ClaimSituation, known?: FreeSiteConflict) => {
      setView({ kind: "loading" });
      if (!hasControlPlane) {
        setView({ kind: "unavailable", message: "Claiming a site needs Reliant's hosted service, which this app is not connected to." });
        return;
      }
      try {
        const preview = await getClaim(pending.token);
        if (!pending.opened) {
          trackEvent("claim_link_opened", { result: preview.claimable ? preview.site.state : preview.refusal, situation });
          updatePendingClaim({ opened: true });
        }
        if (!preview.claimable) {
          setView({ kind: "refused", refusal: preview.refusal });
          return;
        }
        if (preview.site.project) updatePendingClaim({ project: preview.site.project });
        // Whether claiming into THIS account needs a choice, known before the
        // person presses anything. A failed read is not fatal: the claim
        // itself refuses with the same two sites if one is needed.
        let freeSite: FreeSiteConflict | null = known ?? null;
        if (situation === "existing-account" && !known) {
          try {
            freeSite = await getClaimFreeSite(pending.token);
          } catch (readErr) {
            logger.warn("[Claim] GetClaimFreeSite failed", { reason: failureReasonOf(readErr) });
          }
        }
        setView({ kind: "ready", site: preview.site, situation, working: false, error: null, freeSite });
      } catch (err) {
        if (!pending.opened) {
          trackEvent("claim_link_opened", { result: "error", situation });
          updatePendingClaim({ opened: true });
        }
        trackEvent("claim_failed", { stage: "preview", reason: failureReasonOf(err) });
        logger.warn("[Claim] GetClaim failed", { reason: failureReasonOf(err) });
        setView({ kind: "unavailable", message: UNREACHABLE });
      }
    },
    [],
  );
  loadPreviewRef.current = loadPreview;

  // Decide once, when auth has settled, where in the flow this load is.
  useEffect(() => {
    if (decided.current || !initialized || authLoading) return;
    decided.current = true;

    const pending = readPendingClaim();
    if (!pending) {
      setView({ kind: "no-link" });
      return;
    }
    const situation = claimSituation(user, pending);
    if (situation === "finish") {
      void finish(pending);
      return;
    }
    void loadPreview(pending, situation);
  }, [initialized, authLoading, user, finish, loadPreview]);

  const claim = useCallback(() => {
    const pending = readPendingClaim();
    if (!pending || view.kind !== "ready") return;
    const { site, situation } = view;
    // This browser already holds the site's session (it left mid-upgrade).
    // Asking the server for it again is refused by design.
    if (situation === "resume-upgrade") {
      trackEvent("claim_started", { situation });
      void navigate({ to: "/upgrade", search: { returnTo: CLAIM_RETURN_TO } });
      return;
    }
    const fail = (stage: string, err: unknown, message: string) => {
      trackEvent("claim_failed", { stage, reason: failureReasonOf(err) });
      const refusal = claimRefusalOf(err);
      if (refusal) setView({ kind: "refused", refusal });
      else setView({ kind: "ready", site, situation, working: false, error: message, freeSite: null });
    };

    setView({ kind: "ready", site, situation, working: true, error: null, freeSite: null });
    trackEvent("claim_started", { situation });

    void (async () => {
      let session = released.current;
      if (!session) {
        try {
          session = await openClaimSession(pending.token);
          released.current = session;
        } catch (err) {
          fail("open_session", err, messageOf(err, UNREACHABLE));
          return;
        }
      }
      try {
        // A different account in this browser is replaced, not merged: sign it
        // out first so nothing cached for it is read as the claimant's.
        if (useAuthStore.getState().user) await signOut();
        const adopted = await adoptSession(session);
        released.current = null;
        updatePendingClaim({ anonUserId: adopted.id });
        void navigate({ to: "/upgrade", search: { returnTo: CLAIM_RETURN_TO } });
      } catch (err) {
        // The session was released and is only in memory now: say so, and keep
        // it for the retry instead of asking the server for a second one.
        fail("adopt_session", err, "We could not sign this browser in to the site's account. Try again.");
      }
    })();
  }, [view, signOut, adoptSession, navigate]);

  const claimIntoAccount = useCallback(() => {
    const pending = readPendingClaim();
    if (!pending) {
      setView({ kind: "no-link" });
      return;
    }
    // Two free sites cannot both stay live. The person decides which, before
    // anything is claimed; there is no default.
    if (view.kind === "ready" && view.freeSite) {
      trackEvent("claim_free_site_prompted", { source: "preview" });
      setFreeSiteDialog(view.freeSite);
      return;
    }
    trackEvent("claim_started", { situation: "existing-account" });
    void finish(pending);
  }, [finish, view]);

  const cancelFreeSite = useCallback(() => {
    trackEvent("claim_free_site_answered", { answer: "cancel" });
    setFreeSiteDialog(null);
  }, []);

  const answerFreeSite = useCallback(
    (answer: FreeSiteKeep | "subscribe") => {
      trackEvent("claim_free_site_answered", { answer });
      setFreeSiteDialog(null);
      if (answer === "subscribe") {
        // The claim waits: the token is in storage, the link keeps its own
        // expiry, and /claim picks it up again when billing sends them back
        // (this page's address is the return path). Once they have a plan
        // there is no conflict and no question.
        goToBilling();
        return;
      }
      const pending = readPendingClaim();
      if (!pending) {
        setView({ kind: "no-link" });
        return;
      }
      trackEvent("claim_started", { situation: "existing-account", freeSite: answer });
      void finish(pending, answer);
    },
    [finish, goToBilling],
  );

  const retryFinish = useCallback(() => {
    const pending = readPendingClaim();
    if (pending) void finish(pending, keepRef.current);
    else setView({ kind: "no-link" });
  }, [finish]);

  const retryPreview = useCallback(() => {
    const pending = readPendingClaim();
    if (!pending) {
      setView({ kind: "no-link" });
      return;
    }
    void loadPreview(pending, claimSituation(useAuthStore.getState().user, pending));
  }, [loadPreview]);

  return { view, claim, claimIntoAccount, freeSiteDialog, answerFreeSite, cancelFreeSite, retryFinish, retryPreview };
}
