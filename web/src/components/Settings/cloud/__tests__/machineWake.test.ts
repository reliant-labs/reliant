import { describe, expect, it } from "vitest";

import { WAKE_MAX_MS, WAKE_SUSPENDED_GRACE_MS, presentMachineStatus } from "../machineWake";

const T0 = 1_000_000;
const PREPARING = "Preparing your workspace image. This is the last slow step.";

describe("presentMachineStatus", () => {
  it("says Waking up… — not Pending — for a machine the user resumed", () => {
    expect(presentMachineStatus("pending", PREPARING, T0, T0 + 5_000)).toEqual({
      label: "Waking up…",
      progress: PREPARING,
      wakeFinished: false,
    });
  });

  it("covers the moment before the list has refetched the resumed row", () => {
    expect(presentMachineStatus("suspended", "Sleeping after 30 idle minutes", T0, T0 + 1_000).label).toBe(
      "Waking up…",
    );
  });

  it("stops claiming a wake once the machine is up, failed, or never left suspended", () => {
    expect(presentMachineStatus("active", undefined, T0, T0 + 30_000)).toEqual({ wakeFinished: true });
    expect(presentMachineStatus("failed", "No capacity", T0, T0 + 30_000)).toEqual({ wakeFinished: true });
    expect(presentMachineStatus("suspended", undefined, T0, T0 + WAKE_SUSPENDED_GRACE_MS + 1).wakeFinished).toBe(true);
    expect(presentMachineStatus("pending", undefined, T0, T0 + WAKE_MAX_MS + 1).wakeFinished).toBe(true);
  });

  it("shows the control plane's start-up progress for a starting machine nobody woke here", () => {
    expect(presentMachineStatus("pending", PREPARING, undefined)).toEqual({
      progress: PREPARING,
      wakeFinished: false,
    });
  });

  it("leaves other statuses alone", () => {
    expect(presentMachineStatus("suspended", "Sleeping after 30 idle minutes", undefined)).toEqual({
      wakeFinished: false,
    });
  });
});
