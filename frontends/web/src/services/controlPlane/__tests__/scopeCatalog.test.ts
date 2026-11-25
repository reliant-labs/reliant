// @vitest-environment node
/**
 * The web copy of forge's scope catalog (scopeCatalog.ts). That the rows ARE
 * forge's is internal/accesstokenclient/web_scope_catalog_test.go's to pin;
 * this pins what the token page derives from them: a bundle's expansion never
 * shows a sensitive scope, and only developer acts as a person.
 */
import { describe, expect, it } from "vitest";

import {
  bundleActsAsPerson,
  expandBundle,
  FORGE_BUNDLES,
  FORGE_SCOPE_CATALOG,
  FORGE_SCOPES,
  isForgeBundle,
  SENSITIVE_SCOPES,
} from "../scopeCatalog";

describe("scopeCatalog", () => {
  it("names each scope once", () => {
    expect(new Set(FORGE_SCOPES).size).toBe(FORGE_SCOPES.length);
  });

  it("lists no bundle for a sensitive scope, and expands no bundle to one", () => {
    expect(SENSITIVE_SCOPES).toEqual(
      expect.arrayContaining(["db:query", "secret:write", "token:write", "identity:manage", "changes:launch"]),
    );
    for (const e of FORGE_SCOPE_CATALOG.filter((r) => r.class === "sensitive")) expect(e.bundles, e.scope).toEqual([]);
    for (const b of FORGE_BUNDLES) {
      const expanded = expandBundle(b);
      expect(expanded.length, b).toBeGreaterThan(0);
      for (const s of SENSITIVE_SCOPES) expect(expanded, `${b} expands to ${s}`).not.toContain(s);
    }
  });

  it("grows by authority: ci within developer within admin, for org scopes", () => {
    const org = (b: (typeof FORGE_BUNDLES)[number]) =>
      expandBundle(b).filter((s) => FORGE_SCOPE_CATALOG.find((e) => e.scope === s)?.class !== "personal");
    for (const s of org("ci")) expect(org("developer"), s).toContain(s);
    for (const s of org("developer")) expect(org("admin"), s).toContain(s);
  });

  it("keeps secret:read out of developer and in admin", () => {
    expect(expandBundle("developer")).not.toContain("secret:read");
    expect(expandBundle("admin")).toContain("secret:read");
  });

  it("says only developer acts as a person, so an organization token may carry ci and admin", () => {
    expect(FORGE_BUNDLES.filter(bundleActsAsPerson)).toEqual(["developer"]);
  });

  it("recognises bundle names, not bundle references", () => {
    expect(isForgeBundle("ci")).toBe(true);
    expect(isForgeBundle("bundle:ci")).toBe(false);
    expect(isForgeBundle("superuser")).toBe(false);
  });
});
