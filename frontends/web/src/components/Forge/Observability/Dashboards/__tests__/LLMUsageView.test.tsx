// Copyright (c) 2026 Reliant Labs
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { OrganizationUsageRequest } from "@/services/controlPlane/observability/organizationUsageApi";

import { LLMUsageView } from "../LLMUsageView";
import { LLMUsageCompleteness } from "@/gen/reliant/v1/usage_pb";

import type { OwnLLMUsage, OwnLLMUsageRead } from "../llmUsageModel";
import { ownUsage } from "./llmUsageFixtures";
import { NOW_MS, ORG, respond, respondUnavailable } from "./organizationUsageFixtures";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

const state = vi.hoisted(() => ({
  own: { status: "not-deployed" } as unknown,
  accounts: { status: "ready", accounts: [] } as unknown,
  listUsage: vi.fn(),
  refresh: vi.fn(),
}));
vi.mock("@/services/controlPlane/observability/organizationUsageApi", () => ({ listOrganizationUsage: state.listUsage }));
vi.mock("@/hooks/llm-usage-queries", () => ({ useOwnLLMUsage: () => ({ ...(state.own as OwnLLMUsage), refresh: state.refresh, fetching: false }) satisfies OwnLLMUsageRead }));
vi.mock("@/hooks/provider-account-queries", () => ({ useProviderAccountsView: () => state.accounts }));

function view() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <LLMUsageView orgId={ORG} nowMs={NOW_MS} />
    </QueryClientProvider>
  );
}

beforeEach(() => {
  state.own = { status: "not-deployed" } satisfies OwnLLMUsage;
  state.accounts = { status: "ready", accounts: [] };
  state.refresh.mockReset();
  state.listUsage.mockReset().mockImplementation(async (req: OrganizationUsageRequest) => respond(req));
});

