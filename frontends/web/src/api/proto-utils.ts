// Copyright (c) 2025 Reliant Labs
// Canonical protobuf Value conversion helpers.
// All files that need to convert between JS values and google.protobuf.Value
// should import from this module.

import { fromJson, toJson } from "@bufbuild/protobuf";
import { ValueSchema, type Value } from "@bufbuild/protobuf/wkt";

/**
 * TypeScript representation of a protobuf Value's shape as produced by @bufbuild/protobuf.
 * The `{ case: undefined }` variant handles unset/default values.
 */
export type ProtoValue = {
  kind:
    | { case: "nullValue"; value: number }
    | { case: "numberValue"; value: number }
    | { case: "stringValue"; value: string }
    | { case: "boolValue"; value: boolean }
    | { case: "structValue"; value: { fields: Record<string, ProtoValue> } }
    | { case: "listValue"; value: { values: ProtoValue[] } }
    | { case: undefined };
};

/**
 * Convert protobuf Value to JavaScript value.
 * Thin wrapper around protobuf-es toJson.
 */
export function protoValueToJs(value: Value | ProtoValue | undefined): unknown {
  if (!value || !value.kind || value.kind.case === undefined) return undefined;
  try {
    return toJson(ValueSchema, value as Value);
  } catch {
    return undefined;
  }
}

/**
 * Convert JavaScript value to protobuf Value format.
 * Thin wrapper around protobuf-es fromJson.
 */
export function jsToProtoValue(value: unknown): Value {
  if (value === undefined) {
    return fromJson(ValueSchema, null);
  }
  return fromJson(ValueSchema, value as any);
}

/**
 * Unwrap an entire inputs record, converting each proto Value to its
 * plain JS equivalent. Non-proto values pass through unchanged.
 */
export function unwrapProtoInputs(inputs: Record<string, unknown>): Record<string, unknown> {
  const result: Record<string, unknown> = {};
  for (const [key, val] of Object.entries(inputs)) {
    result[key] = protoValueToJs(val as Value | ProtoValue | undefined);
  }
  return result;
}
