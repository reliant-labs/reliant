/**
 * Settings → Providers, account-aware: the account list (desktop and mobile
 * variants), exception-first rows (a healthy account is one line; a limited,
 * nearly limited or signed-out one opens itself), rename / pause / reorder /
 * remove through ProviderAccountService, and the list-query gate.
 *
 * ProviderAccountService is answered at the transport (test/providerAccountTransport),
 * so the generated hooks run for real.
 */

import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithQuery } from "../../../../test/renderWithQuery";
import type { ProviderAccount, UsageWindow } from "../../../../types/providerAccount";
import { setTransport } from "../../../../lib/connect";
import { mockTransport } from "../../../../lib/test-utils";
import { providerAccountHandlers } from "../../../../test/providerAccountTransport";

const mocks = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  updateAccount: vi.fn(),
  reorderAccounts: vi.fn(),
  deleteAccount: vi.fn(),
  addApiKey: vi.fn(),
  refreshUsage: vi.fn(),
  readSetting: vi.fn(),
  upsertStringSetting: vi.fn(),
  refetchModels: vi.fn(),
}));

vi.mock("../../../../lib/settingsPersistence", () => ({
  readSetting: mocks.readSetting,
  upsertStringSetting: mocks.upsertStringSetting,
}));

vi.mock("../../../../store/globalDataStore", () => ({
  useGlobalDataStore: { getState: () => ({ refetchModels: mocks.refetchModels }) },
  useModels: () => ({ models: [], tiers: {}, loading: false, error: null }),
}));

vi.mock("../../../../lib/events", () => ({ getEventBus: () => ({ emit: vi.fn() }) }));

vi.mock("../../../../hooks", () => {
  const flow = () => ({ start: vi.fn(), cancel: vi.fn(), reset: vi.fn() });
  return {
    useClaudeOAuth: flow,
    useCodexOAuth: flow,
    useAntigravityOAuth: flow,
    useCopilotOAuth: () => ({
      phase: "idle",
      isActive: false,
      userCode: null,
      verificationUri: null,
      message: null,
      lastResult: null,
      start: vi.fn(),
      cancel: vi.fn(),
      reset: vi.fn(),
    }),
    useOAuthAvailability: () => ({ available: true, loading: false, recheck: vi.fn() }),
  };
});

import { useAuthStore } from "../../../../store/authStore";
import { AccountList, groupByProvider } from "../AccountList";
import { useProviderAccountsView } from "../../../../hooks/provider-account-queries";
import {
  effectiveWindow,
  formatDuration,
  formatResetsIn,
  planLabel,
  usageTone,
} from "../accountFormat";

const NOW = new Date("2026-10-18T12:00:00Z");
const iso = (minutesFromNow: number) => new Date(NOW.getTime() + minutesFromNow * 60_000).toISOString();

function win(partial: Partial<UsageWindow> & { name: string }): UsageWindow {
  return { label: partial.name, lengthSeconds: 0, ...partial };
}

function account(partial: Partial<ProviderAccount> & { id: string }): ProviderAccount {
  return {
    provider: "claude",
    providerDisplayName: "Claude subscription",
    authKind: "oauth",
    label: partial.email ?? partial.id,
    priority: 0,
    status: "available",
    windows: [],
    usageSource: "headers",
    createdAt: "2026-10-01T00:00:00Z",
    ...partial,
  };
}

const alice = account({
  id: "a1",
  email: "alice@example.com",
  label: "alice@example.com",
  plan: "max_20x",
  planDisplay: "Max 20×",
  priority: 0,
  windows: [
    win({ name: "5h", label: "5-hour", usedFraction: 0.42, resetsAt: iso(133), lengthSeconds: 18_000 }),
    win({ name: "7d", label: "Weekly", usedFraction: 0.6, resetsAt: iso(60 * 50), lengthSeconds: 604_800 }),
  ],
  usageObservedAt: iso(-1),
});
const bob = account({
  id: "a2",
  email: "bob@work.example",
  label: "bob@work.example",
  plan: "pro",
  priority: 1,
  status: "limited",
  limitedUntil: iso(100),
  limitedWindow: "5h",
  windows: [win({ name: "5h", label: "5-hour", usedFraction: 1, resetsAt: iso(100), lengthSeconds: 18_000 })],
  usageObservedAt: iso(-30),
});
const carol = account({
  id: "a3",
  email: "carol@example.com",
  label: "carol@example.com",
  priority: 2,
  status: "paused",
});
const dave = account({
  id: "a4",
  email: "dave@example.com",
  label: "dave@example.com",
  priority: 3,
  status: "needs_sign_in",
  needsSignInReason: "The sign-in expired.",
});
const key = account({
  id: "k1",
  provider: "anthropic",
  providerDisplayName: "Anthropic API",
  authKind: "api_key",
  label: "sk-ant-…a1b2",
  maskedKey: "sk-ant-…a1b2",
});

