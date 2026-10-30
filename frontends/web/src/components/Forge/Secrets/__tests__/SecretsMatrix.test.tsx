// Copyright (c) 2025 Reliant Labs
import { describe, it, expect, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { RosterEnv } from "@/services/forge/roster";
import { SecretsMatrix } from "../SecretsMatrix";
const store = vi.hoisted(() => ({ list: vi.fn(), history: vi.fn(), reveal: vi.fn() }));
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test" }));
vi.mock("@/services/forge/secretStore", async (original) => ({
  ...await original<typeof import("@/services/forge/secretStore")>(),
  listSecrets: store.list, getSecretVersions: store.history, revealSecret: store.reveal,
}));
vi.mock("@tanstack/react-router", async (original) => ({ ...await original<typeof import("@tanstack/react-router")>(), useNavigate: () => vi.fn() }));
const env = (name: string, provider = "hosted"): RosterEnv => ({
  name, source: "backend", lifecycle: "deployed", forge: null,
  live: {
    id: name, name, project: "shop", kind: "persistent", declaredBy: null,
    release: "v1", releaseProvenance: null, promotedByActor: "", promotedByUserId: "",
    phase: "succeeded", observed: { state: "not-reported" }, drift: { state: "not-reported" }, driftDetail: "", provenance: "", holds: [],
    declaredShape: { kind: "persistent", workloads: [], domains: [], clusters: [], secrets: [{ name: "API_KEY", provider, declaredBy: [] }] },
  },
});
function mount(envs: RosterEnv[], projectId: string | null = "project") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(<QueryClientProvider client={client}><SecretsMatrix envs={envs} projectId={projectId} secretHref={(env, name) => `/secrets?env=${env}${name ? `&secret=${name}` : ""}`} /></QueryClientProvider>);
  return { ...view, client };
}
describe("secrets environment matrix", () => {
  it("compares metadata with per-cell links and relative/full dates, never fetching values or histories", async () => {
    store.list.mockImplementation(async (id) => ({ secrets: id === "prod" ? [{ name: "API_KEY", currentVersion: 2, currentVersionDeleted: false, currentVersionDestroyed: false, updatedAt: "2026-03-02T12:00:00Z" }] : [], protected: false }));
    mount([env("prod"), env("staging"), env("dev", "file")]);
    await waitFor(() => expect(screen.getByRole("link", { name: "API_KEY in prod: Set" })).toBeInTheDocument());
    expect(screen.getByRole("link", { name: "API_KEY in staging: Not set" })).toHaveAttribute("href", "/secrets?env=staging&secret=API_KEY");
    expect(screen.getByRole("link", { name: "API_KEY in dev: Local file store" })).toBeInTheDocument();
    const time = within(screen.getByRole("link", { name: "API_KEY in prod: Set" })).getByText(/ago/);
    expect(time).toHaveAttribute("datetime", "2026-03-02T12:00:00Z");
    expect(time).toHaveAttribute("title");
    expect(store.list).toHaveBeenCalledTimes(3);
    expect(store.history).not.toHaveBeenCalled();
    expect(store.reveal).not.toHaveBeenCalled();
  });
  it("keeps failures unknown instead of falsely saying unset", async () => {
    store.list.mockRejectedValue(new Error("unreachable"));
    mount([env("prod")]);
    await waitFor(() => expect(screen.getByRole("link", { name: "API_KEY in prod: Unknown" })).toBeInTheDocument());
    expect(screen.queryByText("Not set")).not.toBeInTheDocument();
  });
  it("lists stored-only secrets for a cloud-only project using its live environment id", async () => {
    store.list.mockResolvedValue({ secrets: [{ name: "STORED_ONLY", currentVersion: 1 }], protected: false });
    mount([env("denv_cloud_42")], null);
    await waitFor(() => expect(screen.getByRole("link", { name: "STORED_ONLY in denv_cloud_42: Set" })).toBeInTheDocument());
    expect(store.list).toHaveBeenCalledWith("denv_cloud_42");
    expect(screen.getByRole("link", { name: "API_KEY in denv_cloud_42: Not set" })).toBeInTheDocument();
    expect(store.history).not.toHaveBeenCalled();
    expect(store.reveal).not.toHaveBeenCalled();
  });
  it("labels retained metadata as refreshing and hides its old status and timestamp until the refetch settles", async () => {
    store.list.mockResolvedValue({ secrets: [{ name: "API_KEY", currentVersion: 2, updatedAt: "2026-03-02T12:00:00Z" }], protected: false });
    const { client, container } = mount([env("prod")]);
    await screen.findByRole("link", { name: "API_KEY in prod: Set" });
    expect(container.querySelector("time")).toBeInTheDocument();
    let finish!: (value: { secrets: never[]; protected: boolean }) => void;
    store.list.mockImplementationOnce(() => new Promise((done) => { finish = done; }));
    let refresh!: Promise<void>;
    act(() => { refresh = client.invalidateQueries(); });
    await screen.findByRole("link", { name: "API_KEY in prod: Refreshing…" });
    expect(screen.queryByRole("link", { name: "API_KEY in prod: Set" })).not.toBeInTheDocument();
    expect(container.querySelector("time")).not.toBeInTheDocument();
    await act(async () => { finish({ secrets: [], protected: false }); await refresh; });
    expect(await screen.findByRole("link", { name: "API_KEY in prod: Not set" })).toBeInTheDocument();
    expect(store.history).not.toHaveBeenCalled();
    expect(store.reveal).not.toHaveBeenCalled();
  });
  it("preserves declared names while metadata is loading", () => {
    store.list.mockImplementation(() => new Promise(() => {}));
    mount([env("prod")]);
    expect(screen.getByRole("rowheader", { name: "API_KEY" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "API_KEY in prod: Loading…" })).toBeInTheDocument();
  });
});
