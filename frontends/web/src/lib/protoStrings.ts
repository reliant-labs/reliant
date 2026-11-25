// Copyright (c) 2026 Reliant Labs

/**
 * One generic walk over the strings of a protobuf message, by descriptor: at
 * any depth, through nested and repeated messages, message-valued maps and
 * set oneof cases. Never per-method code: what a caller decides is made from
 * the field's proto name and the value alone.
 *
 * Two readers, which must agree on what an id is (isIdField):
 *   - lib/sampleGuard.ts, the live transport's backstop: does a request carry
 *     the sample app or a sample id (someString)?
 *   - the shop fixture pack's id prefix (fixtures/shop/handlers/idPrefix.ts):
 *     the sample's pack mints every id with `sample_` (mapStrings).
 */

import type { DescField, DescMessage, Message } from "@bufbuild/protobuf";
import { isMessage, ScalarType } from "@bufbuild/protobuf";

/** Whether a field's proto name says it holds ids: `id`, `…_id`, `…_ids`. */
export function isIdField(name: string): boolean {
  return name === "id" || name === "ids" || name.endsWith("_id") || name.endsWith("_ids");
}

/**
 * How deep a message is walked. Every console message is far shallower; the
 * cap only stops a self-referencing message type from recursing for ever.
 */
const MAX_DEPTH = 16;

type Fields = Record<string, unknown>;

/** A field's value: its own, or its oneof's when that case is the one set; undefined otherwise. */
function valueOf(msg: Fields, field: DescField): unknown {
  if (!field.oneof) return msg[field.localName];
  const oneof = msg[field.oneof.localName] as { case?: string; value?: unknown } | undefined;
  return oneof?.case === field.localName ? oneof.value : undefined;
}

function setValue(msg: Fields, field: DescField, value: unknown): void {
  if (field.oneof) (msg[field.oneof.localName] as { value?: unknown }).value = value;
  else msg[field.localName] = value;
}

/** Whether `visit` holds for any string in `msg` (`field` is the field the string is in). */
export function someString(desc: DescMessage, msg: Message, visit: (field: DescField, value: string) => boolean, depth = 0): boolean {
  if (depth > MAX_DEPTH) return false;
  const fields = msg as unknown as Fields;
  const nested = (sub: DescMessage | undefined, value: unknown) => !!sub && isMessage(value) && someString(sub, value, visit, depth + 1);
  for (const field of desc.fields) {
    const value = valueOf(fields, field);
    if (value == null) continue;
    switch (field.fieldKind) {
      case "scalar":
        if (typeof value === "string" && visit(field, value)) return true;
        break;
      case "message":
        if (nested(field.message, value)) return true;
        break;
      case "list":
        if (field.listKind === "scalar" && (value as unknown[]).some((v) => typeof v === "string" && visit(field, v))) return true;
        if (field.listKind === "message" && (value as unknown[]).some((v) => nested(field.message, v))) return true;
        break;
      case "map": {
        const entries = value as Record<string, unknown>;
        if (field.mapKey === ScalarType.STRING && Object.keys(entries).some((key) => visit(field, key))) return true;
        const values = Object.values(entries);
        if (field.mapKind === "scalar" && values.some((v) => typeof v === "string" && visit(field, v))) return true;
        if (field.mapKind === "message" && values.some((v) => nested(field.message, v))) return true;
        break;
      }
    }
  }
  return false;
}

/**
 * Replaces every string in `msg`, IN PLACE, with `map(field, value)`. Callers
 * own the message they pass (a clone): a pack's answers are often module
 * constants, and a caller's request is the caller's.
 */
export function mapStrings(desc: DescMessage, msg: Message, map: (field: DescField, value: string) => string, depth = 0): void {
  if (depth > MAX_DEPTH) return;
  const fields = msg as unknown as Fields;
  const nested = (sub: DescMessage | undefined, value: unknown) => {
    if (sub && isMessage(value)) mapStrings(sub, value, map, depth + 1);
  };
  for (const field of desc.fields) {
    const value = valueOf(fields, field);
    if (value == null) continue;
    switch (field.fieldKind) {
      case "scalar":
        if (typeof value === "string") setValue(fields, field, map(field, value));
        break;
      case "message":
        nested(field.message, value);
        break;
      case "list": {
        const list = value as unknown[];
        if (field.listKind === "scalar") list.forEach((v, i) => typeof v === "string" && (list[i] = map(field, v)));
        if (field.listKind === "message") list.forEach((v) => nested(field.message, v));
        break;
      }
      case "map": {
        const entries = value as Record<string, unknown>;
        // Rebuild from a snapshot so renamed keys cannot overwrite an entry
        // before it has been visited (including when two keys swap places).
        const mappedEntries = Object.entries(entries).map(([key, v]) => {
          const mappedKey = field.mapKey === ScalarType.STRING ? map(field, key) : key;
          const mappedValue = field.mapKind === "scalar" && typeof v === "string" ? map(field, v) : v;
          if (field.mapKind === "message") nested(field.message, v);
          return [mappedKey, mappedValue];
        });
        setValue(fields, field, Object.fromEntries(mappedEntries));
        break;
      }
    }
  }
}
