// Copyright (c) 2025 Reliant Labs

/**
 * The step-up dialog's state machine: request a code, enter it, retry the
 * write. Pure — no React, no RPCs — so every transition is a unit test.
 *
 * ── WHAT THE SERVER ENFORCES, AND WHAT THIS MIRRORS ─────────────────────────
 *
 * control-plane (docs/secret-step-up.md) owns every rule; this module only
 * keeps the dialog honest about them:
 *
 *   code       10 characters, good for 10 minutes and 5 attempts, single use.
 *              Case, spaces and dashes are ignored.
 *   rate       one code per user and environment per minute
 *              (BeginStepUp answers ResourceExhausted inside it).
 *   grant      15 minutes, for this user in this environment only.
 *
 * The server answers wrong, expired, used and exhausted with ONE error, on
 * purpose, so a caller learns nothing about which. The dialog can still say
 * which, because it knows what it did: it counts its own attempts against the
 * code it asked for, and it knows the code's expiry from BeginStepUp. That is
 * why "attempts exhausted" and "expired" are states here and not errors read
 * off the wire.
 *
 * Attempts are counted CONSERVATIVELY. Every redeem the server saw is
 * committed even when wrong, so a redeem whose answer was lost (a network
 * error) is counted too: showing one attempt fewer than the server holds would
 * send the user into a lockout the dialog said they had room for.
 *
 * ── THE PHASES ─────────────────────────────────────────────────────────────
 *
 *   closed      no dialog.
 *   intro       why a code is needed, and the one action: email me a code.
 *               Nothing is sent until the user asks — a code arriving
 *               unprompted would read as someone else at the door.
 *   sending     BeginStepUp in flight. Remembers the code already out, if
 *               any, so a refused resend can fall back to it.
 *   entering    a code is out; the user types it. Carries the attempts used
 *               on THIS code and at most one notice about the last action.
 *   verifying   CompleteStepUp in flight.
 *   exhausted   five attempts used on this code. Only a new code helps.
 *   expired     the code's ten minutes passed. Only a new code helps.
 *   retrying    the grant is held; the refused write is being re-run.
 *   failed      a dead end this dialog cannot fix: step-up is not available
 *               on this deployment, or the caller is not an org admin.
 */

/** A code is this long once normalised. */
export const STEP_UP_CODE_LENGTH = 10;
/** Redeem attempts the server allows on one code. */
export const STEP_UP_MAX_ATTEMPTS = 5;
/** The server sends at most one code per user and environment per minute. */
export const STEP_UP_RESEND_INTERVAL_MS = 60_000;

/** The code that is out, as far as this dialog knows it. Times are epoch ms. */
export interface StepUpCode {
  /** The masked inbox (j***@example.com); null when the code was sent before this dialog opened. */
  sentTo: string | null;
  /** When the code stops working; null when not known. */
  expiresAt: number | null;
  /** When it was requested — the earliest a new one can be. */
  sentAt: number;
  /** Redeem attempts used on it, counted conservatively (see the header). */
  attempts: number;
}

/** The one thing the entering phase may need to say about the last action. */
export type StepUpNotice =
  /** The code was not accepted. */
  | "invalid"
  /** A code was sent within the last minute; use that one or wait. */
  | "rate-limited"
  /** Sending a code failed for a reason worth retrying. */
  | "send-failed"
  /** The redeem's answer never arrived. */
  | "network";

/** Failures the dialog cannot recover from. */
export type StepUpDeadEnd =
  /** No mailer or no step-up store: the protected environment is read-only here. */
  | "unavailable"
  /** The caller is not an org admin, so no code will be sent. */
  | "forbidden";

export type StepUpState =
  | { phase: "closed" }
  | { phase: "intro"; notice: "send-failed" | null }
  | { phase: "sending"; code: StepUpCode | null }
  | { phase: "entering"; code: StepUpCode; notice: StepUpNotice | null }
  | { phase: "verifying"; code: StepUpCode }
  | { phase: "exhausted"; code: StepUpCode }
  | { phase: "expired"; code: StepUpCode }
  | { phase: "retrying"; validUntil: number | null }
  | { phase: "failed"; reason: StepUpDeadEnd };

/** How a BeginStepUp failed, from the transport error. */
export type SendFailure = "rate-limited" | StepUpDeadEnd | "other";
/** How a CompleteStepUp failed, from the transport error. */
export type VerifyFailure = "invalid" | "unavailable" | "network";

export type StepUpEvent =
  | { type: "open" }
  | { type: "close" }
  | { type: "send" }
  | { type: "sent"; sentTo: string; expiresAt: number | null; at: number }
  | { type: "send-failed"; failure: SendFailure; at: number }
  | { type: "submit" }
  /** `retry`: a refused write is waiting to be re-run. */
  | { type: "verified"; validUntil: number | null; retry: boolean }
  | { type: "verify-failed"; failure: VerifyFailure; at: number }
  | { type: "tick"; at: number }
  /** The retried write settled (either way: its outcome belongs to its caller). */
  | { type: "retried" };

export const STEP_UP_CLOSED: StepUpState = { phase: "closed" };

