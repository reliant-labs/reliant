// Copyright (c) 2026 Reliant Labs

/**
 * The Tests section's URLs (Console/paths.ts): every builder, and
 * consolePageOf reading each back, including a test id that holds the
 * characters a path segment cannot ("go:<module>/<pkg>:TestX/sub").
 */

import { describe, expect, it } from "vitest";

import { consolePageOf, consolePath, flakyTestPath, isTestsPage, testRunPath, testsPath, TESTS_VIEWS } from "../../Console/paths";
import { learnSectionOf } from "../../Console/consoleLearn";

const TEST_ID = "go:github.com/acme/shop/internal/billing:TestRefund/partial";

describe("Tests paths", () => {
  it("builds each Tests page", () => {
    expect(testsPath("shop")).toBe("/forge/p/shop/tests");
    expect(testsPath("shop", "coverage")).toBe("/forge/p/shop/tests/coverage");
    expect(testsPath("shop", "runs")).toBe("/forge/p/shop/tests/runs");
    expect(testsPath("shop", "flaky")).toBe("/forge/p/shop/tests/flaky");
    expect(testRunPath("shop", "01JD7Q")).toBe("/forge/p/shop/tests/runs/01JD7Q");
    expect(flakyTestPath("shop", TEST_ID)).toBe(`/forge/p/shop/tests/flaky/${encodeURIComponent(TEST_ID)}`);
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
