import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { fromJson, toJson, type JsonValue, type JsonObject } from '@bufbuild/protobuf';
import { load } from 'js-yaml';

import { WorkflowSchema } from '../../src/gen/reliant/v1/workflow_v2_pb';


// Remove absent optional YAML properties before protobuf JSON validation.
function wireObject(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(wireObject);
  if (!value || typeof value !== 'object') return value;
  const result: Record<string, unknown> = {};
  for (const [key, child] of Object.entries(value)) {
    if (child === undefined) continue;
    result[key] = wireObject(child);
  }
  return result;
}

// YAML enum is named enum_values on the wire. The editor importer does not
// retain every input constraint, so project inputs directly using the contract.
type YAMLInput = { type: string; inputs?: Record<string, YAMLInput>; [key: string]: unknown };
function inputJSON(inputs: Record<string, YAMLInput>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(inputs).map(([name, input]) => {
    const { type, description, ui, example, required, enum: enumValues, inputs: nested, ...config } = input;
    return [name, { type, [`${type}Input`]: {
      base: { description, ui, example, required }, ...config,
      ...(enumValues ? { enumValues } : {}),
      ...(nested ? { inputs: inputJSON(nested) } : {}),
    } }];
  }));
}

// Real builtins, not a second hand-maintained approximation of the input schema.
// These are wire JSON responses: protobuf oneofs must be serialized, not spread.
// Playwright runs from frontends/web; the preview adapter sets the same cwd.
const builtinRoot = resolve(process.cwd(), '../..', 'internal/workflow/builtin');
export const COMPOSER_WORKFLOWS = ['agent', 'build-workflow', 'pitch-deck', 'parallel-compete'].map(name => {
  const yamlDefinition = readFileSync(resolve(builtinRoot, `${name}.yaml`), 'utf8');
  const raw = load(yamlDefinition) as { name: string; description: string; apiVersion: string; presets?: { tag: string; default: string }; title?: string; inputs: Record<string, YAMLInput> };
  const parsed = raw;
  // Composer projection only: execution graph remains in canonical YAML. No
  // mock claims to execute it; editor runtime nodes are not wire messages.
  const workflow = toJson(WorkflowSchema, fromJson(WorkflowSchema, wireObject({
    name: parsed.name, description: parsed.description, apiVersion: parsed.apiVersion,
    inputs: inputJSON(raw.inputs), presets: parsed.presets, title: raw.title,
  }) as JsonValue)) as JsonObject;
  return { workflow, yamlDefinition, source: 'builtin', status: 2 };
});
const requiredYaml = readFileSync(resolve(process.cwd(), 'e2e/helpers/required-input-workflow.yaml'), 'utf8');
const requiredParsed = load(requiredYaml) as { name: string };
const requiredWorkflow = toJson(WorkflowSchema, fromJson(WorkflowSchema, wireObject({
  name: requiredParsed.name, title: 'Review Request (fixture)', inputs: inputJSON((load(requiredYaml) as { inputs: Record<string, YAMLInput> }).inputs),
}) as JsonValue)) as JsonObject;
COMPOSER_WORKFLOWS.push({ workflow: requiredWorkflow, yamlDefinition: requiredYaml, source: 'project', status: 2 });

export const COMPOSER_PRESETS = ['general', 'researcher', 'code_reviewer'].map(name => {
  const raw = load(readFileSync(resolve(builtinRoot, `presets/${name}.yaml`), 'utf8')) as Record<string, unknown>;
  return { ...raw, name, slug: name, source: 'builtin' };
});

// Same reasoning model used by ChatSettingsPopover.thinking.test.tsx. Numbers
// here describe fixture capacity, not an assertion about a live provider account.
export const COMPOSER_MODELS = [{
  id: 'claude-4.5-sonnet@anthropic', name: 'Claude 4.5 Sonnet',
  provider: 'Anthropic', driverId: 'anthropic', capabilities: ['reasoning', 'vision', 'function_calling'],
  contextWindow: '200000', defaultMaxTokens: '64000', canReason: true,
  supportsAttachments: true, supportsTools: true, supportsCaching: true,
  supportsTemperature: true, tags: ['flagship', 'reasoning'],
  supportedThinkingLevels: ['low', 'medium', 'high', 'xhigh'],
  defaultCompactionThreshold: '115600', maxCompactionThreshold: '136000',
}];
export const COMPOSER_PROVIDERS = [{
  provider: 'anthropic', displayName: 'Anthropic', configured: true,
  outputModalities: ['text'], accountCount: 1,
}];
