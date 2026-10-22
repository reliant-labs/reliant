// Copyright (c) 2025 Reliant Labs

/**
 * The slice of JSON Schema the builder reads from integration manifests:
 * an action's params/output schema and a trigger's payload schema
 * (CatalogEntry in catalog.proto). Read-only, and tolerant: a manifest is
 * validated on the server, so anything unexpected here is skipped rather than
 * thrown on.
 */

export interface JsonSchema {
  type?: string | string[];
  description?: string;
  title?: string;
  properties?: Record<string, JsonSchema>;
  required?: string[];
  items?: JsonSchema;
  enum?: unknown[];
  const?: unknown;
  default?: unknown;
  format?: string;
  pattern?: string;
  minimum?: number;
  maximum?: number;
  minLength?: number;
  maxLength?: number;
  examples?: unknown[];
  additionalProperties?: boolean | JsonSchema;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** The schema as a JsonSchema, or undefined when it is not an object schema at all. */
export function asJsonSchema(value: unknown): JsonSchema | undefined {
  return isRecord(value) ? (value as JsonSchema) : undefined;
}

/** The schema's primary type: the first non-null entry of a type union. */
export function schemaType(schema: JsonSchema | undefined): string | undefined {
  if (!schema) return undefined;
  if (Array.isArray(schema.type)) return schema.type.find((t) => t !== "null");
  if (schema.type) return schema.type;
  if (schema.properties) return "object";
  return undefined;
}

/** The properties of an object schema, in declaration order. */
export function schemaProperties(schema: JsonSchema | undefined): Array<[string, JsonSchema]> {
  if (!schema || !isRecord(schema.properties)) return [];
  return Object.entries(schema.properties).filter((entry): entry is [string, JsonSchema] => isRecord(entry[1]));
}

/**
 * The sub-schema at a dotted path of property names, descending through
 * object properties (and array items, so `labels.name` reaches an array of
 * objects' fields). Undefined when the path leaves the schema.
 */
export function schemaAtPath(schema: JsonSchema | undefined, path: readonly string[]): JsonSchema | undefined {
  let current = schema;
  for (const segment of path) {
    if (!current) return undefined;
    if (schemaType(current) === "array" && current.items) current = current.items;
    const next = current.properties?.[segment];
    current = isRecord(next) ? next : undefined;
  }
  if (current && schemaType(current) === "array" && current.items && path.length > 0) {
    // A path ending at an array completes the element's fields, which is what
    // `.exists(l, l.name …)` and indexing reach.
    return current;
  }
  return current;
}
