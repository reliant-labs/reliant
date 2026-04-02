// Copyright (c) 2025 Reliant Labs

/**
 * Helper getters/setters for proto Input objects.
 *
 * The proto Input uses a oneof `config` field:
 *   { type: "enum", config: { case: "enumInput", value: { base: { description, ui }, enumValues, ... } } }
 *
 * These helpers abstract the config oneof so callers can read/write fields
 * without knowing which config case is active.
 */

import type { MessageInitShape } from "@bufbuild/protobuf";
import type {
  InputBase,
  PropertySchema as ProtoPropertySchema,
  PresetsConfig,
} from "../gen/reliant/v1/workflow_v2_pb";
import { InputSchema } from "../gen/reliant/v1/workflow_v2_pb";
import { protoValueToJs } from "../api/proto-utils";

/** Proto Input init shape — use getters to read fields through the config oneof. */
export type InputDef = MessageInitShape<typeof InputSchema>;

// ---------------------------------------------------------------------------
// Internal utilities
// ---------------------------------------------------------------------------

/** Safely convert bigint/number to number */
function asNumber(value: unknown): number | undefined {
  if (typeof value === "number") return value;
  if (typeof value === "bigint") return Number(value);
  return undefined;
}

/** Unwrap a proto Value to a plain JS value if it looks like a proto Value/Struct wrapper */
function toJsDefault(value: unknown): unknown {
  if (!value) return value;
  if (typeof value !== "object") return value;
  const obj = value as Record<string, unknown>;
  // Proto Value has { kind: { case, value } }
  if ("kind" in obj) return protoValueToJs(value as any);
  // Proto Value message instance (protobuf-es) — check $typeName
  if (obj.$typeName === "google.protobuf.Value") return protoValueToJs(value as any);
  return value;
}

/** Get the base field from any config value */
function getBase(input: InputDef): InputBase | undefined {
  return (input?.config?.value as Record<string, unknown> | undefined)?.base as InputBase | undefined;
}

/** Get the config value from an input */
function getConfigValue(input: InputDef): Record<string, unknown> | undefined {
  return input?.config?.value as Record<string, unknown> | undefined;
}

/** Get the config case from an input */
function getConfigCase(input: InputDef): string | undefined {
  return input?.config?.case;
}

// ---------------------------------------------------------------------------
// Type mapping: input type string -> config case
// ---------------------------------------------------------------------------

const TYPE_TO_CASE: Record<string, string> = {
  string: "stringInput",
  number: "numberInput",
  integer: "integerInput",
  boolean: "booleanInput",
  enum: "enumInput",
  model: "modelInput",
  message: "messageInput",
  attachments: "attachmentsInput",
  tools: "toolsInput",
  array: "arrayInput",
  object: "objectInput",
  any: "anyInput",
  group: "groupInput",
  preset: "presetInput",
};

// ---------------------------------------------------------------------------
// Getters — read fields from proto Input's config oneof
// ---------------------------------------------------------------------------

/** Get the description from an input's base */
export function getInputDescription(input: InputDef): string | undefined {
  return getBase(input)?.description;
}

/** Get the UI mode from an input's base */
export function getInputUI(input: InputDef): string | undefined {
  return getBase(input)?.ui;
}

/** Get the default value, unwrapping proto Value wrappers and bigint→number */
export function getInputDefault(input: InputDef): unknown {
  const cv = getConfigValue(input);
  if (!cv) return undefined;
  const configCase = getConfigCase(input);
  const raw = cv.default;

  // Integer defaults are bigint in proto — convert to number
  if (configCase === "integerInput") {
    return asNumber(raw);
  }

  return toJsDefault(raw);
}

/** Get enum values for an enum input */
export function getInputEnumValues(input: InputDef): string[] | undefined {
  const cv = getConfigValue(input);
  return cv?.enumValues as string[] | undefined;
}

/** Get multi flag for enum/preset inputs */
export function getInputMulti(input: InputDef): boolean | undefined {
  const cv = getConfigValue(input);
  return cv?.multi as boolean | undefined;
}

