// Copyright (c) 2025 Reliant Labs

/**
 * The value model behind RunWorkflowForm, as pure functions.
 *
 * A run's inputs are stored the way the wire carries them — StartChat's
 * `workflow_params` and a trigger's `params` are the same shape:
 *
 *   presets: group name → preset name ("" is the workflow-level group)
 *   params:  NESTED explicit overrides, e.g. { depth: 3, review: { strictness: "high" } }
 *
 * Presets are kept as references, not copied into params. A trigger that says
 * "use preset fast" keeps following that preset when it is edited, which is
 * what the server does at fire time (presets first, params override). Copying
 * the preset's values into params — what the composer does for a one-off
 * send — would freeze them into the automation forever.
 *
 * The input renderers (WorkflowInputGroup → ProtoFieldRenderer) key values
 * FLAT ("review.strictness") and speak display strings for lists, so this
 * module also converts between the two.
 */

import type { Preset } from "@/api/preset-grpc";
import type { InputDef } from "@/lib/inputHelpers";
import { getInputDefault } from "@/lib/inputHelpers";
import type { InputGroupDef } from "../WorkflowInputGroup";

export interface RunWorkflowValue {
  /** Preset per input group; "" is the workflow-level group. */
  presets: Record<string, string>;
  /** Explicit input values, nested by group, as plain JSON. */
  params: Record<string, unknown>;
  /** Unset runs in the project's main checkout. */
  worktreeId?: string;
}

export const EMPTY_RUN_VALUE: RunWorkflowValue = { presets: {}, params: {} };

/** How many input settings (presets plus explicit values) a value carries. */
export function countRunInputs(value: RunWorkflowValue): number {
  const presets = Object.values(value.presets).filter(Boolean).length;
  return presets + Object.keys(value.params).length;
}

/** "" and "default" both name the workflow-level group on the server. */
export function normalizeGroupName(name: string): string {
  return name === "default" ? "" : name;
}

/** The names of the declared input groups (excluding the top level). */
export function groupNamesOf(groups: InputGroupDef[]): Set<string> {
  return new Set(groups.map((g) => g.name).filter(Boolean));
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * Nested wire params → flat renderer keys. Only declared GROUPS are
 * flattened: a top-level object input (a model selector, say) is a value,
 * not a group, and must stay intact.
 */
export function flattenParams(
  params: Record<string, unknown>,
  groupNames: Set<string>,
): Record<string, unknown> {
  const flat: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(params)) {
    if (groupNames.has(key) && isPlainObject(value)) {
      for (const [nestedKey, nestedValue] of Object.entries(value)) {
        flat[`${key}.${nestedKey}`] = nestedValue;
      }
    } else {
      flat[key] = value;
    }
  }
  return flat;
}

/** Flat renderer keys → nested wire params; the inverse of flattenParams. */
export function nestParams(
  flat: Record<string, unknown>,
  groupNames: Set<string>,
): Record<string, unknown> {
  const nested: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(flat)) {
    const dot = key.indexOf(".");
    const group = dot > 0 ? key.slice(0, dot) : "";
    if (group && groupNames.has(group)) {
      const existing = isPlainObject(nested[group]) ? nested[group] : {};
      nested[group] = { ...existing, [key.slice(dot + 1)]: value };
    } else {
      nested[key] = value;
    }
  }
  return nested;
}

/** A preset may carry a bare model id; the inputs speak {id}. */
function normalizePresetValue(key: string, value: unknown): unknown {
  if (key === "model" && typeof value === "string" && value !== "") return { id: value };
  return value;
}

/** The flat values the selected presets supply, before explicit overrides. */
export function presetFlatValues(
  presets: Record<string, string>,
  available: Preset[],
): Record<string, unknown> {
  const flat: Record<string, unknown> = {};
  for (const [rawGroup, presetName] of Object.entries(presets)) {
    if (!presetName) continue;
    const preset = available.find((p) => p.name === presetName);
    if (!preset) continue;
    const group = normalizeGroupName(rawGroup);
    for (const [key, value] of Object.entries(preset.params)) {
      flat[group ? `${group}.${key}` : key] = normalizePresetValue(key, value);
    }
  }
  return flat;
}

/**
 * Whether the server will reject a run that leaves this input unset — the
 * client mirror of model.IsInputRequired: no default, or a model default that
 * selects nothing.
 */
export function isRequiredInput(schema: InputDef): boolean {
  const fallback = getInputDefault(schema);
  if (fallback === undefined || fallback === null) return true;
  if (schema.type === "model" && isPlainObject(fallback)) {
    const selector = fallback as { id?: string; tags?: unknown[]; providers?: unknown[] };
    return !selector.id && !(selector.tags?.length) && !(selector.providers?.length);
  }
  return false;
}

function isEmptyValue(value: unknown): boolean {
  if (value === undefined || value === null || value === "") return true;
  if (isPlainObject(value) && "id" in value) return !value.id;
  return false;
}

/** Inputs that are required and have no value from a preset or an override. */
export function missingRequiredInputs(
  groups: InputGroupDef[],
  effectiveFlat: Record<string, unknown>,
): Array<{ name: string; schema: InputDef }> {
  const missing: Array<{ name: string; schema: InputDef }> = [];
  for (const group of groups) {
    for (const input of group.inputs) {
      if (isRequiredInput(input.schema) && isEmptyValue(effectiveFlat[input.name])) {
        missing.push(input);
      }
    }
  }
  return missing;
}

function splitList(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

/**
 * A renderer emission → the plain JSON value the server validates.
 * `undefined` means "no override": the preset or the default applies.
 *
 * The renderer's widgets are shared with the builder, where every value may
 * be a CEL string, so they emit strings for models and lists. A run carries
 * literals, so this is where they get their real types back.
 */
export function coerceInputValue(schema: InputDef, raw: unknown): unknown {
  if (raw === undefined || raw === null) return undefined;
  switch (schema.type) {
    case "model":
      if (typeof raw === "string") return raw ? { id: raw } : undefined;
      return raw;
    case "tools":
      // An explicit empty list means "no tools", which is not the default.
      return typeof raw === "string" ? splitList(raw) : raw;
    case "array": {
      if (typeof raw !== "string") return raw;
      const text = raw.trim();
      if (!text) return undefined;
      if (text.startsWith("[")) {
        try {
          const parsed: unknown = JSON.parse(text);
          if (Array.isArray(parsed)) return parsed;
        } catch {
          // Not JSON; fall through to a plain list.
        }
      }
      return splitList(text);
    }
    case "object":
    case "any": {
      if (typeof raw !== "string") return raw;
      const text = raw.trim();
      if (!text) return undefined;
      try {
        return JSON.parse(text);
      } catch {
        // Kept verbatim; the server says what is wrong with it.
        return raw;
      }
    }
    default:
      return raw === "" ? undefined : raw;
  }
}

/** A plain JSON value → what the renderer's widget displays. */
export function toDisplayValue(schema: InputDef, value: unknown): unknown {
  if (value === undefined || value === null) return value;
  switch (schema.type) {
    case "tools":
    case "array":
      return Array.isArray(value) ? value.map(String).join(", ") : value;
    case "object":
    case "any":
      return typeof value === "string" ? value : JSON.stringify(value);
    default:
      return value;
  }
}
