// Copyright (c) 2025 Reliant Labs

/**
 * What a field's "Insert data" picker offers: the values an expression in
 * this field can reach, as CEL paths, grouped the way an author thinks about
 * them (research/WORKFLOW_EDITOR_UX_REVIEW.md §1 issue 5):
 *
 *   - Inputs        `inputs.<name>`, the run's inputs
 *   - Trigger       `trigger.<field>`, and `trigger.payload.…` when the
 *                   workflow's declared trigger has a payload schema
 *   - each step that runs BEFORE this one: `nodes.<id>.<output>`
 *
 * A step's outputs are the ones its Outputs tab lists (the catalog's ListNodes
 * output fields, lib/nodeOutputFields), so the two never disagree about what
 * exists: message fields with their sub-fields, and the debug plumbing marked
 * advanced left out — it stays readable by hand, and on the Outputs tab under
 * Advanced. "Before" is the graph's ancestors of the current step: a step
 * that has not run yet has no outputs to read.
 */

import type { CELCompletionContextValue } from "../components/workflow/CELCompletionContext";
import { CONTEXT_NAMESPACES, type CELCompletionContext } from "./monaco-cel-completions";
import { schemaProperties, schemaType, type JsonSchema } from "./jsonSchema";
import { TRIGGER_CEL_FIELDS, triggerCelPath } from "./trigger-cel-fields";
import { childPathPrefix, type OutputField } from "./nodeOutputFields";

export interface InsertableField {
  /** The CEL path the picker inserts, e.g. `nodes.call_llm.tool_calls`. */
  path: string;
  type: string;
  description: string;
}

export interface InsertableGroup {
  id: string;
  label: string;
  /** A second line under the group label (a step's type). */
  detail?: string;
  fields: InsertableField[];
}

export interface InsertableDataInput {
  context: Pick<
    CELCompletionContextValue,
    "nodeIds" | "nodeTypeMap" | "inputParams" | "edges" | "nodeDeclaredOutputs" | "nodeOutputSchemas" | "triggerPayloadSchema"
  >;
  /** The step whose field is being edited; without one no step outputs are offered. */
  currentNodeId?: string | null;
  celContext?: CELCompletionContext["celContext"];
  /** The outputs a node type declares: its Outputs tab's (catalogToOutputFields of ListNodes). */
  nodeOutputFields: (nodeType: string) => OutputField[];
  /** A readable name for a node type ("Call LLM"). */
  nodeTypeLabel?: (nodeType: string) => string;
}

/**
 * The steps that run before `nodeId`: every node with a path of edges to it.
 * Edges may pass through nodes that are not steps (the entry node, switch
 * nodes for conditional edges), so the walk crosses any node and the result
 * keeps only `stepIds`, in their workflow order.
 */
export function upstreamStepIds(
  nodeId: string,
  edges: ReadonlyArray<{ source: string; target: string }> | undefined,
  stepIds: readonly string[],
): string[] {
  const incoming = new Map<string, string[]>();
  for (const edge of edges ?? []) {
    const sources = incoming.get(edge.target);
    if (sources) sources.push(edge.source);
    else incoming.set(edge.target, [edge.source]);
  }
  const seen = new Set<string>();
  const queue = [nodeId];
  while (queue.length > 0) {
    const current = queue.shift()!;
    for (const source of incoming.get(current) ?? []) {
      if (seen.has(source) || source === nodeId) continue;
      seen.add(source);
      queue.push(source);
    }
  }
  return stepIds.filter((id) => seen.has(id));
}

/** A field type as an author reads it: a proto message is an object. */
function readableType(type: string): string {
  switch (type) {
    case "message":
      return "object";
    case "int":
    case "int32":
    case "int64":
      return "integer";
    case "double":
    case "float":
      return "number";
    case "bool":
      return "boolean";
    default:
      return type || "any";
  }
}

function schemaFieldType(schema: JsonSchema): string {
  const type = schemaType(schema);
  if (type === "array") {
    const item = schemaType(schema.items);
    return item ? `list of ${item}` : "list";
  }
  return type ?? "any";
}