/** Get min value (handles bigint→number for integer inputs) */
export function getInputMin(input: InputDef): number | undefined {
  const cv = getConfigValue(input);
  if (!cv) return undefined;

  const configCase = getConfigCase(input);
  switch (configCase) {
    case "stringInput":
      return asNumber(cv.minLength);
    case "numberInput":
    case "integerInput":
      return asNumber(cv.min);
    case "attachmentsInput":
    case "arrayInput":
      return asNumber(cv.minItems);
    default:
      return undefined;
  }
}

/** Get max value (handles bigint→number for integer inputs) */
export function getInputMax(input: InputDef): number | undefined {
  const cv = getConfigValue(input);
  if (!cv) return undefined;

  const configCase = getConfigCase(input);
  switch (configCase) {
    case "stringInput":
      return asNumber(cv.maxLength);
    case "numberInput":
    case "integerInput":
      return asNumber(cv.max);
    case "attachmentsInput":
    case "arrayInput":
      return asNumber(cv.maxItems);
    default:
      return undefined;
  }
}

/** Get object input properties */
export function getInputProperties(
  input: InputDef,
): Record<string, ProtoPropertySchema> | undefined {
  const cv = getConfigValue(input);
  return cv?.properties as Record<string, ProtoPropertySchema> | undefined;
}

/** Get object input required fields */
export function getInputRequired(input: InputDef): string[] | undefined {
  const cv = getConfigValue(input);
  return cv?.required as string[] | undefined;
}

/** Get object input additionalProperties flag */
export function getInputAdditionalProperties(input: InputDef): boolean | undefined {
  const cv = getConfigValue(input);
  return cv?.additionalProperties as boolean | undefined;
}

/** Get nested inputs from a group input */
export function getInputNestedInputs(
  input: InputDef,
): Record<string, InputDef> | undefined {
  const cv = getConfigValue(input);
  return cv?.inputs as Record<string, InputDef> | undefined;
}

/** Get preset config from a group input */
export function getInputPresetConfig(input: InputDef): PresetsConfig | undefined {
  const cv = getConfigValue(input);
  return cv?.presets as PresetsConfig | undefined;
}

/** Get tags from a preset input */
export function getInputTags(input: InputDef): string[] | undefined {
  const cv = getConfigValue(input);
  return cv?.tags as string[] | undefined;
}

/** Get tag string (comma-separated) from a preset input */
export function getInputTag(input: InputDef): string | undefined {
  const tags = getInputTags(input);
  if (Array.isArray(tags)) return tags.join(",");
  return undefined;
}

/** Get pattern from a string input */
export function getInputPattern(input: InputDef): string | undefined {
  const cv = getConfigValue(input);
  return cv?.pattern as string | undefined;
}

/** Get minLength from a string input */
export function getInputMinLength(input: InputDef): number | undefined {
  const cv = getConfigValue(input);
  return asNumber(cv?.minLength);
}

/** Get maxLength from a string input */
export function getInputMaxLength(input: InputDef): number | undefined {
  const cv = getConfigValue(input);
  return asNumber(cv?.maxLength);
}

/** Get minItems from array/attachments input */
export function getInputMinItems(input: InputDef): number | undefined {
  const cv = getConfigValue(input);
  return asNumber(cv?.minItems);
}

/** Get maxItems from array/attachments input */
export function getInputMaxItems(input: InputDef): number | undefined {
  const cv = getConfigValue(input);
  return asNumber(cv?.maxItems);
}

/** Whether this input should be shown in config UI */
export function isConfigurableInput(input: InputDef): boolean {
  const ui = getInputUI(input);
  const type = input?.type;
  return (
    ui !== "hidden" &&
    type !== "message" &&
    type !== "attachments" &&
    type !== "preset" &&
    type !== "thread"
  );
}

// ---------------------------------------------------------------------------
// Setters — return new Input objects (immutable)
// ---------------------------------------------------------------------------

/** Set description on an input */
export function setInputDescription(input: InputDef, description: string): InputDef {
  return updateBase(input, { description });
}

/** Set UI mode on an input */
export function setInputUI(input: InputDef, ui: string): InputDef {
  return updateBase(input, { ui });
}

