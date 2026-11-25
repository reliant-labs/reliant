import { readdirSync, readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { join, resolve } from "node:path";
import { describe, expect, it } from "vitest";

/**
 * The session port builds in the public demo bundle, which forbids lib/auth,
 * store/authStore and @supabase (vite-plugin-demo-bundle.ts). So every module
 * of lib/session imports React, forge-web-runtime's `./session` (which
 * imports only React), or another lib/session module and nothing else,
 * type-only imports included: a type import of the auth store today is a
 * value import tomorrow. The demo build is the final word; this fails first,
 * in the unit lane, naming the import.
 */

const ALLOWED = new Set(["react", "@reliantlabs/forge-web-runtime/session"]);

const dir = resolve(__dirname, "..");
// Every import, export-from, side-effect import and dynamic import(), `type` ones too.
const IMPORT = /(?:^|\n)\s*(?:import|export)\s+(?:[^'"`;]*?\sfrom\s*)?["']([^"']+)["']|import\(\s*["']([^"']+)["']\s*\)/g;

function sources(): string[] {
  return readdirSync(dir).filter((name) => /\.[jt]sx?$/.test(name));
}

describe("lib/session's imports", () => {
  it("has modules to check", () => {
    expect(sources()).toEqual(expect.arrayContaining(["port.ts", "fixed.ts", "index.ts"]));
  });

  it("are React, forge's ./session and lib/session only", () => {
    const outside = sources().flatMap((name) =>
      [...readFileSync(join(dir, name), "utf8").matchAll(IMPORT)]
        .map((match) => match[1] ?? match[2])
        .filter((spec) => !ALLOWED.has(spec) && !(spec.startsWith("./") && !spec.slice(2).includes("/")))
        .map((spec) => `${name} → ${spec}`),
    );
    expect(outside).toEqual([]);
  });

  it("and forge's ./session imports only React", () => {
    const published = readFileSync(createRequire(import.meta.url).resolve("@reliantlabs/forge-web-runtime/session"), "utf8");
    const specs = [...published.matchAll(IMPORT)].map((match) => match[1] ?? match[2]);
    expect(specs.length).toBeGreaterThan(0);
    expect(specs.filter((spec) => spec !== "react" && spec !== "react/jsx-runtime")).toEqual([]);
  });
});
