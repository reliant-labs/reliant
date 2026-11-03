import { describe, expect, it } from "vitest";
import { measureVisualViewport } from "../responsiveGeometry";

describe("visual viewport geometry", () => {
  it.each([
    [844, 844, 1, false], // unobscured phone
    [844, 764, 1, false], // browser chrome
    [844, 694, 1, false], // threshold is exclusive
    [844, 508, 1, true], // docked keyboard
    [390, 190, 1, true], // short landscape keyboard
    [390, 390, 1, false], // rotated, no keyboard
    [1024, 1024, 1, false], // tablet
    [844, 422, 2, false], // pinch zoom in
    [844, 600, 0.8, false], // zoom out is not keyboard evidence either
    [508, 508, 1, false], // resize-content: layout already fits
  ])("layout %s, visual %s, scale %s => occluded %s", (layout, height, scale, keyboardOpen) => {
    expect(measureVisualViewport({ height, scale, offsetTop: 0 }, layout).keyboardOpen).toBe(keyboardOpen);
  });

  it("rounds visual geometry consistently for shell placement", () => {
    expect(measureVisualViewport({ height: 508.4, offsetTop: 119.8, scale: 1 }, 844))
      .toEqual({ height: 508, offsetTop: 120, keyboardOpen: true });
  });
});
