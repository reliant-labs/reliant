/**
 * A workflow with every int64 field it can reach set to a bigint, built from
 * the proto descriptors — so a field added to the proto later is in it too.
 *
 * protobuf-es decodes int64 as a bigint, and JSON.stringify throws on one.
 * Tests that run a workflow through a serialize/copy/key/compare path use
 * this fixture to prove the path is bigint-safe for every int64 field there
 * is, not just the one that last broke.
 */

import { create, ScalarType, type DescField, type DescMessage } from "@bufbuild/protobuf";

import { WorkflowSchema } from "../gen/reliant/v1/workflow_v2_pb";
import type { Workflow } from "../types/workflow";

/** 2^53 + 1: past what a JS number holds exactly. */
export const BIG_INT64 = 2n ** 53n + 1n;

const INT64_TYPES = new Set([
  ScalarType.INT64,
  ScalarType.UINT64,
  ScalarType.SINT64,
  ScalarType.FIXED64,
  ScalarType.SFIXED64,
]);

function scalarOf(field: DescField): ScalarType | undefined {
  if (field.fieldKind === "scalar") return field.scalar;
  if (field.fieldKind === "list" && field.listKind === "scalar") return field.scalar;
  if (field.fieldKind === "map" && field.mapKind === "scalar") return field.scalar;
  return undefined;
}

function messageOf(field: DescField): DescMessage | undefined {
  if (field.fieldKind === "message") return field.message;
  if (field.fieldKind === "list" && field.listKind === "message") return field.message;
  if (field.fieldKind === "map" && field.mapKind === "message") return field.message;
  return undefined;
}

const isInt64 = (field: DescField) => {
  const scalar = scalarOf(field);
  return scalar !== undefined && INT64_TYPES.has(scalar);
};

/** Every int64 field reachable from `root`, as "type.field". */
export function int64FieldNames(root: DescMessage): Set<string> {
  const names = new Set<string>();
  const seen = new Set<string>();
  const visit = (desc: DescMessage) => {
    if (seen.has(desc.typeName)) return;
    seen.add(desc.typeName);
    for (const field of desc.fields) {
      if (isInt64(field)) names.add(`${desc.typeName}.${field.name}`);
      const sub = messageOf(field);
      if (sub) visit(sub);
    }
  };
  visit(root);
  return names;
}

/** The int64 fields actually holding a bigint in `message`, as "type.field". */
export function int64FieldsSetIn(desc: DescMessage, message: unknown, out = new Set<string>()): Set<string> {
  const record = message as Record<string, unknown>;
  const visitValue = (field: DescField, value: unknown) => {
    if (typeof value === "bigint" && isInt64(field)) out.add(`${desc.typeName}.${field.name}`);
    const sub = messageOf(field);
    if (sub && value && typeof value === "object") int64FieldsSetIn(sub, value, out);
  };
  for (const field of desc.fields) {
    const raw = field.oneof
      ? (record[field.oneof.localName] as { case?: string; value?: unknown } | undefined)?.case === field.localName
        ? (record[field.oneof.localName] as { value: unknown }).value
        : undefined
      : record[field.localName];
    if (raw === undefined) continue;
    if (field.fieldKind === "list") (raw as unknown[]).forEach((v) => visitValue(field, v));
    else if (field.fieldKind === "map") Object.values(raw as object).forEach((v) => visitValue(field, v));
    else visitValue(field, raw);
  }
  return out;
}

/**
 * Inits for `desc` that between them set every int64 field below it. A oneof
 * holds one case at a time, so there is one init per case that reaches an
 * int64; a list or map of such messages holds one element per init. A message
 * already being built higher up (an inline sub-workflow) is cut off.
 */
function variants(desc: DescMessage, building: ReadonlySet<string>, reaches: (d: DescMessage) => boolean): Array<Record<string, unknown>> {
  if (building.has(desc.typeName)) return [];
  const inner = new Set(building).add(desc.typeName);
  const base: Record<string, unknown> = {};
  for (const field of desc.fields) {
    if (field.oneof) continue;
    if (isInt64(field)) {
      base[field.localName] =
        field.fieldKind === "list" ? [BIG_INT64] : field.fieldKind === "map" ? { k0: BIG_INT64 } : BIG_INT64;
      continue;
    }
    const sub = messageOf(field);
    if (!sub || !reaches(sub)) continue;
    const subs = variants(sub, inner, reaches);
    if (subs.length === 0) continue;
    if (field.fieldKind === "list") base[field.localName] = subs;
    else if (field.fieldKind === "map") base[field.localName] = Object.fromEntries(subs.map((v, i) => [`k${i}`, v]));
    else base[field.localName] = subs[0];
  }

  const out: Array<Record<string, unknown>> = [];
  for (const oneof of desc.oneofs) {
    for (const field of oneof.fields) {
      if (isInt64(field)) {
        out.push({ ...base, [oneof.localName]: { case: field.localName, value: BIG_INT64 } });
        continue;
      }
      const sub = messageOf(field);
      if (!sub || !reaches(sub)) continue;
      for (const value of variants(sub, inner, reaches)) {
        out.push({ ...base, [oneof.localName]: { case: field.localName, value } });
      }
    }
  }
  return out.length > 0 ? out : [base];
}

/** As GetWorkflow hands it to the builder: a decoded message, int64s as bigints. */
export function everyInt64Workflow(): Workflow {
  const reachable = new Map<string, boolean>();
  const reaches = (desc: DescMessage): boolean => {
    const known = reachable.get(desc.typeName);
    if (known !== undefined) return known;
    reachable.set(desc.typeName, false); // cycle guard: settled below
    const result = desc.fields.some((field) => isInt64(field) || (messageOf(field) ? reaches(messageOf(field)!) : false));
    reachable.set(desc.typeName, result);
    return result;
  };

  const [init] = variants(WorkflowSchema, new Set(), reaches);
  const nodeCases = WorkflowSchema.fields.find((f) => f.localName === "nodes")!;
  const argsOneof = (nodeCases.fieldKind === "list" && nodeCases.listKind === "message" ? nodeCases.message : undefined)!
    .oneofs.find((o) => o.localName === "args")!;
  const nodes = ((init.nodes as Array<Record<string, unknown>>) ?? []).map((node, i) => {
    const argsCase = (node.args as { case?: string } | undefined)?.case;
    const type = argsOneof.fields.find((f) => f.localName === argsCase)?.name ?? "";
    return { ...node, id: `n${i}`, type };
  });
  const inputs = Object.fromEntries(
    Object.entries((init.inputs as Record<string, Record<string, unknown>>) ?? {}).map(([key, input]) => {
      const configCase = (input.config as { case?: string } | undefined)?.case ?? "";
      return [key, { ...input, type: configCase.replace(/Input$/, "") }];
    }),
  );
  return create(WorkflowSchema, {
    ...init,
    name: "every-int64",
    nodes,
    inputs,
    entry: nodes.length > 0 ? [nodes[0].id as string] : [],
  } as never) as unknown as Workflow;
}
