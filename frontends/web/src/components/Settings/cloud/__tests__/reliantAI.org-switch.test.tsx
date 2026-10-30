import { act, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, expect, it, vi } from "vitest";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import { LLMKeyStatus } from "@/gen/controlplane/controlplane/v1/shared_pb";

const mocks = vi.hoisted(() => ({ rotate: vi.fn(), create: vi.fn() }));
vi.mock("@/services/controlPlane/reliantAI", async (original) => ({
  ...await original<Record<string, unknown>>(), reliantAIAvailable: true,
}));
vi.mock("@/hooks/useGoToBilling", () => ({ useGoToBilling: () => vi.fn() }));
vi.mock("@/hooks/useOrgs", () => ({
  useActiveOrg: () => ({ activeOrgId: useActiveOrgStore((s) => s.effectiveId) }),
}));
const query = (data: unknown) => ({ data, isLoading: false, error: null, refetch: vi.fn() });
const mutation = (mutateAsync = vi.fn()) => ({ mutateAsync, mutate: vi.fn(), isPending: false });
vi.mock("@/hooks/useReliantAIQueries", () => ({
  useReliantOverview: () => query({ allowedModels: [] }),
  useWalletOverview: () => query({ wallet: { balanceUsdNanos: 1_000_000_000n } }),
  useLLMKeys: () => query([{ id: "key", name: "App key", status: LLMKeyStatus.LLM_KEY_STATUS_ACTIVE, models: [] }]),
  useAvailableModels: () => query([]),
  useLLMSpend: () => query({ entries: [], totalSpend: 0 }),
  useCreateLLMKey: () => mutation(mocks.create),
  useRevokeLLMKey: () => mutation(),
  useRotateLLMKey: () => mutation(mocks.rotate),
  useRedeemCoupon: () => mutation(),
}));
vi.mock("@/hooks/useCloudBillingQueries", () => ({
  useWalletOverview: () => query({ wallet: { balanceUsdNanos: 1_000_000_000n } }),
}));
import { ReliantAISection } from "../reliantAI";

beforeEach(() => {
  useActiveOrgStore.setState({ userId: "alice", chosenId: "org-a", effectiveId: "org-a" });
  vi.spyOn(window, "confirm").mockReturnValue(true);
  mocks.rotate.mockReset();
  mocks.create.mockReset();
});

it("does not reveal an old created key or clear the next org's draft", async () => {
  let finish!: (value: unknown) => void;
  mocks.create.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  render(<QueryClientProvider client={new QueryClient()}><ReliantAISection /></QueryClientProvider>);
  fireEvent.click(screen.getByRole("button", { name: /^create key$/i }));
  fireEvent.change(screen.getByPlaceholderText("e.g. production-agent"), { target: { value: "Previous org key" } });
  fireEvent.click(screen.getByRole("button", { name: /^create key$/i }));
  expect(mocks.create).toHaveBeenCalledOnce();
  act(() => useActiveOrgStore.setState({ effectiveId: "org-b" }));
  fireEvent.click(screen.getByRole("button", { name: /^create key$/i }));
  fireEvent.change(screen.getByPlaceholderText("e.g. production-agent"), { target: { value: "New org draft" } });
  await act(async () => finish({ plaintextKey: "test-only-old-created-secret" }));
  expect(screen.queryByText("test-only-old-created-secret")).toBeNull();
  expect(screen.getByPlaceholderText("e.g. production-agent")).toHaveValue("New org draft");
});

it("does not replace a newer same-org secret with an older operation", async () => {
  let finish!: (value: unknown) => void;
  mocks.rotate.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  mocks.rotate.mockResolvedValueOnce({ plaintextKey: "test-only-newer-secret" });
  render(<QueryClientProvider client={new QueryClient()}><ReliantAISection /></QueryClientProvider>);
  fireEvent.click(screen.getByRole("button", { name: /rotate/i }));
  fireEvent.click(screen.getByRole("button", { name: /rotate/i }));
  expect(await screen.findByText("test-only-newer-secret")).toBeInTheDocument();
  await act(async () => finish({ plaintextKey: "test-only-older-secret" }));
  expect(screen.queryByText("test-only-older-secret")).toBeNull();
  expect(screen.getByText("test-only-newer-secret")).toBeInTheDocument();
});

it("never reveals a prior org's delayed rotated key after A → B → A", async () => {
  let finish!: (value: unknown) => void;
  mocks.rotate.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  render(<QueryClientProvider client={new QueryClient()}><ReliantAISection /></QueryClientProvider>);
  fireEvent.click(screen.getByRole("button", { name: /rotate/i }));
  expect(mocks.rotate).toHaveBeenCalledOnce();
  act(() => useActiveOrgStore.setState({ effectiveId: "org-b" }));
  act(() => useActiveOrgStore.setState({ effectiveId: "org-a" }));
  await act(async () => finish({ plaintextKey: "test-only-stale-secret" }));
  expect(screen.queryByText("test-only-stale-secret")).toBeNull();
  expect(screen.queryByText(/key rotated/i)).toBeNull();
});
