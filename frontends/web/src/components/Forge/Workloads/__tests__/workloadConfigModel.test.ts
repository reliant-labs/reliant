// Copyright (c) 2025 Reliant Labs
import { describe, expect, it } from "vitest";
import type { JsonObject } from "@bufbuild/protobuf";
import { configRows, diffConfig } from "../workloadConfigModel";

const object = (data: JsonObject): JsonObject[] => [{ apiVersion: "v1", kind: "ConfigMap", metadata: { name: "api", namespace: "prod" }, data }];

describe("server-redacted field tree", () => {
  it("keeps strings, multiline and hostile HTML as string data, distinct from null and missing", () => {
    const rows = configRows(object({ null: null, empty: "", multiline: "one\ntwo", html: '<img src=x onerror="alert(1)">' }));
    expect(rows.find((row) => row.path.endsWith("/null"))?.value).toBe("null");
    expect(rows.find((row) => row.path.endsWith("/empty"))?.value).toBe('""');
    expect(rows.find((row) => row.path.endsWith("/multiline"))?.value).toBe('"one\\ntwo"');
    expect(rows.find((row) => row.path.endsWith("/html"))?.value).toContain("<img");
    expect(diffConfig(object({ nullable: null }), object({}))).toEqual(expect.arrayContaining([expect.objectContaining({ before: "null", after: "missing", change: "removed" })]));
    expect(diffConfig([], object({}))).not.toHaveLength(0);
  });

  it("never compares withheld secrets, even equal markers, and never surfaces legacy hashes", () => {
    const rows = diffConfig(object({ TOKEN: "forge.dev/redacted: withheld", PASSWORD: "forge.dev/redacted: sha256:abc" }), object({ TOKEN: "forge.dev/redacted: withheld", PASSWORD: "forge.dev/redacted: sha256:def" }));
    expect(rows).toHaveLength(2);
    expect(rows.every((row) => row.change === "secret value not compared")).toBe(true);
    expect(JSON.stringify(rows)).not.toMatch(/sha256|abc|def/);
    expect(configRows(object({ PASSWORD: "forge.dev/redacted: sha256:abc" })).find((row) => row.path.endsWith("/PASSWORD"))?.value).toBe("withheld");
  });

  it("treats a withheld subtree as unknown rather than inferring added or removed values", () => {
    const rows = diffConfig(object({ nested: "forge.dev/redacted: withheld" }), object({ nested: { value: "visible" } }));
    expect(rows).toEqual([expect.objectContaining({ before: "not compared", after: "not compared", change: "secret value not compared" })]);
    expect(JSON.stringify(rows)).not.toContain("visible");
  });

  it("aligns objects and named env/container arrays independent of order, retaining references", () => {
    const deployment: JsonObject = { apiVersion: "apps/v1", kind: "Deployment", metadata: { name: "api" }, spec: { containers: [{ name: "api", env: [{ name: "MODE", value: "prod" }, { name: "TOKEN", valueFrom: { secretKeyRef: { name: "credentials", key: "token" } } }] }] } };
    const reordered = structuredClone(deployment);
    (reordered.spec as { containers: { env: unknown[] }[] }).containers[0]!.env.reverse();
    expect(diffConfig([...object({ x: "a" }), deployment], [reordered, ...object({ x: "a" })])).toEqual([]);
    expect(configRows([deployment])).toContainEqual(expect.objectContaining({ value: '"credentials"', withheld: false }));
  });

  it("reports scalar, empty collection, added and removed field differences without guessing", () => {
    expect(diffConfig(object({ a: true, b: 2, remove: "x", empty: [] }), object({ a: false, b: 3, add: {}, empty: {} }))).toEqual(expect.arrayContaining([
      expect.objectContaining({ change: "changed", before: "true", after: "false" }),
      expect.objectContaining({ change: "added", before: "missing", after: "{}" }),
      expect.objectContaining({ change: "removed", before: '"x"', after: "missing" }),
      expect.objectContaining({ change: "changed", before: "[]", after: "{}" }),
    ]));
    expect(configRows([])).toEqual([]);
    expect(configRows(object({ "a/b~c": "x" })).some((row) => row.path.endsWith("/a~1b~0c"))).toBe(true);
  });
});
