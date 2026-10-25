// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { ReplayCandidate } from "@/hooks/trigger-model";
import { candidateLabel, formatPayload, parsePayload } from "../runNowReplay";

describe("parsePayload", () => {
  it("reads a JSON object, and blank as an empty event", () => {
    expect(parsePayload('{"issue": 7}')).toEqual({ ok: true, value: { issue: 7 } });
    expect(parsePayload("   ")).toEqual({ ok: true, value: {} });
  });

  it("refuses text that is not JSON, naming why", () => {
    const parsed = parsePayload('{"issue": ');
    expect(parsed.ok).toBe(false);
    if (!parsed.ok) expect(parsed.error).toMatch(/^Not valid JSON/);
  });

  it("refuses JSON that is not an object: an event body is a map", () => {
    for (const text of ["[1, 2]", "7", '"hi"', "null", "true"]) {
      const parsed = parsePayload(text);
      expect(parsed.ok, text).toBe(false);
      if (!parsed.ok) expect(parsed.error).toMatch(/must be a JSON object/);
    }
  });
});

describe("formatPayload", () => {
  it("pretty-prints, and keeps an empty payload on one line", () => {
    expect(formatPayload({})).toBe("{}");
    expect(formatPayload({ a: { b: 1 } })).toBe('{\n  "a": {\n    "b": 1\n  }\n}');
  });

  it("round-trips through parsePayload", () => {
    const payload = { data: { issue: { number: 7, labels: ["bug"] } } };
    expect(parsePayload(formatPayload(payload))).toEqual({ ok: true, value: payload });
  });
});

describe("candidateLabel", () => {
  const now = Date.parse("2026-10-20T12:00:00Z");
  const candidate = (overrides: Partial<ReplayCandidate> = {}): ReplayCandidate => ({
    triggerName: "new-issue",
    kind: "integration",
    payload: {},
    event: {
      id: "ev-1",
      occurredAt: "2026-10-20T09:00:00Z",
      outcome: "skipped",
      outcomeDetail: "",
      manual: false,
      sender: { kind: "github", id: "42", displayName: "octocat", verified: true },
    },
    ...overrides,
  });

  it("names the trigger, the kind, who sent it and when", () => {
    expect(candidateLabel(candidate(), now)).toBe("new-issue · Integration · octocat · 3 hours ago");
  });

  it("leaves out a sender that was not recorded", () => {
    const withoutSender = candidate();
    withoutSender.event.sender = undefined;
    expect(candidateLabel(withoutSender, now)).toBe("new-issue · Integration · 3 hours ago");
  });

  it("shows an unknown kind as the server sent it", () => {
    expect(candidateLabel(candidate({ kind: "mystery" }), now)).toContain("mystery");
  });
});
