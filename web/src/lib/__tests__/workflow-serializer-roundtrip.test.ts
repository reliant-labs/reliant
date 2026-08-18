import { describe, it, expect } from "vitest";
import {
  serializeWorkflowToYAML,
  deserializeWorkflowFromYAML,
} from "../workflow-serializer";
import type { Workflow, Step } from "../../types/workflow";
import {
  getStepThread,
  getStepParallel,
  getStepItems,
  getStepKey,
  getStepOnFailure,
} from "../../types/workflow";
import { celString, celExpr, celLiteral, directCel } from "../celAdapter";
import {
  getInputEnumValues,
  getInputPresetConfig,
  getInputNestedInputs,
  createInput,
} from "../inputHelpers";
import * as fs from "fs";
import * as path from "path";
import { fileURLToPath } from "url";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

/**
 * Round-trip serialization tests for workflow YAML files.
 * Tests that all fields are preserved when deserializing and re-serializing.
 */
describe("Workflow Serializer Round-Trip", () => {
  // Helper to deep compare objects, ignoring undefined values and checking key subsets
  function deepCompareFields(
    original: any,
    roundTripped: any,
    path: string = "",
  ): string[] {
    const differences: string[] = [];

    if (original === undefined || original === null) {
      return differences;
    }

    if (typeof original !== typeof roundTripped) {
      differences.push(
        `${path}: type mismatch - original: ${typeof original}, roundTripped: ${typeof roundTripped}`,
      );
      return differences;
    }

    if (Array.isArray(original)) {
      if (!Array.isArray(roundTripped)) {
        differences.push(`${path}: expected array, got ${typeof roundTripped}`);
        return differences;
      }
      if (original.length !== roundTripped.length) {
        differences.push(
          `${path}: array length mismatch - original: ${original.length}, roundTripped: ${roundTripped.length}`,
        );
      }
      for (let i = 0; i < Math.max(original.length, roundTripped.length); i++) {
        differences.push(
          ...deepCompareFields(original[i], roundTripped[i], `${path}[${i}]`),
        );
      }
      return differences;
    }

    if (typeof original === "object") {
      const allKeys = new Set([
        ...Object.keys(original),
        ...Object.keys(roundTripped || {}),
      ]);
      for (const key of allKeys) {
        // Skip position as it's moved to ui.positions
        if (key === "position") continue;
        // Skip ui field as positions get restructured
        if (key === "ui" && path === "") continue;

        const origVal = original[key];
        const rtVal = roundTripped?.[key];

        // Check if field was lost (present in original but missing in roundTripped)
        if (origVal !== undefined && rtVal === undefined) {
          differences.push(
            `${path}.${key}: LOST - was ${JSON.stringify(origVal)}`,
          );
        } else if (origVal === undefined && rtVal !== undefined) {
          // Field was added - this might be ok for edges[] default
          if (key !== "edges" || rtVal.length > 0) {
            differences.push(
              `${path}.${key}: ADDED - now ${JSON.stringify(rtVal)}`,
            );
          }
        } else {
          differences.push(
            ...deepCompareFields(origVal, rtVal, `${path}.${key}`),
          );
        }
      }
      return differences;
    }

    // Primitive comparison
    if (original !== roundTripped) {
      differences.push(
        `${path}: value mismatch - original: ${JSON.stringify(original)}, roundTripped: ${JSON.stringify(roundTripped)}`,
      );
    }

    return differences;
  }

  it("should preserve apiVersion field on round-trip", () => {
    const workflow: Workflow = {
      name: "test-with-apiVersion",
      apiVersion: "0.0.5",
      nodes: [{ id: "step1", type: "run", command: "echo hello" }],
      edges: [],
    };

    const yaml = serializeWorkflowToYAML(workflow);
    expect(yaml).toContain("apiVersion: 0.0.5");

    const deserialized = deserializeWorkflowFromYAML(yaml);
    expect(deserialized.apiVersion).toBe("0.0.5");
  });

  it("should preserve presets.tag field on round-trip", () => {
    const workflow: Workflow = {
      name: "test-with-tag",
      presets: { tag: "agent" },
      nodes: [{ id: "step1", type: "run", command: "echo hello" }],
      edges: [],
    };

    const yaml = serializeWorkflowToYAML(workflow);
    expect(yaml).toContain("tag: agent");

    const deserialized = deserializeWorkflowFromYAML(yaml);
    expect(deserialized.presets?.tag).toBe("agent");
  });

  it("should preserve node thread configuration on round-trip", () => {
    const workflow: Workflow = {
      name: "test-with-thread",
      nodes: [
        {
          id: "step1",
          type: "workflow",
          args: {
            case: "workflow" as const,
            value: {
              ref: celString("builtin://agent"),
              args: {},
              presets: {},
              thread: {
                mode: "new",
              },
            },
          } as Step["args"],
        } as Step,
      ],
      edges: [],
    };

    const yaml = serializeWorkflowToYAML(workflow);
    expect(yaml).toContain("thread:");
    expect(yaml).toContain("mode: new");

    const deserialized = deserializeWorkflowFromYAML(yaml);
    expect(getStepThread(deserialized.nodes[0])).toMatchObject({ mode: "new" });
  });

  it("should preserve groups with tag on round-trip", () => {
    const workflow: Workflow = {
      name: "test-with-groups",
      inputs: {
        implementer: createInput("group", {
          description: "Settings for the implementer",
          presets: { tag: "agent" },
          inputs: {
            model: createInput("model", {
              default: "claude-4.5-sonnet",
              description: "Model for implementer",
            }),
            temperature: createInput("number", {
              default: "1.0",
              min: 0,
              max: 1,
            }),
          },
        }),
      },
      nodes: [{ id: "step1", type: "run", command: "echo hello" }],
      edges: [],
    };

    const yaml = serializeWorkflowToYAML(workflow);
    expect(yaml).toContain("inputs:");
    expect(yaml).toContain("implementer:");
    expect(yaml).toContain("type: group");
    expect(yaml).toContain("preset_config:");

    const deserialized = deserializeWorkflowFromYAML(yaml);
    expect(deserialized.inputs).toBeDefined();
    expect(deserialized.inputs?.implementer).toBeDefined();
    expect(deserialized.inputs?.implementer.type).toBe("group");
    expect(getInputPresetConfig(deserialized.inputs?.implementer)?.tag).toBe("agent");
    expect(getInputNestedInputs(deserialized.inputs?.implementer)?.model.type).toBe("model");
  });

  it("should preserve node condition on round-trip", () => {
    const workflow: Workflow = {
      name: "test-with-condition",
      nodes: [
        {
          id: "step1",
          type: "run",
          command: "echo hello",
          condition: directCel('inputs.phases.contains("research")'),
        },
      ],
      edges: [],
    };

    const yaml = serializeWorkflowToYAML(workflow);
    expect(yaml).toContain("condition:");

    const deserialized = deserializeWorkflowFromYAML(yaml);
    expect(deserialized.nodes[0].condition).toEqual(
      directCel('inputs.phases.contains("research")'),
    );
  });

  it("should preserve presets as Record<string, string> on round-trip", () => {
    const workflow: Workflow = {
      name: "test-with-presets",
      nodes: [
        {
          id: "workflow-1",
          type: "workflow",
          args: {
            case: "workflow" as const,
            value: {
              ref: celString("builtin://agent"),
              args: {},
              presets: {
                default: "tester",
                implementer: "fast",
              },
            },
          } as Step["args"],
        },
      ],
      edges: [],
    };

    const yaml = serializeWorkflowToYAML(workflow);
    expect(yaml).toContain("presets:");
    expect(yaml).toContain("default: tester");
    expect(yaml).toContain("implementer: fast");

    const deserialized = deserializeWorkflowFromYAML(yaml);
    const presets = (deserialized.nodes[0].args as { value: { presets: Record<string, string> } })?.value?.presets;
    expect(presets).toEqual({
      default: "tester",
      implementer: "fast",
    });
  });

  it("should preserve loop with inline workflow on round-trip", () => {
    const inlineWorkflow: Workflow = {
      name: "inline-loop",
      entry: ["call_llm"],
      inputs: {
        model: {
          type: "model",
          default: "claude-4.5-sonnet",
        },
      },
      outputs: {
        done: "{{nodes.call_llm.done}}",
      },
      nodes: [
        {
          id: "call_llm",
          type: "call_llm",
        },
      ],
      edges: [],
    };

    const workflow: Workflow = {
      name: "test-with-loop",
      entry: ["agent_loop"],
      nodes: [
        {
          id: "agent_loop",
          type: "loop",
          args: {
            case: "loop" as const,
            value: {
              while: directCel("outputs.done == true || iter.iteration >= 10"),
              inline: inlineWorkflow,
              args: {
                model: "{{inputs.model}}",
              },
              presets: {},

            },
          } as Step["args"],
        },
      ],
      edges: [],
    };

    const yaml = serializeWorkflowToYAML(workflow);
    expect(yaml).toContain("type: loop");
    expect(yaml).toContain("while:");
    expect(yaml).toContain("inline:");

    const deserialized = deserializeWorkflowFromYAML(yaml);
    const loopNode = deserialized.nodes[0];
    expect(loopNode.type).toBe("loop");
    const loopArgs = (loopNode.args as { value: { while: unknown; inline: Workflow } })?.value;
    expect(loopArgs.while).toEqual(directCel("outputs.done == true || iter.iteration >= 10"));
    expect(loopArgs.inline).toBeDefined();
    expect(loopArgs.inline?.nodes).toHaveLength(1);
    expect(loopArgs.inline?.nodes[0].id).toBe("call_llm");
  });

  it("should preserve node-level thread config on round-trip", () => {
    const workflow: Workflow = {
      name: "test-node-thread",
      nodes: [
        {
          id: "workflow-1",
          type: "workflow",
          args: {
            case: "workflow" as const,
            value: {
              ref: celLiteral("builtin://agent"),
              args: {},
              presets: {},
              thread: {
                mode: "inherit",
                inject: {
                  role: celLiteral("user"),
                  content: celLiteral("Hello world"),
                  displayStyle: celLiteral("info"),
                },
              },
            },
          } as Step["args"],
        } as Step,
      ],
      edges: [],
    };

    const yaml = serializeWorkflowToYAML(workflow);
    expect(yaml).toContain("thread:");
    expect(yaml).toContain("mode: inherit");
    expect(yaml).toContain("inject:");
    expect(yaml).toContain("display_style: info");

    const deserialized = deserializeWorkflowFromYAML(yaml);
    expect(getStepThread(deserialized.nodes[0])).toMatchObject({
      mode: "inherit",
      inject: {
        role: celLiteral("user"),
        content: celLiteral("Hello world"),
        displayStyle: celLiteral("info"),
      },
    });
  });

  it("should preserve parallel loop fields on round-trip", () => {
    const workflow: Workflow = {
      name: "test-parallel-loop",
      nodes: [
        {
          id: "implement_all",
          type: "loop",
          args: {
            case: "loop" as const,
            value: {
              ref: celLiteral("builtin://get-it-right"),
              args: {
                prompt: "{{iter.item.spec}}",
              },
              presets: {},

              parallel: true,
              items: celExpr("{{nodes.decompose.output.components}}"),
              key: "{{iter.item.name}}",
              onFailure: "continue",
              thread: {
                mode: "new",
              },
            },
          } as Step["args"],
        } as Step,
      ],
      edges: [],
    };

    const yaml = serializeWorkflowToYAML(workflow);
    expect(yaml).toContain("parallel: true");
    expect(yaml).toContain("items:");
    expect(yaml).toContain("key: '{{iter.item.name}}'");
    expect(yaml).toContain("on_failure: continue");
    expect(yaml).toContain("thread:");

    const deserialized = deserializeWorkflowFromYAML(yaml);
    expect(getStepParallel(deserialized.nodes[0])).toBe(true);
    expect(getStepItems(deserialized.nodes[0])).toBe("{{nodes.decompose.output.components}}");
    expect(getStepKey(deserialized.nodes[0])).toBe("{{iter.item.name}}");
    expect(getStepOnFailure(deserialized.nodes[0])).toBe("continue");
    expect(getStepThread(deserialized.nodes[0])).toMatchObject({ mode: "new" });
  });

  it("should preserve entry field on round-trip", () => {
    const workflow: Workflow = {
      name: "test-with-entry",
      entry: ["start_node"],
      nodes: [{ id: "start_node", type: "run", command: "echo hello" }],
      edges: [],
    };

    const yaml = serializeWorkflowToYAML(workflow);
    expect(yaml).toContain("- start_node");

    const deserialized = deserializeWorkflowFromYAML(yaml);
    expect(deserialized.entry).toEqual(["start_node"]);
  });

  it("should preserve save_message config on round-trip", () => {
    const workflow: Workflow = {
      name: "test-save-message",
      nodes: [
        {
          id: "call_llm",
          type: "call_llm",
          saveMessage: {
            role: celExpr("{{output.message.role}}"),
            content: celExpr("{{output.message.text}}"),
            toolCalls: celExpr("{{output.tool_calls}}"),
            condition: celExpr("{{output.message != null}}"),
          },
        },
      ],
      edges: [],
    };

    const yaml = serializeWorkflowToYAML(workflow);
    expect(yaml).toContain("save_message:");
    expect(yaml).toContain("role:");
    expect(yaml).toContain("content:");

    const deserialized = deserializeWorkflowFromYAML(yaml);
    expect(deserialized.nodes[0].saveMessage).toMatchObject({
      role: celExpr("{{output.message.role}}"),
      content: celExpr("{{output.message.text}}"),
      toolCalls: celExpr("{{output.tool_calls}}"),
      condition: celExpr("{{output.message != null}}"),
    });
  });

  it("should preserve timeout on round-trip", () => {
    const workflow: Workflow = {
      name: "test-timeout",
      nodes: [
        {
          id: "step1",
          type: "compact",
          timeout: celLiteral("10m"),
        },
      ],
      edges: [],
    };

    const yaml = serializeWorkflowToYAML(workflow);
    expect(yaml).toContain("timeout:");

    const deserialized = deserializeWorkflowFromYAML(yaml);
    expect(deserialized.nodes[0].timeout).toEqual(celLiteral("10m"));
  });

  it("should preserve workflow-level outputs on round-trip", () => {
    const workflow: Workflow = {
      name: "test-outputs",
      outputs: {
        message: "{{nodes.agent_loop.message}}",
        response_text: "{{nodes.agent_loop.response_text}}",
      },
      nodes: [{ id: "agent_loop", type: "run", command: "echo hello" }],
      edges: [],
    };

    const yaml = serializeWorkflowToYAML(workflow);
    expect(yaml).toContain("outputs:");
    expect(yaml).toContain("message:");

    const deserialized = deserializeWorkflowFromYAML(yaml);
    expect(deserialized.outputs).toEqual({
      message: "{{nodes.agent_loop.message}}",
      response_text: "{{nodes.agent_loop.response_text}}",
    });
  });

  it("should preserve workflow-level inputs on round-trip", () => {
    const workflow: Workflow = {
      name: "test-inputs",
      inputs: {
        model: createInput("model", {
          default: "claude-4.5-sonnet",
          description: "LLM model to use",
        }),
        mode: createInput("enum", {
          enumValues: ["manual", "auto"],
          default: "auto",
          description: "Execution mode",
        }),
        max_turns: createInput("integer", {
          default: "100",
          min: 1,
          max: 500,
        }),
      },
      nodes: [{ id: "step1", type: "run", command: "echo hello" }],
      edges: [],
    };

    const yaml = serializeWorkflowToYAML(workflow);
    expect(yaml).toContain("inputs:");
    expect(yaml).toContain("model:");
    expect(yaml).toContain("type: model");

    const deserialized = deserializeWorkflowFromYAML(yaml);
    expect(deserialized.inputs).toBeDefined();
    expect(deserialized.inputs?.model.type).toBe("model");
    expect(getInputEnumValues(deserialized.inputs?.mode)).toEqual(["manual", "auto"]);
  });

  describe("Real Workflow YAML Round-Trip", () => {
    const workflowDir = path.resolve(
      __dirname,
      "../../../../internal/workflow/builtin",
    );

    function testRoundTrip(filename: string) {
      const filePath = path.join(workflowDir, filename);

      // Skip if file doesn't exist (running in different environment)
      if (!fs.existsSync(filePath)) {
        console.log(`Skipping ${filename} - file not found at ${filePath}`);
        return;
      }

      const yamlContent = fs.readFileSync(filePath, "utf-8");

      // Deserialize
      const workflow = deserializeWorkflowFromYAML(yamlContent);

      // Re-serialize
      const reserializedYaml = serializeWorkflowToYAML(workflow);

      // Deserialize again
      const roundTripped = deserializeWorkflowFromYAML(reserializedYaml);

      // Compare key fields
      const differences = deepCompareFields(workflow, roundTripped);

      if (differences.length > 0) {
        console.log(`\n=== ${filename} differences ===`);
        differences.forEach((d) => console.log(d));
        console.log("\n=== Original workflow ===");
        console.log(JSON.stringify(workflow, null, 2).slice(0, 2000));
        console.log("\n=== Round-tripped workflow ===");
        console.log(JSON.stringify(roundTripped, null, 2).slice(0, 2000));
      }

      // Core fields that must be preserved
      expect(roundTripped.name).toBe(workflow.name);
      expect(roundTripped.presets?.tag).toBe(workflow.presets?.tag);
      expect(roundTripped.entry).toEqual(workflow.entry);
      expect(roundTripped.description).toBe(workflow.description);
      expect(roundTripped.nodes.length).toBe(workflow.nodes.length);

      // Inputs (including groups with type: "group")
      if (workflow.inputs) {
        expect(roundTripped.inputs).toBeDefined();
        expect(Object.keys(roundTripped.inputs || {})).toEqual(
          Object.keys(workflow.inputs),
        );
      }

      // Check each node
      workflow.nodes.forEach((node, i) => {
        const rtNode = roundTripped.nodes[i];
        expect(rtNode.id).toBe(node.id);
        expect(rtNode.type).toBe(node.type);

        // Check presets
        if (node.presets) {
          expect(rtNode.presets).toEqual(node.presets);
        }

        // Check thread (only on workflow steps)
        if ((node as any).thread) {
          expect((rtNode as any).thread).toEqual((node as any).thread);
        }

        // Check inline workflow
        if (node.inline) {
          expect(rtNode.inline).toBeDefined();
          expect(rtNode.inline?.nodes.length).toBe(node.inline.nodes.length);
        }

        // Check condition
        if (node.condition) {
          expect(rtNode.condition).toBe(node.condition);
        }
      });

      return differences;
    }

    it("agent.yaml round-trip should preserve all fields", () => {
      const differences = testRoundTrip("agent.yaml");
      expect(differences?.filter((d) => d.includes("LOST"))).toEqual([]);
    });

    it("structured-agent.yaml round-trip should preserve all fields", () => {
      const differences = testRoundTrip("structured-agent.yaml");
      expect(differences?.filter((d) => d.includes("LOST"))).toEqual([]);
    });
  });
});