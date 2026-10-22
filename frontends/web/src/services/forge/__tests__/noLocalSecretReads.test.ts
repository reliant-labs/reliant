// Copyright (c) 2025 Reliant Labs

/**
 * THE BROWSER NEVER CALLS LocalSecretService.
 *
 * controlplane.v1.LocalSecretService/PullSecrets returns secret VALUES — it
 * exists for `forge env up`, which injects them into the processes it launches,
 * in memory. Its generated client ships in src/gen/ because the sync script
 * exports every public control-plane service, but nothing in the app may
 * import it: a web surface that can read a secret value is a leak waiting for
 * a log line, and every secrets screen here is built on the premise that no
 * value is ever in the browser.
 *
 * A source scan rather than a runtime check, because the failure this prevents
 * is someone ADDING the import.
 */

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";

import { describe, expect, it } from "vitest";

const SRC = join(__dirname, "..", "..", "..");

function sourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry);
    const rel = relative(SRC, path);
    // The generated client itself, and this test (which names the service).
    if (rel === "gen" || rel.startsWith("gen/")) continue;
    if (statSync(path).isDirectory()) {
      out.push(...sourceFiles(path));
    } else if (/\.(ts|tsx)$/.test(entry) && !path.endsWith("noLocalSecretReads.test.ts")) {
      out.push(path);
    }
  }
  return out;
}

describe("LocalSecretService", () => {
  it("is imported by nothing in the web app", () => {
    const offenders = sourceFiles(SRC).filter((file) => {
      const text = readFileSync(file, "utf8");
      return /local_secret\/v1\/local_secret_pb|LocalSecretService|PullSecrets/.test(text);
    });
    expect(offenders.map((f) => relative(SRC, f))).toEqual([]);
  });
});