describe("LLMUsageView", () => {
  it("says the caller's own usage is not available, and draws nothing for it, on a server without the usage service", async () => {
    view();
    expect(screen.getByTestId("llm-usage-coming-soon")).toHaveTextContent("not available from this server yet");
    for (const panel of ["llm-tokens-by-mode", "llm-calls-errors", "llm-by-model", "llm-cache-hits"]) {
      expect(screen.getByTestId(panel)).toHaveTextContent("Not available from this server yet");
      expect(within(screen.getByTestId(panel)).queryByRole("group")).not.toBeInTheDocument();
      expect(within(screen.getByTestId(panel)).queryByRole("table")).not.toBeInTheDocument();
    }
    // The organization's Reliant AI spend is live meanwhile.
    expect(await within(screen.getByTestId("llm-managed-by-model")).findByText("claude-sonnet-5")).toBeInTheDocument();
    expect(screen.getByTestId("llm-managed-by-model")).toHaveTextContent("$10.00");
    expect(screen.getByTestId("llm-managed-by-model")).toHaveTextContent("Settled charges");
    expect(await screen.findByTestId("llm-managed-mtd")).toHaveTextContent("$20.00");
  });

  it("renders the caller's own usage by access mode, model and cache once it is served", async () => {
    state.own = { status: "ready", usage: ownUsage() } satisfies OwnLLMUsage;
    view();
    expect(screen.queryByTestId("llm-usage-coming-soon")).not.toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Tokens per day by access mode" })).toBeInTheDocument();
    expect(screen.getByTestId("llm-calls-errors")).toHaveTextContent("52 calls · 4 errors · error rate 7.7%");
    const modes = screen.getByTestId("llm-tokens-by-mode");
    expect(within(modes).getByText("Subscriptions").closest("tr")).toHaveTextContent(/^Subscriptions407.4k$/);
    expect(within(modes).getByText("Your API keys").closest("tr")).toHaveTextContent(/^Your API keys5500$/);
    expect(within(modes).getByText("Reliant AI").closest("tr")).toHaveTextContent(/^Reliant AI7770$/);
    // A mode with no usage is absent, not a zero row.
    expect(within(modes).queryByText("Local models")).not.toBeInTheDocument();
    const byModel = screen.getByTestId("llm-by-model");
    const sonnet = within(byModel).getByText("claude-sonnet-5").closest("tr")!;
    expect(sonnet).toHaveTextContent("40");
    expect(sonnet).toHaveTextContent("1.2s");
    expect(within(byModel).getByText("gpt-6")).toBeInTheDocument();
    expect(screen.getByTestId("llm-own-coverage")).toHaveTextContent("Complete");
    expect(screen.getByTestId("llm-own-coverage")).toHaveTextContent("Every day in range has its individual calls.");
    const cache = screen.getByTestId("llm-cache-hits");
    // 4000 cache reads / (2000 uncached input + 4000 cache reads + 400 cache writes).
    expect(within(cache).getByText("claude-sonnet-5").closest("tr")).toHaveTextContent("63%");
    expect(within(cache).getByText("gpt-6").closest("tr")).toHaveTextContent("0%");
  });

  it("counts cache reads and writes in every token total, not only input and output", () => {
    state.own = { status: "ready", usage: ownUsage() } satisfies OwnLLMUsage;
    view();
    // Subscriptions: day 1 is 1000 + 500 + 3000 read + 400 written = 4900, day 2 is 1000 + 500 + 1000 = 2500.
    expect(within(screen.getByTestId("llm-tokens-by-mode")).getByText("Subscriptions").closest("tr")).toHaveTextContent("7.4k");
    const purpose = screen.getByTestId("llm-by-purpose");
    // chat: 4000 + 1100 + 5000 + 600; compaction: 900 + 120; title: 60 + 20.
    expect(within(purpose).getByText("Chat").closest("tr")).toHaveTextContent("10.7k");
    expect(within(purpose).getByText("Context compaction").closest("tr")).toHaveTextContent("1k");
    expect(within(purpose).getByText("Chat titles").closest("tr")).toHaveTextContent("80");
    expect(within(purpose).queryByText("Automation drafts")).not.toBeInTheDocument();
  });

  it("refreshes the caller's own usage even when it is already ready", async () => {
    state.own = { status: "ready", usage: ownUsage() } satisfies OwnLLMUsage;
    view();
    await userEvent.setup().click(await screen.findByRole("button", { name: "Refresh" }));
    expect(state.refresh).toHaveBeenCalledTimes(1);
  });

  it("passes on the server's partial-completeness note for days older than per-call retention", () => {
    state.own = { status: "ready", usage: ownUsage(LLMUsageCompleteness.LLM_USAGE_COMPLETENESS_PARTIAL) } satisfies OwnLLMUsage;
    view();
    const coverage = screen.getByTestId("llm-own-coverage");
    expect(within(coverage).getByTestId("completeness-badge")).toHaveAttribute("data-completeness", "partial");
    expect(coverage).toHaveTextContent("Days before 2026-07-28 come from the daily rollup.");
  });

  it("shows an own-usage failure as unknown, not zero", () => {
    state.own = { status: "error" } satisfies OwnLLMUsage;
    view();
    expect(screen.getByTestId("llm-by-model")).toHaveTextContent("unknown, not zero");
  });

  it("keeps the caller's own panels when organization spend is refused for lack of billing:read", async () => {
    state.own = { status: "ready", usage: ownUsage() } satisfies OwnLLMUsage;
    state.listUsage.mockRejectedValue(new ConnectError("denied", Code.PermissionDenied));
    view();
    expect((await screen.findAllByTestId("llm-managed-denied"))[0]).toHaveTextContent("billing:read");
    expect(screen.queryByText("$10.00")).not.toBeInTheDocument();
    expect(screen.getByTestId("llm-by-model")).toHaveTextContent("gpt-6");
  });

  it("reports unavailable Reliant AI spend as unavailable, not $0", async () => {
    state.listUsage.mockImplementation(async (req: OrganizationUsageRequest) => respondUnavailable(req));
    view();
    expect(await within(screen.getByTestId("llm-managed-by-model")).findByText("Reliant AI spend is unavailable, not zero.")).toBeInTheDocument();
    expect(await screen.findByTestId("llm-managed-mtd")).toHaveTextContent("Unavailable");
    expect(screen.queryByText("$0.00")).not.toBeInTheDocument();
  });

  it("shows each connected subscription's windows as the provider reports them", () => {
    state.accounts = {
      status: "ready",
      accounts: [
        {
          id: "a1",
          provider: "claude",
          providerDisplayName: "Claude",
          authKind: "oauth",
          label: "me@example.com",
          priority: 0,
          status: "available",
          windows: [
            { name: "5h", label: "5-hour", usedFraction: 0.72, resetsAt: "2026-10-25T14:00:00Z", lengthSeconds: 18000 },
            { name: "7d", label: "Weekly", lengthSeconds: 604800 },
          ],
          usageObservedAt: "2026-10-25T11:58:00Z",
          usageSource: "headers",
          createdAt: "2026-01-01T00:00:00Z",
        },
        { id: "k1", provider: "openai", providerDisplayName: "OpenAI", authKind: "api_key", label: "sk-…1234", priority: 1, status: "available", windows: [], usageSource: "none", createdAt: "2026-01-01T00:00:00Z" },
      ],
    };
    view();
    const panel = screen.getByTestId("llm-subscription-windows");
    expect(within(panel).getAllByTestId("llm-subscription-account")).toHaveLength(1);
    expect(panel).toHaveTextContent("72% used");
    expect(panel).toHaveTextContent("resets in 2h");
    expect(panel).toHaveTextContent("Usage not reported");
  });
});
