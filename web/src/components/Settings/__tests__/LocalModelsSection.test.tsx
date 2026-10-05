import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { DaemonInfoSchema, DaemonStatus } from "../../../gen/reliant/v1/daemon_registry_pb";
import type { DaemonInfo } from "../../../gen/reliant/v1/daemon_registry_pb";
import {
  LocalModelEndpointSchema,
  LocalModelInfoSchema,
  LocalModelInventorySchema,
} from "../../../gen/reliant/v1/tools_daemon_pb";

const mocks = vi.hoisted(() => ({
  daemons: [] as DaemonInfo[],
  refreshLocalModels: vi.fn(),
  setLocalModelEndpoints: vi.fn(),
}));

vi.mock("../../../hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({ daemons: mocks.daemons, loading: false, refresh: vi.fn() }),
}));
vi.mock("../../../api/grpc-client", () => ({
  grpcClient: {
    daemonRegistry: () => ({
      refreshLocalModels: mocks.refreshLocalModels,
      setLocalModelEndpoints: mocks.setLocalModelEndpoints,
    }),
  },
}));

import { LocalModelsSection } from "../LocalModelsSection";

const model = (name: string, extra: Record<string, unknown> = {}) =>
  create(LocalModelInfoSchema, {
    name,
    supportsChat: true,
    contextWindow: 32768n,
    parameterSize: "8.2B",
    ...extra,
  });

const inventory = (probedAt = new Date().toISOString()) =>
  create(LocalModelInventorySchema, {
    probedAt,
    endpoints: [
      create(LocalModelEndpointSchema, {
        id: "ollama",
        kind: "ollama",
        baseUrl: "http://localhost:11434/v1",
        source: "detected",
        models: [
          model("qwen3:latest", { supportsTools: true, supportsThinking: true }),
          model("llava:7b", { supportsVision: true }),
          model("nomic-embed-text", { supportsChat: false }),
        ],
      }),
      create(LocalModelEndpointSchema, {
        id: "cfg1",
        kind: "vllm",
        baseUrl: "http://gpu-box.lan:8000/v1",
        source: "configured",
        error: "dial tcp 10.0.0.5:8000: connect: connection refused",
      }),
    ],
  });

const daemon = (over: Partial<DaemonInfo> = {}) =>
  create(DaemonInfoSchema, {
    daemonId: "d1",
    hostname: "Sean's MacBook",
    daemonType: "self_hosted",
    status: DaemonStatus.ACTIVE,
    localModels: inventory(),
    ...over,
  });

beforeEach(() => {
  mocks.refreshLocalModels.mockReset().mockResolvedValue({ localModels: inventory() });
  mocks.setLocalModelEndpoints.mockReset().mockResolvedValue({ localModels: inventory() });
  mocks.daemons = [daemon()];
});