function renderList(accounts: ProviderAccount[], variant: "desktop" | "mobile" = "desktop") {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(NOW);
  return renderWithQuery(<AccountList accounts={accounts} variant={variant} />);
}

/** The account's row in its provider's table. */
function rowFor(label: string): HTMLElement {
  const row = screen.getAllByTestId("account-row").find((r) => within(r).queryByText(label) !== null);
  if (!row) throw new Error(`no account row for ${label}`);
  return row;
}

/** The row's expanded detail: the full usage bars and identity line. */
function detailFor(label: string): HTMLElement {
  const detail = rowFor(label).nextElementSibling;
  if (!(detail instanceof HTMLElement) || detail.getAttribute("data-row-detail") !== "true") {
    throw new Error(`${label} is not expanded`);
  }
  return detail;
}

async function expand(label: string) {
  await userEvent.setup().click(screen.getByRole("button", { name: `Details for ${label}` }));
}

beforeEach(() => {
  Object.values(mocks).forEach((m) => m.mockReset());
  mocks.updateAccount.mockResolvedValue(null);
  mocks.reorderAccounts.mockResolvedValue([]);
  mocks.deleteAccount.mockResolvedValue(undefined);
  mocks.refreshUsage.mockResolvedValue({ account: null, throttled: false });
  mocks.refetchModels.mockResolvedValue(undefined);
  mocks.readSetting.mockResolvedValue({ status: "missing" });
  mocks.upsertStringSetting.mockResolvedValue(undefined);
  setTransport(mockTransport(providerAccountHandlers(mocks)));
});

