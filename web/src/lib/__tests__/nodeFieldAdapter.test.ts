import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import { NodeFieldOptionSchema, NodeInputFieldSchema } from "../../gen/reliant/v1/catalog_pb";
import { createInput } from "../inputHelpers";
import { inputDefToSchema, nodeInputFieldToSchema } from "../nodeFieldAdapter";

describe("nodeInputFieldToSchema", () => {
  it("carries a built-in field's example and type hint (Execute Tools' Tool calls)", () => {
    const schema = nodeInputFieldToSchema(
      create(NodeInputFieldSchema, {
        name: "tool_calls",
        type: "string",
        label: "Tool calls",
        description: "The tool calls to run: wire in the tool_calls output of the Call LLM step that requested them",
        example: "{{nodes.call_llm.tool_calls}}",
        typeHint: "list of tool calls",
        isCel: true,
      }),
    );
    expect(schema).toMatchObject({
      label: "Tool calls",
      example: "{{nodes.call_llm.tool_calls}}",
      typeHint: "list of tool calls",
      description: expect.stringContaining("Call LLM"),
    });
    // The description is printed inline; the popover only adds a default or range.
    expect(schema.helpText).toBeUndefined();
  });

  it("writes a YAML list example the way the comma-separated input takes it", () => {
    const schema = nodeInputFieldToSchema(create(NodeInputFieldSchema, { name: "skills", type: "array", example: "[forge/db, code-review]", isCel: true }));
    expect(schema.example).toBe("forge/db, code-review");
  });

  // Thinking level is an enum with a default of "none". An empty "None" option
  // beside the "none" value offered the same choice twice.
  it("offers each enum value once when the field has a default", () => {
    const schema = nodeInputFieldToSchema(
      create(NodeInputFieldSchema, {
        name: "thinking_level",
        type: "string",
        enumValues: ["none", "low", "medium", "high", "xhigh"],
        defaultValue: "none",
        isCel: true,
      }),
    );
    expect(schema.allowEmptyOption).toBe(false);
    expect(schema.options?.map((o) => o.value)).toEqual(["none", "low", "medium", "high", "xhigh"]);
    expect(schema.helpText).toBe("Default: none");
  });

  // Run Tool's tool was a free-text box: the author had to know the tool's
  // exact name. ListNodes now lists the tools a node may run, with what each
  // does, so the field is a picker — still with manual entry and Expression.
  it("makes a field the server lists options for a picker, with each option's description", () => {
    const schema = nodeInputFieldToSchema(
      create(NodeInputFieldSchema, {
        name: "tool",
        type: "string",
        label: "Tool",
        uiHint: "node_tool",
        isCel: true,
        options: [
          create(NodeFieldOptionSchema, { value: "view", label: "view", description: "Read a file." }),
          create(NodeFieldOptionSchema, { value: "websearch", label: "", description: "" }),
        ],
      }),
    );
    expect(schema).toMatchObject({ widget: "picker", valueKind: "string", celCapable: true, showCelModeToggle: true });
    expect(schema.options).toEqual([
      { value: "view", label: "view", description: "Read a file." },
      { value: "websearch", label: "websearch", description: undefined },
    ]);
  });

  it("keeps a node_tool field a picker while the list is empty, so manual entry is offered", () => {
    const schema = nodeInputFieldToSchema(create(NodeInputFieldSchema, { name: "tool", type: "string", uiHint: "node_tool", isCel: true }));
    expect(schema.widget).toBe("picker");
    expect(schema.options).toEqual([]);
  });
});

describe("inputDefToSchema", () => {
  it("carries a workflow input's example into the run form field", () => {
    const schema = inputDefToSchema("channel", createInput("string", { description: "Where to post", example: "C0123ABCDEF" }));
    expect(schema).toMatchObject({ description: "Where to post", example: "C0123ABCDEF" });
  });
});
