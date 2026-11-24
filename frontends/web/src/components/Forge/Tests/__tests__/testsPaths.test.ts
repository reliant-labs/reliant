// Copyright (c) 2026 Reliant Labs

/**
 * The Tests section's URLs (Console/paths.ts): every builder, and
 * consolePageOf reading each back, including a test id that holds the
 * characters a path segment cannot ("go:<module>/<pkg>:TestX/sub").
 */

import { describe, expect, it } from "vitest";

import { consolePageOf, consolePath, decodeTestIdSegment, encodeTestIdSegment, flakyTestPath, isTestsPage, testRunPath, testsPath, TESTS_VIEWS } from "../../Console/paths";
import { learnSectionOf } from "../../Console/consoleLearn";

const TEST_ID = "go:github.com/acme/shop/internal/billing:TestRefund/partial";

describe("Tests paths", () => {
  it("builds each Tests page", () => {
    expect(testsPath("shop")).toBe("/forge/p/shop/tests");
    expect(testsPath("shop", "coverage")).toBe("/forge/p/shop/tests/coverage");
    expect(testsPath("shop", "runs")).toBe("/forge/p/shop/tests/runs");
    expect(testsPath("shop", "flaky")).toBe("/forge/p/shop/tests/flaky");
    expect(testRunPath("shop", "01JD7Q")).toBe("/forge/p/shop/tests/runs/01JD7Q");
    expect(flakyTestPath("shop", TEST_ID)).toBe(`/forge/p/shop/tests/flaky/${encodeTestIdSegment(TEST_ID)}`);
  });

  it("puts a test id in its segment as unpadded base64url, never a raw or %2F-encoded id", () => {
    // Static hosts and CDNs answer a path holding %2F with "Bad request".
    const segment = flakyTestPath("shop", TEST_ID).split("/").pop()!;
    expect(segment).toMatch(/^[A-Za-z0-9_-]+$/);
    expect(segment).toBe(btoa(TEST_ID).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, ""));
  });

  it("round-trips a test id holding /, :, %, spaces and unicode", () => {
    const ids = [
      TEST_ID,
      "go:github.com/acme/shop/internal/jobs:TestSyncJob",
      "go:github.com/acme/shop:TestRate/100%_off",
      "go:github.com/acme/shop:TestRate/%2F_literal",
      "vitest:src/cart.test.ts:adds an item / removes it",
      "go:example.com/i18n:TestGrüße/日本語/🎉",
      "go:a:b", // short: every padding length
      "go:a:bc",
      "go:a:bcd",
    ];
    for (const id of ids) {
      const segment = encodeTestIdSegment(id);
      expect(segment).toMatch(/^[A-Za-z0-9_-]+$/);
      expect(decodeTestIdSegment(segment)).toBe(id);
      // Through the path and back, as the nav reads it.
      expect(consolePageOf(flakyTestPath("shop", id))?.page).toEqual({ kind: "tests-flaky-test", test: id });
    }
  });

  it("still reads a link in the old percent-encoded form", () => {
    for (const id of [TEST_ID, "go:github.com/acme/shop:TestRate/100%_off", "go:example.com/i18n:TestGrüße/日本語"]) {
      const old = `/forge/p/shop/tests/flaky/${encodeURIComponent(id)}`;
      expect(consolePageOf(old)?.page).toEqual({ kind: "tests-flaky-test", test: id });
      // The router hands the page its param percent-decoded.
      expect(decodeTestIdSegment(id)).toBe(id);
    }
  });

  it("reads a segment that is not canonical base64url, or not UTF-8, as the id itself", () => {
    expect(decodeTestIdSegment("go:pkg:TestX")).toBe("go:pkg:TestX");
    expect(decodeTestIdSegment("abcde")).toBe("abcde"); // a length base64 cannot have
    expect(decodeTestIdSegment("_w")).toBe("_w"); // 0xFF: not UTF-8
    expect(decodeTestIdSegment("Zm9vYg==")).toBe("Zm9vYg=="); // padded: not ours
  });

  it("reads every Tests path back as its page", () => {
    expect(consolePageOf("/forge/p/shop/tests")?.page).toEqual({ kind: "tests" });
    expect(consolePageOf("/forge/p/shop/tests/coverage")?.page).toEqual({ kind: "tests-coverage" });
    expect(consolePageOf("/forge/p/shop/tests/runs")?.page).toEqual({ kind: "tests-runs" });
    expect(consolePageOf("/forge/p/shop/tests/runs/01JD7Q")?.page).toEqual({ kind: "tests-run", run: "01JD7Q" });
    expect(consolePageOf("/forge/p/shop/tests/flaky")?.page).toEqual({ kind: "tests-flaky" });
    expect(consolePageOf(flakyTestPath("shop", TEST_ID))?.page).toEqual({ kind: "tests-flaky-test", test: TEST_ID });
  });

  it("round-trips every view through consolePath", () => {
    for (const view of TESTS_VIEWS) {
      const page = consolePageOf(testsPath("shop", view))!.page;
      expect(consolePath("shop", page)).toBe(testsPath("shop", view));
      expect(isTestsPage(page)).toBe(true);
      expect(learnSectionOf(page)).toBe("tests");
    }
  });

  it("is no page for a path it does not know", () => {
    expect(consolePageOf("/forge/p/shop/tests/nope")).toBeNull();
    expect(consolePageOf("/forge/p/shop/tests/coverage/internal")).toBeNull();
    expect(consolePageOf("/forge/p/shop/tests/runs/a/b")).toBeNull();
    expect(isTestsPage({ kind: "releases" })).toBe(false);
  });
});
