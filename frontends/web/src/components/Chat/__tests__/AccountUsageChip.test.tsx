/**
 * The composer's account chip: absent until the chat's account is at 80% or
 * limited; "Limited · next account" when a sibling can take over, "Limited
 * until" when none can; hover opens the usage bars; the mobile surface gets the
 * compact chip.
 */

import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ProviderAccount, UsageWindow } from "../../../api/provider-account-grpc";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { useAuthStore } from "../../../store/authStore";

const mocks = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  getChatAccount: vi.fn(),
}));

vi.mock("../../../api/provider-account-grpc", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../../api/provider-account-grpc")>();
  return {
    ...actual,
    providerAccountGrpc: { listAccounts: mocks.listAccounts, getChatAccount: mocks.getChatAccount },
  };
});
vi.mock("../../../lib/settingsPersistence", () => ({ readSetting: vi.fn(), upsertStringSetting: vi.fn() }));

import { AccountUsageChip } from "../AccountUsageChip";

const iso = (min: number) => new Date(Date.now() + min * 60_000).toISOString();
const win = (name: string, label: string, used: number, resetMin: number): UsageWindow => ({
  name,
  label,
  usedFraction: used,
  resetsAt: iso(resetMin),
  lengthSeconds: name === "5h" ? 18_000 : 604_800,
});
function acct(p: Partial<ProviderAccount> & { id: string }): ProviderAccount {
  return {
    provider: "claude",
    providerDisplayName: "Claude subscription",
    authKind: "oauth",
    label: `${p.id}@acme.com`,
    email: `${p.id}@acme.com`,
    priority: 0,
    status: "available",
    windows: [],
    usageSource: "headers",
    createdAt: "2026-10-01T00:00:00Z",
    usageObservedAt: iso(-1),
    ...p,
  };
}

function setup(active: ProviderAccount | null, all: ProviderAccount[], surface: "desktop" | "mobile" = "desktop") {
  mocks.listAccounts.mockResolvedValue(all);
  mocks.getChatAccount.mockResolvedValue(active);
  return renderWithQuery(
    <SurfaceProvider surface={surface}>
      <AccountUsageChip chatId="chat-1" />
    </SurfaceProvider>,
  );
}

beforeEach(() => {
  mocks.listAccounts.mockReset();
  mocks.getChatAccount.mockReset();
  useAuthStore.setState({ session: { access_token: "t" } as never });
});
afterEach(() => useAuthStore.setState({ session: null }));

describe("AccountUsageChip", () => {
  it("is absent below 80%", async () => {
    const a = acct({ id: "alice", windows: [win("5h", "5-hour", 0.5, 120)] });
    setup(a, [a]);
    await waitFor(() => expect(mocks.getChatAccount).toHaveBeenCalled());
    await act(async () => undefined);
    expect(screen.queryByTestId("account-usage-chip")).toBeNull();
  });

  it("is absent before the chat has made a model call", async () => {
    const a = acct({ id: "alice", windows: [win("5h", "5-hour", 0.95, 120)] });
    setup(null, [a]);
    await waitFor(() => expect(mocks.getChatAccount).toHaveBeenCalled());
    await act(async () => undefined);
    expect(screen.queryByTestId("account-usage-chip")).toBeNull();
  });

  it("asks nothing of a user with no accounts", async () => {
    setup(null, []);
    await waitFor(() => expect(mocks.listAccounts).toHaveBeenCalled());
    await act(async () => undefined);
    expect(mocks.getChatAccount).not.toHaveBeenCalled();
    expect(screen.queryByTestId("account-usage-chip")).toBeNull();
  });

  it("shows percent, window and account from 80%", async () => {
    const a = acct({ id: "alice", windows: [win("5h", "5-hour", 0.82, 120), win("7d", "Weekly", 0.4, 5000)] });
    setup(a, [a]);
    const chip = await screen.findByTestId("account-usage-chip");
    expect(chip).toHaveTextContent("82% of 5h · alice@");
    expect(chip).toHaveAttribute("data-state", "usage");
  });

  it("says 'Limited · next account' when a sibling can serve", async () => {
    const a = acct({ id: "alice", status: "limited", limitedUntil: iso(100), limitedWindow: "5h" });
    const b = acct({ id: "bob", priority: 1 });
    setup(a, [a, b]);
    const chip = await screen.findByTestId("account-usage-chip");
    expect(chip).toHaveTextContent("Limited · next account bob@");
    expect(chip).toHaveAttribute("data-state", "limited_next");
  });

  it("says 'Limited until' when no other account can serve", async () => {
    const a = acct({ id: "alice", status: "limited", limitedUntil: iso(100), limitedWindow: "5h" });
    setup(a, [a]);
    const chip = await screen.findByTestId("account-usage-chip");
    expect(chip.textContent).toMatch(/^Limited until /);
    expect(chip).toHaveAttribute("data-state", "limited_until");
  });

  it("hover opens the usage bars and leaving closes them", async () => {
    const a = acct({
      id: "alice",
      windows: [win("5h", "5-hour", 0.82, 120), win("7d", "Weekly", 0.4, 5000)],
    });
    setup(a, [a]);
    const chip = await screen.findByTestId("account-usage-chip");
    expect(screen.queryByTestId("account-usage-popover")).toBeNull();

    fireEvent.mouseEnter(chip.parentElement!);
    const popover = await screen.findByTestId("account-usage-popover");
    expect(popover).toHaveTextContent("alice@acme.com");
    expect(screen.getAllByRole("progressbar")).toHaveLength(2);
    expect(popover).toHaveTextContent("82% used");

    fireEvent.mouseLeave(chip.parentElement!);
    expect(screen.queryByTestId("account-usage-popover")).toBeNull();
  });

  it("renders compact on the mobile surface and opens on tap", async () => {
    const a = acct({ id: "alice", windows: [win("5h", "5-hour", 0.82, 120)] });
    setup(a, [a], "mobile");
    const chip = await screen.findByTestId("account-usage-chip");
    expect(chip).toHaveTextContent("82% of 5h");
    expect(chip).not.toHaveTextContent("alice@");

    fireEvent.click(chip);
    expect(await screen.findByTestId("account-usage-popover")).toHaveTextContent("82% used");
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByTestId("account-usage-popover")).toBeNull();
  });
});
