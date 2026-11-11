// Copyright (c) 2025 Reliant Labs
import { afterEach, describe, expect, it, vi } from "vitest";
import { appDataTargets, identityDataLinksEnabled, identityTarget, parseAppDataSubject, uniqueExactEmail } from "../identityLinks-model";
import { decodeFilter, patchSearch } from "../dataUrl-model";
import { forgeConsoleDataSearchSchema, forgeConsoleEnvSearchSchema } from "@/routeSchemas";
import type { DbTable } from "@/services/controlPlane/observability/appDatabaseApi";

const subject = { environmentId: "prod-id", userId: `id.'",&=/#\\`, email: "Ada@example.com" };
const table = { schema: "public", name: "members", columns: [{ name: "identity.id", identitySubject: "id" }, { name: "email", identitySubject: "email" }, { name: "external_id" }] } as DbTable;
afterEach(() => vi.unstubAllEnvs());
describe("explicit identity subjects", () => {
 it("aligns ID bounds with GetUser without shrinking email search", () => {
  const accepted = "x".repeat(128);
  const rejected = "x".repeat(129);
  expect(identityTarget({ identitySubject: "id" }, accepted, false)).toEqual({ user: accepted });
  expect(identityTarget({ identitySubject: "id" }, rejected, false)).toBeNull();
  expect(forgeConsoleEnvSearchSchema.parse({ user: accepted }).user).toBe(accepted);
  expect(forgeConsoleEnvSearchSchema.parse({ user: rejected }).user).toBeUndefined();
  expect(forgeConsoleEnvSearchSchema.parse({ q: "x".repeat(200) }).q).toHaveLength(200);
 });
 it("keeps default-on behavior and accepts only the explicit disabled containment value", () => {
  vi.stubEnv("VITE_IDENTITY_DATA_LINKS", undefined);
  expect(identityDataLinksEnabled()).toBe(true);
  vi.stubEnv("VITE_IDENTITY_DATA_LINKS", "disabled");
  expect(identityDataLinksEnabled()).toBe(false);
  vi.stubEnv("VITE_IDENTITY_DATA_LINKS", "false");
  expect(identityDataLinksEnabled()).toBe(true);
 });
 it("never infers a subject from the column name or accepts partial/composite values", () => {
  expect(identityTarget(undefined, "u", false)).toBeNull();
  for (const v of [null, "", " ", "a\nb", "x".repeat(201), 123, ["a", "b"], { id: "a" }]) expect(identityTarget({ identitySubject: "id" }, v, false)).toBeNull();
  expect(identityTarget({ identitySubject: "id" }, "u", true)).toBeNull();
  expect(identityTarget({ identitySubject: "id" }, "u…[truncated: 1 of 10 bytes shown]", false)).toBeNull();
  expect(identityTarget({ identitySubject: "email" }, "not-email", false)).toBeNull();
  expect(identityTarget({ identitySubject: "id" }, subject.userId, false)).toEqual({ user: subject.userId });
 });
 it("keeps quoted keys as bound EQ values, never syntax or comma lists", () => {
  const links = appDataTargets([table], subject);
  expect(links).toHaveLength(2);
  expect(decodeFilter(links[0]!.search.f![0]!)).toEqual({ column: "identity.id", op: "eq", values: [subject.userId] });
  expect(decodeFilter(links[1]!.search.f![0]!)?.values).toEqual([subject.email]);
 });
 it("binds discovery to environment, rejects malformed state and bounds enumeration", () => {
  expect(parseAppDataSubject(JSON.stringify(subject), "staging-id")).toBeNull();
  expect(parseAppDataSubject("{}", "prod-id")).toBeNull();
  expect(parseAppDataSubject("not-json", "prod-id")).toBeNull();
  expect(parseAppDataSubject(JSON.stringify(subject), "prod-id")).toEqual(subject);
  expect(appDataTargets(Array.from({ length: 1000 }, () => table), subject)).toHaveLength(25);
 });
 it("does not select substring, ambiguous or incomplete email results", () => {
  expect(uniqueExactEmail([{ email: "otherada@example.com" }], "ada@example.com", true)).toBeNull();
  expect(uniqueExactEmail([{ email: "ada@example.com" }, { email: "ADA@example.com" }], "ada@example.com", true)).toBeNull();
  expect(uniqueExactEmail([{ email: "ada@example.com" }], "ada@example.com", false)).toBeNull();
  expect(uniqueExactEmail([{ email: "  ADA@example.com " }], "ada@example.com", true)).toEqual({ email: "  ADA@example.com " });
 });
 it("validates URL selection without dropping existing tabs and carries discovery across database choice", () => {
  expect(forgeConsoleEnvSearchSchema.parse({ tab: "running", user: "x".repeat(201), q: ["bad"] })).toMatchObject({ tab: "running", user: undefined, q: undefined });
  const search = forgeConsoleDataSearchSchema.parse({ subject: JSON.stringify(subject), env: "prod", db: "a" });
  expect(patchSearch(search, { db: "b" }).subject).toBe(JSON.stringify(subject));
  expect(JSON.stringify(search)).not.toContain("reason");
 });
});
