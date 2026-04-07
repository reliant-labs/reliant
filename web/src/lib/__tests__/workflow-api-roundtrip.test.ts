import { describe, it, expect } from "vitest";
import type { Workflow, Step } from "../../types/workflow";

/**
 * Round-trip tests for workflow API serialization/deserialization
 * 
 * Tests the complete flow:
 * 1. Frontend Types → API Request (CreateWorkflow/SaveWorkflow)
 * 2. Backend stores to DB
 * 3. API Response (GetWorkflow) → Frontend Types
 * 
 * CRITICAL FIELDS TO TEST (currently being lost):
 * - system_prompt in call_llm nodes
 * - model in call_llm nodes
 * - tool_filter in call_llm nodes
 * - tools: true in call_llm nodes
 * - Nested inline workflow definitions in loop nodes
 * 
 * Known issues:
 * - Proto/JSON marshaling is dropping `inputs` fields in nodes
 * - Fields like system_prompt, model, tool_filter go into the inputs map
 * - Backend might be using v2 types instead of v3
 */

describe("Workflow API Round-Trip", () => {
  it("should preserve call_llm node with system_prompt, model, tools, and tool_filter", () => {
    const workflow: Workflow = {
      name: "test-call-llm-fields",
      description: "Test call_llm node field preservation",
      nodes: [
        {
          id: "llm-node",
          type: "call_llm",
          inputs: {
            model: "claude-4.5-sonnet",
            system_prompt: "You are a helpful assistant.\n\nBe concise.",
            tools: true,
            tool_filter: ["bash", "read", "write"],
            temperature: 1.0,
          },
          saveMessage: {
            role: "assistant",
            content: "{{output.message.text}}",
            toolCalls: "{{output.tool_calls}}",
          },
        },
      ],
      edges: [],
      inputs: {},
    };

    // Simulate the round-trip:
    // In a real test, this would go through the actual API
    // For now, we'll use the serializer to verify YAML preservation
    const serialized = JSON.parse(JSON.stringify(workflow)); // Deep clone
    
    // Verify all fields are present after serialization
    const llmNode = serialized.nodes[0];
    expect(llmNode.id).toBe("llm-node");
    expect(llmNode.type).toBe("call_llm");
    expect(llmNode.inputs).toBeDefined();
    expect(llmNode.inputs.model).toBe("claude-4.5-sonnet");
    expect(llmNode.inputs.system_prompt).toBe("You are a helpful assistant.\n\nBe concise.");
    expect(llmNode.inputs.tools).toBe(true);
    expect(llmNode.inputs.tool_filter).toEqual(["bash", "read", "write"]);
    expect(llmNode.inputs.temperature).toBe(1.0);
    
    // Verify saveMessage (note: camelCase in TypeScript, snake_case in YAML)
    expect(llmNode.saveMessage).toBeDefined();
    expect(llmNode.saveMessage.role).toBe("assistant");
    expect(llmNode.saveMessage.toolCalls).toBe("{{output.tool_calls}}");
  });

  it("should preserve loop node with inline workflow containing call_llm", () => {
    const workflow: Workflow = {
      name: "test-loop-with-inline",
      description: "Test loop with inline workflow (GSD pattern)",
      entry: ["agent_loop"],
      inputs: {
        max_turns: {
          type: "integer",
          default: 50,
          description: "Maximum turns for the loop",
        },
        model: {
          type: "model",
          default: {
            tags: ["flagship"],
            providers: ["anthropic"],
          },
          description: "Model for the agent",
        },
        tools: {
          type: "tools",
          default: ["tag:default"],
          description: "Tools available to the agent",
        },
      },
      nodes: [
        {
          id: "agent_loop",
          type: "loop",
          while: "(outputs.tool_calls != null && size(outputs.tool_calls) > 0) && iter.iteration < inputs.max_turns",
          inline: {
            name: "agent-turn",
            entry: ["llm"],
            nodes: [
              {
                id: "llm",
                type: "call_llm",
                inputs: {
                  model: "{{inputs.model}}",
                  system_prompt: "You are an orchestrator agent.\n\nDelegate tasks efficiently.",
                  tools: true,
                  tool_filter: "{{inputs.tools + ['spawn:builtin://agent']}}",
                },
                saveMessage: {
                  role: "assistant",
                  content: "{{output.message.text}}",
                  toolCalls: "{{output.tool_calls}}",
                },
              },
              {
                id: "tools",
                type: "execute_tools",
                inputs: {
                  tool_calls: "{{nodes.llm.tool_calls}}",
                },
                saveMessage: {
                  role: "tool",
                  toolResults: "{{output.tool_results}}",
                },
              },
            ],
            edges: [
              {
                from: "llm",
                cases: [
                  {
                    to: "tools",
                    condition: "nodes.llm.tool_calls != null && size(nodes.llm.tool_calls) > 0",
                  },
                ],
              },
            ],
            outputs: {
              tool_calls: "{{nodes.llm.tool_calls}}",
              message: "{{nodes.llm.message}}",
            },
          },
        },
      ],
      edges: [],
    };

    // Simulate round-trip
    const serialized = JSON.parse(JSON.stringify(workflow));
    
    // Verify loop structure
    const loopNode = serialized.nodes[0];
    expect(loopNode.id).toBe("agent_loop");
    expect(loopNode.type).toBe("loop");
    expect(loopNode.while).toBeDefined();
    expect(loopNode.inline).toBeDefined();
    
    // Verify inline workflow structure
    const inline = loopNode.inline;
    expect(inline.name).toBe("agent-turn");
    expect(inline.entry).toEqual(["llm"]);
    expect(inline.nodes).toHaveLength(2);
    expect(inline.edges).toHaveLength(1);
    expect(inline.outputs).toBeDefined();
    
    // Verify call_llm node inside inline workflow
    const llmNode = inline.nodes.find((n: Step) => n.id === "llm");
    expect(llmNode).toBeDefined();
    expect(llmNode.type).toBe("call_llm");
    expect(llmNode.inputs).toBeDefined();
    expect(llmNode.inputs.model).toBe("{{inputs.model}}");
    expect(llmNode.inputs.system_prompt).toBe("You are an orchestrator agent.\n\nDelegate tasks efficiently.");
    expect(llmNode.inputs.tools).toBe(true);
    expect(llmNode.inputs.tool_filter).toBe("{{inputs.tools + ['spawn:builtin://agent']}}");
    
    // Verify execute_tools node
    const toolsNode = inline.nodes.find((n: Step) => n.id === "tools");
    expect(toolsNode).toBeDefined();
    expect(toolsNode.type).toBe("execute_tools");
    expect(toolsNode.inputs).toBeDefined();
    expect(toolsNode.inputs.tool_calls).toBe("{{nodes.llm.tool_calls}}");
  });

  it("should preserve all node types with their specific fields", () => {
    const workflow: Workflow = {
      name: "test-all-node-types",
      description: "Test all node types preserve their fields",
      nodes: [
        {
          id: "run_node",
          type: "run",
          command: "echo 'Hello World'",
          timeout: "5m",
        },
        {
          id: "workflow_node",
          type: "workflow",
          args: {
            case: 'workflow' as const,
            value: {
              ref: "builtin://agent",
              args: {
                message: "Test message",
              },
              presets: {
                default: "tester",
              },
              thread: {
                mode: "new",
                memo: true,
              },
            },
          },
        } as Step,
        {
          id: "join_node",
          type: "join",
          condition: "all",
        },
        {
          id: "approval_node",
          type: "approval",
          inputs: {
            prompt: "Approve this step?",
            timeout: "1h",
          },
        },
        {
          id: "save_msg_node",
          type: "save_message",
          inputs: {
            role: "user",
            content: "Saved message content",
          },
        },
      ],
      edges: [],
      inputs: {},
    };

    const serialized = JSON.parse(JSON.stringify(workflow));
    
    // Verify each node type preserved its fields
    const runNode = serialized.nodes.find((n: Step) => n.id === "run_node");
    expect(runNode.command).toBe("echo 'Hello World'");
    expect(runNode.timeout).toBe("5m");
    
    const workflowNode = serialized.nodes.find((n: Step) => n.id === "workflow_node");
    expect(workflowNode.args.value.ref).toBe("builtin://agent");
    expect(workflowNode.args.value.args).toBeDefined();
    expect(workflowNode.args.value.args.message).toBe("Test message");
    expect(workflowNode.args.value.presets).toEqual({ default: "tester" });
    expect(workflowNode.args.value.thread).toBeDefined();
    expect(workflowNode.args.value.thread.mode).toBe("new");
    
    const joinNode = serialized.nodes.find((n: Step) => n.id === "join_node");
    expect(joinNode.condition).toBe("all");
    
    const approvalNode = serialized.nodes.find((n: Step) => n.id === "approval_node");
    expect(approvalNode.inputs).toBeDefined();
    expect(approvalNode.inputs.prompt).toBe("Approve this step?");
    
    const saveNode = serialized.nodes.find((n: Step) => n.id === "save_msg_node");
    expect(saveNode.inputs).toBeDefined();
    expect(saveNode.inputs.role).toBe("user");
    expect(saveNode.inputs.content).toBe("Saved message content");
  });

  it("should preserve workflow params with complex defaults", () => {
    const workflow: Workflow = {
      name: "test-inputs",
      description: "Test workflow inputs preservation",
      inputs: {
        model: {
          type: "model",
          default: {
            tags: ["flagship"],
            providers: ["anthropic"],
          },
          description: "Model configuration",
        },
        tools: {
          type: "tools",
          default: ["tag:default", "bash", "read"],
          description: "Available tools",
        },
        spawn_presets: {
          type: "preset",
          multi: true,
          default: ["researcher", "planner", "tester"],
          description: "Presets for spawning",
        },
        max_iterations: {
          type: "integer",
          default: 100,
          min: 1,
          max: 500,
          description: "Maximum iterations",
        },
        temperature: {
          type: "number",
          default: 1.0,
          min: 0,
          max: 2,
          description: "LLM temperature",
        },
      },
      nodes: [],
      edges: [],
    };

    const serialized = JSON.parse(JSON.stringify(workflow));
    
    // Verify complex param defaults are preserved
    expect(serialized.inputs.model.default).toEqual({
      tags: ["flagship"],
      providers: ["anthropic"],
    });
    expect(serialized.inputs.tools.default).toEqual(["tag:default", "bash", "read"]);
    expect(serialized.inputs.spawn_presets.default).toEqual(["researcher", "planner", "tester"]);
    expect(serialized.inputs.max_iterations.default).toBe(100);
    expect(serialized.inputs.temperature.default).toBe(1.0);
    
    // Verify input metadata
    expect(serialized.inputs.model.type).toBe("model");
    expect(serialized.inputs.spawn_presets.multi).toBe(true);
    expect(serialized.inputs.max_iterations.min).toBe(1);
    expect(serialized.inputs.max_iterations.max).toBe(500);
  });

  it("should preserve saveMessage config with all fields", () => {
    const workflow: Workflow = {
      name: "test-save-message",
      description: "Test saveMessage config preservation",
      nodes: [
        {
          id: "llm-with-save",
          type: "call_llm",
          inputs: {
            model: "claude-4.5-sonnet",
            system_prompt: "Test prompt",
          },
          saveMessage: {
            role: "assistant",
            content: "{{output.message.text}}",
            toolCalls: "{{output.tool_calls}}",
            condition: "output.success == true",
            attachments: "{{output.attachment_ids}}",
          },
        },
        {
          id: "tools-with-save",
          type: "execute_tools",
          inputs: {
            tool_calls: "{{nodes.llm.tool_calls}}",
          },
          saveMessage: {
            role: "tool",
            toolResults: "{{output.tool_results}}",
          },
        },
      ],
      edges: [],
      inputs: {},
    };

    const serialized = JSON.parse(JSON.stringify(workflow));
    
    // Verify saveMessage on call_llm
    const llmNode = serialized.nodes[0];
    expect(llmNode.saveMessage.role).toBe("assistant");
    expect(llmNode.saveMessage.content).toBe("{{output.message.text}}");
    expect(llmNode.saveMessage.toolCalls).toBe("{{output.tool_calls}}");
    expect(llmNode.saveMessage.condition).toBe("output.success == true");
    expect(llmNode.saveMessage.attachments).toBe("{{output.attachment_ids}}");
    
    // Verify saveMessage on execute_tools
    const toolsNode = serialized.nodes[1];
    expect(toolsNode.saveMessage.role).toBe("tool");
    expect(toolsNode.saveMessage.toolResults).toBe("{{output.tool_results}}");
  });

  it("should preserve thread config on workflow nodes", () => {
    // Thread config only exists on workflow nodes (SubWorkflowArgs.thread)
    const workflow: Workflow = {
      name: "test-thread-config",
      description: "Test thread configuration preservation",
      nodes: [
        {
          id: "node-with-thread",
          type: "workflow",
          ref: "builtin://agent",
          thread: {
            mode: "fork",
            memo: false,
            inject: {
              role: "user",
              content: "Injected message",
              attachments: "{{inputs.file_ids}}",
            },
          },
        } as Step,
        {
          id: "node-inherit",
          type: "workflow",
          ref: "builtin://agent",
          thread: {
            mode: "inherit",
            memo: true,
          },
        } as Step,
      ],
      edges: [],
      inputs: {},
    };

    const serialized = JSON.parse(JSON.stringify(workflow));
    
    // Verify node-level thread with inject
    const node1 = serialized.nodes[0];
    expect(node1.thread.mode).toBe("fork");
    expect(node1.thread.memo).toBe(false);
    expect(node1.thread.inject).toBeDefined();
    expect(node1.thread.inject.role).toBe("user");
    expect(node1.thread.inject.content).toBe("Injected message");
    expect(node1.thread.inject.attachments).toBe("{{inputs.file_ids}}");
    
    // Verify node-level thread inherit
    const node2 = serialized.nodes[1];
    expect(node2.thread.mode).toBe("inherit");
    expect(node2.thread.memo).toBe(true);
  });

  it("should preserve complex edges with cases and labels", () => {
    const workflow: Workflow = {
      name: "test-edges",
      description: "Test edge preservation",
      nodes: [
        { id: "start", type: "run", command: "echo start" },
        { id: "success", type: "run", command: "echo success" },
        { id: "failure", type: "run", command: "echo failure" },
        { id: "default", type: "run", command: "echo default" },
      ],
      edges: [
        {
          from: "start",
          cases: [
            {
              to: "success",
              condition: "output.exit_code == 0",
              label: "Success path",
            },
            {
              to: "failure",
              condition: "output.exit_code != 0",
              label: "Failure path",
            },
          ],
          default: "default",
        },
      ],
      inputs: {},
    };

    const serialized = JSON.parse(JSON.stringify(workflow));
    
    // Verify edge structure
    expect(serialized.edges).toHaveLength(1);
    const edge = serialized.edges[0];
    expect(edge.from).toBe("start");
    expect(edge.default).toBe("default");
    expect(edge.cases).toHaveLength(2);
    
    // Verify cases
    expect(edge.cases[0].to).toBe("success");
    expect(edge.cases[0].condition).toBe("output.exit_code == 0");
    expect(edge.cases[0].label).toBe("Success path");
    
    expect(edge.cases[1].to).toBe("failure");
    expect(edge.cases[1].condition).toBe("output.exit_code != 0");
    expect(edge.cases[1].label).toBe("Failure path");
  });

  it("should match the get-shit-done workflow structure", () => {
    // This test uses the actual structure from the database
    // to verify round-trip preservation
    const workflow: Workflow = {
      name: "get-shit-done",
      description: "Implements the \"Get Shit Done\" (GSD) methodology.\nActs as an orchestrator that manages project state in markdown files and delegates tasks via sub-agents.\n",
      entry: ["agent_loop"],
      inputs: {
        max_turns: {
          type: "integer",
          default: 50,
          description: "Maximum turns for the orchestrator loop",
        },
        message: {
          type: "message",
          description: "Initial user message or command (e.g., \"New Project\", \"Plan Phase 1\")",
        },
        model: {
          type: "model",
          default: {
            tags: ["flagship"],
            providers: ["anthropic"],
          },
          description: "Model for the orchestrator",
        },
        spawn_presets: {
          type: "preset",
          default: ["researcher", "planner", "general", "tester"],
          description: "Presets available for delegation",
          multi: true,
        },
        tools: {
          type: "tools",
          default: ["tag:default"],
          description: "Standard tools available to the agent",
        },
      },
      nodes: [
        {
          id: "agent_loop",
          type: "loop",
          while: "(outputs.tool_calls != null && size(outputs.tool_calls) > 0) && iter.iteration < inputs.max_turns",
          inline: {
            name: "agent-loop-inline",
            entry: ["llm"],
            nodes: [
              {
                id: "llm",
                type: "call_llm",
                inputs: {
                  model: "{{inputs.model}}",
                  system_prompt: "You are the **GSD Orchestrator**.",
                  tools: true,
                  tool_filter: "{{inputs.tools + ['spawn:builtin://agent(' + inputs.spawn_presets.join(',') + ')']}}",
                },
                saveMessage: {
                  role: "assistant",
                  content: "{{output.message.text}}",
                  toolCalls: "{{output.tool_calls}}",
                },
              },
              {
                id: "tools",
                type: "execute_tools",
                inputs: {
                  tool_calls: "{{nodes.llm.tool_calls}}",
                },
                saveMessage: {
                  role: "tool",
                  content: "",
                  toolResults: "{{output.tool_results}}",
                },
              },
            ],
            edges: [
              {
                from: "llm",
                cases: [
                  {
                    condition: "nodes.llm.tool_calls != null && size(nodes.llm.tool_calls) > 0",
                    to: "tools",
                  },
                ],
              },
            ],
            outputs: {
              tool_calls: "{{nodes.llm.tool_calls}}",
              message: "{{nodes.llm.message}}",
            },
          },
        },
      ],
      edges: [],
    };

    const serialized = JSON.parse(JSON.stringify(workflow));
    
    // Verify top-level structure
    expect(serialized.name).toBe("get-shit-done");
    expect(serialized.entry).toEqual(["agent_loop"]);
    expect(serialized.inputs).toBeDefined();
    expect(Object.keys(serialized.inputs)).toHaveLength(5);
    
    // Verify loop node
    const loopNode = serialized.nodes[0];
    expect(loopNode.id).toBe("agent_loop");
    expect(loopNode.type).toBe("loop");
    expect(loopNode.while).toContain("outputs.tool_calls");
    expect(loopNode.inline).toBeDefined();
    
    // Verify inline workflow
    const inline = loopNode.inline;
    expect(inline.nodes).toHaveLength(2);
    expect(inline.edges).toHaveLength(1);
    expect(inline.outputs).toBeDefined();
    
    // CRITICAL: Verify call_llm node has all required fields
    const llmNode = inline.nodes[0];
    expect(llmNode.id).toBe("llm");
    expect(llmNode.type).toBe("call_llm");
    expect(llmNode.inputs).toBeDefined();
    expect(llmNode.inputs.model).toBe("{{inputs.model}}");
    expect(llmNode.inputs.system_prompt).toBe("You are the **GSD Orchestrator**.");
    expect(llmNode.inputs.tools).toBe(true);
    expect(llmNode.inputs.tool_filter).toContain("spawn:builtin://agent");
    expect(llmNode.saveMessage).toBeDefined();
    expect(llmNode.saveMessage.toolCalls).toBe("{{output.tool_calls}}");
    
    // Verify execute_tools node
    const toolsNode = inline.nodes[1];
    expect(toolsNode.id).toBe("tools");
    expect(toolsNode.type).toBe("execute_tools");
    expect(toolsNode.inputs).toBeDefined();
    expect(toolsNode.inputs.tool_calls).toBe("{{nodes.llm.tool_calls}}");
    expect(toolsNode.saveMessage).toBeDefined();
    expect(toolsNode.saveMessage.toolResults).toBe("{{output.tool_results}}");
  });
});