// Copyright (c) 2025 Reliant Labs

/**
 * An integration action's params JSON Schema → the ProtoFieldSchema list
 * ProtoFieldRenderer draws: the same renderer, and the same CEL toggle, that
 * every built-in node's config uses (nodeFieldAdapter.ts is the NodeInfo
 * twin of this). No per-integration components.
 *
 * Values cross this boundary as plain JS (what `with:` holds once unwrapped
 * from proto Values) and as the renderer's strings/booleans/numbers:
 *
 *   - string / enum      text / select; a `{{ … }}` value is a template
 *   - integer / number   number input, or a template in CEL mode
 *   - boolean            toggle, or a template in CEL mode
 *   - array of scalars   comma-separated list
 *   - object / other     JSON text, or a template
 */

import type { ProtoFieldSchema } from "../types/workflowFieldSchema";
import { schemaProperties, schemaType, type JsonSchema } from "./jsonSchema";

/** Params the action form does not render: the connection has its own picker. */
const RESERVED_PARAMS = new Set(["connection"]);

export type ParamValueKind = "string" | "number" | "integer" | "boolean" | "list" | "json";

export interface ActionParamField {
  name: string;
  required: boolean;
  valueKind: ParamValueKind;
  schema: ProtoFieldSchema;
  defaultValue?: unknown;
}

function labelFor(name: string, property: JsonSchema): string {
  if (property.title) return property.title;
  const words = name.replace(/[_-]+/g, " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

function helpFor(property: JsonSchema, required: boolean): string | undefined {
  const parts: string[] = [];
  if (property.description) parts.push(property.description.trim());
  if (property.default !== undefined) parts.push(`Default: ${JSON.stringify(property.default)}`);
  if (property.minimum !== undefined && property.maximum !== undefined) parts.push(`Range: ${property.minimum} – ${property.maximum}`);
  if (required) parts.push("Required");
  return parts.length > 0 ? parts.join(". ") : undefined;
}

function placeholderFor(property: JsonSchema, kind: ParamValueKind): string | undefined {
  const example = property.examples?.[0];
  if (typeof example === "string" || typeof example === "number") return String(example);
  if (kind === "list") return "one, two, three";
  if (kind === "json") return '{"key": "value"} or {{ nodes.x.data }}';
  return undefined;
}

function valueKindOf(property: JsonSchema): ParamValueKind {
  const type = schemaType(property);
  switch (type) {
    case "integer":
      return "integer";
    case "number":
      return "number";
    case "boolean":
      return "boolean";
    case "array": {
      const itemType = schemaType(property.items);
      return !itemType || itemType === "string" || itemType === "integer" || itemType === "number" ? "list" : "json";
    }
    case "object":
      return "json";
    default:
      return "string";
  }
}

export function actionParamFields(paramsSchema: JsonSchema | undefined): ActionParamField[] {
  const required = new Set(paramsSchema?.required ?? []);
  return schemaProperties(paramsSchema)
    .filter(([name]) => !RESERVED_PARAMS.has(name))
    .map(([name, property]) => {
      const isRequired = required.has(name);
      const valueKind = valueKindOf(property);
      const base = {
        key: name,
        label: isRequired ? `${labelFor(name, property)} *` : labelFor(name, property),
        description: property.description,
        helpText: helpFor(property, isRequired),
        placeholder: placeholderFor(property, valueKind),
        celCapable: true,
        defaultValue: property.default,
        minValue: property.minimum,
        maxValue: property.maximum,
      };
      let schema: ProtoFieldSchema;
      if (Array.isArray(property.enum) && property.enum.length > 0 && valueKind === "string") {
        schema = {
          ...base,
          widget: "select",
          valueKind: "string",
          showCelModeToggle: true,
          options: property.enum.map((option) => ({ value: String(option), label: String(option) })),
          allowEmptyOption: !isRequired,
          emptyOptionLabel: "None",
        };
      } else if (valueKind === "boolean") {
        schema = { ...base, widget: "checkbox", valueKind: "boolean" };
      } else if (valueKind === "integer" || valueKind === "number") {
        schema = { ...base, widget: "number", isInteger: valueKind === "integer", showCelModeToggle: true };
      } else if (valueKind === "json") {
        schema = { ...base, widget: "textarea", valueKind: "string" };
      } else if (valueKind === "list") {
        // stringList + textarea is the combination ProtoFieldRenderer holds a
        // draft for, so a separator still being typed (", ") is not eaten.
        schema = { ...base, widget: "textarea", valueKind: "stringList" };
      } else {
        const long = property.format === "markdown" || /body|text|message|description|content/i.test(name);
        schema = { ...base, widget: long ? "textarea" : "text", valueKind: "string" };
      }
      return { name, required: isRequired, valueKind, schema, defaultValue: property.default };
    });
}

const TEMPLATE = /\{\{[\s\S]*\}\}/;

/** A `with:` value as the renderer's control value. */
export function paramToFieldValue(field: ActionParamField, value: unknown): unknown {
  if (value === undefined || value === null) return undefined;
  if (typeof value === "string" && TEMPLATE.test(value)) return value;
  switch (field.valueKind) {
    case "boolean":
      return typeof value === "boolean" ? value : value === "true";
    case "integer":
    case "number":
      return typeof value === "number" ? value : String(value);
    case "list":
      return Array.isArray(value) ? value.map(String).join(", ") : String(value);
    case "json":
      return typeof value === "string" ? value : JSON.stringify(value, null, 2);
    default:
      return typeof value === "string" ? value : JSON.stringify(value);
  }
}

/**
 * The renderer's control value back to a `with:` value. A template is kept
 * as a string whatever the type; JSON that does not parse yet is kept as text
 * so a half-typed object is not lost (server validation reports it).
 */
export function fieldValueToParam(field: ActionParamField, value: unknown): unknown {
  if (value === undefined || value === null || value === "") return undefined;
  if (typeof value === "string" && TEMPLATE.test(value)) return value;
  switch (field.valueKind) {
    case "boolean":
      return typeof value === "boolean" ? value : value === "true";
    case "integer":
    case "number": {
      const parsed = typeof value === "number" ? value : Number(value);
      return Number.isFinite(parsed) ? (field.valueKind === "integer" ? Math.trunc(parsed) : parsed) : String(value);
    }
    case "list":
      return String(value)
        .split(",")
        .map((entry) => entry.trim())
        .filter(Boolean);
    case "json":
      try {
        return JSON.parse(String(value));
      } catch {
        return String(value);
      }
    default:
      return String(value);
  }
}

/** Declared defaults for a new action node. */
export function actionParamDefaults(paramsSchema: JsonSchema | undefined): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const field of actionParamFields(paramsSchema)) {
    if (field.defaultValue !== undefined) out[field.name] = field.defaultValue;
  }
  return out;
}

/** Required params with no value, for the "fill these in" hint. */
export function missingRequiredParams(fields: ActionParamField[], params: Record<string, unknown>): string[] {
  return fields
    .filter((field) => field.required)
    .filter((field) => {
      const value = params[field.name];
      return value === undefined || value === "" || (Array.isArray(value) && value.length === 0);
    })
    .map((field) => field.name);
}
