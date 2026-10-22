import { useCallback, useEffect, useRef, useState } from "react";
import { useAuthStore } from "@/store/authStore";
import {
  advanceStage,
  loadOrInitState,
  markPrompted,
  shouldPrompt,
  type AnonNudgeState,
} from "@/lib/anonSignInNudge";

// How often we re-check whether the session has crossed the next threshold. The
// thresholds are hours/days apart, so a coarse poll is plenty — it just means a
// long-lived tab notices the crossing without a reload. Also re-checked on
// mount and whenever the anon user id changes.
const CHECK_INTERVAL_MS = 5 * 60 * 1000; // 5 minutes

interface UseAnonSignInNudge {
  /** True when the sign-in nudge modal should be visible. */
  open: boolean;
  /** "Later" — dismiss and advance to the next (larger) backoff stage. */
  dismiss: () => void;
  /** Close without advancing the stage (e.g. after routing into sign-in). */
  close: () => void;
}

/**
 * Drives the anonymous-session sign-in nudge. Only ever active for genuine
 * anonymous Supabase sessions (`user.is_anonymous === true`); signed-in users,
 * api-key sessions, dev/mock users, and the logged-out state all short-circuit
 * to `open: false`. Persistence + schedule live in lib/anonSignInNudge.ts.
 */
export function useAnonSignInNudge(): UseAnonSignInNudge {
  const user = useAuthStore((s) => s.user);
  const initialized = useAuthStore((s) => s.initialized);

  // Only real anonymous Supabase sessions carry is_anonymous === true. The
  // api-key/mock/dev synthetic users set is_anonymous: false, so they never
  // trip this — we must NEVER show the nudge to a signed-in user.
  const isAnon = !!user && user.is_anonymous === true;
  const anonUserId = isAnon ? user.id : null;

  const [open, setOpen] = useState(false);
  // Holds the live persisted record for the active anon session so dismiss/close
  // can mutate the correct stage. Cleared whenever we're not anon.
  const stateRef = useRef<AnonNudgeState | null>(null);

  const evaluate = useCallback(() => {
    if (!initialized || !isAnon) {
      stateRef.current = null;
      setOpen(false);
      return;
    }

    const now = Date.now();
    // loadOrInitState anchors firstSeen on first sight and persists it, so a
    // reload resumes the existing clock rather than re-starting it.
    const state = loadOrInitState(anonUserId, now);
    stateRef.current = state;

    if (shouldPrompt(state, now)) {
      // Stamp lastPromptAt (without advancing the stage) so a re-evaluation
      // while the modal is open doesn't churn.
      stateRef.current = markPrompted(anonUserId, state, now);
      setOpen(true);
    }
  }, [initialized, isAnon, anonUserId]);

  useEffect(() => {
    evaluate();
    if (!initialized || !isAnon) return;

    const timer = window.setInterval(evaluate, CHECK_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [evaluate, initialized, isAnon]);

  const dismiss = useCallback(() => {
    const state = stateRef.current;
    if (state) {
      stateRef.current = advanceStage(anonUserId, state, Date.now());
    }
    setOpen(false);
  }, [anonUserId]);

  const close = useCallback(() => {
    setOpen(false);
  }, []);

  return { open, dismiss, close };
}
