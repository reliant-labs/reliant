import { afterEach, expect, it } from "vitest";
import { isForgeUIEnabled, setForgeUIEnabled, clearForgeUIPreference } from "./forgeFeature";
import { resetShellForTests } from "./shell";

afterEach(() => {
  clearForgeUIPreference();
  resetShellForTests();
});

it("keeps the forge console reachable despite the Reliant opt-out", () => {
  setForgeUIEnabled(false);
  resetShellForTests("forge");
  expect(isForgeUIEnabled()).toBe(true);
  resetShellForTests("reliant");
  expect(isForgeUIEnabled()).toBe(false);
});
