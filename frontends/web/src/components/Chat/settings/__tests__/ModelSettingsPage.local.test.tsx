import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ModelSettingsPage } from "../ModelSettingsPage";

const mocks = vi.hoisted(() => ({
  models: [
    {
      id: "claude-5-opus@anthropic",
      name: "Claude 5 Opus",
      provider: "Anthropic",
      driverId: "anthropic",
      capabilities: [],
      tags: [],
    },
    {
      id: "qwen3:latest@local",
      name: "qwen3:latest",
      provider: "Local",
      driverId: "local",
      contextWindow: 4096,
      local: { daemonId: "d1", machineName: "MacBook", endpointId: "ollama", endpointKind: "ollama", online: true },
      capabilities: [],
      tags: [],
    },
    {
      id: "qwen3:latest@local",
      name: "qwen3:latest",
      provider: "Local",
      driverId: "local",
      contextWindow: 65536,
      local: { daemonId: "d2", machineName: "Studio", endpointId: "ollama", endpointKind: "ollama", online: false },
      capabilities: [],
      tags: [],
    },
  ],
}));

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));
vi.mock("../../../../store/globalDataStore", () => ({
  useModels: () => ({ models: mocks.models, tiers: {}, loading: false, error: null }),
}));

function renderPage(value: unknown, onChange = vi.fn()) {
  render(<ModelSettingsPage value={value} onChange={onChange} onBack={() => {}} onClose={() => {}} />);
  return onChange;
}

describe("ModelSettingsPage local models", () => {
  it("groups local models under one 'Local · <machine>' heading per machine", () => {
    renderPage({ id: "claude-5-opus@anthropic" });
    expect(screen.getByText("Local · MacBook")).toBeTruthy();
    expect(screen.getByText("Local · Studio")).toBeTruthy();
    expect(screen.queryByText("Local")).toBeNull();
  });

  it("disables models on an offline machine", () => {
    renderPage({ id: "claude-5-opus@anthropic" });
    const buttons = screen.getAllByRole("button", { name: /qwen3:latest/ });
    expect(buttons).toHaveLength(2);
    expect((buttons[0] as HTMLButtonElement).disabled).toBe(false);
    expect((buttons[1] as HTMLButtonElement).disabled).toBe(true);
  });

  it("selecting a local model writes id and providers, keeping overrides", async () => {
    const onChange = renderPage({ id: "claude-5-opus@anthropic", temperature: 0.4 });
    await userEvent.click(screen.getAllByRole("button", { name: /qwen3:latest/ })[0]);
    expect(onChange).toHaveBeenCalledWith({
      id: "qwen3:latest@local",
      providers: ["local:d1"],
      temperature: 0.4,
    });
  });

  it("selecting a cloud model drops any previous local providers", async () => {
    const onChange = renderPage({ id: "qwen3:latest@local", providers: ["local:d1"] });
    await userEvent.click(screen.getByRole("button", { name: /Claude 5 Opus/ }));
    expect(onChange).toHaveBeenCalledWith({ id: "claude-5-opus@anthropic" });
  });

  it("shows each local model's context, warning only when under 64K", () => {
    renderPage({ id: "claude-5-opus@anthropic" });
    const [small, large] = screen.getAllByTestId("local-context");
    expect(small.textContent).toMatch(/4K ctx · too small for agents/);
    expect(large.textContent).toBe("64K ctx");
  });
});
