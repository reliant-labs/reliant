// Copyright (c) 2026 Reliant Labs

/**
 * Settings → Notifications → Inbox: the two Inbox filters, stored with the
 * person's preferences (UpdatePreferences), off by default. A change marks
 * every Inbox read stale, since the filters shape what ListInbox returns.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";

import { GetPreferencesResponseSchema, UpdatePreferencesResponseSchema } from "@/gen/reliant/v1/settings_pb";
import { ListInboxResponseSchema } from "@/gen/reliant/v1/inbox_pb";
import { inboxServiceKeys } from "@/hooks/inbox-service-hooks_gen";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";

const mocks = vi.hoisted(() => ({ getPreferences: vi.fn(), updatePreferences: vi.fn(), toastError: vi.fn() }));

const authState: { session: unknown } = { session: { access_token: "token" } };
vi.mock("@/store/authStore", () => {
  const useAuthStore = <T,>(selector: (s: { session: unknown }) => T): T => selector(authState);
  useAuthStore.getState = () => authState;
  return { useAuthStore };
});
vi.mock("@/lib/toast-manager", () => ({ toast: { notify: vi.fn(), error: mocks.toastError } }));

import { InboxFilterSettings } from "../NotificationSettings";

function renderSettings() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  // An Inbox read already in the cache, to see the change mark it stale.
  queryClient.setQueryData(inboxServiceKeys.listInbox({}), create(ListInboxResponseSchema, {}));
  render(
    <QueryClientProvider client={queryClient}>
      <InboxFilterSettings />
    </QueryClientProvider>,
  );
  return queryClient;
}

beforeEach(() => {
  for (const mock of Object.values(mocks)) mock.mockReset();
  mocks.getPreferences.mockResolvedValue(create(GetPreferencesResponseSchema, { inboxShowHousekeeping: false }));
  mocks.updatePreferences.mockResolvedValue(create(UpdatePreferencesResponseSchema, {}));
  setTransport(
    mockTransport({
      "SettingsService.GetPreferences": () => mocks.getPreferences(),
      "SettingsService.UpdatePreferences": (req) => mocks.updatePreferences(req),
    }),
  );
});

afterEach(() => setTransport(null));

describe("Settings → Notifications → Inbox", () => {
  it("both filters are off by default", async () => {
    renderSettings();
    await waitFor(() => expect(mocks.getPreferences).toHaveBeenCalled());
    expect(screen.getByRole("switch", { name: "Show machine housekeeping in the Inbox" })).toHaveAttribute("aria-checked", "false");
    expect(screen.getByRole("switch", { name: "Show every automation failure" })).toHaveAttribute("aria-checked", "false");
    expect(screen.getByText(/on each sandbox.s page/)).toBeInTheDocument();
  });

  it("shows what is stored", async () => {
    mocks.getPreferences.mockResolvedValue(
      create(GetPreferencesResponseSchema, { inboxShowHousekeeping: true, inboxShowEveryAutomationFailure: true }),
    );
    renderSettings();
    await waitFor(() =>
      expect(screen.getByRole("switch", { name: "Show machine housekeeping in the Inbox" })).toHaveAttribute("aria-checked", "true"),
    );
    expect(screen.getByRole("switch", { name: "Show every automation failure" })).toHaveAttribute("aria-checked", "true");
  });

  it("turning housekeeping on stores only that filter and marks the Inbox stale", async () => {
    const queryClient = renderSettings();
    await waitFor(() => expect(mocks.getPreferences).toHaveBeenCalled());
    await userEvent.click(screen.getByRole("switch", { name: "Show machine housekeeping in the Inbox" }));

    await waitFor(() => expect(mocks.updatePreferences).toHaveBeenCalledTimes(1));
    const req = mocks.updatePreferences.mock.calls[0]![0];
    expect(req.inboxShowHousekeeping).toBe(true);
    expect(req.inboxShowEveryAutomationFailure).toBeUndefined();
    await waitFor(() => expect(queryClient.getQueryState(inboxServiceKeys.listInbox({}))?.isInvalidated).toBe(true));
  });

  it("turning every automation failure on stores that filter", async () => {
    renderSettings();
    await waitFor(() => expect(mocks.getPreferences).toHaveBeenCalled());
    await userEvent.click(screen.getByRole("switch", { name: "Show every automation failure" }));
    await waitFor(() => expect(mocks.updatePreferences).toHaveBeenCalledTimes(1));
    const req = mocks.updatePreferences.mock.calls[0]![0];
    expect(req.inboxShowEveryAutomationFailure).toBe(true);
    expect(req.inboxShowHousekeeping).toBeUndefined();
  });

  it("a refused write puts the switch back and says why", async () => {
    mocks.updatePreferences.mockRejectedValue(new Error("nope"));
    renderSettings();
    const toggle = screen.getByRole("switch", { name: "Show machine housekeeping in the Inbox" });
    await waitFor(() => expect(mocks.getPreferences).toHaveBeenCalled());
    await userEvent.click(toggle);
    await waitFor(() => expect(mocks.toastError).toHaveBeenCalled());
    expect(toggle).toHaveAttribute("aria-checked", "false");
  });
});
