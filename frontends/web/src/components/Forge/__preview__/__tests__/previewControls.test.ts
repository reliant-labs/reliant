// Copyright (c) 2026 Reliant Labs

/** The preview harness's pickers: shown in local dev, and anywhere else only on `?devtools=1`. */

import "@/components/Forge/Ml/__tests__/jsdomShims";
import { describe, expect, it } from "vitest";

import { previewControlsShown } from "../ConsoleFixturePreview";

describe("the preview pickers", () => {
  it("show in local dev unless ?devtools=0", () => {
    expect(previewControlsShown("", true)).toBe(true);
    expect(previewControlsShown("?devtools=0", true)).toBe(false);
  });
  it("hide from a shared link outside dev, unless ?devtools=1", () => {
    expect(previewControlsShown("?case=entitled&view=runs", false)).toBe(false);
    expect(previewControlsShown("?case=entitled&devtools=1", false)).toBe(true);
  });
});
