// Copyright (c) 2025 Reliant Labs

/**
 * The chat's question before it moves from its org's AI accounts to the
 * person's own (org-provider-accounts §10.2): each of the three cases says
 * what happened and offers its actions; an answer goes to
 * ResolvePersonalFundingChoice, and "Don't ask again" both sends remember and
 * shows the remembered choice in Settings at once. Closing it is no answer:
 * it stays closed until "Review" reopens it.
 */

import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import {
  PersonalFundingQuestionSchema,
  PersonalFundingQuestion_Reason,
  PersonalFundingQuestion_State,
  ResolvePersonalFundingChoiceRequest_Choice,
} from "@/gen/reliant/v1/provider_account_pb";
import { OrgProviderAccountAccess } from "@/gen/reliant/v1/org_provider_account_pb";
import { orgFundingKey } from "@/hooks/org-funding-queries";
import { usePersonalFundingPrompt } from "@/hooks/personal-funding-queries";
import { settingsSync } from "@/services/settingsSync";
import { ORG_FUNDING_KEY, parseOrgFunding } from "@/lib/orgFunding";

const mocks = vi.hoisted(() => ({
  question: null as Record<string, unknown> | null,
  resolve: vi.fn(),
}));

vi.mock("@/services/controlPlane/config", () => ({ hasControlPlane: true }));
vi.mock("@/store/authStore", () => ({
  useAuthStore: (sel: (s: unknown) => unknown) => sel({ user: { id: "u-me" }, session: { access_token: "t" } }),
}));
vi.mock("@/hooks/useOrgs", () => ({
  useMyOrgs: () => ({ data: { orgs: [{ id: "org-a", name: "Acme", slug: "acme", role: "member", isOwner: false, isDefault: true }], defaultOrgId: "org-a" } }),
}));
vi.mock("@/store/globalDataStore", () => ({
  useModels: () => ({
    models: [
      { id: "claude-opus-4@claude", name: "Claude Opus", provider: "anthropic", driverId: "claude" },
      { id: "claude-sonnet-4@anthropic", name: "Claude Sonnet", provider: "anthropic", driverId: "anthropic" },
      { id: "gpt-5@openai", name: "GPT-5", provider: "openai", driverId: "openai" },
    ],
    tiers: {},
  }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() } }));

import { PersonalFundingPrompt, FundingReviewLink } from "../PersonalFundingPrompt";

const reset = new Date(Date.now() + 2 * 3600_000);

function question(reason: PersonalFundingQuestion_Reason, extra: Record<string, unknown> = {}) {
  return create(PersonalFundingQuestionSchema, {
    chatId: "chat-1",
    orgId: "org-a",
    reason,
    state: PersonalFundingQuestion_State.ASKED,
    model: "claude-opus-4",
    personalProvider: "claude",
    personalAccountLabel: "alice@example.com",
    askedAt: "2026-11-13T12:00:00Z",
    ...extra,
  });
}

let qc: QueryClient;
function renderPrompt() {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <PersonalFundingPrompt chatId="chat-1" waiting />
      <FundingReviewLink chatId="chat-1" />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  mocks.resolve.mockReset();
  usePersonalFundingPrompt.setState({ openFor: null, closed: {} });
  settingsSync.updateCache(ORG_FUNDING_KEY, "");
  setTransport(
    mockTransport({
      "ProviderAccountService.GetPersonalFundingQuestion": () => ({ question: mocks.question ?? undefined }),
      "ProviderAccountService.ResolvePersonalFundingChoice": (req) => {
        mocks.resolve(req);
        mocks.question = null;
        return { question: question(PersonalFundingQuestion_Reason.LIMITED, { state: PersonalFundingQuestion_State.USE_PERSONAL }) };
      },
      "OrgProviderAccountService.ListOrgProviderAccounts": () => ({
        accounts: [
          {
            account: { id: "k1", provider: "anthropic", providerDisplayName: "Anthropic", label: "Acme prod", status: 1 },
            access: OrgProviderAccountAccess.MEMBERS,
          },
        ],
        callerIsAdmin: false,
        addingEnabled: true,
      }),
    }),
  );
});

