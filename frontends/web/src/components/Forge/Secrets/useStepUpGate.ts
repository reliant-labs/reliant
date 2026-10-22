// Copyright (c) 2025 Reliant Labs

/**
 * The step-up gate: run a write, and if a protected environment refuses it
 * for want of a step-up, hold it while the user proves who they are, then
 * run it again.
 *
 * The transitions live in services/forge/stepUp.ts (pure, tested there);
 * this hook is the wiring — the two RPCs, the held write, and a clock.
 *
 * ── THE HELD WRITE ──────────────────────────────────────────────────────────
 *
 * `run(write)` returns a promise that settles with the write's EVENTUAL
 * outcome: the first attempt's, when it was not refused; the retry's, when
 * the user confirmed; a StepUpCancelledError when they closed the dialog. So
 * a caller handles one promise and never needs to know a dialog happened —
 * the set-secret form's own CAS and error handling apply to the retry exactly
 * as they did to the first attempt.
 *
 * The write is a closure, so a set-secret value is held for as long as the
 * dialog is open. It is already held that long by the form that typed it, and
 * the ref is cleared the moment the dialog settles either way.
 */

import { useCallback, useEffect, useReducer, useRef, useState } from "react";

import {
  beginStepUp,
  completeStepUp,
  isStepUpRequired,
  sendFailureOf,
  verifyFailureOf,
} from "@/services/forge/secretStore";
import {
  STEP_UP_CLOSED,
  canSend,
  canSubmit,
  stepUpReducer,
  type StepUpState,
} from "@/services/forge/stepUp";

/** The rejection of a held write whose dialog the user closed. Not a failure to report. */
export class StepUpCancelledError extends Error {
  constructor() {
    super("The change was not confirmed, so it was not saved.");
    this.name = "StepUpCancelledError";
  }
}

export function isStepUpCancelled(err: unknown): boolean {
  return err instanceof StepUpCancelledError;
}

/** A clock that ticks once a second while `active`, for countdowns. */
export function useNow(active: boolean, clock: () => number = Date.now): number {
  const [now, setNow] = useState(clock);
  useEffect(() => {
    if (!active) return;
    setNow(clock());
    const id = window.setInterval(() => setNow(clock()), 1000);
    return () => window.clearInterval(id);
  }, [active, clock]);
  return now;
}

export interface StepUpGate {
  state: StepUpState;
  /** True while the dialog should be on screen. */
  isOpen: boolean;
  /** True when a refused write is waiting on this dialog. */
  hasPendingWrite: boolean;
  /** The grant this dialog obtained (epoch ms), or null. The listing's own value may be later. */
  grantUntil: number | null;
  /** The ticking clock the dialog's countdowns read. */
  now: number;
  run<T>(write: () => Promise<T>): Promise<T>;
  /** Step up ahead of any write — for a run of changes, or for `forge secret set`. */
  unlock(): void;
  send(): void;
  submit(code: string): void;
  cancel(): void;
}

interface HeldWrite {
  write: () => Promise<unknown>;
  resolve: (value: unknown) => void;
  reject: (reason: unknown) => void;
}

export function useStepUpGate(
  environmentId: string | null,
  options: { onGranted?: () => void; clock?: () => number } = {}
): StepUpGate {
  const { onGranted, clock = Date.now } = options;
  const [state, dispatch] = useReducer(stepUpReducer, STEP_UP_CLOSED);
  const [grantUntil, setGrantUntil] = useState<number | null>(null);
  const held = useRef<HeldWrite | null>(null);
  // Bumped on every close, so an RPC answering after the dialog closed (or
  // after a newer request) is ignored rather than resurrecting it.
  const generation = useRef(0);
  const stateRef = useRef(state);
  useEffect(() => {
    stateRef.current = state;
  }, [state]);
  const [hasPendingWrite, setHasPendingWrite] = useState(false);

  const isOpen = state.phase !== "closed";
  const now = useNow(isOpen, clock);

  // The code's expiry is a transition too, driven by the clock.
  useEffect(() => {
    if (state.phase === "entering") dispatch({ type: "tick", at: now });
  }, [now, state.phase]);

  // Settle a held write when the component goes away, so no caller awaits forever.
  useEffect(
    () => () => {
      held.current?.reject(new StepUpCancelledError());
      held.current = null;
    },
    []
  );

  const run = useCallback(
    async <T,>(write: () => Promise<T>): Promise<T> => {
      try {
        return await write();
      } catch (err) {
        if (!isStepUpRequired(err) || !environmentId) throw err;
        return new Promise<T>((resolve, reject) => {
          // A second refused write supersedes the first: the dialog confirms
          // one change at a time, and the newest is the one on screen.
          held.current?.reject(new StepUpCancelledError());
          held.current = { write, resolve: resolve as (value: unknown) => void, reject };
          setHasPendingWrite(true);
          dispatch({ type: "open" });
        });
      }
    },
    [environmentId]
  );

  const unlock = useCallback(() => {
    if (environmentId) dispatch({ type: "open" });
  }, [environmentId]);

  const send = useCallback(() => {
    if (!environmentId || !canSend(stateRef.current, clock())) return;
    const gen = generation.current;
    dispatch({ type: "send" });
    beginStepUp(environmentId).then(
      (challenge) => {
        if (gen !== generation.current) return;
        const expiresAt = challenge.expiresAt ? Date.parse(challenge.expiresAt) : NaN;
        dispatch({
          type: "sent",
          sentTo: challenge.sentTo,
          expiresAt: Number.isFinite(expiresAt) ? expiresAt : null,
          at: clock(),
        });
      },
      (err) => {
        if (gen !== generation.current) return;
        dispatch({ type: "send-failed", failure: sendFailureOf(err), at: clock() });
      }
    );
  }, [environmentId, clock]);

  const submit = useCallback(
    (code: string) => {
      if (!environmentId || !canSubmit(stateRef.current, code)) return;
      const gen = generation.current;
      dispatch({ type: "submit" });
      completeStepUp(environmentId, code).then(
        (grant) => {
          if (gen !== generation.current) return;
          const until = grant.validUntil ? Date.parse(grant.validUntil) : NaN;
          setGrantUntil(Number.isFinite(until) ? until : null);
          onGranted?.();
          const pending = held.current;
          held.current = null;
          dispatch({ type: "verified", validUntil: Number.isFinite(until) ? until : null, retry: !!pending });
          if (pending) {
            // The retry's outcome belongs to its caller, either way.
            pending
              .write()
              .then(pending.resolve, pending.reject)
              .finally(() => {
                setHasPendingWrite(false);
                dispatch({ type: "retried" });
              });
          }
        },
        (err) => {
          if (gen !== generation.current) return;
          dispatch({ type: "verify-failed", failure: verifyFailureOf(err), at: clock() });
        }
      );
    },
    [environmentId, clock, onGranted]
  );

  const cancel = useCallback(() => {
    // A retry already in flight is not cancellable: it is the user's write,
    // confirmed, and its own outcome will settle the caller.
    if (stateRef.current.phase === "retrying") return;
    generation.current += 1;
    held.current?.reject(new StepUpCancelledError());
    held.current = null;
    setHasPendingWrite(false);
    dispatch({ type: "close" });
  }, []);

  return {
    state,
    isOpen,
    hasPendingWrite,
    grantUntil,
    now,
    run,
    unlock,
    send,
    submit,
    cancel,
  };
}
