import { describe, expect, it, vi } from "vitest";

import { quotaOf } from "../quota";
import { ObsQuotaStore, SOFT_POLL_FACTOR } from "../quotaStore";

const NOW = Date.UTC(2026, 9, 26, 17, 51, 27);
const RESET = Date.UTC(2026, 9, 26, 18, 0, 0);
const exhaustion = (over: Partial<NonNullable<ReturnType<typeof quotaOf>>> = {}) => ({
  limit: "queries" as const,
  lane: "interactive",
  resetsAtMs: RESET,
  soft: false,
  ...over,
});

describe("ObsQuotaStore", () => {
  it("polls at the base rate while ok, never when exhausted, and slower when tight", () => {
    const store = new ObsQuotaStore(() => NOW);
    expect(store.pollMs(60_000)).toBe(60_000);
    expect(store.pollMs(false)).toBe(false);

    store.report(exhaustion({ soft: true }));
    expect(store.getState().kind).toBe("soft");
    expect(store.pollMs(60_000)).toBe(60_000 * SOFT_POLL_FACTOR);

    store.report(exhaustion());
    expect(store.getState().kind).toBe("exhausted");
    expect(store.pollMs(60_000)).toBe(false);
  });

  it("does not let a soft report downgrade a gone quota", () => {
    const store = new ObsQuotaStore(() => NOW);
    store.report(exhaustion());
    store.report(exhaustion({ soft: true }));
    expect(store.getState().kind).toBe("exhausted");
  });

  it("clamps a reset further out than one quota interval (a bad clock or header)", () => {
    const store = new ObsQuotaStore(() => NOW);
    store.report(exhaustion({ resetsAtMs: NOW + 24 * 3_600_000 }));
    const state = store.getState();
    expect(state.kind === "exhausted" && state.resetsAtMs).toBeLessThanOrEqual(NOW + 65 * 60_000);
  });

  it("notifies subscribers once per change, and not for a repeat of what it already knows", () => {
    const store = new ObsQuotaStore(() => NOW);
    const listener = vi.fn();
    store.subscribe(listener);
    store.report(exhaustion());
    store.report(exhaustion());
    expect(listener).toHaveBeenCalledTimes(1);
    store.clear();
    store.clear();
    expect(listener).toHaveBeenCalledTimes(2);
    expect(store.getState().kind).toBe("ok");
  });

  it("answers a guarded read with the quota error, without running it, until the reset", async () => {
    let now = NOW;
    const store = new ObsQuotaStore(() => now);
    const run = vi.fn().mockResolvedValue("fresh");
    store.report(exhaustion());

    const refused = await store.guard(run).catch((e: unknown) => e);
    expect(run).not.toHaveBeenCalled();
    expect(quotaOf(refused, now)).toMatchObject({ lane: "interactive", resetsAtMs: RESET });

    // Past the reset a person's read goes through, and its success ends the pause.
    now = RESET + 1;
    await expect(store.guard(run)).resolves.toBe("fresh");
    expect(store.getState().kind).toBe("ok");
  });

  it("runs a guarded read normally while ok or soft", async () => {
    const store = new ObsQuotaStore(() => NOW);
    store.report(exhaustion({ soft: true }));
    await expect(store.guard(() => Promise.resolve(7))).resolves.toBe(7);
  });
});
