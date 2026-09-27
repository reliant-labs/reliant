// Copyright (c) 2025 Reliant Labs

/**
 * The additive `forge secret list --json` fields: report `verifiable`,
 * per-secret `presence` (set | missing | unknown) and `version`, and the
 * `rendered` provider. Each one is a way a newer report could be MISLABELLED
 * — an unknown painted as missing, an unverifiable `ok:false` painted as a
 * failure, a verified provider painted as unrecognised — so each is pinned.
 */

import { describe, expect, it } from "vitest";

import type { ForgeSecretsReport } from "@/services/forge/secrets";
import {
  blocksEnvUp,
  presenceOf,
  presenceTally,
  providerKind,
  secretEntries,
  storeState,
} from "@/services/forge/secrets";
import { modeSupportsWrite, surfaceMode } from "@/services/forge/secretSurface";

function presences(report: ForgeSecretsReport): string[] {
  return secretEntries(report).map((entry) => presenceOf(report, entry));
}

describe("per-secret presence", () => {
  it("never renders presence `unknown` as missing, even with present:false", () => {
    const report: ForgeSecretsReport = {
      provider: "hosted",
      verifiable: true,
      secrets: [{ name: "A", present: false, presence: "unknown" }],
    };
    expect(presences(report)).toEqual(["undetermined"]);
    expect(blocksEnvUp(report)).toBe(false);
  });

  it("maps set and missing to known-good and known-bad", () => {
    const report: ForgeSecretsReport = {
      provider: "hosted",
      verifiable: true,
      secrets: [
        { name: "A", present: true, presence: "set", version: 3 },
        { name: "B", present: false, presence: "missing" },
      ],
      missing: ["B"],
      ok: false,
    };
    expect(presences(report)).toEqual(["present", "missing"]);
    expect(presenceTally(report)).toEqual({ "known-good": 1, "known-bad": 1, unknown: 0 });
    expect(blocksEnvUp(report)).toBe(true);
  });

  it("treats an unrecognised presence word as not known rather than guessing", () => {
    const report: ForgeSecretsReport = {
      provider: "file",
      verifiable: true,
      secrets: [{ name: "A", present: false, presence: "pending" }],
    };
    expect(presences(report)).toEqual(["undetermined"]);
  });

  it("still reads the boolean from a forge that predates `presence`", () => {
    const report: ForgeSecretsReport = {
      provider: "file",
      secrets: [
        { name: "A", present: true },
        { name: "B", present: false },
      ],
    };
    expect(presences(report)).toEqual(["present", "missing"]);
  });
});

describe("verifiable:false", () => {
  it("is not a failure: ok:false with nothing checked blocks nothing", () => {
    const report: ForgeSecretsReport = {
      provider: "hosted",
      verifiable: false,
      ok: false,
      secrets: [{ name: "A", present: false, presence: "missing" }],
      missing: ["A"],
      missing_count: 1,
    };
    expect(presences(report)).toEqual(["undetermined"]);
    expect(blocksEnvUp(report)).toBe(false);
    expect(presenceTally(report)).toEqual({ "known-good": 0, "known-bad": 0, unknown: 1 });
  });

  it("keeps an external provider's rows as held-externally, not missing", () => {
    const report: ForgeSecretsReport = {
      provider: "external",
      verifiable: false,
      ok: false,
      secrets: [{ name: "A", present: false, presence: "unknown" }],
    };
    expect(presences(report)).toEqual(["not-held"]);
    expect(blocksEnvUp(report)).toBe(false);
  });
});

describe("the rendered provider", () => {
  const rendered: ForgeSecretsReport = {
    provider: "rendered",
    store_exists: true,
    verifiable: true,
    secrets: [
      { name: "app-secrets/db_url", present: true, presence: "set" },
      { name: "app-secrets/stripe", present: false, presence: "missing" },
    ],
    missing: ["app-secrets/stripe"],
    ok: false,
  };

  it("is recognised, and its presence is an observation", () => {
    expect(providerKind(rendered)).toBe("rendered");
    expect(presences(rendered)).toEqual(["present", "missing"]);
    expect(storeState(rendered)).toBe("populated");
  });

  it("is read-only from the browser, even when a managed store is reachable", () => {
    const mode = surfaceMode(rendered, true);
    expect(mode).toBe("file");
    expect(modeSupportsWrite(mode)).toBe(false);
  });
});
