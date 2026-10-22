// Copyright (c) 2025 Reliant Labs

/**
 * The step-up dialog's state machine, transition by transition.
 *
 * The server answers wrong, expired, used and exhausted with one error; these
 * tests pin that the dialog still tells the user which — from its own attempt
 * count and the code's expiry — and that the one-code-per-minute limit turns
 * into "use the code you have", not a dead end.
 */

import { describe, expect, it } from "vitest";

import {
  STEP_UP_CLOSED,
  STEP_UP_MAX_ATTEMPTS,
  STEP_UP_RESEND_INTERVAL_MS,
  attemptsLeft,
  canSend,
  canSubmit,
  codeExpiresInMs,
  formatCountdown,
  isCodeComplete,
  normaliseStepUpCode,
  resendWaitMs,
  stepUpReducer,
  type StepUpEvent,
  type StepUpState,
} from "../stepUp";

const T0 = 1_000_000;
const EXPIRES = T0 + 10 * 60_000;

function run(events: StepUpEvent[], from: StepUpState = STEP_UP_CLOSED): StepUpState {
  return events.reduce(stepUpReducer, from);
}

/** open → send → sent: a code is out, nothing tried yet. */
const CODE_OUT: StepUpEvent[] = [
  { type: "open" },
  { type: "send" },
  { type: "sent", sentTo: "a***@acme.dev", expiresAt: EXPIRES, at: T0 },
];

describe("request a code", () => {
  it("opens on the explanation and sends nothing until asked", () => {
    expect(run([{ type: "open" }])).toEqual({ phase: "intro", notice: null });
  });

  it("moves to code entry with where the code went and when it expires", () => {
    const state = run(CODE_OUT);
    expect(state).toEqual({
      phase: "entering",
      code: { sentTo: "a***@acme.dev", expiresAt: EXPIRES, sentAt: T0, attempts: 0 },
      notice: null,
    });
  });

  it("re-opening an open dialog keeps the code the user is reading", () => {
    const state = run(CODE_OUT);
    expect(stepUpReducer(state, { type: "open" })).toBe(state);
  });

  it("a refused send for being too soon still lets the user enter the code already out", () => {
    const state = run([{ type: "open" }, { type: "send" }, { type: "send-failed", failure: "rate-limited", at: T0 }]);
    expect(state).toEqual({
      phase: "entering",
      code: { sentTo: null, expiresAt: null, sentAt: T0, attempts: 0 },
      notice: "rate-limited",
    });
    // And the next code is a minute away, not now.
    expect(canSend(state, T0 + 1_000)).toBe(false);
    expect(canSend(state, T0 + STEP_UP_RESEND_INTERVAL_MS)).toBe(true);
  });

  it("a refused RESEND falls back to the code already out, attempts intact", () => {
    const tried = run([...CODE_OUT, { type: "submit" }, { type: "verify-failed", failure: "invalid", at: T0 }]);
    const state = run(
      [{ type: "send" }, { type: "send-failed", failure: "rate-limited", at: T0 + 1_000 }],
      // Pretend the minute passed so send is accepted by the UI; the server still said no.
      tried
    );
    expect(state).toMatchObject({ phase: "entering", notice: "rate-limited", code: { sentTo: "a***@acme.dev", attempts: 1 } });
  });

  it.each([
    ["unavailable", { phase: "failed", reason: "unavailable" }],
    ["forbidden", { phase: "failed", reason: "forbidden" }],
  ] as const)("a %s send is a dead end the dialog names", (failure, want) => {
    expect(run([{ type: "open" }, { type: "send" }, { type: "send-failed", failure, at: T0 }])).toEqual(want);
  });

  it("any other send failure returns to the explanation with a retryable notice", () => {
    expect(run([{ type: "open" }, { type: "send" }, { type: "send-failed", failure: "other", at: T0 }])).toEqual({
      phase: "intro",
      notice: "send-failed",
    });
  });

  it("a successful resend starts a fresh code with fresh attempts", () => {
    const tried = run([...CODE_OUT, { type: "submit" }, { type: "verify-failed", failure: "invalid", at: T0 }]);
    const state = run([{ type: "send" }, { type: "sent", sentTo: "a***@acme.dev", expiresAt: EXPIRES + 60_000, at: T0 + 60_000 }], tried);
    expect(state).toMatchObject({ phase: "entering", notice: null, code: { attempts: 0, sentAt: T0 + 60_000 } });
  });
});

