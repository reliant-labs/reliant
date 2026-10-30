// Copyright (c) 2026 Reliant Labs
import { describe, expect, it } from "vitest";
import { fixtureCatalog, GRAPH_RPC } from "../../__fixtures__/fixtureGraph";
import { catalogMethods, parseDeclaredCatalog, schemaText, sourceLabel } from "../catalogModel";

describe("browse-only catalog model", () => {
  it("reads the exact Forge catalog including descriptor validation", () => {
    const result = parseDeclaredCatalog(fixtureCatalog().declaredCatalogs[0].json);
    expect(result.reason).toBe("");
    expect(schemaText(result.catalog!.services[0].descriptor, "shop.v1.GetOrderRequest")).toContain("validate_options");
    expect(result.catalog!.services[0].methods[0].idempotency).toBe("NO_SIDE_EFFECTS");
  });
  it("accepts canonical Go nil slices and optional serves component", () => {
    const value = JSON.parse(fixtureCatalog().declaredCatalogs[0].json);
    value.bindings = null; value.clients = null; value.diagnostics = null;
    value.processes[0].clients = null; value.components[0].methods = null;
    expect(parseDeclaredCatalog(JSON.stringify(value)).catalog?.bindings).toEqual([]);
    expect(parseDeclaredCatalog(JSON.stringify(value)).catalog?.processes[0].serves[0].component).toBe("Orders");
  });
  it("reports invalid/oversized/unsupported metadata as unavailable", () => {
    expect(parseDeclaredCatalog("not-json").catalog).toBeNull();
    expect(parseDeclaredCatalog('{"schema":"future"}').catalog).toBeNull();
    expect(parseDeclaredCatalog(" ".repeat(8 * 1024 * 1024 + 1)).reason).toContain("limit");
  });
  it("keeps declared capability and observed use distinct and dedups exact identity", () => {
    const c = fixtureCatalog(); const method = c.methods[0];
    c.methods.push({ ...method }, { ...method, evidence: "declared", observedCount: 0n });
    expect(catalogMethods(c, "shop.v1.OrdersService", GRAPH_RPC)).toHaveLength(2);
    expect(catalogMethods(c, "other.v1.OrdersService")).toHaveLength(0);
  });
  it("never manufactures executable source URLs", () => {
    expect(sourceLabel({ path: "javascript:alert(1)", line: 1 })).toContain("unavailable");
    expect(sourceLabel({ path: "../../secret", line: 1 })).toContain("unavailable");
    expect(sourceLabel({ path: "internal/app/roots.go", line: 4 })).toBe("internal/app/roots.go:4");
  });
  it("missing schema is unavailable, never an empty successful object", () => {
    expect(schemaText({}, "Missing")).toBeNull();
    expect(schemaText({ Schemas: {} }, "toString")).toBeNull();
  });
});
