import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ChatSettingsPopover } from "../ChatSettingsPopover";
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
}));

// A `model` input whose selection lives only in the schema default, which is the
// shape a fresh chat has before the user touches the model picker.
const modelInputWithTagDefault: InputDef = {
  type: "model",
  config: {
    case: "modelInput",
    value: {
      base: { description: "LLM model to use", ui: "config" },
      default: { id: "", tags: ["flagship"], providers: [] },
    },
  },
} as unknown as InputDef;

function renderModelPage(values: Record<string, unknown>) {
  return render(
    <ChatSettingsPopover
      isOpen
      onClose={() => {}}
      initialPage="model"
      inputs={{ model: modelInputWithTagDefault }}
      values={values}
      onChange={() => {}}
    />,
  );
}

function thinkingOptionLabels(): string[] {
  const select = screen.getByRole("combobox");
  return Array.from(select.querySelectorAll("option")).map(
    (option) => option.textContent ?? "",
  );
}

describe("ChatSettingsPopover thinking levels", () => {
  it("offers the model's thinking levels when the model is set explicitly", () => {
    renderModelPage({ model: { tags: ["flagship"] } });

    expect(thinkingOptionLabels()).toEqual([
      "Auto",
      "Low",
      "Medium",
      "High",
      "X-High",
    ]);
  });

  it("offers the model's thinking levels when the model comes from the schema default", () => {
    renderModelPage({});

    expect(thinkingOptionLabels()).toEqual([
      "Auto",
      "Low",
      "Medium",
      "High",
      "X-High",
    ]);
  });
});
