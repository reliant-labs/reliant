import { describe, expect, it } from "vitest";
import { isEditableElement, measureVisualViewport } from "../responsiveGeometry";

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

  // interactive-widget=resizes-content: Chromium shrinks the layout viewport
  // with the visual one, so only the drop from the resting height shows the
  // keyboard — and only while a text field has focus.
  it.each([
    [508, 508, 844, true, true], // resized keyboard over a focused composer
    [508, 508, 844, false, false], // same drop, nothing editable: a window resize
    [764, 764, 844, true, false], // URL bar shown while typing is not a keyboard
    [190, 190, 390, true, true], // landscape keyboard against the landscape baseline
    [390, 390, 390, true, false], // just rotated: this width's own baseline
  ])("resized: layout %s, visual %s, resting %s, focused %s => occluded %s", (layout, height, resting, focused, keyboardOpen) => {
    expect(
      measureVisualViewport({ height, scale: 1, offsetTop: 0 }, layout, {
        restingLayoutHeight: resting,
        editableFocused: focused,
      }).keyboardOpen,
    ).toBe(keyboardOpen);
  });

  it("does not read pinch zoom as a resized keyboard", () => {
    expect(
      measureVisualViewport({ height: 400, scale: 2, offsetTop: 0 }, 400, {
        restingLayoutHeight: 844,
        editableFocused: true,
      }).keyboardOpen,
    ).toBe(false);
  });
});

describe("isEditableElement", () => {
  it("counts only fields that raise a keyboard", () => {
    const textarea = document.createElement("textarea");
    const text = Object.assign(document.createElement("input"), { type: "text" });
    const checkbox = Object.assign(document.createElement("input"), { type: "checkbox" });
    const readOnly = Object.assign(document.createElement("input"), { readOnly: true });
    expect(isEditableElement(textarea)).toBe(true);
    expect(isEditableElement(text)).toBe(true);
    expect(isEditableElement(checkbox)).toBe(false);
    expect(isEditableElement(readOnly)).toBe(false);
    expect(isEditableElement(document.createElement("button"))).toBe(false);
    expect(isEditableElement(null)).toBe(false);
  });
});
