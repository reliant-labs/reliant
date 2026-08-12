/**
 * Regression tests for the two-enum collision.
 *
 * These assert against the CONTROL-PLANE numeric values deliberately, rather
 * than importing whichever enum happens to be in scope — the bug was that the
 * wrong enum was in scope, so a test written the same way would have passed
 * while the UI lied.
 */

import { describe, expect, it } from "vitest";

import type { Daemon as CloudDaemon } from "../../../services/controlPlane/daemon";
import { cloudDaemonStatusLabel } from "../cloudDaemonStatusLabel";

/** Control-plane DaemonStatus, spelled out. See the module doc comment. */
const CP_UNSPECIFIED = 0;
const CP_PENDING = 1;
const CP_ACTIVE = 2;
const CP_SUSPENDED = 3;
const CP_DISCONNECTED = 4;
const CP_FAILED = 5;

const row = (status: number): CloudDaemon => ({ status }) as CloudDaemon;

describe("cloudDaemonStatusLabel", () => {
  it("labels a starting machine as starting, not active", () => {
    // The headline regression. PENDING is 1, which is ACTIVE in the registry
    // enum — the old fallthrough read it that way and painted a green
    // "active" dot on a machine that was still booting.
    expect(cloudDaemonStatusLabel(row(CP_PENDING), false)).toBe("starting");
  });

  it("labels a disconnected machine rather than dropping to unknown", () => {
    // DISCONNECTED is 4, which has no registry counterpart, so the old code
    // indexed past the end of the enum and rendered "unknown".
    expect(cloudDaemonStatusLabel(row(CP_DISCONNECTED), false)).toBe("disconnected");
  });

  it("maps the remaining control-plane statuses", () => {
    expect(cloudDaemonStatusLabel(row(CP_ACTIVE), false)).toBe("active");
    expect(cloudDaemonStatusLabel(row(CP_SUSPENDED), false)).toBe("suspended");
    expect(cloudDaemonStatusLabel(row(CP_FAILED), false)).toBe("failed");
  });

  it("reports unknown for an unset status", () => {
    expect(cloudDaemonStatusLabel(row(CP_UNSPECIFIED), false)).toBe("unknown");
  });

  it("shows the in-flight resume regardless of stored status", () => {
    // The stored row still says SUSPENDED while the resume is in flight; the
    // optimistic label is what tells the user their click registered.
    expect(cloudDaemonStatusLabel(row(CP_SUSPENDED), true)).toBe("resuming");
  });

  it("never returns a label the status-dot map has no colour for", () => {
    // A label with no entry falls back to grey, which silently reads as
    // "suspended" — so the label set and the colour map have to stay in sync.
    const known = new Set([
      "active",
      "starting",
      "resuming",
      "suspended",
      "failed",
      "disconnected",
      "unknown",
    ]);
    for (const status of [
      CP_UNSPECIFIED,
      CP_PENDING,
      CP_ACTIVE,
      CP_SUSPENDED,
      CP_DISCONNECTED,
      CP_FAILED,
    ]) {
      expect(known).toContain(cloudDaemonStatusLabel(row(status), false));
    }
  });
});
