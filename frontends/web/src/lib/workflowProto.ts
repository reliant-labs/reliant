// Copyright (c) 2025 Reliant Labs

/**
 * The builder's Workflow as the reliant.v1.Workflow message a save or a
 * validation sends — and the one safe way to serialize, key or compare it.
 *
 * Builder state mixes protobuf-es messages (as GetWorkflow decoded them) with
 * plain init objects (built in the editor). In both, every int64 field is a
 * bigint: an integer input's default, min and max, and CelInt literals such
 * as call_llm's max_tokens. JSON.stringify throws on a bigint ("Do not know
 * how to serialize a BigInt"), so a workflow is never JSON.stringify'd: it
 * goes through this message and protobuf-es's JSON (toJsonString), which
 * writes int64 as a decimal string and reads it back exactly. To copy one,
 * use structuredClone; to compare plain data that may hold one,
 * structurallyEqual.
 */

import { create, type MessageInitShape } from "@bufbuild/protobuf";

import { WorkflowSchema, type Workflow as WorkflowMessage } from "../gen/reliant/v1/workflow_v2_pb";
import type { Workflow } from "../types/workflow";

/**
 * Recursively strips $typeName from an object tree.
 *
 * protobuf-es's create(Schema, init) short-circuits when init has a matching
 * $typeName — it returns the object as-is without processing nested fields.
 * When JS spread ({...protoMsg}) copies $typeName but leaves nested objects
 * as plain (no $typeName), serialization fails with "cannot use field X with
 * message undefined". Stripping $typeName forces create() to always take the
 * full recursive initMessage path.
 */
function stripProtoMeta(obj: unknown): unknown {
  if (obj == null || typeof obj !== "object") return obj;
  if (obj instanceof Uint8Array) return obj;
  if (Array.isArray(obj)) return obj.map(stripProtoMeta);
  const result: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(obj)) {
    if (key === "$typeName" || key === "$unknown") continue;
    result[key] = stripProtoMeta(value);
  }
  return result;
}

function toWorkflowInit(workflow: Workflow): MessageInitShape<typeof WorkflowSchema> {
  // Strip $typeName from the entire object tree so create() always takes the
  // full recursive initMessage path, properly constructing all nested messages.
  const plain = stripProtoMeta(workflow) as Workflow;

  // Normalize edge.default and edge case .to from string|string[] to string[]
  // Proto expects repeated string fields; passing a bare string causes character-by-character iteration.
  const normalizedEdges = (plain.edges || []).map(edge => ({
    ...edge,
    default: edge.default ? (Array.isArray(edge.default) ? edge.default : [edge.default]) : [],
    cases: (edge.cases || []).map(c => ({
      ...c,
      to: c.to ? (Array.isArray(c.to) ? c.to : [c.to]) : [],
    })),
  }));
  return { ...plain, edges: normalizedEdges } as MessageInitShape<typeof WorkflowSchema>;
}

/**
 * The builder's workflow as a fresh reliant.v1.Workflow message, sharing no
 * objects with it. Serialize it with toJsonString(WorkflowSchema, …), which
 * throws — as a save would — on a field holding a value its type cannot encode.
 */
export function toWorkflowMessage(workflow: Workflow): WorkflowMessage {
  return create(WorkflowSchema, toWorkflowInit(workflow));
}
