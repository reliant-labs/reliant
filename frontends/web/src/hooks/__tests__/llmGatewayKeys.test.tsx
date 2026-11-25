/**
 * The Reliant AI tab's key and spend hooks run on the generated
 * LLMGatewayService hooks. What the hand-written `["reliantAI", …]` keys gave
 * must still hold:
 *
 *   - each read hands the UI the shape it read before (keys, models, spend);
 *   - a key write refreshes the org's key list and every spend window of that
 *     org, and nothing else: not another org's, not the plan's models;
 *   - an org switch purges every read (its root is in ORG_SCOPED_QUERY_ROOTS);
 *   - creating a key with no org fails before any request is sent.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { llmGatewayServiceKeys } from "@/hooks/controlplane/llm-gateway-service-hooks_gen";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { ORG_SCOPED_QUERY_ROOTS } from "@/store/activeOrgStore";
import { serviceHandlers } from "@/test/serviceTransport";
import {
  useAvailableModels,
  useCreateLLMKey,
  useLLMKeys,
  useLLMSpend,
  useRevokeLLMKey,
  useRotateLLMKey,
} from "../useReliantAIQueries";

const A_KEYS = llmGatewayServiceKeys.listLLMKeys({ orgId: "org-a" });
const A_SPEND = llmGatewayServiceKeys.getLLMSpend({ orgId: "org-a", startDate: "2026-10-01", endDate: "2026-10-31" });
const A_SPEND_OTHER_WINDOW = llmGatewayServiceKeys.getLLMSpend({ orgId: "org-a", startDate: "2026-11-01", endDate: "2026-11-30" });
const A_MODELS = llmGatewayServiceKeys.listAvailableModels({ orgId: "org-a" });
const B_KEYS = llmGatewayServiceKeys.listLLMKeys({ orgId: "org-b" });
const B_SPEND = llmGatewayServiceKeys.getLLMSpend({ orgId: "org-b", startDate: "2026-10-01", endDate: "2026-10-31" });

const calls = vi.fn();
let client: QueryClient;
function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

beforeEach(() => {
  calls.mockReset();
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  for (const key of [A_KEYS, A_SPEND, A_SPEND_OTHER_WINDOW, A_MODELS, B_KEYS, B_SPEND]) client.setQueryData(key, {});
  const answer = (method: string, response: unknown) => (request: unknown) => {
    calls(method, request);
    return response;
  };
  setTransport(
    mockTransport(
      serviceHandlers("LLMGatewayService", {
        listLLMKeys: answer("ListLLMKeys", { keys: [{ id: "k1", name: "App" }] }),
        listAvailableModels: answer("ListAvailableModels", { models: ["claude-sonnet-5"] }),
        getLLMSpend: answer("GetLLMSpend", { entries: [{ model: "claude-sonnet-5", spend: 1.5 }], totalSpend: 1.5, sampleDays: 7 }),
        createLLMKey: answer("CreateLLMKey", { key: { id: "k2" }, plaintextKey: "sk-new" }),
        revokeLLMKey: answer("RevokeLLMKey", {}),
        rotateLLMKey: answer("RotateLLMKey", { key: { id: "k1" }, plaintextKey: "sk-rotated" }),
      }),
    ),
  );
});
afterEach(() => setTransport(null));

function invalidated() {
  return Object.fromEntries(
    Object.entries({ A_KEYS, A_SPEND, A_SPEND_OTHER_WINDOW, A_MODELS, B_KEYS, B_SPEND }).map(([name, key]) => [
      name,
      client.getQueryState(key)?.isInvalidated,
    ]),
  );
}
const ORG_A_WRITE = {
  A_KEYS: true,
  A_SPEND: true,
  A_SPEND_OTHER_WINDOW: true,
  A_MODELS: false,
  B_KEYS: false,
  B_SPEND: false,
};

it("an org switch purges every LLMGatewayService read", () => {
  expect(ORG_SCOPED_QUERY_ROOTS).toContain(llmGatewayServiceKeys.all[0]);
});

it("the reads hand the UI the shapes it read before", async () => {
  client.clear();
  const keys = renderHook(() => useLLMKeys("org-a"), { wrapper });
  const models = renderHook(() => useAvailableModels("org-a"), { wrapper });
  const spend = renderHook(() => useLLMSpend({ orgId: "org-a", startDate: "2026-10-01", endDate: "2026-10-31" }), { wrapper });
  await waitFor(() => expect(keys.result.current.data?.map((k) => k.id)).toEqual(["k1"]));
  await waitFor(() => expect(models.result.current.data).toEqual(["claude-sonnet-5"]));
  await waitFor(() => expect(spend.result.current.data).toMatchObject({ totalSpend: 1.5, sampleDays: 7 }));
  expect(spend.result.current.data?.entries).toHaveLength(1);
  expect(calls).toHaveBeenCalledWith("GetLLMSpend", expect.objectContaining({ orgId: "org-a", startDate: "2026-10-01" }));
});

it("no org: the reads stay disabled", () => {
  renderHook(() => useLLMKeys(""), { wrapper });
  renderHook(() => useLLMSpend({ orgId: "", startDate: "2026-10-01", endDate: "2026-10-31" }), { wrapper });
  renderHook(() => useAvailableModels("org-a", false), { wrapper });
  expect(calls).not.toHaveBeenCalled();
});

it("creating a key refreshes the org's keys and spend, and returns the one-time secret", async () => {
  const { result } = renderHook(() => useCreateLLMKey(), { wrapper });
  const res = await result.current.mutateAsync({ orgId: "org-a", name: "App", models: ["claude-sonnet-5"] });
  expect(res.plaintextKey).toBe("sk-new");
  expect(calls).toHaveBeenCalledWith("CreateLLMKey", expect.objectContaining({ orgId: "org-a", name: "App" }));
  await waitFor(() => expect(invalidated()).toEqual(ORG_A_WRITE));
});

it("creating a key with no org sends nothing", async () => {
  const { result } = renderHook(() => useCreateLLMKey(), { wrapper });
  await expect(result.current.mutateAsync({ orgId: "", name: "App", models: [] })).rejects.toThrow(/organization/);
  expect(calls).not.toHaveBeenCalled();
});

it("revoking a key refreshes the org's keys and spend", async () => {
  const { result } = renderHook(() => useRevokeLLMKey("org-a"), { wrapper });
  await result.current.mutateAsync("k1");
  expect(calls).toHaveBeenCalledWith("RevokeLLMKey", expect.objectContaining({ keyId: "k1" }));
  await waitFor(() => expect(invalidated()).toEqual(ORG_A_WRITE));
});

it("rotating a key refreshes the org's keys and spend, and returns the new secret", async () => {
  const { result } = renderHook(() => useRotateLLMKey("org-a"), { wrapper });
  const res = await result.current.mutateAsync({ keyId: "k1", gracePeriod: "24h" });
  expect(res.plaintextKey).toBe("sk-rotated");
  expect(calls).toHaveBeenCalledWith("RotateLLMKey", expect.objectContaining({ keyId: "k1", gracePeriod: "24h" }));
  await waitFor(() => expect(invalidated()).toEqual(ORG_A_WRITE));
});
