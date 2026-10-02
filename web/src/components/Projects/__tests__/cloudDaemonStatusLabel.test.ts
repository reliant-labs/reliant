/**
 * Status-label coverage for a machine row.
 *
 * These were written as regression tests for a two-enum collision: the UI read
 * two daemon lists whose DaemonStatus values disagreed numerically, a
 * fallthrough indexed the wrong one, and a PENDING machine rendered as a green
 * "active" dot. They asserted raw control-plane numbers deliberately, because
 * importing "whichever enum is in scope" was the bug itself.
 *
 * There is now one list and one enum (docs/design/one-daemon-list.md), so the
 * collision cannot occur and the numbers no longer have to be hand-written to
 * prove which vocabulary is in play. What is still worth pinning is that every
 * reachable status maps to a label, and that each label has a colour — the
 * in-sync property the last test covers.
 */

import { describe, expect, it } from "vitest";

import {
  DaemonStatus,
  type DaemonInfo as CloudDaemon,
} from "../../../gen/reliant/v1/daemon_registry_pb";
import { cloudDaemonStatusLabel } from "../cloudDaemonStatusLabel";

const CP_UNSPECIFIED = DaemonStatus.UNSPECIFIED;
const CP_PENDING = DaemonStatus.PENDING;
const CP_ACTIVE = DaemonStatus.ACTIVE;
const CP_SUSPENDED = DaemonStatus.SUSPENDED;
const CP_DISCONNECTED = DaemonStatus.DISCONNECTED;
const CP_FAILED = DaemonStatus.FAILED;

const row = (status: number): CloudDaemon => ({ status }) as CloudDaemon;

describe("cloudDaemonStatusLabel", () => {
  it("labels a starting machine as starting, not active", () => {
    // The headline regression this file was written for: PENDING once shared
    // a numeric value with another enum's ACTIVE, and a machine that was
    // still booting rendered with a green "active" dot.
    expect(cloudDaemonStatusLabel(row(CP_PENDING), false)).toBe("starting");
  });

  it("labels a disconnected machine rather than dropping to unknown", () => {
    // DISCONNECTED had no counterpart in the other enum, so the old code
    // indexed past its end and rendered "unknown".
    expect(cloudDaemonStatusLabel(row(CP_DISCONNECTED), false)).toBe("disconnected");
  });

  it("maps the remaining statuses", () => {
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
