// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { shopPack } from "..";
import { DEMO_RELEASES, demoNow } from "../shopStory";
import { shopActivity } from "../model/activity";

const FIXED_NOW = Date.parse("2026-11-18T15:30:00Z");

describe("the shop pack's clock", () => {
  // One test: the clock is module state, and this file's first read is the point.
  it("is unread after importing the pack, so shopPack({ now }) pins it, and refuses to move once read", () => {
    shopPack({ now: FIXED_NOW });
    expect(demoNow()).toBe(FIXED_NOW);
    // The story is told at the pinned instant.
    expect(Math.max(...shopActivity(demoNow()).map((e) => e.atMs))).toBeLessThanOrEqual(FIXED_NOW);
    expect(DEMO_RELEASES.length).toBeGreaterThan(0);
    expect(() => shopPack({ now: FIXED_NOW })).not.toThrow();
    expect(() => shopPack({ now: FIXED_NOW + 1 })).toThrow(/already read/);
    expect(() => shopPack()).not.toThrow();
  });
});