describe("enter the code", () => {
  it("submits only a complete code, ignoring case, spaces and dashes", () => {
    const state = run(CODE_OUT);
    expect(canSubmit(state, "abcde")).toBe(false);
    expect(canSubmit(state, "abcde-fghjk")).toBe(true);
    expect(canSubmit(state, " ABCDE FGHJK ")).toBe(true);
    expect(normaliseStepUpCode("ab-cd e")).toBe("ABCDE");
    expect(isCodeComplete("ABCDEFGHJK")).toBe(true);
  });

  it("a wrong code counts an attempt and says how many are left", () => {
    const state = run([...CODE_OUT, { type: "submit" }, { type: "verify-failed", failure: "invalid", at: T0 + 1 }]);
    expect(state).toMatchObject({ phase: "entering", notice: "invalid", code: { attempts: 1 } });
    if (state.phase !== "entering") throw new Error("unreachable");
    expect(attemptsLeft(state.code)).toBe(STEP_UP_MAX_ATTEMPTS - 1);
  });

  it("a lost answer is counted too: the server commits every attempt it saw", () => {
    const state = run([...CODE_OUT, { type: "submit" }, { type: "verify-failed", failure: "network", at: T0 + 1 }]);
    expect(state).toMatchObject({ phase: "entering", notice: "network", code: { attempts: 1 } });
  });

  it("the fifth wrong attempt exhausts the code", () => {
    const wrong: StepUpEvent[] = [];
    for (let i = 0; i < STEP_UP_MAX_ATTEMPTS; i++) {
      wrong.push({ type: "submit" }, { type: "verify-failed", failure: "invalid", at: T0 + i });
    }
    const state = run([...CODE_OUT, ...wrong]);
    expect(state).toMatchObject({ phase: "exhausted", code: { attempts: STEP_UP_MAX_ATTEMPTS } });
    // No more submits on a dead code; only a new one.
    expect(canSubmit(state, "ABCDEFGHJK")).toBe(false);
    expect(stepUpReducer(state, { type: "submit" })).toBe(state);
    expect(stepUpReducer(state, { type: "send" })).toMatchObject({ phase: "sending" });
  });

  it("a wrong answer after the expiry is reported as expired, not as wrong", () => {
    const state = run([...CODE_OUT, { type: "submit" }, { type: "verify-failed", failure: "invalid", at: EXPIRES }]);
    expect(state.phase).toBe("expired");
  });

  it("the clock alone expires an untried code", () => {
    const entering = run(CODE_OUT);
    expect(stepUpReducer(entering, { type: "tick", at: EXPIRES - 1 })).toBe(entering);
    expect(stepUpReducer(entering, { type: "tick", at: EXPIRES })).toMatchObject({ phase: "expired" });
  });

  it("a code whose expiry is not known never expires on the clock", () => {
    const state = run([{ type: "open" }, { type: "send" }, { type: "send-failed", failure: "rate-limited", at: T0 }]);
    expect(stepUpReducer(state, { type: "tick", at: T0 + 60 * 60_000 })).toBe(state);
  });

  it("an unavailable redeem is a dead end", () => {
    expect(run([...CODE_OUT, { type: "submit" }, { type: "verify-failed", failure: "unavailable", at: T0 }])).toEqual({
      phase: "failed",
      reason: "unavailable",
    });
  });
});

describe("retry the write", () => {
  it("with a refused write held, confirming moves to retrying, then closes", () => {
    const state = run([...CODE_OUT, { type: "submit" }, { type: "verified", validUntil: T0 + 900_000, retry: true }]);
    expect(state).toEqual({ phase: "retrying", validUntil: T0 + 900_000 });
    expect(stepUpReducer(state, { type: "retried" })).toEqual(STEP_UP_CLOSED);
  });

  it("with nothing held (Confirm now), confirming simply closes", () => {
    expect(run([...CODE_OUT, { type: "submit" }, { type: "verified", validUntil: null, retry: false }])).toEqual(
      STEP_UP_CLOSED
    );
  });

  it("closing from any phase closes", () => {
    expect(run([...CODE_OUT, { type: "close" }])).toEqual(STEP_UP_CLOSED);
  });
});

describe("countdowns", () => {
  it("resend waits a minute from the last code", () => {
    const state = run(CODE_OUT);
    if (state.phase !== "entering") throw new Error("unreachable");
    expect(resendWaitMs(state.code, T0 + 20_000)).toBe(40_000);
    expect(resendWaitMs(state.code, T0 + 61_000)).toBe(0);
    expect(resendWaitMs(null, T0)).toBe(0);
    // A clock read just before the send never shows more than the interval.
    expect(resendWaitMs(state.code, T0 - 900)).toBe(STEP_UP_RESEND_INTERVAL_MS);
    expect(codeExpiresInMs(state.code, T0 + 60_000)).toBe(9 * 60_000);
  });

  it("formats m:ss and rounds up so 0:00 never shows while time remains", () => {
    expect(formatCountdown(9 * 60_000 + 41_000)).toBe("9:41");
    expect(formatCountdown(500)).toBe("0:01");
    expect(formatCountdown(0)).toBe("0:00");
    expect(formatCountdown(-5)).toBe("0:00");
  });
});
