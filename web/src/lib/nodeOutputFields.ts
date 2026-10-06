// Copyright (c) 2025 Reliant Labs

/**
 * A node type's output fields as the builder shows them, from the one source
 * both the Outputs tab and the Insert data picker read: the catalog's
 * ListNodes output fields (CatalogService.ListNodes).
 */

import type { NodeInputField } from "../gen/reliant/v1/catalog_pb";

export interface OutputField {
  name: string;
  type: string;
  description: string;
  /** Sub-fields for nested/message types (a list's are its items' fields). */
  children?: OutputField[];
  /** Debug and plumbing fields, listed under "Advanced" (FieldMeta visibility_contexts). */
  advanced?: boolean;
}

const ADVANCED_CONTEXTS = new Set(["advanced", "debug"]);

/**
 * The catalog's output fields, as the server lists them: message fields with
 * their sub-fields (tool_calls[].name, message.text), and debug plumbing
 * marked advanced (FieldMeta visibility_contexts).
 */
export function catalogToOutputFields(fields: readonly NodeInputField[] | undefined): OutputField[] {
  if (!fields || fields.length === 0) return [];
  return fields.map((f) => ({
    name: f.name,
    type: f.type,
    description: f.description,
    children: f.children && f.children.length > 0 ? catalogToOutputFields(f.children) : undefined,
    advanced: (f.visibilityContexts ?? []).some((context) => ADVANCED_CONTEXTS.has(context)),
  }));
}

/** The CEL path of a field's sub-fields: a list's are read through its first item. */
export function childPathPrefix(field: Pick<OutputField, "type">, path: string): string {
  return field.type === "array" ? `${path}[0]` : path;
}
