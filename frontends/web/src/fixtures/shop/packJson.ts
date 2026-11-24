// Copyright (c) 2025 Reliant Labs

/**
 * Loading the pack's static JSON (data/*.json), strictly and on the pack clock.
 *
 * Each file is the proto3 JSON of one message, decoded with protobuf-es
 * `fromJson` WITHOUT `ignoreUnknownFields`: a field the proto no longer has,
 * a misspelt key or an enum name that does not exist is a load error, so a
 * fixture cannot drift from the contract unseen. This is the check forge's
 * `fixtureJson` (forge#2716) makes; `fixtureJson` below has its name and
 * signature, and becomes a re-export of forge's once a release ships it.
 *
 * The files are written at one instant, DATA_ANCHOR (the screenshot harness's
 * FIXED_NOW). `packJson` moves every instant in a decoded message, a
 * `google.protobuf.Timestamp` or an RFC 3339 string field (reliant's protos
 * carry most times as strings), by `now - DATA_ANCHOR`, so "5m ago" reads
 * "5m ago" on any clock. Under the harness the shift is zero and the bytes
 * are the file's.
 */

import { fromJson, ScalarType, type DescField, type DescMessage, type JsonValue, type MessageShape } from "@bufbuild/protobuf";
import { timestampFromMs, timestampMs, type Timestamp } from "@bufbuild/protobuf/wkt";

import { demoNow } from "./shopStory";

/** The instant data/*.json is written at: 2026-11-18T15:30:00Z. */
export const DATA_ANCHOR = Date.UTC(2026, 10, 18, 15, 30, 0);

/**
 * One message from its proto3 JSON, strictly: an unknown field is an error.
 * `json` is the parsed value or the file's text (a `?raw` import, which keeps
 * a large file's literal type out of the type checker).
 */
export function fixtureJson<Desc extends DescMessage>(schema: Desc, json: unknown): MessageShape<Desc> {
  return fromJson(schema, (typeof json === "string" ? JSON.parse(json) : json) as JsonValue);
}

/** `fixtureJson`, with every instant moved from DATA_ANCHOR to the pack clock's `now`. */
export function packJson<Desc extends DescMessage>(schema: Desc, json: unknown, now: number = demoNow()): MessageShape<Desc> {
  const message = fixtureJson(schema, json);
  const delta = now - DATA_ANCHOR;
  return delta === 0 ? message : (shiftMessage(schema, message, delta) as MessageShape<Desc>);
}

const INSTANT = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/;

function shiftMessage(desc: DescMessage, message: unknown, delta: number): unknown {
  if (message == null) return message;
  if (desc.typeName === "google.protobuf.Timestamp") return timestampFromMs(timestampMs(message as Timestamp) + delta);
  const m = message as Record<string, unknown>;
  for (const member of desc.members) {
    if (member.kind === "oneof") {
      const chosen = m[member.localName] as { case?: string; value?: unknown } | undefined;
      const field = member.fields.find((f) => f.localName === chosen?.case);
      if (chosen && field) chosen.value = shiftSingle(field, chosen.value, delta);
      continue;
    }
    m[member.localName] = shiftField(member, m[member.localName], delta);
  }
  return m;
}

function shiftScalar(scalar: ScalarType, value: unknown, delta: number): unknown {
  return scalar === ScalarType.STRING && typeof value === "string" && INSTANT.test(value) ? new Date(Date.parse(value) + delta).toISOString() : value;
}

/** A singular field's value (a oneof member is one). */
function shiftSingle(field: DescField, value: unknown, delta: number): unknown {
  if (field.fieldKind === "scalar") return shiftScalar(field.scalar, value, delta);
  if (field.fieldKind === "message") return shiftMessage(field.message, value, delta);
  return value;
}

function shiftField(field: DescField, value: unknown, delta: number): unknown {
  switch (field.fieldKind) {
    case "list": {
      const list = value as unknown[];
      if (field.listKind === "message") return list.map((v) => shiftMessage(field.message, v, delta));
      if (field.listKind === "scalar") return list.map((v) => shiftScalar(field.scalar, v, delta));
      return list;
    }
    case "map": {
      const map = value as Record<string, unknown>;
      for (const key of Object.keys(map)) {
        if (field.mapKind === "message") map[key] = shiftMessage(field.message, map[key], delta);
        else if (field.mapKind === "scalar") map[key] = shiftScalar(field.scalar, map[key], delta);
      }
      return map;
    }
    default:
      return shiftSingle(field, value, delta);
  }
}
