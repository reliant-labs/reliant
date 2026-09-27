import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ParamsSettingsPage } from "../ParamsSettingsPage";
import type { InputDef } from "../../../../lib/inputHelpers";

const mocks = vi.hoisted(() => ({
  tiers: {} as Record<string, { modelId: string; thinkingLevel: string }>,
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
  useModels: () => ({ models: mocks.models, tiers: mocks.tiers, loading: false, error: null }),
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

function renderParams(
  values: Record<string, unknown>,
  onChange: (values: Record<string, unknown>) => void = () => {},
  thinking: InputDef = thinkingInput,
) {
  return render(
    <ParamsSettingsPage
      inputs={{ model: modelInput, thinking_level: thinking }}
      values={values}
      onChange={onChange}
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

  describe("tag selector (tier owns the effort)", () => {
    // The agent workflow's thinking_level has no default: empty means "let the
    // model selector decide".
    const unsetThinkingInput = {
      ...thinkingInput,
      config: {
        case: "enumInput",
        value: {
          base: { description: "Thinking effort", ui: "config" },
          enumValues: ["low", "medium", "high", "xhigh"],
        },
      },
    } as unknown as InputDef;

    it("does not pin a concrete level when the tier decides", async () => {
      mocks.tiers = { flagship: { modelId: "claude-4.5-sonnet@anthropic", thinkingLevel: "xhigh" } };
      const onChange = vi.fn();
      try {
        renderParams({ model: { tags: ["flagship"] } }, onChange, unsetThinkingInput);

        // Empty renders as the tier's effort, and nothing is written back —
        // an explicit thinking_level would override the tier server-side.
        expect(await screen.findByRole("button", { name: "Auto (xhigh)" })).toBeTruthy();
        expect(onChange).not.toHaveBeenCalled();
      } finally {
        mocks.tiers = {};
      }
    });

    it("clears an unsupported level back to the tier rather than a model default", async () => {
      mocks.tiers = { flagship: { modelId: "claude-4.5-sonnet@anthropic", thinkingLevel: "high" } };
      mocks.models[0].supportedThinkingLevels = ["low", "high"];
      const onChange = vi.fn();
      try {
        renderParams({ model: { tags: ["flagship"] }, thinking_level: "xhigh" }, onChange, unsetThinkingInput);

        await screen.findByRole("button", { name: "xhigh" });
        expect(onChange).toHaveBeenCalledWith(
          expect.objectContaining({ thinking_level: "" }),
        );
      } finally {
        mocks.tiers = {};
        mocks.models[0].supportedThinkingLevels = ["low", "medium", "high", "xhigh"];
      }
    });
  });
});
