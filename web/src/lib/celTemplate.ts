// Copyright (c) 2025 Reliant Labs

/**
 * Detect whether a string value is a CEL template expression (e.g. "{{inputs.foo}}").
 * Templates are evaluated at runtime — in the authoring UI we can only display them.
 */
export function isCelTemplate(value: string | null | undefined): boolean {
  return !!value && value.includes("{{") && value.includes("}}");
}
