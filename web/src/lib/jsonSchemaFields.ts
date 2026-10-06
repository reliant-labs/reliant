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

/** What the ? popover adds to the inline description: the default and range. */
function extrasFor(property: JsonSchema): string | undefined {
  const parts: string[] = [];
  if (property.default !== undefined) parts.push(`Default: ${JSON.stringify(property.default)}`);
  if (property.minimum !== undefined && property.maximum !== undefined) parts.push(`Range: ${property.minimum} – ${property.maximum}`);
  return parts.length > 0 ? parts.join(". ") : undefined;
}

function isScalar(value: unknown): value is string | number | boolean {
  return typeof value === "string" || typeof value === "number" || typeof value === "boolean";
}

/** examples[0] as the text an author would type into this field's input. */
function exampleFor(property: JsonSchema, kind: ParamValueKind): string | undefined {
  const example = property.examples?.[0];
  if (example === undefined || example === null) return undefined;
  if (isScalar(example)) return String(example);
  if (kind === "list" && Array.isArray(example) && example.every(isScalar)) return example.map(String).join(", ");
  return JSON.stringify(example);
}

/** A fallback placeholder for the shapes that are hardest to guess, when there is no example. */
function fallbackPlaceholder(kind: ParamValueKind): string | undefined {
  if (kind === "list") return "one, two, three";
  if (kind === "json") return '{"key": "value"}';
  return undefined;
}

const FORMAT_HINTS: Record<string, string> = {
  uri: "URL",
  email: "email address",
  "date-time": "date and time, RFC 3339",
  date: "date, YYYY-MM-DD",
};

/**
 * The kind of value, when the widget does not already say it: a number box,
 * a toggle and a dropdown need none, but a textarea could hold anything.
 */
function typeHintFor(property: JsonSchema, kind: ParamValueKind): string | undefined {
  switch (kind) {
    case "list":
      return "list, comma-separated";
    case "json": {
      const type = schemaType(property);
      if (type === "object") return "JSON object";
      if (type === "array") return "JSON list";
      return "JSON value";
    }
    case "string":
      return property.format ? FORMAT_HINTS[property.format] : undefined;
    default:
      return undefined;
  }
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

/**
 * The form's order: required params first, then the rest, each in the order
 * the manifest declares them (`order`, CatalogEntry.param_order). Never
 * alphabetical: Post message's author lists Channel and Text before Blocks.
 * A param missing from `order` keeps the schema's order, after the rest.
 */
function orderProperties(entries: Array<[string, JsonSchema]>, required: Set<string>, order: readonly string[]): Array<[string, JsonSchema]> {
  const rank = new Map(order.map((name, index) => [name, index]));
  const position = (name: string, fallback: number) => rank.get(name) ?? order.length + fallback;
  return entries
    .map((entry, index) => ({ entry, required: required.has(entry[0]), at: position(entry[0], index) }))
    .sort((a, b) => Number(b.required) - Number(a.required) || a.at - b.at)
    .map(({ entry }) => entry);
}

export function actionParamFields(paramsSchema: JsonSchema | undefined, order: readonly string[] = []): ActionParamField[] {
  const required = new Set(paramsSchema?.required ?? []);
  return orderProperties(schemaProperties(paramsSchema), required, order)
    .filter(([name]) => !RESERVED_PARAMS.has(name))
    .map(([name, property]) => {
      const isRequired = required.has(name);
      const valueKind = valueKindOf(property);
      const example = exampleFor(property, valueKind);
      const base = {
        key: name,
        label: isRequired ? `${labelFor(name, property)} *` : labelFor(name, property),
        description: property.description?.trim() || undefined,
        helpText: extrasFor(property),
        example,
        placeholder: example === undefined ? fallbackPlaceholder(valueKind) : undefined,
        typeHint: Array.isArray(property.enum) && property.enum.length > 0 ? undefined : typeHintFor(property, valueKind),
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
          // Unset means the default, which the select shows selected; an
          // extra "None" beside it would offer the same value twice.
          allowEmptyOption: !isRequired && property.default === undefined,
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
