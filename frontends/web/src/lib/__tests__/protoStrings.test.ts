// Copyright (c) 2026 Reliant Labs

import { create } from "@bufbuild/protobuf";
import { StructSchema } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";

import { EnableWorkflowRequestSchema } from "@/gen/reliant/v1/trigger_pb";
import { mapStrings, someString } from "../protoStrings";
import { carriesSample } from "../sampleGuard";

describe("protobuf map strings", () => {
  it("finds sample names and ids in map keys", () => {
    const request = create(EnableWorkflowRequestSchema, { connectionIds: { sample_connection: "real-connection" } });
    expect(carriesSample(EnableWorkflowRequestSchema, request)).toBe(true);
    const message = create(StructSchema, { fields: { sample_shop: { kind: { case: "boolValue", value: true } } } });
    expect(carriesSample(StructSchema, message)).toBe(true);
    expect(someString(StructSchema, message, (_, value) => value === "absent")).toBe(false);
  });

  it("maps scalar map keys and values without losing swapped entries", () => {
    const request = create(EnableWorkflowRequestSchema, { connectionIds: { first: "one", second: "two" } });
    const replacements: Record<string, string> = { first: "second", second: "first" };
    mapStrings(EnableWorkflowRequestSchema, request, (_, value) => replacements[value] ?? value.toUpperCase());
    expect(request.connectionIds).toEqual({ second: "ONE", first: "TWO" });
  });

  it("maps message map keys and recursively maps their values", () => {
    const message = create(StructSchema, { fields: { outer: { kind: { case: "stringValue", value: "inner" } } } });
    mapStrings(StructSchema, message, (_, value) => `mapped_${value}`);
    expect(Object.keys(message.fields)).toEqual(["mapped_outer"]);
    expect(message.fields.mapped_outer.kind).toEqual({ case: "stringValue", value: "mapped_inner" });
    expect(someString(StructSchema, message, (_, value) => value === "mapped_inner")).toBe(true);
  });
});