/** The code in hand, for the phases that have one. */
export function codeOf(state: StepUpState): StepUpCode | null {
  switch (state.phase) {
    case "sending":
      return state.code;
    case "entering":
    case "verifying":
    case "exhausted":
    case "expired":
      return state.code;
    default:
      return null;
  }
}

export function stepUpReducer(state: StepUpState, event: StepUpEvent): StepUpState {
  switch (event.type) {
    case "open":
      // Opening an open dialog keeps its place: a second refused write while
      // the user is reading their email must not throw away the code.
      return state.phase === "closed" ? { phase: "intro", notice: null } : state;

    case "close":
      return STEP_UP_CLOSED;

    case "send":
      if (state.phase === "closed" || state.phase === "sending" || state.phase === "verifying") return state;
      if (state.phase === "retrying" || state.phase === "failed") return state;
      return { phase: "sending", code: codeOf(state) };

    case "sent":
      if (state.phase !== "sending") return state;
      // A new code retires the older ones server-side, and its attempts
      // start from zero.
      return {
        phase: "entering",
        code: { sentTo: event.sentTo || null, expiresAt: event.expiresAt, sentAt: event.at, attempts: 0 },
        notice: null,
      };

    case "send-failed": {
      if (state.phase !== "sending") return state;
      if (event.failure === "unavailable" || event.failure === "forbidden") {
        return { phase: "failed", reason: event.failure };
      }
      if (event.failure === "rate-limited") {
        // A code went out within the last minute — this dialog's own, or one
        // from a dialog the user closed. Either way it is in the inbox and
        // still good, so the honest next step is to enter it. With no record
        // of it here, its expiry is unknown and the earliest resend is a
        // minute from now.
        const code = state.code ?? { sentTo: null, expiresAt: null, sentAt: event.at, attempts: 0 };
        return { phase: "entering", code, notice: "rate-limited" };
      }
      return state.code
        ? { phase: "entering", code: state.code, notice: "send-failed" }
        : { phase: "intro", notice: "send-failed" };
    }

    case "submit":
      if (state.phase !== "entering") return state;
      return { phase: "verifying", code: state.code };

    case "verified":
      if (state.phase !== "verifying") return state;
      return event.retry ? { phase: "retrying", validUntil: event.validUntil } : STEP_UP_CLOSED;

    case "verify-failed": {
      if (state.phase !== "verifying") return state;
      if (event.failure === "unavailable") return { phase: "failed", reason: "unavailable" };
      const code = { ...state.code, attempts: state.code.attempts + 1 };
      if (code.attempts >= STEP_UP_MAX_ATTEMPTS) return { phase: "exhausted", code };
      if (code.expiresAt !== null && event.at >= code.expiresAt) return { phase: "expired", code };
      return { phase: "entering", code, notice: event.failure === "network" ? "network" : "invalid" };
    }

    case "tick":
      if (state.phase === "entering" && state.code.expiresAt !== null && event.at >= state.code.expiresAt) {
        return { phase: "expired", code: state.code };
      }
      return state;

    case "retried":
      return state.phase === "retrying" ? STEP_UP_CLOSED : state;

    default: {
      const unhandled: never = event;
      return unhandled;
    }
  }
}

// ── Selectors ───────────────────────────────────────────────────────────────

/**
 * Whether "send a code" is possible now. The server would refuse a second
 * code inside a minute anyway; offering the button and then explaining the
 * refusal is worse than counting down to when it will work.
 */
export function canSend(state: StepUpState, now: number): boolean {
  switch (state.phase) {
    case "intro":
      return true;
    case "entering":
    case "exhausted":
    case "expired":
      return resendWaitMs(state.code, now) === 0;
    default:
      return false;
  }
}

export function canSubmit(state: StepUpState, raw: string): boolean {
  return state.phase === "entering" && isCodeComplete(raw);
}

export function attemptsLeft(code: StepUpCode): number {
  return Math.max(0, STEP_UP_MAX_ATTEMPTS - code.attempts);
}

/** Milliseconds until a new code may be requested; 0 when one may be now. */
export function resendWaitMs(code: StepUpCode | null, now: number): number {
  if (!code) return 0;
  // Clamped to the interval: a display clock that last ticked just before
  // the code was sent would otherwise count down from 1:01.
  return Math.min(STEP_UP_RESEND_INTERVAL_MS, Math.max(0, code.sentAt + STEP_UP_RESEND_INTERVAL_MS - now));
}

/** Milliseconds until the code expires; null when the expiry is not known. */
export function codeExpiresInMs(code: StepUpCode, now: number): number | null {
  return code.expiresAt === null ? null : Math.max(0, code.expiresAt - now);
}

/**
 * The code as the server compares it: upper-case, with spaces and dashes
 * removed. Used only to decide whether the field is complete — the raw text
 * is what is sent, since the server normalises it the same way.
 */
export function normaliseStepUpCode(raw: string): string {
  return raw.replace(/[\s-]/g, "").toUpperCase();
}

export function isCodeComplete(raw: string): boolean {
  return normaliseStepUpCode(raw).length === STEP_UP_CODE_LENGTH;
}

/** m:ss, for countdowns. Rounds UP so "0:00" is never shown while time remains. */
export function formatCountdown(ms: number): string {
  const total = Math.max(0, Math.ceil(ms / 1000));
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  return `${minutes}:${seconds.toString().padStart(2, "0")}`;
}
