import { describe, it, expect } from "vitest";
import { parseAskUserMetadata } from "../askUserUtils";

describe("parseAskUserMetadata", () => {
  it("returns null for null/undefined/empty metadata", () => {
    expect(parseAskUserMetadata(null)).toBeNull();
    expect(parseAskUserMetadata(undefined)).toBeNull();
    expect(parseAskUserMetadata("")).toBeNull();
  });

  it("returns null for non-ask_user metadata", () => {
    expect(parseAskUserMetadata("{}")).toBeNull();
    expect(parseAskUserMetadata('{"type":"other"}')).toBeNull();
  });

  it("returns null for ask_user with no questions", () => {
    expect(parseAskUserMetadata('{"type":"ask_user"}')).toBeNull();
    expect(parseAskUserMetadata('{"type":"ask_user","questions":[]}')).toBeNull();
  });

  it("parses new format (questions at top level)", () => {
    const metadata = JSON.stringify({
      type: "ask_user",
      tool_call_id: "call_abc123",
      questions: [
        {
          question: "Which approach?",
          options: [
            { label: "A", description: "Option A" },
            { label: "B", description: "Option B" },
          ],
          allow_multiple: false,
        },
      ],
    });

    const result = parseAskUserMetadata(metadata);
    expect(result).not.toBeNull();
    expect(result!.type).toBe("ask_user");
    expect(result!.tool_call_id).toBe("call_abc123");
    expect(result!.questions).toHaveLength(1);
    expect(result!.questions[0].question).toBe("Which approach?");
    expect(result!.questions[0].options).toHaveLength(2);
  });

  it("parses old envelope format (questions inside nested 'input' string)", () => {
    const innerInput = JSON.stringify({
      questions: [
        {
          question: "Pick a flavor",
          options: [
            { label: "Vanilla", description: "Simple" },
            { label: "Chocolate", description: "Rich" },
          ],
          allow_multiple: false,
        },
        {
          question: "Pick toppings",
          options: [
            { label: "Sprinkles", description: "Colorful" },
            { label: "Nuts", description: "Crunchy" },
          ],
          allow_multiple: true,
        },
      ],
    });

    const metadata = JSON.stringify({
      __reliant_tool_meta__: {
        available_tools: ["bash", "edit", "ask_user"],
      },
      input: innerInput,
      tool_call_id: "call_xyz789",
      type: "ask_user",
    });

    const result = parseAskUserMetadata(metadata);
    expect(result).not.toBeNull();
    expect(result!.type).toBe("ask_user");
    expect(result!.tool_call_id).toBe("call_xyz789");
    expect(result!.questions).toHaveLength(2);
    expect(result!.questions[0].question).toBe("Pick a flavor");
    expect(result!.questions[1].question).toBe("Pick toppings");
    expect(result!.questions[1].allow_multiple).toBe(true);
  });

  it("returns null for invalid JSON metadata", () => {
    expect(parseAskUserMetadata("not-json")).toBeNull();
    expect(parseAskUserMetadata("{broken")).toBeNull();
  });

  it("returns null for ask_user with invalid inner input JSON", () => {
    const metadata = JSON.stringify({
      type: "ask_user",
      input: "not-valid-json",
      tool_call_id: "call_bad",
    });
    expect(parseAskUserMetadata(metadata)).toBeNull();
  });

  it("handles multi-question new format correctly", () => {
    const metadata = JSON.stringify({
      type: "ask_user",
      tool_call_id: "call_multi",
      questions: [
        {
          question: "Question 1?",
          options: [{ label: "Yes", description: "" }],
          allow_multiple: false,
        },
        {
          question: "Question 2?",
          options: [
            { label: "A", description: "" },
            { label: "B", description: "" },
            { label: "C", description: "" },
          ],
          allow_multiple: true,
        },
        {
          question: "Question 3?",
          options: [{ label: "X", description: "" }],
          allow_multiple: false,
        },
      ],
    });

    const result = parseAskUserMetadata(metadata);
    expect(result).not.toBeNull();
    expect(result!.questions).toHaveLength(3);
    expect(result!.questions[1].allow_multiple).toBe(true);
    expect(result!.questions[1].options).toHaveLength(3);
  });
});