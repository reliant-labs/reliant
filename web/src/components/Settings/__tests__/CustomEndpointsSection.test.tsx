import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { DaemonInfoSchema, DaemonStatus } from "../../../gen/reliant/v1/daemon_registry_pb";
import type { DaemonInfo } from "../../../gen/reliant/v1/daemon_registry_pb";
import {
  ModelEndpointModelSchema,
  ModelEndpointRoute,
  ModelEndpointSchema,
} from "../../../gen/reliant/v1/model_endpoint_pb";
import { LocalModelEndpointSchema, LocalModelInfoSchema } from "../../../gen/reliant/v1/tools_daemon_pb";

const mocks = vi.hoisted(() => ({
  daemons: [] as DaemonInfo[],
  list: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  remove: vi.fn(),
  test: vi.fn(),
}));

vi.mock("../../../hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({ daemons: mocks.daemons, loading: false, refresh: vi.fn() }),
}));
vi.mock("../../../api/grpc-client", () => ({ getTransport: () => ({}) }));
vi.mock("@connectrpc/connect", () => ({
  createClient: () => ({
    listModelEndpoints: mocks.list,
    createModelEndpoint: mocks.create,
    updateModelEndpoint: mocks.update,
    deleteModelEndpoint: mocks.remove,
    testModelEndpoint: mocks.test,
  }),
}));

import { CustomEndpointsSection } from "../CustomEndpointsSection";

const probe = (names: string[], extra: Record<string, unknown> = {}) =>
  create(LocalModelEndpointSchema, {
    id: "p",
    kind: "vllm",
    baseUrl: "https://llm.example.com/v1",
    source: "configured",
    models: names.map((n) => create(LocalModelInfoSchema, { name: n, supportsChat: true, contextWindow: 32768n })),
    ...extra,
  });

const endpoint = (over: Record<string, unknown> = {}) =>
  create(ModelEndpointSchema, {
    id: "e1",
    name: "Lab GPU",
    baseUrl: "https://llm.example.com/v1",
    route: ModelEndpointRoute.DIRECT,
    probe: probe(["llama", "mixtral"]),
    models: [create(ModelEndpointModelSchema, { name: "llama" }), create(ModelEndpointModelSchema, { name: "mixtral", hidden: true })],
    ...over,
  });

const daemon = (over: Partial<DaemonInfo> = {}) =>
  create(DaemonInfoSchema, { daemonId: "d1", hostname: "Sean's MacBook", daemonType: "self_hosted", status: DaemonStatus.ACTIVE, ...over });

beforeEach(() => {
  for (const m of [mocks.list, mocks.create, mocks.update, mocks.remove, mocks.test]) m.mockReset();
  mocks.list.mockResolvedValue({ endpoints: [] });
  mocks.create.mockResolvedValue({});
  mocks.update.mockResolvedValue({});
  mocks.remove.mockResolvedValue({});
  mocks.test.mockResolvedValue({ probe: probe(["llama", "mixtral"]), latencyMs: 42n });
  mocks.daemons = [daemon()];
});

const openAddForm = async () => {
  render(<CustomEndpointsSection />);
  await screen.findByTestId("no-endpoints");
  await userEvent.click(screen.getByRole("button", { name: /add custom endpoint/i }));
};

