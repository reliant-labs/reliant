/**
 * Anonymous-session sign-in nudge — schedule + persistence logic.
 *
 * Free-tier users start out as anonymous Supabase sessions. Their chats and
 * workspaces are tied to that session (see UpgradeAccount.tsx), so if they lose
 * the browser session they lose their work. This module owns the *pure* logic
 * for deciding when to nudge them to attach a real identity, with an escalating
 * (exponential-ish) backoff so we prompt more often early and then back off:
 *
 *   stage 0 →  1 hour   after first seen
 *   stage 1 → 24 hours
 *   stage 2 →  7 days
 *   stage 3 → 30 days
 *   stage 4+ → every +30 days thereafter (steady state, never spammy)
 *
 * Each "Later" dismissal advances the stage, so the next prompt only fires once
 * the session crosses the *next* threshold. State is persisted in localStorage,
 * keyed per anon user id when we have one (falls back to a stable shared key),
 * so a page reload never immediately re-prompts.
 *
 * Everything here is pure / storage-only and framework-free so it is trivially
 * unit-testable; the React glue lives in hooks/useAnonSignInNudge.ts.
 */

const HOUR_MS = 60 * 60 * 1000;
const DAY_MS = 24 * HOUR_MS;

// Escalating thresholds measured from the anon session's first-seen timestamp.
// After the last explicit threshold we keep prompting at a fixed interval.
export const NUDGE_THRESHOLDS_MS: readonly number[] = [
  1 * HOUR_MS, //  stage 0 → 1h
  24 * HOUR_MS, // stage 1 → 24h
  7 * DAY_MS, //   stage 2 → 7d
  30 * DAY_MS, //  stage 3 → 30d
];

// Steady-state cadence once every explicit threshold has been crossed.
export const NUDGE_REPEAT_INTERVAL_MS = 30 * DAY_MS;

const STORAGE_KEY_PREFIX = "reliant-anon-signin-nudge";
const FALLBACK_ID = "anon";

export interface AnonNudgeState {
  /** Epoch ms of when this anon session was first observed by the nudge. */
  firstSeen: number;
  /** Next backoff stage to fire (0-based index into the escalation schedule). */
  stage: number;
  /** Epoch ms of the last time we actually showed the prompt (0 if never). */
  lastPromptAt: number;
}

/**
 * The localStorage key for a given anon user. Keying per-id keeps two anon
 * sessions on the same browser (rare, but possible after sign-out) from
 * inheriting each other's backoff stage. When no id is available we fall back
 * to a single stable key so the nudge still works.
 */
export function nudgeStorageKey(userId: string | null | undefined): string {
  return `${STORAGE_KEY_PREFIX}:${userId || FALLBACK_ID}`;
}

/**
 * The age threshold (ms from firstSeen) at which the given stage should fire.
 * Stages past the explicit schedule advance by a fixed repeat interval so we
 * never stop nudging, but also never spam.
 */
export function thresholdForStage(stage: number): number {
  if (stage < NUDGE_THRESHOLDS_MS.length) {
    return NUDGE_THRESHOLDS_MS[stage];
  }
  const extraStages = stage - (NUDGE_THRESHOLDS_MS.length - 1);
  const lastExplicit = NUDGE_THRESHOLDS_MS[NUDGE_THRESHOLDS_MS.length - 1];
  return lastExplicit + extraStages * NUDGE_REPEAT_INTERVAL_MS;
}

/**
 * Decide whether the prompt should be shown right now given the persisted state
 * and the current time. Pure: no side effects, no storage access.
 */
export function shouldPrompt(state: AnonNudgeState, now: number): boolean {
  const age = now - state.firstSeen;
  return age >= thresholdForStage(state.stage);
}

function isAnonNudgeState(value: unknown): value is AnonNudgeState {
  if (typeof value !== "object" || value === null) return false;
  const v = value as Record<string, unknown>;
  return (
    typeof v.firstSeen === "number" &&
    typeof v.stage === "number" &&
    typeof v.lastPromptAt === "number"
  );
}

/**
 * Read the persisted state for an anon user, creating (and persisting) a fresh
 * record on first sight so `firstSeen` anchors to the moment we first observed
 * the session — NOT to page load, so a reload can't reset the clock.
 */
export function loadOrInitState(
  userId: string | null | undefined,
  now: number,
): AnonNudgeState {
  const key = nudgeStorageKey(userId);
  try {
    const raw = localStorage.getItem(key);
    if (raw) {
      const parsed = JSON.parse(raw) as unknown;
      if (isAnonNudgeState(parsed)) return parsed;
    }
  } catch {
    // Corrupt/unavailable storage → fall through and re-initialize.
  }

  const fresh: AnonNudgeState = { firstSeen: now, stage: 0, lastPromptAt: 0 };
  saveState(userId, fresh);
  return fresh;
}

export function saveState(
  userId: string | null | undefined,
  state: AnonNudgeState,
): void {
  try {
    localStorage.setItem(nudgeStorageKey(userId), JSON.stringify(state));
  } catch {
    // Best-effort: if storage is unavailable we simply lose backoff memory.
  }
}

/**
 * Record that we just showed the prompt: stamp lastPromptAt but DON'T advance
 * the stage — advancing is what "Later" (an explicit dismissal) does. This
 * keeps `shouldPrompt` from firing repeatedly while the same modal is open.
 */
export function markPrompted(
  userId: string | null | undefined,
  state: AnonNudgeState,
  now: number,
): AnonNudgeState {
  const next: AnonNudgeState = { ...state, lastPromptAt: now };
  saveState(userId, next);
  return next;
}

/**
 * Advance to the next backoff stage after a "Later" dismissal, so the next
 * prompt only fires once the session crosses the next (larger) threshold.
 */
export function advanceStage(
  userId: string | null | undefined,
  state: AnonNudgeState,
  now: number,
): AnonNudgeState {
  const next: AnonNudgeState = {
    ...state,
    stage: state.stage + 1,
    lastPromptAt: now,
  };
  saveState(userId, next);
  return next;
}
