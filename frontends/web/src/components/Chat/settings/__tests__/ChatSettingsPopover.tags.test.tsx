import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ChatSettingsPopover } from "../ChatSettingsPopover";
import type { InputDef } from "../../../../lib/inputHelpers";

const mocks = vi.hoisted(() => ({
  models: [
    {
      id: "claude-5.1-fable@anthropic",
      name: "Claude 5.1 Fable",
      provider: "Anthropic",
      driverId: "anthropic",
      canReason: true,
      supportedThinkingLevels: ["low", "medium", "high", "xhigh"],
      capabilities: ["reasoning"],
      tags: ["powerful", "flagship", "reasoning"],
    },
  ],
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => vi.fn(),
}));

vi.mock("../../../../store/globalDataStore", () => ({
  useModels: () => ({ models: mocks.models, loading: false, error: null }),
}));

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

function renderModelPage() {
  return render(
    <ChatSettingsPopover
      isOpen
      onClose={() => {}}
      initialPage="model"
      inputs={{ model: modelInput }}
      values={{ model: { tags: ["flagship"] } }}
      onChange={() => {}}
    />,
  );
}

describe("ChatSettingsPopover tag selection", () => {
  it("offers powerful ahead of the other selector tags", () => {
    renderModelPage();

    // The tag rows render the tag name as their heading, in TAGS order.
    for (const tag of ["powerful", "flagship", "moderate", "fast", "cheap"]) {
      expect(screen.getByText(tag)).toBeInTheDocument();
    }

    const powerful = screen.getByText("powerful");
    const flagship = screen.getByText("flagship");
    expect(
      powerful.compareDocumentPosition(flagship) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("describes what powerful selects for", () => {
    renderModelPage();

    expect(
      screen.getByText("Maximum capability — the hardest work"),
    ).toBeInTheDocument();
  });
});
