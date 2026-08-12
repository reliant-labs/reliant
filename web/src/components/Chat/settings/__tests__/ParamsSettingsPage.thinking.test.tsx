import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ParamsSettingsPage } from "../ParamsSettingsPage";
import type { InputDef } from "../../../../lib/inputHelpers";

const mocks = vi.hoisted(() => ({
  models: [
    {
      id: "claude-4.5-sonnet@anthropic",
      name: "Claude 4.5 Sonnet",
      provider: "Anthropic",
      driverId: "anthropic",
      canReason: true,
      supportedThinkingLevels: ["low", "medium", "high", "xhigh"],
      capabilities: ["reasoning"],
      tags: ["flagship", "reasoning"],
    },
  ],
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => vi.fn(),
}));

vi.mock("../../../../store/globalDataStore", () => ({
  useModels: () => ({ models: mocks.models, loading: false, error: null }),
  useGlobalDataStore: Object.assign(
    () => ({ models: mocks.models }),
    { getState: () => ({ models: mocks.models }) },
  ),
}));

// A workflow with a top-level `thinking_level` enum alongside its `model` input
// — the shape used by migrate.yaml, gsd.yaml and friends.
const modelInput: InputDef = {
  type: "model",
  config: {
    case: "modelInput",
    value: {
      base: { description: "LLM model to use", ui: "config" },
      default: { id: "", tags: ["flagship"], providers: [] },
    },
  },
} as unknown as InputDef;

const thinkingInput: InputDef = {
  type: "enum",
  config: {
    case: "enumInput",
    value: {
      base: { description: "Thinking effort", ui: "config" },
      enumValues: ["low", "medium", "high", "xhigh"],
      default: "medium",
    },
  },
} as unknown as InputDef;

function renderParams(values: Record<string, unknown>) {
  return render(
    <ParamsSettingsPage
      inputs={{ model: modelInput, thinking_level: thinkingInput }}
      values={values}
      onChange={() => {}}
      onBack={() => {}}
      onClose={() => {}}
      excludeParams={["model"]}
    />,
  );
}

const KNOWN_LEVELS = ["low", "medium", "high", "xhigh", "max", "ultra"];

/** Open the thinking_level dropdown once and read the options it offers. */
async function openThinkingOptions(current: string): Promise<string[]> {
  const trigger = await screen.findByRole("button", { name: current });
  fireEvent.click(trigger);
  return screen
    .getAllByRole("button")
    .map((button) => button.textContent ?? "")
    .filter((label) => KNOWN_LEVELS.includes(label))
    // Drop the trigger itself, leaving only the dropdown options.
    .slice(1);
}

describe("ParamsSettingsPage thinking_level", () => {
  // Options render in descending capability order.
  it("offers the model's thinking levels when the model is set explicitly", async () => {
    renderParams({ model: { tags: ["flagship"] }, thinking_level: "medium" });

    expect(await openThinkingOptions("medium")).toEqual([
      "xhigh",
      "high",
      "medium",
      "low",
    ]);
  });

  it("offers the model's thinking levels when the model comes from the schema default", async () => {
    renderParams({ thinking_level: "medium" });

    expect(await openThinkingOptions("medium")).toEqual([
      "xhigh",
      "high",
      "medium",
      "low",
    ]);
  });

  it("narrows the options to what the model supports", async () => {
    // gemini-style model: only low/high, so medium and xhigh must not be offered.
    mocks.models[0].supportedThinkingLevels = ["low", "high"];
    try {
      renderParams({ model: { tags: ["flagship"] }, thinking_level: "low" });

      expect(await openThinkingOptions("low")).toEqual(["high", "low"]);
    } finally {
      mocks.models[0].supportedThinkingLevels = ["low", "medium", "high", "xhigh"];
    }
  });
});
