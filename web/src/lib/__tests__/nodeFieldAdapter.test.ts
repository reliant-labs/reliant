import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import { NodeInputFieldSchema } from "../../gen/reliant/v1/catalog_pb";
import { nodeInputFieldToSchema } from "../nodeFieldAdapter";

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
});