describe("LocalModelsSection", () => {
  it("renders a machine with endpoints, models, capability chips and a plain-words error", () => {
    render(<LocalModelsSection />);
    expect(screen.getByText("Sean's MacBook")).toBeTruthy();
    expect(screen.getByText("Online")).toBeTruthy();
    expect(screen.getByText("Ollama")).toBeTruthy();
    expect(screen.getByText("vLLM")).toBeTruthy();
    expect(screen.getByText("qwen3:latest")).toBeTruthy();
    const qwen = screen.getByText("qwen3:latest").closest("li")!;
    expect(within(qwen).getByText("tools")).toBeTruthy();
    expect(within(qwen).getByText("thinking")).toBeTruthy();
    expect(within(qwen).getByText("32K ctx")).toBeTruthy();
    expect(screen.getByText(/Nothing is listening on gpu-box\.lan:8000/)).toBeTruthy();
  });

  it("hides embedding models until the toggle is on", async () => {
    render(<LocalModelsSection />);
    expect(screen.queryByText("nomic-embed-text")).toBeNull();
    await userEvent.click(screen.getByLabelText("Show embedding models"));
    expect(screen.getByText("nomic-embed-text")).toBeTruthy();
  });

  it("Test connection calls refreshLocalModels for that daemon", async () => {
    render(<LocalModelsSection />);
    await userEvent.click(screen.getByRole("button", { name: /test connection/i }));
    await waitFor(() => expect(mocks.refreshLocalModels).toHaveBeenCalledTimes(1));
    expect(mocks.refreshLocalModels.mock.calls[0][0].daemonId).toBe("d1");
  });

  it("adding an endpoint sends the full configured list plus the new URL", async () => {
    render(<LocalModelsSection />);
    await userEvent.type(screen.getByLabelText("Endpoint URL"), "http://localhost:1234/v1");
    await userEvent.click(screen.getByRole("button", { name: /add endpoint/i }));
    await waitFor(() => expect(mocks.setLocalModelEndpoints).toHaveBeenCalledTimes(1));
    const req = mocks.setLocalModelEndpoints.mock.calls[0][0];
    expect(req.daemonId).toBe("d1");
    expect(req.baseUrls).toEqual(["http://gpu-box.lan:8000/v1", "http://localhost:1234/v1"]);
  });

  it("rejects a malformed URL without calling the RPC", async () => {
    render(<LocalModelsSection />);
    await userEvent.type(screen.getByLabelText("Endpoint URL"), "not a url");
    await userEvent.click(screen.getByRole("button", { name: /add endpoint/i }));
    expect(screen.getByText(/doesn't look like a URL/)).toBeTruthy();
    expect(mocks.setLocalModelEndpoints).not.toHaveBeenCalled();
  });

  it("removing a configured endpoint sends the list without it; detected ones have no remove", async () => {
    render(<LocalModelsSection />);
    expect(screen.queryByLabelText("Remove http://localhost:11434/v1")).toBeNull();
    await userEvent.click(screen.getByLabelText("Remove http://gpu-box.lan:8000/v1"));
    await waitFor(() => expect(mocks.setLocalModelEndpoints).toHaveBeenCalledTimes(1));
    expect(mocks.setLocalModelEndpoints.mock.calls[0][0].baseUrls).toEqual([]);
  });

  it("an offline machine is read-only with an explanation", () => {
    mocks.daemons = [daemon({ status: DaemonStatus.DISCONNECTED })];
    render(<LocalModelsSection />);
    expect(screen.getByText(/Machine is offline — local models are unavailable until it reconnects/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /test connection/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /add endpoint/i })).toBeNull();
    expect(screen.queryByLabelText(/^Remove /)).toBeNull();
  });

  it("explains local models when there are no machines", () => {
    mocks.daemons = [];
    render(<LocalModelsSection />);
    expect(screen.getByText(/run on your machine through the Reliant daemon/)).toBeTruthy();
  });

  it("shows a getting-started block when a machine has no endpoints", () => {
    mocks.daemons = [daemon({ localModels: create(LocalModelInventorySchema, { endpoints: [] }) })];
    render(<LocalModelsSection />);
    expect(screen.getByText(/ollama pull qwen3/)).toBeTruthy();
  });

  it("words the empty state for a cloud machine without a localhost GPU", () => {
    mocks.daemons = [
      daemon({ daemonType: "managed", localModels: create(LocalModelInventorySchema, { endpoints: [] }) }),
    ];
    render(<LocalModelsSection />);
    expect(screen.getByText(/cloud machine, so it has no GPU of its own/)).toBeTruthy();
    expect(screen.queryByText(/ollama pull qwen3/)).toBeNull();
  });
});

describe("LocalModelsSection context warnings", () => {
  const withModels = (kind: string, ctxs: number[], extraEndpoint?: boolean) => {
    const mk = (id: string, k: string, sizes: number[]) =>
      create(LocalModelEndpointSchema, {
        id,
        kind: k,
        baseUrl: `http://localhost:${id === "ollama" ? 11434 : 9000}/v1`,
        source: "detected",
        models: sizes.map((n, i) => model(`m${id}${i}`, { contextWindow: BigInt(n) })),
      });
    const endpoints = [mk("ollama", kind, ctxs)];
    if (extraEndpoint) endpoints.push(mk("ollama2", "ollama", ctxs));
    mocks.daemons = [
      daemon({ localModels: create(LocalModelInventorySchema, { probedAt: new Date().toISOString(), endpoints }) }),
    ];
  };

  it("shows a warning chip at 4096 and the Ollama fix block", () => {
    withModels("ollama", [4096]);
    render(<LocalModelsSection />);
    expect(screen.getByText(/4K context — too small for agent work/)).toBeTruthy();
    const fix = screen.getByTestId("ollama-context-fix");
    expect(fix.textContent).toMatch(/Ollama is giving these models a 4K context/);
    expect(fix.textContent).toMatch(/Settings → Context length/);
    expect(fix.textContent).toMatch(/OLLAMA_CONTEXT_LENGTH=64000 ollama serve/);
    expect(fix.textContent).toMatch(/Then click Test connection/);
  });

  it("shows only a soft note at 32768", () => {
    withModels("ollama", [32768]);
    render(<LocalModelsSection />);
    expect(screen.getByText(/32K context — smaller than the ~64K/)).toBeTruthy();
    expect(screen.queryByText(/too small for agent work/)).toBeNull();
  });

  it("shows nothing at 65536 or when the context is unknown", () => {
    withModels("ollama", [65536, 0]);
    render(<LocalModelsSection />);
    expect(screen.queryByText(/context —/)).toBeNull();
    expect(screen.queryByTestId("ollama-context-fix")).toBeNull();
  });

  it("renders the fix once per endpoint, not per model, and not for non-Ollama servers", () => {
    withModels("ollama", [4096, 8192], true);
    const { unmount } = render(<LocalModelsSection />);
    expect(screen.getAllByTestId("ollama-context-fix")).toHaveLength(2);
    unmount();
    withModels("vllm", [4096]);
    render(<LocalModelsSection />);
    expect(screen.queryByTestId("ollama-context-fix")).toBeNull();
    expect(screen.getByText(/4K context — too small/)).toBeTruthy();
  });
});