/** Set the default value on an input */
export function setInputDefault(input: InputDef, defaultValue: unknown): InputDef {
  return updateConfigValue(input, { default: defaultValue });
}

/** Set enum values on an enum input */
export function setInputEnumValues(input: InputDef, enumValues: string[]): InputDef {
  return updateConfigValue(input, { enumValues });
}

/** Set multi flag on an enum/preset input */
export function setInputMulti(input: InputDef, multi: boolean): InputDef {
  return updateConfigValue(input, { multi });
}

/** Set min value on an input (maps to correct field based on config case) */
export function setInputMin(input: InputDef, min: number | undefined): InputDef {
  const configCase = getConfigCase(input);
  switch (configCase) {
    case "stringInput":
      return updateConfigValue(input, { minLength: min });
    case "attachmentsInput":
    case "arrayInput":
      return updateConfigValue(input, { minItems: min });
    default:
      return updateConfigValue(input, { min });
  }
}

/** Set max value on an input (maps to correct field based on config case) */
export function setInputMax(input: InputDef, max: number | undefined): InputDef {
  const configCase = getConfigCase(input);
  switch (configCase) {
    case "stringInput":
      return updateConfigValue(input, { maxLength: max });
    case "attachmentsInput":
    case "arrayInput":
      return updateConfigValue(input, { maxItems: max });
    default:
      return updateConfigValue(input, { max });
  }
}

/** Set pattern on a string input */
export function setInputPattern(input: InputDef, pattern: string): InputDef {
  return updateConfigValue(input, { pattern });
}

/** Set minLength on a string input */
export function setInputMinLength(
  input: InputDef,
  minLength: number | undefined,
): InputDef {
  return updateConfigValue(input, { minLength });
}

/** Set maxLength on a string input */
export function setInputMaxLength(
  input: InputDef,
  maxLength: number | undefined,
): InputDef {
  return updateConfigValue(input, { maxLength });
}

/** Set minItems on an array/attachments input */
export function setInputMinItems(
  input: InputDef,
  minItems: number | undefined,
): InputDef {
  return updateConfigValue(input, { minItems });
}

/** Set maxItems on an array/attachments input */
export function setInputMaxItems(
  input: InputDef,
  maxItems: number | undefined,
): InputDef {
  return updateConfigValue(input, { maxItems });
}

/** Set properties on an object input */
export function setInputProperties(
  input: InputDef,
  properties: Record<string, ProtoPropertySchema>,
): InputDef {
  return updateConfigValue(input, { properties });
}

/** Set required fields on an object input */
export function setInputRequired(input: InputDef, required: string[]): InputDef {
  return updateConfigValue(input, { required });
}

/** Set additionalProperties on an object input */
export function setInputAdditionalProperties(
  input: InputDef,
  additionalProperties: boolean,
): InputDef {
  return updateConfigValue(input, { additionalProperties });
}

/** Set nested inputs on a group input */
export function setInputNestedInputs(
  input: InputDef,
  inputs: Record<string, InputDef>,
): InputDef {
  return updateConfigValue(input, { inputs });
}

/** Set preset config on a group input */
export function setInputPresetConfig(
  input: InputDef,
  presets: PresetsConfig,
): InputDef {
  return updateConfigValue(input, { presets });
}

/** Set tags on a preset input */
export function setInputTags(input: InputDef, tags: string[]): InputDef {
  return updateConfigValue(input, { tags });
}

// ---------------------------------------------------------------------------
// Factory — create new Input objects with correct config case
// ---------------------------------------------------------------------------

