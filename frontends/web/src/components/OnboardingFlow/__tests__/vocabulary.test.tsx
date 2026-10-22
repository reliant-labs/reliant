/**
 * One noun, everywhere the user can see it.
 *
 * `lib/daemon-wait.ts` settled this for the wait states: the user-facing name
 * for the thing that runs your code is a "machine" (MACHINE_NOUN), and
 * daemon-wait.test.ts pins it there. But that migration originally stopped at
 * the wait states — onboarding still said "Start cloud daemon", the connecting
 * gate said "Connecting your environment", and the header status dot said "No
 * daemon connected". Three nouns for one concept, all of them in the first
 * five minutes of using the product.
 *
 * Onboarding is the worst possible place to leak implementation vocabulary: it
 * is the one screen every user sees, before they have any model of the system
 * to hang the jargon on. So this guards the SOURCE of the onboarding surfaces
 * rather than a rendered tree — no mocking, no render, and it catches a
 * regression in a file this test does not otherwise know about.
 *
 * It reads the JSX/user-string side only. Identifiers, types, imports, RPC
 * names, analytics event names and comments legitimately keep saying "daemon"
 * — that IS the internal name, and renaming it would be a much larger and far
 * less useful change.
 */
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const ONBOARDING_DIR = join(__dirname, "..");
const STEPS_DIR = join(ONBOARDING_DIR, "steps");

/** Words that describe our architecture, not the user's goal. */
const BANNED = ["daemon", "environment"];

/**
 * Strip everything that is not user-visible prose:
 *   - block and line comments
 *   - import statements
 *   - `data-testid` / `className` / `aria-label`-style attribute values that
 *     are wired to selectors rather than read
 *   - anything inside a `useState`/type position is left alone by virtue of
 *     only matching quoted prose and JSX text below.
 */
function stripNonUserText(source: string): string {
  return source
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/^\s*\/\/.*$/gm, "")
    .replace(/^\s*import[\s\S]*?from\s+["'].*?["'];?$/gm, "")
    .replace(/className=(?:"[^"]*"|\{[^}]*\})/g, "")
    .replace(/data-testid="[^"]*"/g, "")
    .replace(/queryKey:\s*\[[^\]]*\]/g, "")
    // Developer-facing logging. These are read in a log file by us, never by
    // a user, and they SHOULD keep using the internal name.
    .replace(/(?:console|logger)\.\w+\([\s\S]*?\);/g, "");
}

/**
 * Lines that look like prose a user reads: JSX text nodes, and string
 * literals that contain a space (a bare "local_daemon" is an enum value; "No
 * daemon connected" is a sentence).
 */
function userFacingLines(source: string): string[] {
  const cleaned = stripNonUserText(source);
  const hits: string[] = [];

  for (const raw of cleaned.split("\n")) {
    const line = raw.trim();
    if (!line) continue;

    // A quoted string containing a space — prose, not an identifier or enum.
    for (const match of line.matchAll(/["'`]([^"'`]{2,})["'`]/g)) {
      const value = match[1];
      if (value.includes(" ")) hits.push(value);
    }

    // A JSX text node: a line with no tag characters and no code punctuation,
    // e.g. `  Start my machine`.
    if (
      !line.includes("<") &&
      !line.includes(">") &&
      !line.includes("=") &&
      !line.includes("(") &&
      !line.includes("{") &&
      /^[A-Za-z][A-Za-z ,.'’—-]+$/.test(line) &&
      line.includes(" ")
    ) {
      hits.push(line);
    }
  }

  return hits;
}

function sourceFiles(): { name: string; source: string }[] {
  const files: { name: string; source: string }[] = [];
  for (const dir of [ONBOARDING_DIR, STEPS_DIR]) {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      if (!entry.isFile()) continue;
      if (!entry.name.endsWith(".tsx") && !entry.name.endsWith(".ts")) continue;
      files.push({
        name: `${dir === STEPS_DIR ? "steps/" : ""}${entry.name}`,
        source: readFileSync(join(dir, entry.name), "utf8"),
      });
    }
  }
  return files;
}

describe("onboarding speaks the user's language", () => {
  it("never shows the words 'daemon' or 'environment' to a user", () => {
    const offenders: string[] = [];

    for (const { name, source } of sourceFiles()) {
      for (const line of userFacingLines(source)) {
        const lower = line.toLowerCase();
        for (const banned of BANNED) {
          if (!lower.includes(banned)) continue;
          // "environments" is the real name of the settings SECTION we deep
          // link to, and `section: "environments"` is a route param.
          if (banned === "environment" && lower.includes("/settings")) continue;
          offenders.push(`${name}: ${line}`);
        }
      }
    }

    expect(
      offenders,
      `Onboarding is the first thing every user sees, so it must not leak our ` +
        `internal vocabulary. Use "machine" (see MACHINE_NOUN in ` +
        `lib/daemon-wait.ts) instead of "daemon"/"environment":\n` +
        offenders.map((o) => `  - ${o}`).join("\n"),
    ).toEqual([]);
  });

  it("still says 'machine' somewhere, so the check above can actually fail", () => {
    // Guards the guard: if the extraction above silently stopped matching
    // anything, the banned-word test would pass vacuously forever.
    const allProse = sourceFiles()
      .flatMap(({ source }) => userFacingLines(source))
      .join(" ")
      .toLowerCase();

    expect(allProse).toContain("machine");
  });
});
