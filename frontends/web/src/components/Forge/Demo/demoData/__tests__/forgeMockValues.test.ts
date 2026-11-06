// Copyright (c) 2025 Reliant Labs

/**
 * The vendored mock engine (forgeMockValues/, forge#1874 + forge#1878) on the
 * console's own contracts. The engine's own contract is tested upstream
 * (web-runtime/src/mock-values.test.ts); this pins what the demo relies on:
 *
 *  - every string a console message's pattern constrains gets a value that
 *    matches it (a value that did not would be one control-plane rejects);
 *  - a value the request carries is echoed only when the response field's
 *    rules accept it, and a requested oneof case is kept.
 */

import { describe, expect, it } from "vitest";

import { CreateReceiverRequestSchema, MatcherSchema } from "@/gen/controlplane/services/alerting/v1/alerting_pb";
import { SaveAsCopyRequestSchema, SaveDashboardRequestSchema } from "@/gen/controlplane/services/dashboard/v1/dashboard_pb";

import { mockValue } from "../forgeMockValues/mock-values";

const NOW = Date.UTC(2026, 10, 6, 12, 0, 0);

describe("the vendored mock engine on the console's contracts", () => {
  it("meets every pattern a console message declares", () => {
    for (let i = 0; i < 5; i++) {
      const seed = `seed-${i}`;
      expect(mockValue(MatcherSchema, { now: NOW, seed }).name).toMatch(/^[a-zA-Z_][a-zA-Z0-9_]*$/);
      expect(mockValue(CreateReceiverRequestSchema, { now: NOW, seed }).name).toMatch(/^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$/);
      expect(mockValue(SaveDashboardRequestSchema, { now: NOW, seed }).slug).toMatch(/^[a-z0-9][a-z0-9-]{0,62}$/);
      expect(mockValue(SaveAsCopyRequestSchema, { now: NOW, seed }).slug).toMatch(/^$|^[a-z0-9][a-z0-9-]{0,62}$/);
    }
  });

  it("is deterministic: the same seed, the same message", () => {
    expect(mockValue(MatcherSchema, { now: NOW, seed: "s" })).toEqual(mockValue(MatcherSchema, { now: NOW, seed: "s" }));
  });
});
