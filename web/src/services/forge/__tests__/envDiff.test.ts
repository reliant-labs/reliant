// Copyright (c) 2025 Reliant Labs

/**
 * THE ONE MISTAKE THIS MODULE EXISTS TO PREVENT: a diff that could not be
 * computed reading as "nothing changed".
 *
 * They look identical in the data — no counts, no workloads, empty everywhere —
 * and they mean opposite things. One says "deploying this is a no-op", the
 * other says "we have no idea what deploying this does". So the status is
 * checked separately from the counts, and the fallback for an unknown status is
 * the cautious direction.
 */

import { describe, expect, it } from "vitest";

import {
  diffEntries,
  diffHasChanges,
  diffIsAnswered,
  envDiffStatusOf,
  missingSecrets,
  type ForgeEnvDiffEntry,
} from "../envDiff";

describe("the per-environment status", () => {
  it("decodes forge's closed set", () => {
    expect(envDiffStatusOf("ok")).toBe("ok");
    expect(envDiffStatusOf("impure")).toBe("impure");
    expect(envDiffStatusOf("stale")).toBe("stale");
    expect(envDiffStatusOf("error")).toBe("error");
    expect(envDiffStatusOf("unsupported")).toBe("unsupported");
  });

  it("decodes an UNRECOGNISED status as unknown, never as ok", () => {
    // The direction is the safety property. A newer forge's status read as ok
    // would present whatever happened to be in the document as a trustworthy
    // diff — which is the failure this module is shaped around.
    expect(envDiffStatusOf("something_new")).toBe("unknown");
    expect(envDiffStatusOf(undefined)).toBe("unknown");
  });

  it("treats ONLY ok as an answer", () => {
    for (const status of ["impure", "stale", "error", "unsupported", "unknown", undefined]) {
      expect(diffIsAnswered({ env: "prod", status })).toBe(false);
    }
    expect(diffIsAnswered({ env: "prod", status: "ok" })).toBe(true);
  });
});

describe("whether anything would change", () => {
  function answered(diff: ForgeEnvDiffEntry["diff"]): ForgeEnvDiffEntry {
    return { env: "prod", status: "ok", diff };
  }

  /**
   * THESE FIELD NAMES ARE FORGE'S, AND THAT IS THE POINT OF THE TEST.
   *
   * An earlier version of this block asserted `objects_added`,
   * `objects_changed`, `images_changed` and `secrets_needed` — names forge has
   * never emitted. The tests passed, because the type declared the same
   * invented names the tests used, and the two agreed with each other about a
   * shape neither had checked against the producer.
   *
   * What shipped behind that agreement was the exact failure this file opens by
   * describing: a real diff with nine added objects has no key by any of those
   * names, so every lookup missed, and `diffHasChanges` returned FALSE —
   * telling the person about to deploy that nothing would change.
   *
   * So the names below are read off captured `forge env diff --json` output,
   * and the cards' own contract tests parse those fixtures directly rather than
   * hand-building entries. A unit test for a wire contract is only worth what
   * its fixture is.
   */
  it("finds changes from forge's object arrays", () => {
    expect(diffHasChanges(answered({ added: [{ kind: "Deployment", name: "api" }] }))).toBe(true);
    expect(diffHasChanges(answered({ removed: [{ kind: "Service", name: "gone" }] }))).toBe(true);
    expect(
      diffHasChanges(answered({ changed: [{ candidate: { kind: "Deployment", name: "api" } }] }))
    ).toBe(true);
  });

  it("finds changes from the lists too", () => {
    expect(diffHasChanges(answered({ workloads_added: ["api"] }))).toBe(true);
    expect(diffHasChanges(answered({ workloads_removed: ["old"] }))).toBe(true);
    expect(diffHasChanges(answered({ secrets_added: [{ name: "DB_URL" }] }))).toBe(true);
    expect(diffHasChanges(answered({ domains_added: ["a.example.com"] }))).toBe(true);
    expect(diffHasChanges(answered({ clusters_added: ["prod-gke"] }))).toBe(true);
    expect(
      diffHasChanges(answered({ runtime_changes: [{ workload: "web", from: "hosted", to: "bucket" }] }))
    ).toBe(true);
  });

  it("treats a kind change as a change — it is an error, not a no-op", () => {
    expect(
      diffHasChanges(answered({ kind_changed: { live: "persistent", candidate: "self_managed" } }))
    ).toBe(true);
  });

  it("reports no changes for a genuinely identical answered diff", () => {
    // Forge emits a diff object with every array omitted when nothing differs.
    expect(diffHasChanges(answered({}))).toBe(false);
  });

  it("returns false for an UNANSWERED entry — which is why the status must be read too", () => {
    // THE PAIRING IS THE POINT. This returns false both for "nothing changed"
    // and for "we could not look", so a caller that checks only this renders a
    // failed render as a safe deploy. diffIsAnswered is what separates them,
    // and this test documents that it is mandatory rather than optional.
    const failed: ForgeEnvDiffEntry = { env: "prod", status: "error", detail: "render failed" };
    expect(diffHasChanges(failed)).toBe(false);
    expect(diffIsAnswered(failed)).toBe(false);

    const identical: ForgeEnvDiffEntry = { env: "prod", status: "ok", diff: {} };
    expect(diffHasChanges(identical)).toBe(false);
    expect(diffIsAnswered(identical)).toBe(true);
  });
});

describe("secrets", () => {
  it("lists the keys this checkout needs that are not there", () => {
    const entry: ForgeEnvDiffEntry = {
      env: "prod",
      status: "ok",
      secret_presence: { DB_URL: false, API_KEY: true, STRIPE: false },
    };
    expect(missingSecrets(entry)).toEqual(["DB_URL", "STRIPE"]);
  });

  it("is empty when nothing is declared", () => {
    expect(missingSecrets({ env: "prod", status: "ok" })).toEqual([]);
  });
});

describe("reading the document", () => {
  it("returns forge's entries in order", () => {
    const entries = diffEntries({
      environments: [
        { env: "dev", status: "ok" },
        { env: "prod", status: "error" },
      ],
    });
    expect(entries.map((entry) => entry.env)).toEqual(["dev", "prod"]);
  });

  it("is empty for an absent document rather than throwing", () => {
    expect(diffEntries(null)).toEqual([]);
    expect(diffEntries(undefined)).toEqual([]);
  });
});