describe("CustomEndpointsSection list", () => {
  it("shows an empty state, then the endpoints with route, kind and visible models", async () => {
    mocks.list.mockResolvedValue({ endpoints: [endpoint(), endpoint({ id: "e2", name: "Home vLLM", route: ModelEndpointRoute.VIA_DAEMON, daemonId: "d1" })] });
    render(<CustomEndpointsSection />);
    const row = await screen.findByTestId("custom-endpoint-e1");
    expect(within(row).getByText("Lab GPU")).toBeTruthy();
    expect(within(row).getByText("Reliant cloud")).toBeTruthy();
    expect(within(row).getByText("vLLM")).toBeTruthy();
    expect(within(row).getByText(/1 model: llama$/)).toBeTruthy();
    const via = screen.getByTestId("custom-endpoint-e2");
    expect(within(via).getByText("via Sean's MacBook")).toBeTruthy();
  });

  it("explains a failed probe in plain words", async () => {
    mocks.list.mockResolvedValue({ endpoints: [endpoint({ probe: probe([], { error: "dial tcp 10.0.0.5:8000: connect: connection refused" }) })] });
    render(<CustomEndpointsSection />);
    expect(await screen.findByText(/Nothing is listening on llm\.example\.com/)).toBeTruthy();
  });

  it("surfaces a load failure without hiding the Add button", async () => {
    mocks.list.mockRejectedValue(new Error("boom"));
    render(<CustomEndpointsSection />);
    expect(await screen.findByText(/Couldn't load your endpoints: boom/)).toBeTruthy();
    expect(screen.getByRole("button", { name: /add custom endpoint/i })).toBeTruthy();
  });
});

describe("add / edit form", () => {
  it("validates before calling the server", async () => {
    await openAddForm();
    await userEvent.click(screen.getByRole("button", { name: /save endpoint/i }));
    expect(await screen.findByText("Give the endpoint a name.")).toBeTruthy();
    expect(mocks.create).not.toHaveBeenCalled();

    await userEvent.type(screen.getByLabelText("Name"), "Lab");
    await userEvent.type(screen.getByLabelText("Base URL"), "not a url");
    await userEvent.click(screen.getByRole("button", { name: /save endpoint/i }));
    expect(await screen.findByText(/doesn't look like a URL/)).toBeTruthy();
    expect(mocks.create).not.toHaveBeenCalled();
  });

  it("creates a direct endpoint and returns to the list", async () => {
    await openAddForm();
    await userEvent.type(screen.getByLabelText("Name"), "Lab");
    await userEvent.type(screen.getByLabelText("Base URL"), "https://llm.example.com/v1");
    await userEvent.click(screen.getByRole("button", { name: /save endpoint/i }));
    await waitFor(() => expect(mocks.create).toHaveBeenCalledTimes(1));
    const input = mocks.create.mock.calls[0][0].endpoint;
    expect(input).toMatchObject({ name: "Lab", baseUrl: "https://llm.example.com/v1", route: ModelEndpointRoute.DIRECT, daemonId: "" });
    expect(input.apiKey).toBeUndefined();
    await waitFor(() => expect(screen.queryByRole("form", { name: /new custom endpoint/i })).toBeNull());
    expect(mocks.list).toHaveBeenCalledTimes(2);
  });

  it("explains both routes and requires a machine for the second", async () => {
    await openAddForm();
    expect(screen.getByText(/For servers on the public internet/)).toBeTruthy();
    expect(screen.getByText(/localhost, a LAN, or a server behind a VPN/)).toBeTruthy();

    await userEvent.type(screen.getByLabelText("Name"), "Home");
    await userEvent.type(screen.getByLabelText("Base URL"), "http://10.0.0.5:8000/v1");
    await userEvent.click(screen.getByLabelText("Through one of my machines"));
    await userEvent.click(screen.getByRole("button", { name: /save endpoint/i }));
    expect(await screen.findByText(/Choose which of your machines/)).toBeTruthy();
    expect(mocks.create).not.toHaveBeenCalled();

    await userEvent.selectOptions(screen.getByLabelText("Machine"), "d1");
    await userEvent.click(screen.getByRole("button", { name: /save endpoint/i }));
    await waitFor(() => expect(mocks.create).toHaveBeenCalledTimes(1));
    expect(mocks.create.mock.calls[0][0].endpoint).toMatchObject({ route: ModelEndpointRoute.VIA_DAEMON, daemonId: "d1" });
  });

  it("disables offline machines and says so", async () => {
    mocks.daemons = [daemon({ daemonId: "d2", hostname: "Old laptop", status: DaemonStatus.DISCONNECTED })];
    await openAddForm();
    await userEvent.click(screen.getByLabelText("Through one of my machines"));
    const option = screen.getByRole("option", { name: /Old laptop \(offline\)/ }) as HTMLOptionElement;
    expect(option.disabled).toBe(true);
    expect(screen.getByText(/All your machines are offline/)).toBeTruthy();
  });

  it("disables the key and header fields with a coming-soon note, and never sends a key", async () => {
    await openAddForm();
    expect((screen.getByLabelText("API key") as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByLabelText("Custom headers") as HTMLInputElement).disabled).toBe(true);
    expect(screen.getByTestId("credential-note").textContent).toMatch(/sealed credential store/);
  });

  it("enables the key field once the store is available, and sends the key", async () => {
    render(<CustomEndpointsSection credentialsAvailable />);
    await screen.findByTestId("no-endpoints");
    await userEvent.click(screen.getByRole("button", { name: /add custom endpoint/i }));
    expect((screen.getByLabelText("API key") as HTMLInputElement).disabled).toBe(false);
    expect(screen.queryByTestId("credential-note")).toBeNull();
    await userEvent.type(screen.getByLabelText("Name"), "Keyed");
    await userEvent.type(screen.getByLabelText("Base URL"), "https://llm.example.com/v1");
    await userEvent.type(screen.getByLabelText("API key"), "sk-abc");
    await userEvent.click(screen.getByRole("button", { name: /save endpoint/i }));
    await waitFor(() => expect(mocks.create).toHaveBeenCalledTimes(1));
    expect(mocks.create.mock.calls[0][0].endpoint.apiKey).toBe("sk-abc");
  });

  it("shows the server's rejection and keeps the form open", async () => {
    mocks.create.mockRejectedValue(Object.assign(new Error("[failed_precondition] x"), { rawMessage: "API keys for custom endpoints need the sealed credential store; coming with the next sync" }));
    await openAddForm();
    await userEvent.type(screen.getByLabelText("Name"), "Lab");
    await userEvent.type(screen.getByLabelText("Base URL"), "https://llm.example.com/v1");
    await userEvent.click(screen.getByRole("button", { name: /save endpoint/i }));
    expect((await screen.findByTestId("save-error")).textContent).toMatch(/sealed credential store/);
    expect(screen.getByRole("form", { name: /new custom endpoint/i })).toBeTruthy();
  });

  describe("Test connection", () => {
    it("tests the unsaved draft, shows models and latency, and fills in per-model rows", async () => {
      await openAddForm();
      await userEvent.type(screen.getByLabelText("Name"), "Lab");
      await userEvent.type(screen.getByLabelText("Base URL"), "https://llm.example.com/v1");
      await userEvent.click(screen.getByRole("button", { name: /test connection/i }));
      expect((await screen.findByTestId("probe-ok")).textContent).toMatch(/Connected to vLLM: 2 models in 42 ms/);
      const req = mocks.test.mock.calls[0][0];
      expect(req.target.case).toBe("draft");
      expect(req.target.value.baseUrl).toBe("https://llm.example.com/v1");
      expect(screen.getByTestId("model-llama")).toBeTruthy();
      expect(screen.getByTestId("model-mixtral")).toBeTruthy();
      expect(mocks.create).not.toHaveBeenCalled();
    });

    it("reports a failing probe in plain words", async () => {
      mocks.test.mockResolvedValue({ probe: probe([], { error: "GET /v1/models returned HTTP 401" }), latencyMs: 9n });
      await openAddForm();
      await userEvent.type(screen.getByLabelText("Name"), "Lab");
      await userEvent.type(screen.getByLabelText("Base URL"), "https://llm.example.com/v1");
      await userEvent.click(screen.getByRole("button", { name: /test connection/i }));
      expect(await screen.findByText(/rejected the request \(HTTP 401\)/)).toBeTruthy();
    });

    it("needs a name-free but valid URL first", async () => {
      await openAddForm();
      await userEvent.click(screen.getByRole("button", { name: /test connection/i }));
      expect(await screen.findByText("Give the endpoint a name.")).toBeTruthy();
      expect(mocks.test).not.toHaveBeenCalled();
    });
  });

  describe("per-model rows", () => {
    const openWithModels = async () => {
      await openAddForm();
      await userEvent.type(screen.getByLabelText("Name"), "Lab");
      await userEvent.type(screen.getByLabelText("Base URL"), "https://llm.example.com/v1");
      await userEvent.click(screen.getByRole("button", { name: /test connection/i }));
      await screen.findByTestId("model-llama");
    };

    it("hides a model, sets caps, params and extra JSON, and sends only what changed", async () => {
      await openWithModels();
      await userEvent.click(screen.getByLabelText("Hide mixtral"));
      await userEvent.click(screen.getByRole("button", { name: "Settings for llama" }));
      await userEvent.type(screen.getByLabelText("Context window (tokens)"), "65536");
      await userEvent.type(screen.getByLabelText("Max output (tokens)"), "4096");
      await userEvent.type(screen.getByLabelText("Default temperature"), "0.2");
      await userEvent.type(screen.getByLabelText("Default top-p"), "0.9");
      await userEvent.selectOptions(screen.getByLabelText("Tools"), "on");
      await userEvent.selectOptions(screen.getByLabelText("Vision"), "off");
      fireInput(screen.getByLabelText("Advanced: extra request JSON"), '{"min_p": 0.05}');
      await userEvent.click(screen.getByRole("button", { name: /save endpoint/i }));
      await waitFor(() => expect(mocks.create).toHaveBeenCalledTimes(1));
      const models = mocks.create.mock.calls[0][0].endpoint.models;
      expect(models.map((m: { name: string }) => m.name).sort()).toEqual(["llama", "mixtral"]);
      const llama = models.find((m: { name: string }) => m.name === "llama");
      expect(llama).toMatchObject({ contextWindow: 65536n, maxOutputTokens: 4096n, temperature: 0.2, topP: 0.9, supportsTools: true, supportsVision: false, extraBodyJson: '{"min_p": 0.05}' });
      expect(llama.supportsThinking).toBeUndefined();
      expect(models.find((m: { name: string }) => m.name === "mixtral").hidden).toBe(true);
    });

    it("validates the extra JSON inline and blocks saving on it", async () => {
      await openWithModels();
      await userEvent.click(screen.getByRole("button", { name: "Settings for llama" }));
      const box = screen.getByLabelText("Advanced: extra request JSON");
      fireInput(box, "{ not json");
      expect(await screen.findByText("That isn't valid JSON.")).toBeTruthy();
      fireInput(box, '{"model": "x"}');
      expect(await screen.findByText(/"model" is controlled by Reliant/)).toBeTruthy();
      fireInput(box, "[1]");
      expect(await screen.findByText(/Must be a JSON object/)).toBeTruthy();

      await userEvent.click(screen.getByRole("button", { name: /save endpoint/i }));
      expect(await screen.findByText(/Fix the settings for llama/)).toBeTruthy();
      expect(mocks.create).not.toHaveBeenCalled();

      fireInput(box, '{"min_p": 0.1}');
      await waitFor(() => expect(screen.queryByText(/Must be a JSON object/)).toBeNull());
    });

    it("rejects out-of-range numbers inline", async () => {
      await openWithModels();
      await userEvent.click(screen.getByRole("button", { name: "Settings for llama" }));
      await userEvent.type(screen.getByLabelText("Default temperature"), "5");
      expect(await screen.findByText(/Temperature must be between 0 and 2/)).toBeTruthy();
      await userEvent.type(screen.getByLabelText("Default top-p"), "2");
      expect(await screen.findByText(/Top-p must be above 0/)).toBeTruthy();
    });
  });

  it("editing saves with the id and tests the saved endpoint by id", async () => {
    mocks.list.mockResolvedValue({ endpoints: [endpoint()] });
    render(<CustomEndpointsSection />);
    await screen.findByTestId("custom-endpoint-e1");
    await userEvent.click(screen.getByRole("button", { name: "Edit Lab GPU" }));
    expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("Lab GPU");
    expect((await screen.findByTestId("model-llama"))).toBeTruthy();
    expect((screen.getByLabelText("Hide mixtral") as HTMLInputElement).checked).toBe(true);

    await userEvent.click(screen.getByRole("button", { name: /test connection/i }));
    await waitFor(() => expect(mocks.test).toHaveBeenCalledTimes(1));
    expect(mocks.test.mock.calls[0][0].target).toMatchObject({ case: "id", value: "e1" });

    await userEvent.click(screen.getByRole("button", { name: /save changes/i }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledTimes(1));
    expect(mocks.update.mock.calls[0][0].id).toBe("e1");
  });
});

describe("delete", () => {
  it("asks first, then deletes and reloads", async () => {
    mocks.list.mockResolvedValueOnce({ endpoints: [endpoint()] }).mockResolvedValue({ endpoints: [] });
    render(<CustomEndpointsSection />);
    await screen.findByTestId("custom-endpoint-e1");
    await userEvent.click(screen.getByRole("button", { name: "Delete Lab GPU" }));
    const dialog = screen.getByTestId("confirm-delete");
    expect(mocks.remove).not.toHaveBeenCalled();
    await userEvent.click(within(dialog).getByRole("button", { name: /delete endpoint/i }));
    await waitFor(() => expect(mocks.remove).toHaveBeenCalledTimes(1));
    expect(mocks.remove.mock.calls[0][0].id).toBe("e1");
    expect(await screen.findByTestId("no-endpoints")).toBeTruthy();
  });

  it("keeps it when the user backs out, and shows a failure", async () => {
    mocks.list.mockResolvedValue({ endpoints: [endpoint()] });
    mocks.remove.mockRejectedValue(new Error("nope"));
    render(<CustomEndpointsSection />);
    await screen.findByTestId("custom-endpoint-e1");
    await userEvent.click(screen.getByRole("button", { name: "Delete Lab GPU" }));
    await userEvent.click(screen.getByRole("button", { name: /keep it/i }));
    expect(screen.queryByTestId("confirm-delete")).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Delete Lab GPU" }));
    await userEvent.click(within(screen.getByTestId("confirm-delete")).getByRole("button", { name: /delete endpoint/i }));
    expect(await screen.findByText("nope")).toBeTruthy();
    expect(screen.getByTestId("custom-endpoint-e1")).toBeTruthy();
  });
});

// userEvent.type treats "{" as a key-descriptor, so JSON is set directly.
import { fireEvent } from "@testing-library/react";
function fireInput(el: HTMLElement, value: string) {
  fireEvent.change(el, { target: { value } });
}
