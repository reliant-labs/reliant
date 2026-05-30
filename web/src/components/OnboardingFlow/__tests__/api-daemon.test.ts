import { describe, expect, it } from "vitest";
import type { Daemon } from "@/services/controlPlane/daemon";
import {
  DAEMON_STATUS_ACTIVE,
  DAEMON_STATUS_PENDING,
  getDaemonStatusMessage,
  hasActiveDaemon,
} from "@/services/controlPlane/daemon";
import { DaemonStatus } from "@/gen/controlplane/v1/public/shared_pb";

// `Daemon` is the proto-generated message type. Tests build partial daemons
// by casting through `unknown` so we only have to spell out the fields under
// test instead of every required string field on the message shape.
function makeDaemon(partial: Partial<Daemon>): Daemon {
  return partial as unknown as Daemon;
}

// ── hasActiveDaemon ──────────────────────────────────────────

describe("hasActiveDaemon", () => {
  it("returns false for empty list", () => {
    expect(hasActiveDaemon([])).toBe(false);
  });

  it("returns true for control-plane ACTIVE", () => {
    expect(
      hasActiveDaemon([makeDaemon({ status: DaemonStatus.ACTIVE })]),
    ).toBe(true);
  });

  it("returns false for PENDING", () => {
    expect(
      hasActiveDaemon([makeDaemon({ status: DAEMON_STATUS_PENDING })]),
    ).toBe(false);
  });

  it("returns false for UNSPECIFIED", () => {
    expect(
      hasActiveDaemon([makeDaemon({ status: DaemonStatus.UNSPECIFIED })]),
    ).toBe(false);
  });

  it("returns true if any daemon is active", () => {
    expect(
      hasActiveDaemon([
        makeDaemon({ status: DaemonStatus.PENDING }),
        makeDaemon({ status: DAEMON_STATUS_ACTIVE }),
      ]),
    ).toBe(true);
  });
});

// ── getDaemonStatusMessage ───────────────────────────────────

describe("getDaemonStatusMessage", () => {
  it("returns empty string for undefined", () => {
    expect(getDaemonStatusMessage(undefined)).toBe("");
  });

  it("returns the lastStatusMessage when present", () => {
    expect(
      getDaemonStatusMessage(
        makeDaemon({ lastStatusMessage: "image pull failed" }),
      ),
    ).toBe("image pull failed");
  });

  it("returns empty string when lastStatusMessage is empty", () => {
    expect(getDaemonStatusMessage(makeDaemon({}))).toBe("");
  });
});