afterEach(() => {
  setTransport(null);
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("account rows", () => {
  it("keeps a healthy account to one line: label, plan as text, small meters, no status", () => {
    renderList([alice, bob, carol, dave]);

    const a = within(rowFor("alice@example.com"));
    expect(rowFor("alice@example.com")).toHaveAttribute("data-expanded", "false");
    expect(a.getByText("alice@example.com")).toBeInTheDocument();
    expect(a.getByTestId("account-plan")).toHaveTextContent("Max 20×");
    // No "Available" pill: a healthy account says nothing about its health.
    expect(a.queryByTestId("account-status")).not.toBeInTheDocument();
    expect(a.queryByRole("progressbar")).not.toBeInTheDocument();
    expect(a.getByRole("img", { name: "5-hour 42% used" })).toBeInTheDocument();
    expect(a.getByRole("img", { name: "Weekly 60% used" })).toBeInTheDocument();
  });

  it("opens a limited or signed-out account by itself and says what is wrong in one sentence", () => {
    renderList([alice, bob, carol, dave]);

    const b = within(rowFor("bob@work.example"));
    expect(rowFor("bob@work.example")).toHaveAttribute("data-expanded", "true");
    expect(b.getByTestId("account-status")).toHaveTextContent(/^Limited: 5-hour cap · back /);
    expect(b.getByTestId("account-plan")).toHaveTextContent("Pro");
    expect(within(detailFor("bob@work.example")).getByRole("progressbar")).toBeInTheDocument();

    expect(within(rowFor("carol@example.com")).getByTestId("account-status")).toHaveTextContent("Paused");
    expect(rowFor("carol@example.com")).toHaveAttribute("data-expanded", "false");

    const d = within(rowFor("dave@example.com"));
    expect(rowFor("dave@example.com")).toHaveAttribute("data-expanded", "true");
    expect(d.getByTestId("account-status")).toHaveTextContent("Needs sign-in");
    expect(d.getByRole("button", { name: "Reconnect" })).toBeInTheDocument();
    expect(within(detailFor("dave@example.com")).getByText("The sign-in expired.")).toBeInTheDocument();
  });

  it("opens any row on request for its bars", async () => {
    renderList([alice]);
    await expand("alice@example.com");
    const a = within(detailFor("alice@example.com"));
    expect(a.getAllByRole("progressbar")).toHaveLength(2);
    expect(a.getByText(/42% used · resets in 2h 13m/)).toBeInTheDocument();
    expect(a.getByText(/60% used · resets in 2d 2h/)).toBeInTheDocument();
  });

  it("shows the masked key, and 'Per-minute limits only' once opened, for an API-key account", async () => {
    renderList([key]);
    const row = within(rowFor("sk-ant-…a1b2"));
    expect(row.getByText("sk-ant-…a1b2")).toBeInTheDocument();
    expect(row.queryByTestId("usage-meters")).not.toBeInTheDocument();
    await expand("sk-ant-…a1b2");
    const detail = within(detailFor("sk-ant-…a1b2"));
    expect(detail.getByText("Per-minute limits only")).toBeInTheDocument();
    expect(detail.queryByRole("progressbar")).not.toBeInTheDocument();
  });

  it("captions usage 'as of N min ago' once it is stale, and not while fresh", async () => {
    renderList([alice, bob]);
    expect(within(detailFor("bob@work.example")).getByText("as of 30 min ago")).toBeInTheDocument();
    await expand("alice@example.com");
    expect(within(detailFor("alice@example.com")).queryByText(/^as of /)).not.toBeInTheDocument();
  });

  it("degrades to 'Usage unknown' for a window with no reported fraction", async () => {
    renderList([account({ id: "u", email: "u@x.example", label: "u@x.example", windows: [win({ name: "5h", label: "5-hour" })] })]);
    expect(screen.getByRole("img", { name: "5-hour usage unknown" })).toBeInTheDocument();
    await expand("u@x.example");
    const bar = screen.getByRole("progressbar", { name: "5-hour usage" });
    expect(bar).toHaveAttribute("aria-valuetext", "unknown");
    expect(screen.getByText("Usage unknown")).toBeInTheDocument();
  });

  it("colors bars amber from 80% and red when the window benched the account", () => {
    const hot = account({
      id: "h",
      email: "h@x.example",
      label: "h@x.example",
      windows: [
        win({ name: "5h", label: "5-hour", usedFraction: 0.79, resetsAt: iso(60) }),
        win({ name: "7d", label: "Weekly", usedFraction: 0.8, resetsAt: iso(600) }),
      ],
    });
    renderList([hot, bob]);
    // At 80% the account is near its limit, so it opens by itself.
    expect(rowFor("h@x.example")).toHaveAttribute("data-expanded", "true");
    const h = within(detailFor("h@x.example")).getAllByRole("progressbar");
    expect(h[0]).toHaveAttribute("data-tone", "ok");
    expect(h[1]).toHaveAttribute("data-tone", "warn");
    expect(within(detailFor("bob@work.example")).getByRole("progressbar")).toHaveAttribute("data-tone", "danger");
  });
});

describe("account actions", () => {
  it("renames through UpdateProviderAccount and updates the label optimistically", async () => {
    const user = userEvent.setup();
    mocks.listAccounts.mockResolvedValue([alice]);
    renderList([alice]);

    await user.click(screen.getByRole("button", { name: "Actions for alice@example.com" }));
    await user.click(screen.getByRole("menuitem", { name: "Rename" }));
    const input = screen.getByRole("textbox", { name: /name for alice/i });
    await user.clear(input);
    await user.type(input, "Personal Max{Enter}");

    await waitFor(() => expect(mocks.updateAccount).toHaveBeenCalledWith("a1", { label: "Personal Max" }));
  });

  it("sends an empty label to restore the default email", async () => {
    const user = userEvent.setup();
    const renamed = account({ ...alice, label: "Personal Max" });
    renderList([renamed]);

    await user.click(screen.getByRole("button", { name: "Actions for Personal Max" }));
    await user.click(screen.getByRole("menuitem", { name: "Rename" }));
    await user.clear(screen.getByRole("textbox", { name: /name for personal max/i }));
    await user.keyboard("{Enter}");

    await waitFor(() => expect(mocks.updateAccount).toHaveBeenCalledWith("a1", { label: "" }));
  });

  it("pauses and resumes", async () => {
    const user = userEvent.setup();
    renderList([alice, carol]);

    await user.click(screen.getByRole("button", { name: "Actions for alice@example.com" }));
    await user.click(screen.getByRole("menuitem", { name: "Pause" }));
    await waitFor(() => expect(mocks.updateAccount).toHaveBeenCalledWith("a1", { paused: true }));

    await user.click(screen.getByRole("button", { name: "Actions for carol@example.com" }));
    await user.click(screen.getByRole("menuitem", { name: "Resume" }));
    await waitFor(() => expect(mocks.updateAccount).toHaveBeenCalledWith("a3", { paused: false }));
  });

  it("reorders with Move up / Move down, sending the provider's full order", async () => {
    const user = userEvent.setup();
    renderList([alice, bob, carol]);

    await user.click(screen.getByRole("button", { name: "Actions for bob@work.example" }));
    await user.click(screen.getByRole("menuitem", { name: "Move up" }));
    await waitFor(() => expect(mocks.reorderAccounts).toHaveBeenCalledWith("claude", ["a2", "a1", "a3"]));

    await user.click(screen.getByRole("button", { name: "Actions for alice@example.com" }));
    await user.click(screen.getByRole("menuitem", { name: "Move down" }));
    await waitFor(() => expect(mocks.reorderAccounts).toHaveBeenLastCalledWith("claude", ["a2", "a1", "a3"]));
  });

  it("disables Move up on the first account and Move down on the last", async () => {
    const user = userEvent.setup();
    renderList([alice, bob]);
    await user.click(screen.getByRole("button", { name: "Actions for alice@example.com" }));
    expect(screen.getByRole("menuitem", { name: "Move up" })).toHaveAttribute("aria-disabled", "true");
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "Actions for bob@work.example" }));
    expect(screen.getByRole("menuitem", { name: "Move down" })).toHaveAttribute("aria-disabled", "true");
  });

  it("reorders from the keyboard on the drag handle (arrow keys)", async () => {
    const user = userEvent.setup();
    renderList([alice, bob, carol]);

    const handle = screen.getByRole("button", { name: "Reorder carol@example.com" });
    expect(handle).toHaveAccessibleDescription(/Position 3 of 3/);
    handle.focus();
    await user.keyboard("{ArrowUp}");
    await waitFor(() => expect(mocks.reorderAccounts).toHaveBeenCalledWith("claude", ["a1", "a3", "a2"]));
  });

  it("reorders by dragging a handle onto another row", async () => {
    const { fireEvent } = await import("@testing-library/react");
    renderList([alice, bob, carol]);

    const handle = screen.getByRole("button", { name: "Reorder carol@example.com" });
    const dataTransfer = { setData: vi.fn(), effectAllowed: "" };
    fireEvent.dragStart(handle, { dataTransfer });
    fireEvent.dragOver(rowFor("alice@example.com"), { dataTransfer });
    fireEvent.drop(rowFor("alice@example.com"), { dataTransfer });

    await waitFor(() => expect(mocks.reorderAccounts).toHaveBeenCalledWith("claude", ["a3", "a1", "a2"]));
  });

  it("removes only after the in-app confirm, and says where its chats go", async () => {
    const user = userEvent.setup();
    renderList([alice, bob]);

    await user.click(screen.getByRole("button", { name: "Actions for bob@work.example" }));
    await user.click(screen.getByRole("menuitem", { name: "Remove" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveTextContent("move to your other Claude accounts");
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(mocks.deleteAccount).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Actions for bob@work.example" }));
    await user.click(screen.getByRole("menuitem", { name: "Remove" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(mocks.deleteAccount).toHaveBeenCalledWith("a2"));
  });

  it("rolls back and reports when the server refuses", async () => {
    const user = userEvent.setup();
    mocks.updateAccount.mockRejectedValue(new ConnectError("nope", Code.Internal));
    renderList([alice]);

    await user.click(screen.getByRole("button", { name: "Actions for alice@example.com" }));
    await user.click(screen.getByRole("menuitem", { name: "Pause" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/nope/);
  });

  it("refreshes usage and says so when the server throttles it", async () => {
    const user = userEvent.setup();
    mocks.refreshUsage.mockResolvedValue({ account: null, throttled: true });
    renderList([alice]);

    await user.click(screen.getByRole("button", { name: "Details for alice@example.com" }));
    await user.click(screen.getByRole("button", { name: "Refresh usage for alice@example.com" }));
    expect(mocks.refreshUsage).toHaveBeenCalledWith("a1");
    expect(await screen.findByRole("status")).toHaveTextContent(/try again in a minute/i);
  });
});

describe("rotation and adding", () => {
  it("offers Balance (default) | In order on the card's header line and persists the choice", async () => {
    const user = userEvent.setup();
    renderList([alice, bob]);

    const select = await screen.findByRole("combobox", { name: "Rotation" });
    expect(select).toHaveValue("balance");
    await user.selectOptions(select, "priority");

    await waitFor(() => expect(mocks.upsertStringSetting).toHaveBeenCalledWith("provider.routing.claude", "priority"));
    expect(screen.getByRole("combobox", { name: "Rotation" })).toHaveValue("priority");
  });

  it("reads a saved routing mode", async () => {
    mocks.readSetting.mockResolvedValue({ status: "found", value: "priority" });
    renderList([alice, bob]);
    await waitFor(() => expect(screen.getByRole("combobox", { name: "Rotation" })).toHaveValue("priority"));
  });

  it("hides the rotation select, the grip and the move items when a provider has one account", async () => {
    renderList([alice]);
    expect(screen.queryByRole("combobox", { name: "Rotation" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Reorder / })).not.toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "Actions for alice@example.com" }));
    expect(screen.queryByRole("menuitem", { name: "Move up" })).not.toBeInTheDocument();
  });

  it("scopes the add button to its provider: 'Add another Claude account', 'Add another Anthropic key'", () => {
    renderList([alice, key]);
    const claude = within(screen.getByRole("region", { name: "Claude" }));
    const anthropic = within(screen.getByRole("region", { name: "Anthropic API" }));
    expect(claude.getByRole("button", { name: "Add another Claude account" })).toBeInTheDocument();
    expect(anthropic.getByRole("button", { name: "Add another Anthropic key" })).toBeInTheDocument();
  });

  it("gives every provider its own mark, so no two avatars collide", () => {
    renderList([
      alice,
      key,
      account({ id: "o", provider: "openrouter", authKind: "api_key", label: "o" }),
      account({ id: "g", provider: "gemini", authKind: "api_key", label: "g" }),
      account({ id: "ag", provider: "antigravity", label: "ag" }),
      account({ id: "cp", provider: "copilot", authKind: "device", label: "cp" }),
    ]);
    const marks = screen.getAllByTestId("provider-mark").map((m) => m.getAttribute("data-mark"));
    expect(new Set(marks).size).toBe(marks.length);
    expect(marks).not.toContain("fallback");
  });

  it("adds another API key through AddProviderAPIKey and reports a duplicate", async () => {
    const user = userEvent.setup();
    mocks.addApiKey.mockResolvedValue({ account: null, created: false });
    renderList([key]);

    await user.click(screen.getByRole("button", { name: "Add another Anthropic key" }));
    expect(screen.getByTestId("connect-panel")).toHaveTextContent("Extra keys are used in turn");
    await user.type(screen.getByLabelText("API key"), "sk-ant-second");
    await user.click(screen.getByRole("button", { name: "Add key" }));

    await waitFor(() => expect(mocks.addApiKey).toHaveBeenCalledWith("anthropic", "sk-ant-second", undefined));
    expect(await screen.findByText("That key is already connected.")).toBeInTheDocument();
  });

  it("groups providers in Settings order with accounts in priority order", () => {
    const groups = groupByProvider([key, bob, alice]);
    expect(groups.map((g) => g.provider)).toEqual(["claude", "anthropic"]);
    expect(groups[0].accounts.map((a) => a.id)).toEqual(["a1", "a2"]);
  });
});

describe("mobile variant", () => {
  it("renders the same list, with up/down reorder and no drag handle", async () => {
    const user = userEvent.setup();
    renderList([alice, bob], "mobile");

    expect(within(rowFor("alice@example.com")).queryByTestId("account-status")).not.toBeInTheDocument();
    expect(within(rowFor("bob@work.example")).getByTestId("account-status")).toHaveTextContent(/^Limited: /);
    expect(screen.queryByRole("button", { name: /^Reorder / })).not.toBeInTheDocument();

    const menuButton = screen.getByRole("button", { name: "Actions for bob@work.example" });
    expect(menuButton.className).toContain("min-h-[44px]");
    await user.click(menuButton);
    // Touch-sized items: the menu sizes every item it holds.
    expect(screen.getByRole("menu").className).toContain("[&_[role=menuitem]]:min-h-[44px]");
    await user.click(screen.getByRole("menuitem", { name: "Move up" }));
    await waitFor(() => expect(mocks.reorderAccounts).toHaveBeenCalledWith("claude", ["a2", "a1"]));
  });
});

describe("the list query", () => {
  function Probe() {
    const view = useProviderAccountsView();
    return <div data-testid="view">{view.status === "ready" ? `ready:${view.accounts.length}` : view.status}</div>;
  }

  beforeEach(() => {
    useAuthStore.setState({ session: { access_token: "t" } as never });
  });
  afterEach(() => {
    useAuthStore.setState({ session: null });
  });

  it("is ready once the server returns accounts", async () => {
    mocks.listAccounts.mockResolvedValue([alice, bob]);
    renderWithQuery(<Probe />);
    await waitFor(() => expect(screen.getByTestId("view")).toHaveTextContent("ready:2"));
  });

  it("falls back, quietly and once, when the server answers Unimplemented", async () => {
    mocks.listAccounts.mockRejectedValue(new ConnectError("not implemented", Code.Unimplemented));
    renderWithQuery(<Probe />);
    await waitFor(() => expect(screen.getByTestId("view")).toHaveTextContent("unavailable"));
    expect(mocks.listAccounts).toHaveBeenCalledTimes(1);
  });

  it("falls back on any other failure too, rather than showing nothing", async () => {
    mocks.listAccounts.mockRejectedValue(new ConnectError("down", Code.Unavailable));
    renderWithQuery(<Probe />);
    // One retry (1s backoff) before the error settles; today's view shows throughout.
    await waitFor(() => expect(screen.getByTestId("view")).toHaveTextContent("unavailable"), { timeout: 4000 });
  });

  it("does not call the RPC while signed out", async () => {
    useAuthStore.setState({ session: null });
    renderWithQuery(<Probe />);
    expect(screen.getByTestId("view")).toHaveTextContent("unavailable");
    expect(mocks.listAccounts).not.toHaveBeenCalled();
  });
});

describe("formatting", () => {
  it("formats durations and reset times", () => {
    expect(formatDuration(133 * 60_000)).toBe("2h 13m");
    expect(formatDuration(50 * 3_600_000)).toBe("2d 2h");
    expect(formatDuration(20_000)).toBe("<1m");
    expect(formatResetsIn(iso(45), NOW)).toBe("resets in 45m");
    expect(formatResetsIn(undefined, NOW)).toBe("");
    expect(formatResetsIn(iso(-5), NOW)).toBe("");
  });

  it("treats a window whose reset has passed as a fresh one", () => {
    const w = win({ name: "5h", usedFraction: 1, resetsAt: iso(-1), lengthSeconds: 18_000 });
    expect(effectiveWindow(w, iso(-200), NOW)).toEqual({ used: 0, resetsAt: undefined, rolledOver: true });
  });

  it("treats a window observed longer ago than its length, with no reset, as unknown", () => {
    const w = win({ name: "5h", usedFraction: 0.5, lengthSeconds: 18_000 });
    expect(effectiveWindow(w, iso(-400), NOW).used).toBeNull();
    expect(effectiveWindow(w, iso(-10), NOW).used).toBe(0.5);
  });

  it("derives tone from the account and the window", () => {
    const w = win({ name: "5h" });
    expect(usageTone(null, w, alice)).toBe("ok");
    expect(usageTone(0.8, w, alice)).toBe("warn");
    expect(usageTone(1, w, bob)).toBe("danger");
  });

  it("falls back to a readable plan from the normalized id", () => {
    expect(planLabel({ plan: "max_20x" })).toBe("Max 20×");
    expect(planLabel({ plan: "plus" })).toBe("Plus");
    expect(planLabel({})).toBe("");
  });
});
