/**
 * A tour restart begun for user A must not touch user B's checklist or
 * settings when B is adopted while A's restart is still waiting on the
 * checklist store's import (useTourStore.reset bumps the generation).
 */
import { describe, expect, it, vi } from "vitest";

const held = vi.hoisted(() => {
  let open!: () => void;
  let entered!: () => void;
  const gate = new Promise<void>((resolve) => (open = resolve));
  const reached = new Promise<void>((resolve) => (entered = resolve));
  return { gate, open: () => open(), reached, entered: () => entered() };
});

const checklist = vi.hoisted(() => ({
  setState: vi.fn(),
  revive: vi.fn(async () => undefined),
}));

const persistence = vi.hoisted(() => ({
  upsertStringSetting: vi.fn(async () => undefined),
  deleteSettingIfExists: vi.fn(async () => undefined),
}));

vi.mock("../../lib/settingsPersistence", () => ({
  safeGetSetting: vi.fn(async () => null),
  upsertStringSetting: persistence.upsertStringSetting,
  deleteSettingIfExists: persistence.deleteSettingIfExists,
}));

vi.mock("../../lib/analytics", () => ({ trackEvent: vi.fn() }));

vi.mock("../../api/fileSystem", () => ({ getFileTree: vi.fn(async () => []) }));

// The first import of the checklist store is held until the test opens it.
vi.mock("../onboardingChecklistStore", async () => {
  held.entered();
  await held.gate;
  return {
    useOnboardingChecklistStore: {
      getState: () => ({ revive: checklist.revive }),
      setState: checklist.setState,
    },
  };
});

import { useTourStore } from "../tourStore";

describe("tourStore — a restart across an identity change", () => {
  it("leaves the next user's checklist and settings alone", async () => {
    const restart = useTourStore.getState().resetTourProgress();
    await held.reached;

    // User B is adopted while A's restart waits on the import.
    useTourStore.getState().reset();
    persistence.upsertStringSetting.mockClear();
    persistence.deleteSettingIfExists.mockClear();
    const removeItem = vi.spyOn(Storage.prototype, "removeItem");

    held.open();
    await restart;

    expect(checklist.setState).not.toHaveBeenCalled();
    expect(checklist.revive).not.toHaveBeenCalled();
    expect(persistence.upsertStringSetting).not.toHaveBeenCalled();
    expect(persistence.deleteSettingIfExists).not.toHaveBeenCalled();
    expect(removeItem).not.toHaveBeenCalledWith(
      "reliant.checklist.completedItems",
    );
  });
});
