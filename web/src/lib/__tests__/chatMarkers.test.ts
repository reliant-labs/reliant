// Copyright (c) 2025 Reliant Labs

import { describe, it, expect } from "vitest";
import {
  CHAT_MARKER_KINDS,
  extractChatMarker,
  stripChatMarker,
  type ChatMarkerKind,
} from "../chatMarkers";

/**
 * Drift guard: hard-codes the wire-contract strings for every chat marker
 * kind. The Go mirror in `reliant/internal/chatmarkers/markers_test.go`
 * hard-codes the SAME strings in its own drift-guard test. If a rename slips
 * through one side without the other, BOTH tests fail loudly.
 *
 * DO NOT change these literals to reference the constants — the whole point
 * is to catch accidental renames of the constants themselves.
 */
describe("chat marker kind literals (cross-process drift guard)", () => {
  it("RELIANT_MANAGED_QUOTA_EXHAUSTED string matches Go mirror", () => {
    expect(CHAT_MARKER_KINDS.ReliantManagedQuotaExhausted).toBe(
      "RELIANT_MANAGED_QUOTA_EXHAUSTED",
    );
  });

  it("RELIANT_DAEMON_OFFLINE_HALT string matches Go mirror", () => {
    expect(CHAT_MARKER_KINDS.DaemonOfflineHalt).toBe(
      "RELIANT_DAEMON_OFFLINE_HALT",
    );
  });
});

describe("extractChatMarker", () => {
  it("extracts quota marker with URL payload", () => {
    const got = extractChatMarker(
      "Free tier exceeded [RELIANT_MANAGED_QUOTA_EXHAUSTED:/billing/plans]",
    );
    expect(got).toEqual({
      kind: "RELIANT_MANAGED_QUOTA_EXHAUSTED" satisfies ChatMarkerKind,
      payload: "/billing/plans",
    });
  });

  it("extracts daemon-halt marker with integer payload", () => {
    const got = extractChatMarker(
      "daemon offline for 3 consecutive turns; halting workflow. Reconnect the workspace and start a new turn. [RELIANT_DAEMON_OFFLINE_HALT:3]",
    );
    expect(got).toEqual({
      kind: "RELIANT_DAEMON_OFFLINE_HALT" satisfies ChatMarkerKind,
      payload: "3",
    });
  });

  it("extracts marker with a full URL payload", () => {
    const got = extractChatMarker(
      "quota gone [RELIANT_MANAGED_QUOTA_EXHAUSTED:https://example.com/upgrade?x=1]",
    );
    expect(got).toEqual({
      kind: "RELIANT_MANAGED_QUOTA_EXHAUSTED",
      payload: "https://example.com/upgrade?x=1",
    });
  });

  it("returns null when no marker present", () => {
    expect(extractChatMarker("just a regular error message")).toBeNull();
  });

  it("returns null on empty input", () => {
    expect(extractChatMarker("")).toBeNull();
  });

  it("extracts marker at start of string", () => {
    expect(extractChatMarker("[RELIANT_DAEMON_OFFLINE_HALT:5]")).toEqual({
      kind: "RELIANT_DAEMON_OFFLINE_HALT",
      payload: "5",
    });
  });

  it("first marker wins when two are present", () => {
    const got = extractChatMarker(
      "a [RELIANT_MANAGED_QUOTA_EXHAUSTED:/p] b [RELIANT_DAEMON_OFFLINE_HALT:7]",
    );
    expect(got).toEqual({
      kind: "RELIANT_MANAGED_QUOTA_EXHAUSTED",
      payload: "/p",
    });
  });

  it("returns null on bracketed non-marker text in user prompts", () => {
    expect(
      extractChatMarker("user typed [oops not a marker] in the prompt"),
    ).toBeNull();
  });

  it("returns null on shape-matching but unknown KIND", () => {
    // Defense-in-depth: bracketed SCREAMING_SNAKE_CASE that isn't one of OUR
    // kinds (e.g. user paste, future producer not yet in sync) is treated as
    // no marker so downstream routing doesn't fire on garbage.
    expect(
      extractChatMarker("oops [FUTURE_UNKNOWN_KIND:whatever] tail"),
    ).toBeNull();
  });
});

describe("stripChatMarker", () => {
  it("strips quota marker with URL", () => {
    expect(
      stripChatMarker(
        "Free tier exceeded [RELIANT_MANAGED_QUOTA_EXHAUSTED:/billing/plans]",
      ),
    ).toBe("Free tier exceeded");
  });

  it("strips daemon-halt marker", () => {
    expect(
      stripChatMarker(
        "daemon offline for 3 consecutive turns; halting workflow. Reconnect the workspace and start a new turn. [RELIANT_DAEMON_OFFLINE_HALT:3]",
      ),
    ).toBe(
      "daemon offline for 3 consecutive turns; halting workflow. Reconnect the workspace and start a new turn.",
    );
  });

  it("preserves text with no marker (apart from trim)", () => {
    expect(stripChatMarker("just a regular error message")).toBe(
      "just a regular error message",
    );
  });

  it("returns empty string for empty input", () => {
    expect(stripChatMarker("")).toBe("");
  });

  it("consumes leading whitespace before the marker", () => {
    expect(
      stripChatMarker("message    [RELIANT_DAEMON_OFFLINE_HALT:1]"),
    ).toBe("message");
  });

  it("bare marker strips to empty", () => {
    expect(stripChatMarker("[RELIANT_MANAGED_QUOTA_EXHAUSTED:/p]")).toBe("");
  });

  it("trims surrounding whitespace", () => {
    expect(
      stripChatMarker("  message [RELIANT_DAEMON_OFFLINE_HALT:1]   "),
    ).toBe("message");
  });
});
