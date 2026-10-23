import { describe, it, expect } from "vitest";
import { askUserQuestionItemsFromInput, isAsyncAskUserInput, parseAskUserMetadata } from "../askUserUtils";

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

  it("recovers questions persisted as a double-encoded JSON string", () => {
    const questions = [
      {
        question: "Which approach?",
        options: [{ label: "A", description: "Option A" }],
        allow_multiple: false,
      },
    ];
    const metadata = JSON.stringify({
      type: "ask_user",
      tool_call_id: "call_double",
      questions: JSON.stringify(questions),
    });

    const result = parseAskUserMetadata(metadata);
    expect(result).not.toBeNull();
    expect(Array.isArray(result!.questions)).toBe(true);
    expect(result!.questions).toHaveLength(1);
    expect(result!.questions[0].question).toBe("Which approach?");
    expect(result!.questions[0].options).toHaveLength(1);
  });

  it("returns null when questions is a plain object", () => {
    const metadata = JSON.stringify({
      type: "ask_user",
      tool_call_id: "call_obj",
      questions: { question: "Solo?", options: [] },
    });
    expect(parseAskUserMetadata(metadata)).toBeNull();
  });

  it("returns null when questions is a number or garbage string", () => {
    expect(
      parseAskUserMetadata(JSON.stringify({ type: "ask_user", questions: 42 }))
    ).toBeNull();
    expect(
      parseAskUserMetadata(JSON.stringify({ type: "ask_user", questions: "not json at all" }))
    ).toBeNull();
    // Valid JSON string, but not an array
    expect(
      parseAskUserMetadata(JSON.stringify({ type: "ask_user", questions: '{"question":"hi"}' }))
    ).toBeNull();
  });

  it("filters out entries without a string question field", () => {
    const metadata = JSON.stringify({
      type: "ask_user",
      tool_call_id: "call_filter",
      questions: [
        "just a string",
        42,
        null,
        { no_question: true },
        { question: 123 },
        { question: "Valid one?" }, // no options — should be coerced to []
      ],
    });

    const result = parseAskUserMetadata(metadata);
    expect(result).not.toBeNull();
    expect(result!.questions).toHaveLength(1);
    expect(result!.questions[0].question).toBe("Valid one?");
    expect(result!.questions[0].options).toEqual([]);
  });

  it("returns null when no valid question entries remain", () => {
    const metadata = JSON.stringify({
      type: "ask_user",
      questions: [null, 42, { nope: true }],
    });
    expect(parseAskUserMetadata(metadata)).toBeNull();
  });

  it("recovers double-encoded questions inside the envelope format", () => {
    const questions = [
      {
        question: "Envelope double?",
        options: [{ label: "Yes", description: "" }],
        allow_multiple: false,
      },
    ];
    const metadata = JSON.stringify({
      __reliant_tool_meta__: { available_tools: ["ask_user"] },
      type: "ask_user",
      tool_call_id: "call_env_double",
      input: JSON.stringify({ questions: JSON.stringify(questions) }),
    });

    const result = parseAskUserMetadata(metadata);
    expect(result).not.toBeNull();
    expect(Array.isArray(result!.questions)).toBe(true);
    expect(result!.questions).toHaveLength(1);
    expect(result!.questions[0].question).toBe("Envelope double?");
  });

  it("returns null for envelope format with non-array questions", () => {
    const asObject = JSON.stringify({
      type: "ask_user",
      input: JSON.stringify({ questions: { question: "Solo?" } }),
    });
    expect(parseAskUserMetadata(asObject)).toBeNull();

    const asNumber = JSON.stringify({
      type: "ask_user",
      input: JSON.stringify({ questions: 7 }),
    });
    expect(parseAskUserMetadata(asNumber)).toBeNull();

    const asGarbageString = JSON.stringify({
      type: "ask_user",
      input: JSON.stringify({ questions: "garbage {" }),
    });
    expect(parseAskUserMetadata(asGarbageString)).toBeNull();
  });

  it("filters invalid entries in the envelope format", () => {
    const metadata = JSON.stringify({
      type: "ask_user",
      tool_call_id: "call_env_filter",
      input: JSON.stringify({
        questions: [{ question: "Keep me" }, { bogus: true }, "nope"],
      }),
    });

    const result = parseAskUserMetadata(metadata);
    expect(result).not.toBeNull();
    expect(result!.questions).toHaveLength(1);
    expect(result!.questions[0].question).toBe("Keep me");
    expect(result!.questions[0].options).toEqual([]);
  });
});
describe("isAsyncAskUserInput", () => {
  it("is async by default: only blocking: true waits", () => {
    expect(isAsyncAskUserInput({ questions: [] })).toBe(true);
    expect(isAsyncAskUserInput('{"questions":[]}')).toBe(true);
    expect(isAsyncAskUserInput({ blocking: false, questions: [] })).toBe(true);
    expect(isAsyncAskUserInput({ blocking: true, questions: [] })).toBe(false);
    expect(isAsyncAskUserInput({ blocking: " TRUE ", questions: [] })).toBe(false);
  });

  it("reads the flag inside the tool-meta envelope", () => {
    const inner = JSON.stringify({ blocking: true, questions: [] });
    expect(isAsyncAskUserInput({ __reliant_tool_meta__: {}, input: inner })).toBe(false);
  });
});

describe("askUserQuestionItemsFromInput", () => {
  const q = { question: "Which database?", options: [{ label: "Postgres", description: "" }] };
  const want = [{ question: "Which database?", options: q.options, allowMultiple: false }];

  it("reads every shape a model writes questions in", () => {
    expect(askUserQuestionItemsFromInput({ questions: [q] })).toEqual(want);
    // Prod, 2026-10-22: a JSON-encoded string, inside a JSON-string input.
    expect(askUserQuestionItemsFromInput(JSON.stringify({ questions: JSON.stringify([q]) }))).toEqual(want);
    expect(askUserQuestionItemsFromInput({ questions: q })).toEqual(want);
    expect(askUserQuestionItemsFromInput({ __reliant_tool_meta__: {}, input: JSON.stringify({ questions: [q] }) })).toEqual(want);
  });

  it("falls back to the bare question text", () => {
    const text = [{ question: "Which database?", options: [], allowMultiple: false }];
    expect(askUserQuestionItemsFromInput({ questions: " Which database? " })).toEqual(text);
    expect(askUserQuestionItemsFromInput({ question: "Which database?" })).toEqual(text);
  });

  it("is null only when there is no question text at all", () => {
    expect(askUserQuestionItemsFromInput(undefined)).toBeNull();
    expect(askUserQuestionItemsFromInput("not json")).toBeNull();
    expect(askUserQuestionItemsFromInput({ questions: 42 })).toBeNull();
    expect(askUserQuestionItemsFromInput({ questions: "  " })).toBeNull();
  });
});