/** Create a new Input with the correct config.case for the given type */
export function createInput(type: string, init?: Record<string, unknown>): InputDef {
  const configCase = TYPE_TO_CASE[type];
  const base = init?.description || init?.ui
    ? { description: init.description ?? "", ui: init.ui ?? "" }
    : { description: "", ui: "" };

  const configValue: Record<string, unknown> = { base };

  // Apply any init values to the config value
  if (init) {
    for (const [key, value] of Object.entries(init)) {
      if (key === "description" || key === "ui" || key === "type") continue;
      configValue[key] = value;
    }
  }

  // Ensure required array fields exist
  if (type === "enum") {
    configValue.enumValues = configValue.enumValues ?? [];
    configValue.multi = configValue.multi ?? false;
  }
  if (type === "preset") {
    configValue.tags = configValue.tags ?? [];
    configValue.multi = configValue.multi ?? false;
  }
  if (type === "object") {
    configValue.properties = configValue.properties ?? {};
    configValue.required = configValue.required ?? [];
  }
  if (type === "group") {
    configValue.inputs = configValue.inputs ?? {};
  }

  return {
    type,
    config: configCase
      ? { case: configCase, value: configValue }
      : { case: undefined, value: undefined },
  } as InputDef;
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

/** Update the base fields of an input, returning a new object */
function updateBase(input: InputDef, updates: Partial<InputBase>): InputDef {
  const cv = getConfigValue(input);
  if (!cv) return input;
  return {
    ...input,
    config: {
      ...input.config,
      value: {
        ...cv,
        base: { ...(cv.base as InputBase || { description: "", ui: "" }), ...updates },
      },
    },
  } as InputDef;
}

/** Update config value fields, returning a new object */
function updateConfigValue(input: InputDef, updates: Record<string, unknown>): InputDef {
  const cv = getConfigValue(input);
  if (!cv) return input;
  return {
    ...input,
    config: {
      ...input.config,
      value: { ...cv, ...updates },
    },
  } as InputDef;
}

// ---------------------------------------------------------------------------
// Batch updates — apply flat-named field updates to a proto Input
// ---------------------------------------------------------------------------

/**
 * Change the type of an input, preserving common base fields.
 * Creates a new Input with the target type's config case.
 */
export function changeInputType(input: InputDef, newType: string): InputDef {
  const desc = getInputDescription(input);
  const ui = getInputUI(input);
  return createInput(newType, {
    description: desc ?? "",
    ui: ui ?? "",
  });
}

/**
 * Apply flat-named field updates to a proto Input.
 * Routes each key to the appropriate setter so callers can write
 * `applyInputUpdates(input, { description: "...", enumValues: [...] })`
 * without knowing about config cases.
 */
export function applyInputUpdates(
  input: InputDef,
  updates: Record<string, unknown>,
): InputDef {
  let result = input;
  for (const [key, value] of Object.entries(updates)) {
    switch (key) {
      case "type":
        result = changeInputType(result, value as string);
        break;
      case "description":
        result = setInputDescription(result, value as string);
        break;
      case "ui":
        result = setInputUI(result, value as string);
        break;
      case "default":
        result = setInputDefault(result, value);
        break;
      case "enumValues":
        result = setInputEnumValues(result, value as string[]);
        break;
      case "multi":
        result = setInputMulti(result, value as boolean);
        break;
      case "min":
        result = setInputMin(result, value as number | undefined);
        break;
      case "max":
        result = setInputMax(result, value as number | undefined);
        break;
      case "pattern":
        result = setInputPattern(result, value as string);
        break;
      case "minLength":
        result = setInputMinLength(result, value as number | undefined);
        break;
      case "maxLength":
        result = setInputMaxLength(result, value as number | undefined);
        break;
      case "minItems":
        result = setInputMinItems(result, value as number | undefined);
        break;
      case "maxItems":
        result = setInputMaxItems(result, value as number | undefined);
        break;
      case "properties":
        result = setInputProperties(
          result,
          value as Record<string, ProtoPropertySchema>,
        );
        break;
      case "required":
        result = setInputRequired(result, value as string[]);
        break;
      case "additionalProperties":
        result = setInputAdditionalProperties(result, value as boolean);
        break;
      case "inputs":
        result = setInputNestedInputs(
          result,
          value as Record<string, InputDef>,
        );
        break;
      case "presetConfig":
      case "presets":
        result = setInputPresetConfig(result, value as PresetsConfig);
        break;
      case "tags":
        result = setInputTags(result, value as string[]);
        break;
      default:
        // Unknown fields: store directly on the object (e.g. _id, _name, workflow)
        result = { ...result, [key]: value } as InputDef;
        break;
    }
  }
  return result;
}