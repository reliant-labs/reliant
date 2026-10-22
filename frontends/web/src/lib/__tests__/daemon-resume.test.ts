// Copyright (c) 2025 Reliant Labs

/**
 * The copy for a refused resume, shared by ResumeDaemonPill, the Inbox's
 * "Wake <machine>" and MachineStatus's "Wake it", and the pending-error
 * classifier MachineStatus and the Inbox lean on.
 */

import { describe, expect, it } from "vitest";
import { ConnectError, Code } from "@connectrpc/connect";

import { formatResumeError, isQuotaResumeError } from "../daemon-resume";
import { isDaemonPendingError } from "../daemon-errors";

describe("formatResumeError", () => {
  it("names the spent entitlement for a quota refusal, in either wire form", () => {
    const copy =
      "You've used the compute included with your account. Upgrade or redeem a coupon to resume this environment.";
    expect(formatResumeError("[resource_exhausted] nope")).toBe(copy);
    expect(formatResumeError("compute limit reached")).toBe(copy);
    expect(isQuotaResumeError("[resource_exhausted] nope")).toBe(true);
  });

  it("passes any other message through unchanged", () => {
    expect(formatResumeError("daemon not found")).toBe("daemon not found");
    expect(isQuotaResumeError("daemon not found")).toBe(false);
  });
});

describe("isDaemonPendingError", () => {
  it("recognises toolexec.ErrDaemonPending's suspended and starting forms", () => {
    expect(
      isDaemonPendingError(
        "the machine for this request is suspended and will wake when you next message it: no daemon connected: daemon record exists but has not registered yet (still starting)",
      ),
    ).toBe(true);
    expect(
      isDaemonPendingError(new ConnectError("your machine is still starting: no daemon connected", Code.Unavailable)),
    ).toBe(true);
  });

  it("does not claim a genuine failure", () => {
    expect(isDaemonPendingError("no daemon available for user u-1")).toBe(false);
    expect(isDaemonPendingError(new Error("permission denied"))).toBe(false);
  });
});
