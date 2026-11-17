// The base-path contract. Every case here was previously re-derived, byte for
// byte, into every scaffolded project's src/lib/basepath_gen.ts — and tested
// in none of them. Moving the behaviour into the package is what made it
// testable once instead of eighty times.
import { describe, expect, it } from "vitest";

import { createBasePath, joinBasePath, normalizeBasePath } from "./basepath.js";

describe("normalizeBasePath", () => {
  it("treats the empty value and the bare root as 'no prefix'", () => {
    // Both spellings reach here: forge.yaml omitting base_path renders "",
    // and a user writing base_path: "/" means the same thing.
    expect(normalizeBasePath("")).toBe("");
    expect(normalizeBasePath("/")).toBe("");
  });

  it("adds the leading slash and drops the trailing one", () => {
    expect(normalizeBasePath("admin")).toBe("/admin");
    expect(normalizeBasePath("/admin")).toBe("/admin");
    expect(normalizeBasePath("/admin/")).toBe("/admin");
    expect(normalizeBasePath("admin/")).toBe("/admin");
  });

  it("normalises a multi-segment prefix", () => {
    expect(normalizeBasePath("ops/admin/")).toBe("/ops/admin");
  });
});

describe("joinBasePath", () => {
  it("returns the path unchanged when there is no prefix", () => {
    expect(joinBasePath("", "/billing/success")).toBe("/billing/success");
    expect(joinBasePath("", "billing")).toBe("/billing");
  });

  it("prefixes an app-relative path, with or without a leading slash", () => {
    expect(joinBasePath("/admin", "/billing/success")).toBe(
      "/admin/billing/success",
    );
    expect(joinBasePath("/admin", "billing/success")).toBe(
      "/admin/billing/success",
    );
  });

  it("is idempotent — a path already under the prefix is left alone", () => {
    // This is the guard that makes an accidental double-wrap harmless, which
    // is why callers can apply it defensively.
    expect(joinBasePath("/admin", "/admin/billing")).toBe("/admin/billing");
    expect(joinBasePath("/admin", joinBasePath("/admin", "/billing"))).toBe(
      "/admin/billing",
    );
  });

  it("maps the root path to the prefix itself, with no trailing slash", () => {
    expect(joinBasePath("/admin", "/")).toBe("/admin");
  });

  it("does not treat a prefix-lookalike segment as already-prefixed", () => {
    // "/administration" starts with "/admin" as a STRING but is not under it
    // as a PATH — matching on the string alone would drop the prefix here.
    expect(joinBasePath("/admin", "/administration")).toBe(
      "/admin/administration",
    );
  });

  it("passes absolute http(s) URLs through untouched", () => {
    expect(joinBasePath("/admin", "https://x.test/y")).toBe("https://x.test/y");
    expect(joinBasePath("/admin", "http://x.test/y")).toBe("http://x.test/y");
    expect(joinBasePath("/admin", "HTTPS://X.test/y")).toBe("HTTPS://X.test/y");
  });
});

describe("createBasePath", () => {
  it("normalises the raw value once and binds the joiner to it", () => {
    const { BASE_PATH, joinBasePath: join } = createBasePath("admin/");
    expect(BASE_PATH).toBe("/admin");
    expect(join("/billing")).toBe("/admin/billing");
    expect(join(join("/billing"))).toBe("/admin/billing");
  });

  it("degrades to a pass-through when the app is served from the root", () => {
    // The overwhelmingly common case: no base_path, no env override. The
    // bound joiner must still be safe to call everywhere.
    const { BASE_PATH, joinBasePath: join } = createBasePath("");
    expect(BASE_PATH).toBe("");
    expect(join("/billing")).toBe("/billing");
    expect(join("https://x.test/y")).toBe("https://x.test/y");
  });
});
