// Copyright (c) 2025 Reliant Labs

/**
 * Which section of the setup dialog each workflow input belongs in
 * (automation-ux-spec.md §3.2), decided from data alone — never from an
 * input's name — so a hundred integrations need nothing here:
 *
 *   watches   inputs a trigger is scoped by (`match_input`), which are always
 *             required, then the typed inputs scoped BY those (a label picked
 *             from the chosen repositories). "What it watches".
 *   people    typed inputs whose type is an integration's `user`: "Who can
 *             ask it to work".
 *   settings  every other typed input, then every required input.
 *   more      everything with a default: "More settings", collapsed.
 *
 * Inputs every trigger maps from its event are not asked for at all (the
 * event wins over a param of that name), nor are runtime-only inputs.
 */

import type { InputGroupDef } from "../components/workflow/WorkflowInputGroup";
import { isRequiredInput } from "../components/workflow/run/runWorkflowValues";
import { integrationOf, type DeclaredTrigger } from "./declaredTriggers";
import { getIntegrationInputScope, isInternalInput, isIntegrationInput, type InputDef } from "./inputHelpers";

export interface SetupInput {
  name: string;
  schema: InputDef;
  required: boolean;
  /** The declared triggers this input scopes (watches only). */
  scopes: string[];
}

export interface SetupInputs {
  watches: SetupInput[];
  people: SetupInput[];
  settings: SetupInput[];
  more: SetupInput[];
  /** Inputs every trigger sets from its event; never asked. */
  mapped: string[];
}

/** The inputs every declared trigger maps from its event. */
export function inputsMappedByEveryTrigger(declared: readonly DeclaredTrigger[]): string[] {
  const maps = declared.map((t) => new Set(Object.keys(t.inputs ?? {})));
  if (maps.length === 0) return [];
  return [...maps[0]].filter((name) => maps.every((m) => m.has(name)));
}

/** input name → the declared triggers it scopes through `match_input`. */
export function scopeInputsOf(declared: readonly DeclaredTrigger[]): Map<string, string[]> {
  const out = new Map<string, string[]>();
  for (const trigger of declared) {
    const source = integrationOf(trigger);
    for (const input of Object.values(source?.matchInput ?? {})) {
      out.set(input, [...(out.get(input) ?? []), trigger.name ?? ""]);
    }
  }
  return out;
}

function isPersonType(type: string | undefined): boolean {
  return !!type && type.slice(type.indexOf(".") + 1) === "user";
}

export function classifyInputs(groups: readonly InputGroupDef[], declared: readonly DeclaredTrigger[]): SetupInputs {
  const mapped = inputsMappedByEveryTrigger(declared);
  const mappedSet = new Set(mapped);
  const scoped = scopeInputsOf(declared);
  const all = groups
    .flatMap((group) => group.inputs)
    .filter(({ name, schema }) => !mappedSet.has(name) && !isInternalInput(schema));

  const result: SetupInputs = { watches: [], people: [], settings: [], more: [], mapped };
  const placed = new Set<string>();
  const entry = (name: string, schema: InputDef, required: boolean): SetupInput => ({
    name,
    schema,
    required,
    scopes: scoped.get(name) ?? [],
  });

  // Scope inputs first, in declaration order; they are always required.
  for (const { name, schema } of all) {
    if (!scoped.has(name)) continue;
    result.watches.push(entry(name, schema, true));
    placed.add(name);
  }
  // Then the typed inputs whose list those scope inputs narrow.
  for (const { name, schema } of all) {
    if (placed.has(name) || !isIntegrationInput(schema)) continue;
    const bindings = Object.values(getIntegrationInputScope(schema));
    if (bindings.length > 0 && bindings.every((input) => placed.has(input) && scoped.has(input))) {
      result.watches.push(entry(name, schema, isRequiredInput(schema)));
      placed.add(name);
    }
  }
  for (const { name, schema } of all) {
    if (placed.has(name) || !isIntegrationInput(schema) || !isPersonType(schema.type)) continue;
    result.people.push(entry(name, schema, isRequiredInput(schema)));
    placed.add(name);
  }
  for (const { name, schema } of all) {
    if (placed.has(name) || !isIntegrationInput(schema)) continue;
    result.settings.push(entry(name, schema, isRequiredInput(schema)));
    placed.add(name);
  }
  for (const { name, schema } of all) {
    if (placed.has(name)) continue;
    const required = isRequiredInput(schema);
    (required ? result.settings : result.more).push(entry(name, schema, required));
    placed.add(name);
  }
  return result;
}
