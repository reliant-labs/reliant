// Copyright (c) 2025 Reliant Labs

/**
 * The terminal replay's engine, apart from React and timers so it can be
 * tested step by step: where playback is (which step, how many of its output
 * events are on screen), what moving forward does, and how long to wait before
 * the next move.
 *
 * A command step shows its prompt line first, then its events one at a time at
 * their recorded (already shortened) times; after the last one it waits out the
 * rest of the step's duration, then the next step starts. An edit step (the
 * proto you write) shows whole, and holds for EDIT_HOLD seconds.
 */

import type { CastStep, ScaffoldCast } from "./fixtures";

export const SPEEDS = [1, 2, 4] as const;
export type Speed = (typeof SPEEDS)[number];

/** How long an edit step stays on screen before the next step, at 1×. */
export const EDIT_HOLD = 1.5;

export interface ReplayState {
  /** The step on screen last; every step before it shows in full. */
  step: number;
  /** How many of that step's events show (an edit step: 0 or 1). */
  shown: number;
  playing: boolean;
  speed: Speed;
}

export type ReplayAction =
  | { type: "play" }
  | { type: "pause" }
  | { type: "speed"; speed: Speed }
  /** One timer's worth of playback: the next event, or the next step. */
  | { type: "tick" }
  /** Finish the step on screen; if it is finished, show the whole of the next one. Pauses. */
  | { type: "step" }
  | { type: "restart" };

/** The speed it starts at: brisk, about 15 s for the whole run. */
export const DEFAULT_SPEED: Speed = 4;

export const initialReplay = (speed: Speed = DEFAULT_SPEED): ReplayState => ({ step: 0, shown: 0, playing: false, speed });

/** The end of the run, every step on screen, paused: what a reader who asked for reduced motion sees first. */
export function finalReplay(cast: ScaffoldCast, speed: Speed = DEFAULT_SPEED): ReplayState {
  const last = cast.steps.length - 1;
  return { step: last, shown: eventCount(cast.steps[last]), playing: false, speed };
}

/** How many events a step has, for `shown`: an edit step counts as one. */
export function eventCount(step: CastStep): number {
  return step.kind === "command" ? step.events.length : 1;
}

export function isComplete(cast: ScaffoldCast, state: ReplayState): boolean {
  const last = cast.steps.length - 1;
  return state.step === last && state.shown >= eventCount(cast.steps[last]);
}

export function replayReducer(cast: ScaffoldCast, state: ReplayState, action: ReplayAction): ReplayState {
  const steps = cast.steps;
  switch (action.type) {
    case "play":
      // Play from the end starts over.
      return isComplete(cast, state) ? { ...initialReplay(state.speed), playing: true } : { ...state, playing: true };
    case "pause":
      return { ...state, playing: false };
    case "speed":
      return { ...state, speed: action.speed };
    case "restart":
      return initialReplay(state.speed);
    case "tick": {
      if (state.shown < eventCount(steps[state.step])) return { ...state, shown: state.shown + 1 };
      if (state.step < steps.length - 1) return { ...state, step: state.step + 1, shown: 0 };
      return { ...state, playing: false };
    }
    case "step": {
      if (state.shown < eventCount(steps[state.step])) return { ...state, shown: eventCount(steps[state.step]), playing: false };
      if (state.step < steps.length - 1) return { ...state, step: state.step + 1, shown: eventCount(steps[state.step + 1]), playing: false };
      return { ...state, playing: false };
    }
  }
}

/** Seconds (at this speed) until the next tick should fire, or null when there is nothing left to play. */
export function delayToNext(cast: ScaffoldCast, state: ReplayState): number | null {
  if (isComplete(cast, state)) return null;
  const step = cast.steps[state.step];
  let seconds: number;
  if (step.kind === "edit") {
    // Show it at once; then hold it before the next step.
    seconds = state.shown === 0 ? 0.3 : EDIT_HOLD;
  } else if (state.shown < step.events.length) {
    const previous = state.shown === 0 ? 0 : step.events[state.shown - 1][0];
    seconds = step.events[state.shown][0] - previous;
  } else {
    const last = step.events.length ? step.events[step.events.length - 1][0] : 0;
    seconds = step.duration - last;
  }
  return Math.max(0, seconds) / state.speed;
}

/** A step's output as a terminal shows it: a carriage return overwrites the line it is on. */
export function terminalText(text: string): string {
  return text
    .replace(/\r\n/g, "\n")
    .split("\n")
    .map((line) => {
      const cut = line.lastIndexOf("\r");
      return cut < 0 ? line : line.slice(cut + 1);
    })
    .join("\n");
}

/** What a command step has printed once `shown` of its events are on screen. */
export function outputOf(step: CastStep, shown: number): string {
  if (step.kind !== "command") return "";
  return terminalText(
    step.events
      .slice(0, shown)
      .map((event) => event[1])
      .join("")
  );
}

/** "38s", "2m 20s": a step's real running time. */
export function formatSeconds(seconds: number): string {
  const whole = Math.round(seconds);
  if (whole < 60) return `${whole}s`;
  const minutes = Math.floor(whole / 60);
  const rest = whole % 60;
  return rest ? `${minutes}m ${rest}s` : `${minutes}m`;
}
