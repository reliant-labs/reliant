// Copyright (c) 2025 Reliant Labs

/**
 * Which setup-dialog section each input lands in, from data alone: the
 * trigger's match_input, the input's type and scope, `required`, defaults.
 */

import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import {
  InputBaseSchema,
  InputSchema,
  IntegrationInputConfigSchema,
  StringInputConfigSchema,
  type Input,
} from "../../gen/reliant/v1/workflow_v2_pb";
import type { InputGroupDef } from "../../components/workflow/WorkflowInputGroup";
import type { DeclaredTrigger } from "../declaredTriggers";
import { classifyInputs, inputsMappedByEveryTrigger, scopeInputsOf } from "../setupInputs";

function typed(type: string, opts: { multi?: boolean; required?: boolean; minItems?: number; scope?: Record<string, string>; fallback?: boolean } = {}): Input {
  return create(InputSchema, {
    type,
    config: {
      case: "integrationInput",
      value: create(IntegrationInputConfigSchema, {
        base: create(InputBaseSchema, { required: opts.required ?? false }),
        multi: opts.multi ?? false,
        minItems: opts.minItems,
        scope: opts.scope ?? {},
        default: opts.fallback ? { kind: { case: "stringValue", value: "skip-review" } } : undefined,
      }),
    },
  });
}

function text(fallback?: string): Input {
  return create(InputSchema, {
    type: "string",
    config: { case: "stringInput", value: create(StringInputConfigSchema, { base: create(InputBaseSchema, {}), default: fallback }) },
  });
}

const groups: InputGroupDef[] = [
  {
    name: "",
    label: "",
    inputs: [
      { name: "model", schema: text("flagship") },
      { name: "trusted_ids", schema: typed("github.user", { multi: true, fallback: true }) },
      { name: "skip_label", schema: typed("github.label", { scope: { repository: "repos" }, fallback: true }) },
      { name: "repos", schema: typed("github.repository", { multi: true, required: true, minItems: 1 }) },
      { name: "guidance", schema: text() },
      { name: "pr_number", schema: text() },
      { name: "deploy_env", schema: typed("vercel.environment") },
    ],
  },
];

const declared = [
  {
    name: "pr-opened",
    inputs: { pr_number: "{{ trigger.payload.data.number }}" },
    source: { case: "integration", value: { integration: "github", events: ["pull_request.opened"], matchInput: { repository: "repos" } } },
  },
  {
    name: "pr-ready",
    inputs: { pr_number: "{{ trigger.payload.data.number }}" },
    source: { case: "integration", value: { integration: "github", events: ["pull_request.ready_for_review"], matchInput: { repository: "repos" } } },
  },
] as unknown as DeclaredTrigger[];

describe("classifyInputs", () => {
  const result = classifyInputs(groups, declared);
  const names = (list: { name: string }[]) => list.map((i) => i.name);

  it("puts the scope input first in What it watches, required, then what it scopes", () => {
    expect(names(result.watches)).toEqual(["repos", "skip_label"]);
    expect(result.watches[0].required).toBe(true);
    expect(result.watches[0].scopes).toEqual(["pr-opened", "pr-ready"]);
  });

  it("asks for people under Who can ask, other typed and required inputs under Settings", () => {
    expect(names(result.people)).toEqual(["trusted_ids"]);
    expect(names(result.settings)).toEqual(["deploy_env", "guidance"]);
  });

  it("collapses inputs with defaults into More settings, and never asks for one every trigger maps", () => {
    expect(names(result.more)).toEqual(["model"]);
    expect(result.mapped).toEqual(["pr_number"]);
  });
});

describe("helpers", () => {
  it("finds the inputs every trigger maps, and what each scope input scopes", () => {
    expect(inputsMappedByEveryTrigger([declared[0], { ...declared[1], inputs: {} } as DeclaredTrigger])).toEqual([]);
    expect([...scopeInputsOf(declared).entries()]).toEqual([["repos", ["pr-opened", "pr-ready"]]]);
  });
});
