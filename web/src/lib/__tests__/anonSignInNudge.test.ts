import { beforeEach, describe, expect, it } from "vitest";
import {
  advanceStage,
  loadOrInitState,
  markPrompted,
  NUDGE_REPEAT_INTERVAL_MS,
  nudgeStorageKey,
  shouldPrompt,
  thresholdForStage,
  type AnonNudgeState,
} from "../anonSignInNudge";

const HOUR = 60 * 60 * 1000;
const DAY = 24 * HOUR;

describe("anonSignInNudge schedule", () => {
  // jsdom's localStorage stub in this project is incomplete (no .clear()), so
  // install a minimal in-memory implementation for these storage-backed tests.
  beforeEach(() => {
    const store = new Map<string, string>();
    const mock: Storage = {
      get length() {
        return store.size;
      },
      clear: () => store.clear(),
      getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
      setItem: (k: string, v: string) => {
        store.set(k, String(v));
      },
      removeItem: (k: string) => {
        store.delete(k);
      },
      key: (i: number) => Array.from(store.keys())[i] ?? null,
    };
    Object.defineProperty(globalThis, "localStorage", {
      value: mock,
      configurable: true,
      writable: true,
    });
  });

  it("keys storage per anon user id, with a stable fallback", () => {
    expect(nudgeStorageKey("user-abc")).toBe(
      "reliant-anon-signin-nudge:user-abc",
    );
    expect(nudgeStorageKey(null)).toBe("reliant-anon-signin-nudge:anon");
    expect(nudgeStorageKey(undefined)).toBe("reliant-anon-signin-nudge:anon");
  });

  it("escalates thresholds 1h → 24h → 7d → 30d then repeats every 30d", () => {
    expect(thresholdForStage(0)).toBe(1 * HOUR);
    expect(thresholdForStage(1)).toBe(24 * HOUR);
    expect(thresholdForStage(2)).toBe(7 * DAY);
    expect(thresholdForStage(3)).toBe(30 * DAY);
    // Steady state: each further stage adds one repeat interval.
    expect(thresholdForStage(4)).toBe(30 * DAY + NUDGE_REPEAT_INTERVAL_MS);
    expect(thresholdForStage(5)).toBe(30 * DAY + 2 * NUDGE_REPEAT_INTERVAL_MS);
  });

  it("shouldPrompt fires only once the current stage threshold is crossed", () => {
    const state: AnonNudgeState = { firstSeen: 0, stage: 0, lastPromptAt: 0 };
    expect(shouldPrompt(state, HOUR - 1)).toBe(false);
    expect(shouldPrompt(state, HOUR)).toBe(true);

    const stage1: AnonNudgeState = { ...state, stage: 1 };
    // Past the 1h mark but not yet 24h → stage 1 does not fire.
    expect(shouldPrompt(stage1, 2 * HOUR)).toBe(false);
    expect(shouldPrompt(stage1, 24 * HOUR)).toBe(true);
  });

  it("loadOrInitState anchors firstSeen once and a reload does not reset it", () => {
    const first = loadOrInitState("u1", 1000);
    expect(first).toEqual({ firstSeen: 1000, stage: 0, lastPromptAt: 0 });

    // Simulate a reload at a later time — firstSeen must be preserved.
    const reloaded = loadOrInitState("u1", 9999);
    expect(reloaded.firstSeen).toBe(1000);
  });

  it("markPrompted stamps time without advancing the stage", () => {
    const state = loadOrInitState("u1", 0);
    const after = markPrompted("u1", state, 5000);
    expect(after.stage).toBe(0);
    expect(after.lastPromptAt).toBe(5000);
    // Persisted.
    expect(loadOrInitState("u1", 6000).lastPromptAt).toBe(5000);
  });

  it("advanceStage moves to the next stage and persists it (Later behavior)", () => {
    const state = loadOrInitState("u1", 0);
    const advanced = advanceStage("u1", state, HOUR);
    expect(advanced.stage).toBe(1);
    expect(loadOrInitState("u1", HOUR).stage).toBe(1);
  });

  it("full lifecycle: prompt at each escalating threshold, backing off via Later", () => {
    let state = loadOrInitState("u1", 0);

    // Nothing before 1h.
    expect(shouldPrompt(state, 59 * 60 * 1000)).toBe(false);

    // 1h: prompt fires, user clicks Later → stage 1.
    expect(shouldPrompt(state, 1 * HOUR)).toBe(true);
    state = advanceStage("u1", state, 1 * HOUR);

    // Doesn't re-fire until 24h.
    expect(shouldPrompt(state, 12 * HOUR)).toBe(false);
    expect(shouldPrompt(state, 24 * HOUR)).toBe(true);
    state = advanceStage("u1", state, 24 * HOUR);

    // 7d.
    expect(shouldPrompt(state, 3 * DAY)).toBe(false);
    expect(shouldPrompt(state, 7 * DAY)).toBe(true);
    state = advanceStage("u1", state, 7 * DAY);

    // 30d.
    expect(shouldPrompt(state, 20 * DAY)).toBe(false);
    expect(shouldPrompt(state, 30 * DAY)).toBe(true);
    state = advanceStage("u1", state, 30 * DAY);

    // Steady state: next prompt at +30d, never sooner.
    expect(shouldPrompt(state, 45 * DAY)).toBe(false);
    expect(shouldPrompt(state, 60 * DAY)).toBe(true);
  });

  it("keeps separate backoff state for different anon ids", () => {
    const a = advanceStage("a", loadOrInitState("a", 0), HOUR);
    const b = loadOrInitState("b", 0);
    expect(a.stage).toBe(1);
    expect(b.stage).toBe(0);
  });
});