/** `<prefix>.<prop>` for each property of an object schema, one level deep. */
function schemaFields(prefix: string, schema: JsonSchema | undefined): InsertableField[] {
  return schemaProperties(schema).map(([name, property]) => ({
    path: `${prefix}.${name}`,
    type: schemaFieldType(property),
    description: property.description?.trim() ?? "",
  }));
}

function stepGroup(id: string, input: InsertableDataInput): InsertableGroup {
  const { context } = input;
  const nodeType = context.nodeTypeMap[id];
  const fields: InsertableField[] = [];
  const seen = new Set<string>();
  const add = (field: InsertableField) => {
    if (seen.has(field.path)) return;
    seen.add(field.path);
    fields.push(field);
  };
  // Declared outputs (a router's `outputs:`) are what the author named, so
  // they lead.
  for (const name of context.nodeDeclaredOutputs?.[id] ?? []) {
    add({ path: `nodes.${id}.${name}`, type: "any", description: "Declared output" });
  }
  for (const field of nodeType ? input.nodeOutputFields(nodeType) : []) {
    if (field.advanced) continue;
    const path = `nodes.${id}.${field.name}`;
    add({ path, type: readableType(field.type), description: field.description });
    // One level of sub-fields (message.text, tool_calls[0].name): what a
    // template usually wants is inside the object, not the object.
    for (const child of field.children ?? []) {
      if (child.advanced) continue;
      add({ path: `${childPathPrefix(field, path)}.${child.name}`, type: readableType(child.type), description: child.description });
    }
  }
  // An integration action's `data` is typed by its manifest's output schema.
  const dataSchema = context.nodeOutputSchemas?.[id];
  if (dataSchema) {
    add({ path: `nodes.${id}.data`, type: "object", description: "The action's result" });
    for (const field of schemaFields(`nodes.${id}.data`, dataSchema)) add(field);
  }
  // Nothing known about its outputs (the catalog has not loaded, or the type
  // declares none): the step is still upstream, and its whole output is
  // still readable. Dropping it would read as "nothing runs before this".
  if (fields.length === 0) {
    add({ path: `nodes.${id}`, type: "object", description: "Everything this step produced" });
  }
  return {
    id: `node:${id}`,
    label: id,
    detail: nodeType ? (input.nodeTypeLabel?.(nodeType) ?? nodeType) : undefined,
    fields,
  };
}

/** Every group the picker offers, in the order it lists them. */
export function insertableData(input: InsertableDataInput): InsertableGroup[] {
  const { context, currentNodeId } = input;
  const allowed = new Set(CONTEXT_NAMESPACES[input.celContext ?? "default"] ?? CONTEXT_NAMESPACES.default);
  const groups: InsertableGroup[] = [];

  if (allowed.has("inputs")) {
    const fields = Object.entries(context.inputParams ?? {}).map(([name, info]) => ({
      path: `inputs.${name}`,
      type: info.type,
      description: info.description ?? "",
    }));
    if (fields.length > 0) groups.push({ id: "inputs", label: "Inputs", detail: "This run's inputs", fields });
  }

  if (allowed.has("trigger")) {
    const fields: InsertableField[] = TRIGGER_CEL_FIELDS.map((field) => ({
      path: triggerCelPath(field),
      type: field.type,
      description: field.description,
    }));
    // The declared trigger's payload, two levels deep: the envelope's
    // `data` is where an integration event's fields live.
    for (const field of schemaFields("trigger.payload", context.triggerPayloadSchema)) {
      fields.push(field);
      const name = field.path.slice("trigger.payload.".length);
      const nested = context.triggerPayloadSchema?.properties?.[name];
      if (nested && schemaType(nested) === "object") fields.push(...schemaFields(field.path, nested));
    }
    groups.push({ id: "trigger", label: "Trigger", detail: "The event that started the run", fields });
  }

  if (allowed.has("nodes") && currentNodeId) {
    for (const id of upstreamStepIds(currentNodeId, context.edges, context.nodeIds)) {
      groups.push(stepGroup(id, input));
    }
  }
  return groups;
}

/** Whether a field matches a search: by path, type or description, case-insensitively. */
export function matchesSearch(field: InsertableField, group: InsertableGroup, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [field.path, field.type, field.description, group.label, group.detail ?? ""].some((text) => text.toLowerCase().includes(q));
}
