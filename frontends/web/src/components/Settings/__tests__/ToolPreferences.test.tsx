import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  readSetting: vi.fn(),
  upsertStringSetting: vi.fn(),
  deleteSettingIfExists: vi.fn(),
  models: [] as Array<{ driverId?: string; tags?: string[] }>,
}));

vi.mock("../../../lib/settingsPersistence", () => ({
  readSetting: mocks.readSetting,
  upsertStringSetting: mocks.upsertStringSetting,
  deleteSettingIfExists: mocks.deleteSettingIfExists,
}));

vi.mock("../../../store/globalDataStore", () => ({
  useModels: () => ({ models: mocks.models, loading: false, error: null }),
}));

import {
  ToolPreferences,
  toolBindingsKey,
  imageModelSelector,
  loadToolBindings,
  saveToolBindings,
} from "../ToolPreferences";

/** Every provider the panel can offer, all image-capable and credentialed. */
const allProviders = [
  { provider: "reliant", configured: true },
  { provider: "openai", configured: true },
  { provider: "codex", configured: true },
];

const imageCapableModels = [
  { driverId: "reliant", tags: ["image-gen"] },
  { driverId: "openai", tags: ["image-gen"] },
  { driverId: "codex", tags: ["image-gen"] },
];

beforeEach(() => {
  vi.clearAllMocks();
  mocks.readSetting.mockResolvedValue({ status: "missing" });
  mocks.upsertStringSetting.mockResolvedValue(undefined);
  mocks.deleteSettingIfExists.mockResolvedValue(undefined);
  mocks.models = imageCapableModels;
});

describe("tool bindings storage", () => {
  // The key is the contract with the server. internal/toolbindings reads
  // `tool.bindings.<tool>` with a single prefix query; a different spelling
  // here means the preference is written and never read.
  it("namespaces per tool under tool.bindings.", () => {
    expect(toolBindingsKey("generate_image")).toBe(
      "tool.bindings.generate_image",
    );
  });

  it("writes the literal-binding shape the server decodes", async () => {
    await saveToolBindings("generate_image", {
      model: { literal: { tags: ["image-gen"], providers: ["codex"] } },
    });

    expect(mocks.upsertStringSetting).toHaveBeenCalledWith(
      "tool.bindings.generate_image",
      JSON.stringify({
        model: { literal: { tags: ["image-gen"], providers: ["codex"] } },
      }),
    );
  });

  it("deletes rather than storing an empty object when a preference is cleared", async () => {
    await saveToolBindings("generate_image", {});

    expect(mocks.deleteSettingIfExists).toHaveBeenCalledWith(
      "tool.bindings.generate_image",
    );
    expect(mocks.upsertStringSetting).not.toHaveBeenCalled();
  });

  it("treats a malformed row as no preference rather than throwing", async () => {
    mocks.readSetting.mockResolvedValue({ status: "found", value: "not json" });

    await expect(loadToolBindings("generate_image")).resolves.toEqual({});
  });

  it("reads back the selector it wrote", () => {
    const selector = imageModelSelector({
      model: { literal: { tags: ["image-gen"], providers: ["openai"] } },
    });
    expect(selector?.providers).toEqual(["openai"]);
  });
});

describe("ToolPreferences panel", () => {
  it("defaults to Automatic when nothing is stored", async () => {
    render(<ToolPreferences providers={allProviders} />);

    const automatic = await screen.findByRole("radio", { name: /Automatic/ });
    expect(automatic).toBeChecked();
  });

  it("reflects a stored provider preference", async () => {
    mocks.readSetting.mockResolvedValue({
      status: "found",
      value: JSON.stringify({
        model: { literal: { tags: ["image-gen"], providers: ["codex"] } },
      }),
    });

    render(<ToolPreferences providers={allProviders} />);

    const codex = await screen.findByRole("radio", { name: /ChatGPT \/ Codex/ });
    expect(codex).toBeChecked();
  });

  // This is the resolution chain's global link, from the user's side: picking a
  // provider must persist a binding the server will read, carrying the TAG and
  // not a pinned model id. A pinned id keeps being requested after the model
  // retires; the tag re-resolves against the registry every call.
  it("persists a tag-plus-provider selector when a provider is chosen", async () => {
    const user = userEvent.setup();
    render(<ToolPreferences providers={allProviders} />);

    await user.click(await screen.findByRole("radio", { name: /OpenAI/ }));

    await waitFor(() => {
      expect(mocks.upsertStringSetting).toHaveBeenCalledWith(
        "tool.bindings.generate_image",
        JSON.stringify({
          model: { literal: { tags: ["image-gen"], providers: ["openai"] } },
        }),
      );
    });

    const written = JSON.parse(mocks.upsertStringSetting.mock.calls[0][1]);
    expect(written.model.literal.id).toBeUndefined();
  });

  it("clears the preference when Automatic is chosen again", async () => {
    const user = userEvent.setup();
    mocks.readSetting.mockResolvedValue({
      status: "found",
      value: JSON.stringify({
        model: { literal: { tags: ["image-gen"], providers: ["openai"] } },
      }),
    });

    render(<ToolPreferences providers={allProviders} />);
    await user.click(await screen.findByRole("radio", { name: /Automatic/ }));

    await waitFor(() => {
      expect(mocks.deleteSettingIfExists).toHaveBeenCalledWith(
        "tool.bindings.generate_image",
      );
    });
  });

  // Which pocket a choice spends from is the load-bearing half of the answer.
  // A BYO subscription is not metered by the control-plane proxy; managed
  // credits are. Offering the choice without saying that makes the user guess.
  it("says which pocket each provider spends from", async () => {
    render(<ToolPreferences providers={allProviders} />);

    expect(
      await screen.findByText(/draws down your Reliant AI credit balance/i),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Covered by your own ChatGPT subscription/i),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Billed by OpenAI to your own key/i),
    ).toBeInTheDocument();
  });

  it("omits a provider the user has no credential for", async () => {
    render(
      <ToolPreferences
        providers={[
          { provider: "openai", configured: true },
          { provider: "codex", configured: false },
        ]}
      />,
    );

    expect(await screen.findByRole("radio", { name: /OpenAI/ })).toBeInTheDocument();
    expect(
      screen.queryByRole("radio", { name: /ChatGPT \/ Codex/ }),
    ).not.toBeInTheDocument();
  });

  // Derived from the registry, not hardcoded: a provider the user has a key
  // for but which has no image model must not be offered, or the choice fails
  // at call time.
  it("omits a credentialed provider with no image model behind it", async () => {
    mocks.models = [{ driverId: "reliant", tags: ["image-gen"] }];

    render(<ToolPreferences providers={allProviders} />);

    // Anchored: "Automatic" describes itself as falling back to Reliant
    // credits, so an unanchored match would hit both radios.
    expect(
      await screen.findByRole("radio", { name: /^Reliant credits/ }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("radio", { name: /OpenAI/ })).not.toBeInTheDocument();
  });

  it("explains itself when no provider can generate images", async () => {
    mocks.models = [];

    render(<ToolPreferences providers={allProviders} />);

    expect(
      await screen.findByText(/No configured provider can generate images yet/i),
    ).toBeInTheDocument();
  });
});
