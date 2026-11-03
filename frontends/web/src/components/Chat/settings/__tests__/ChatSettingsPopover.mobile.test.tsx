import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ChatSettingsPopover } from "../ChatSettingsPopover";
import { type InputDef, setInputMulti, setInputEnumValues } from "../../../../lib/inputHelpers";

import { SurfaceProvider } from "../../../../lib/surfaceContext";
const navigate = vi.hoisted(() => vi.fn());
const modelState = vi.hoisted(() => ({ loading: false, error: null as string | null }));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));
vi.mock("../../../../store/globalDataStore", () => ({
  useModels: () => ({ models: [{ id: "claude-sonnet@anthropic", name: "Claude Sonnet", driverId: "anthropic", provider: "Anthropic", tags: ["flagship"], supportedThinkingLevels: ["low", "high"], supportsTemperature: true }], ...modelState }),
}));

// InputDef is the decoded protobuf oneof, not the YAML input shorthand.
const inputs = {
  model: { type: "model", config: { case: "modelInput", value: { base: { ui: "config" }, default: { tags: ["flagship"] } } } },
  mode: { type: "enum", config: { case: "enumInput", value: { base: { ui: "toolbar" }, enumValues: ["manual", "auto", "plan"], default: "auto" } } },
  goal: { type: "string", config: { case: "stringInput", value: { base: { required: true, description: "Goal to achieve" } } } },
  "review.instructions": { type: "string", config: { case: "stringInput", value: { base: {}, default: "Review carefully" } } },
  hidden: { type: "string", config: { case: "stringInput", value: { base: { ui: "hidden" }, default: "Secret prompt" } } },
} as unknown as Record<string, InputDef>;

 describe.each([false, true])("shared settings (embedded=%s)", (embedded) => {
  it("reaches the same model policy, execution mode, required and grouped inputs", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<ChatSettingsPopover embedded={embedded} isOpen onClose={vi.fn()} inputs={inputs} values={{}} onChange={onChange} />);
    // The popover is its own dialog; embedded, the host sheet is.
    if (!embedded) expect(screen.getByRole("dialog", { name: "Chat settings" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "plan" }));
    expect(onChange).toHaveBeenLastCalledWith({ mode: "plan" });
    await user.click(screen.getByRole("button", { name: /Model flagship/ }));
    expect(screen.getByRole("option", { name: "High" })).toBeInTheDocument();
    expect(screen.getByText("Temperature")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Back" }));
    await user.click(screen.getByRole("button", { name: /More Settings/ }));
    expect(screen.getByText("Goal to achieve")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /review/i }));
    expect(screen.getByDisplayValue("Review carefully")).toBeInTheDocument();
    expect(screen.queryByDisplayValue("Secret prompt")).not.toBeInTheDocument();
  });

  it("keeps multi-enum mode-like inputs in More Settings and writes arrays", async () => {
    const onChange = vi.fn();
    render(<ChatSettingsPopover embedded={embedded} isOpen onClose={vi.fn()} inputs={{ modes: setInputMulti(inputs.mode, true) }} values={{ modes: ["auto"] }} onChange={onChange} />);
    expect(screen.queryByText("Execution Mode")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /More Settings/ }));
    await userEvent.click(screen.getByRole("button", { name: "1 of 3 selected" }));
    await userEvent.click(screen.getByRole("button", { name: "plan" }));
    expect(onChange).toHaveBeenLastCalledWith({ modes: ["auto", "plan"] });
  });

  it("blocks child thinking reconciliation after permissions are revoked", () => {
    const onChange = vi.fn();
    const thinking = setInputEnumValues(inputs.mode, ["low", "high"]);
    const props = { embedded, isOpen: true, initialPage: "params" as const, onClose: vi.fn(), inputs: { model: inputs.model, thinking_level: thinking }, values: { model: { id: "claude-sonnet@anthropic" }, thinking_level: "high" }, onChange };
    const { rerender } = render(<ChatSettingsPopover {...props} />);
    expect(onChange).not.toHaveBeenCalled();
    // Same effect as a catalog/model capability update: high is no longer valid.
    const restricted = { ...props, values: { ...props.values, model: { id: "unavailable-model" } } };
    rerender(<ChatSettingsPopover {...restricted} disabled />);
    expect(onChange).not.toHaveBeenCalled();
    rerender(<ChatSettingsPopover {...restricted} />);
    expect(onChange).toHaveBeenCalledWith({ ...restricted.values, thinking_level: "" });
  });

  it("opens model preferences on the matching surface", async () => {
    render(<SurfaceProvider surface={embedded ? "mobile" : "desktop"}><ChatSettingsPopover embedded={embedded} isOpen initialPage="model" onClose={vi.fn()} inputs={inputs} values={{}} onChange={vi.fn()} /></SurfaceProvider>);
    await userEvent.click(screen.getByRole("button", { name: "Model preferences" }));
    expect(navigate).toHaveBeenLastCalledWith(embedded ? { to: "/m/settings", search: { section: "ai" } } : { to: "/settings/$section", params: { section: "ai" } });
  });

  it("shows catalog loading and failure rather than silently claiming no models", () => {
    modelState.loading = true;
    const props = { embedded, isOpen: true, initialPage: "model" as const, onClose: vi.fn(), inputs, values: {}, onChange: vi.fn() };
    const { rerender } = render(<ChatSettingsPopover {...props} />);
    expect(screen.getByRole("status")).toHaveTextContent("Loading models");
    modelState.loading = false;
    modelState.error = "Offline";
    rerender(<ChatSettingsPopover {...props} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Could not load models");
    modelState.error = null;
  });

  it("disables mutation when permission changes while open", async () => {
    const onChange = vi.fn();
    render(<ChatSettingsPopover embedded={embedded} disabled isOpen onClose={vi.fn()} inputs={inputs} values={{}} onChange={onChange} />);
    expect(screen.getByRole("button", { name: "plan" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "plan" }));
    expect(onChange).not.toHaveBeenCalled();
  });
 });

it("embedded: back from the page it opened on returns to the host, with no close of its own", async () => {
  const onExit = vi.fn();
  render(<ChatSettingsPopover embedded onExit={onExit} isOpen initialPage="model" onClose={vi.fn()} inputs={inputs} values={{}} onChange={vi.fn()} />);
  expect(screen.queryByRole("button", { name: "Close settings" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Back" }));
  expect(onExit).toHaveBeenCalledTimes(1);
});

it("embedded: a page reached from the main page goes back to it", async () => {
  const onExit = vi.fn();
  render(<ChatSettingsPopover embedded onExit={onExit} isOpen onClose={vi.fn()} inputs={inputs} values={{}} onChange={vi.fn()} />);
  await userEvent.click(screen.getByRole("button", { name: /More Settings/ }));
  await userEvent.click(screen.getByRole("button", { name: "Back" }));
  expect(onExit).not.toHaveBeenCalled();
  expect(screen.getByText("Chat Settings")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Back" }));
  expect(onExit).toHaveBeenCalledTimes(1);
});
