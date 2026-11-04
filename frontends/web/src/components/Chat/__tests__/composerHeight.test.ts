import { describe, it, expect } from "vitest";
import {
  composerMaxHeight,
  COMPOSER_MAX_PX,
  MOBILE_COMPOSER_MIN_PX,
} from "../composerHeight";

const base = { lineHeight: 24, paddingY: 0 };

describe("composerMaxHeight", () => {
  it("keeps the desktop cap: ten lines, never past the ceiling", () => {
    expect(composerMaxHeight({ lineHeight: 16, paddingY: 0 })).toBe(160);
    expect(composerMaxHeight({ lineHeight: 24, paddingY: 0 })).toBe(COMPOSER_MAX_PX);
  });

  it("takes a share of the visible viewport on mobile", () => {
    // 20% of 600 = 120
    expect(composerMaxHeight({ ...base, viewportHeight: 600 })).toBe(120);
  });

  it("is much shorter with the keyboard open than closed", () => {
    const closed = composerMaxHeight({ ...base, viewportHeight: 844 });
    const open = composerMaxHeight({ ...base, viewportHeight: 844 - 336 });
    expect(open).toBeLessThan(closed);
    expect(open).toBe(101);
  });

  it("never exceeds the ceiling on a tall viewport", () => {
    expect(composerMaxHeight({ ...base, viewportHeight: 2000 })).toBe(COMPOSER_MAX_PX);
  });

  it("holds a usable floor on a very short viewport (landscape + keyboard)", () => {
    expect(composerMaxHeight({ ...base, viewportHeight: 200 })).toBe(MOBILE_COMPOSER_MIN_PX);
  });

  it("always leaves room for at least one line", () => {
    expect(composerMaxHeight({ lineHeight: 100, paddingY: 40, viewportHeight: 200 })).toBe(140);
  });
});
