/**
 * Guardrail: the tourStore must not couple itself to the router.
 *
 * The whole point of the URL refactor is that tour state lives in the URL
 * search param, and `useTourNavigation` is the one place that translates
 * between URL ↔ store. If the store starts calling `router.navigate()` or
 * pulling `__RELIANT_ROUTER` off globalThis again we end up right back in
 * the original mess (store and router both racing to own tour state).
 *
 * This test reads tourStore.ts as text and asserts the forbidden references
 * don't appear. A future PR can't quietly re-couple them — the diff fails CI.
 */
import { describe, it, expect } from "vitest";
import { readFileSync } from "fs";
import { resolve, dirname } from "path";
import { fileURLToPath } from "url";

const __dirname = dirname(fileURLToPath(import.meta.url));

const FORBIDDEN_REFS = [
  "router.navigate",
  "__RELIANT_ROUTER",
  "getRouter",
  "exitWorkflowRoute",
  "exitSettingsRoute",
  "navigateToWorkflow",
];

describe("tourStore source guardrail", () => {
  it("does not contain any router-coupling references", () => {
    const sourcePath = resolve(__dirname, "..", "tourStore.ts");
    const source = readFileSync(sourcePath, "utf-8");

    for (const forbidden of FORBIDDEN_REFS) {
      // Surface every offending ref in a single assertion failure so the
      // failure message is actually useful when CI breaks.
      const present = source.includes(forbidden);
      expect(present, `tourStore.ts must not reference \`${forbidden}\``).toBe(
        false
      );
    }
  });

  it("does not import @tanstack/react-router", () => {
    const sourcePath = resolve(__dirname, "..", "tourStore.ts");
    const source = readFileSync(sourcePath, "utf-8");
    // Match either form: `from "@tanstack/react-router"` or single-quote.
    const importPattern = /from\s+["']@tanstack\/react-router["']/;
    expect(
      importPattern.test(source),
      "tourStore.ts must not import from @tanstack/react-router — navigation belongs in useTourNavigation"
    ).toBe(false);
  });
});