describe("PersonalFundingPrompt", () => {
  it("case 1, limited: names the org, the reset and the person's own account; Use my account answers once", async () => {
    mocks.question = question(PersonalFundingQuestion_Reason.LIMITED, { orgResetAt: reset.toISOString() });
    renderPrompt();
    const prompt = await screen.findByTestId("personal-funding-prompt");
    expect(prompt).toHaveAttribute("data-reason", "limited");
    const text = screen.getByTestId("funding-question").textContent ?? "";
    expect(text).toMatch(/^Acme's AI accounts are rate-limited until .+\. Continue on your own Claude account \(alice@example\.com\) until then\?$/);
    expect(screen.queryByTestId("funding-ask-admin")).toBeNull();
    expect(screen.queryByTestId("funding-choose-model")).toBeNull();
    expect(screen.getByText("Don't ask again in Acme")).toBeInTheDocument();

    fireEvent.click(screen.getByTestId("funding-use-personal"));
    await waitFor(() => expect(mocks.resolve).toHaveBeenCalledTimes(1));
    expect(mocks.resolve.mock.calls[0][0]).toMatchObject({
      chatId: "chat-1",
      choice: ResolvePersonalFundingChoiceRequest_Choice.USE_PERSONAL,
      remember: false,
    });
    await waitFor(() => expect(screen.queryByTestId("personal-funding-prompt")).toBeNull());
  });

  it("case 2, unusable: offers Ask an admin beside the answer", async () => {
    mocks.question = question(PersonalFundingQuestion_Reason.UNUSABLE);
    renderPrompt();
    await screen.findByTestId("personal-funding-prompt");
    expect(screen.getByTestId("funding-question")).toHaveTextContent("Acme's AI accounts can't serve this chat right now.");
    expect(screen.getByTestId("funding-ask-admin")).toBeInTheDocument();
    expect(screen.queryByTestId("funding-choose-model")).toBeNull();
  });

  it("case 3, unserved: names the model, lists what the org's accounts serve, and offers another model", async () => {
    mocks.question = question(PersonalFundingQuestion_Reason.UNSERVED);
    const opened = vi.fn();
    window.addEventListener("open-model-selector", opened);
    renderPrompt();
    await screen.findByTestId("personal-funding-prompt");
    expect(screen.getByTestId("funding-question")).toHaveTextContent("None of Acme's AI accounts serve Claude Opus.");
    await waitFor(() => expect(screen.getByTestId("funding-org-models")).toHaveTextContent("Claude Sonnet"));
    expect(screen.getByTestId("funding-org-models")).not.toHaveTextContent("GPT-5");
    fireEvent.click(screen.getByTestId("funding-choose-model"));
    expect(opened).toHaveBeenCalledTimes(1);
    expect(mocks.resolve).not.toHaveBeenCalled();
    window.removeEventListener("open-model-selector", opened);
  });

  it("remembers: Keep waiting with Don't ask again sends remember and shows Wait in Settings without a reload", async () => {
    mocks.question = question(PersonalFundingQuestion_Reason.LIMITED);
    renderPrompt();
    await screen.findByTestId("personal-funding-prompt");
    fireEvent.click(screen.getByTestId("funding-remember"));
    fireEvent.click(screen.getByTestId("funding-keep-waiting"));
    await waitFor(() => expect(mocks.resolve).toHaveBeenCalledTimes(1));
    expect(mocks.resolve.mock.calls[0][0]).toMatchObject({ choice: ResolvePersonalFundingChoiceRequest_Choice.KEEP_WAITING, remember: true });
    await waitFor(() => expect(qc.getQueryData(orgFundingKey)).toEqual({ "org-a": { on_org_limit: "wait" } }));
    expect(parseOrgFunding(settingsSync.getSettingFromCache(ORG_FUNDING_KEY)?.value)).toEqual({ "org-a": { on_org_limit: "wait" } });
  });

  it("closing is no answer: it stays closed, the chat keeps waiting, and Review reopens it", async () => {
    mocks.question = question(PersonalFundingQuestion_Reason.LIMITED);
    renderPrompt();
    await screen.findByTestId("personal-funding-prompt");
    act(() => usePersonalFundingPrompt.getState().close("chat-1", "2026-11-13T12:00:00Z"));
    await waitFor(() => expect(screen.queryByTestId("personal-funding-prompt")).toBeNull());
    expect(mocks.resolve).not.toHaveBeenCalled();

    const review = await screen.findByTestId("funding-review");
    expect(review).toHaveTextContent("Waiting for Acme's accounts");
    fireEvent.click(screen.getByRole("button", { name: "Review" }));
    expect(await screen.findByTestId("personal-funding-prompt")).toBeInTheDocument();
  });

  it("shows nothing for a chat with no question, or one already answered", async () => {
    mocks.question = question(PersonalFundingQuestion_Reason.LIMITED, { state: PersonalFundingQuestion_State.USE_PERSONAL });
    renderPrompt();
    await act(async () => {
      await new Promise((r) => setTimeout(r, 20));
    });
    expect(screen.queryByTestId("personal-funding-prompt")).toBeNull();
    expect(screen.queryByTestId("funding-review")).toBeNull();
  });
});
