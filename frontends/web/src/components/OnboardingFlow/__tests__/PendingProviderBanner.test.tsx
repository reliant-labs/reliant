import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  readPendingProvider: vi.fn(),
  clearPendingProvider: vi.fn(),
  statuses: { data: { providers: [] as { configured: boolean }[] } } as { data?: { providers: { configured: boolean }[] } },
  openModal: vi.fn(),
}));

vi.mock("@/lib/pendingProvider", () => ({
  readPendingProvider: () => mocks.readPendingProvider(),
  clearPendingProvider: () => mocks.clearPendingProvider(),
}));
vi.mock("@/hooks/settings-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/settings-queries")>()),
  useProviderStatuses: () => mocks.statuses,
}));
vi.mock("@/store/modalStore", () => ({
  useModalStore: (select: (s: { openModal: typeof mocks.openModal }) => unknown) => select({ openModal: mocks.openModal }),
}));
vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));

import { PendingProviderBanner } from "../PendingProviderBanner";

beforeEach(() => {
  vi.clearAllMocks();
  mocks.clearPendingProvider.mockResolvedValue(undefined);
  mocks.readPendingProvider.mockResolvedValue("claude");
  mocks.statuses = { data: { providers: [] } };
});

describe("PendingProviderBanner — finish the sign-in the user chose in onboarding", () => {
  it("offers to connect the subscription they picked", async () => {
    render(<PendingProviderBanner />);

    expect(await screen.findByText(/connect your claude subscription/i)).toBeInTheDocument();
    expect(screen.getByText(/desktop app or cli/i)).toBeInTheDocument();
  });

  it("opens the sign-in dialog on that provider", async () => {
    render(<PendingProviderBanner />);
    fireEvent.click(await screen.findByRole("button", { name: /^connect claude subscription$/i }));

    expect(mocks.openModal).toHaveBeenCalledWith("api-key-setup", { provider: "claude" });
  });

  it("'Use an API key instead' opens the dialog on the Anthropic key", async () => {
    render(<PendingProviderBanner />);
    fireEvent.click(await screen.findByRole("button", { name: /use an api key instead/i }));

    expect(mocks.openModal).toHaveBeenCalledWith("api-key-setup", { provider: "anthropic" });
  });

  it("shows nothing when no provider was deferred", async () => {
    mocks.readPendingProvider.mockResolvedValue(null);
    render(<PendingProviderBanner />);

    await waitFor(() => expect(mocks.readPendingProvider).toHaveBeenCalled());
    expect(screen.queryByTestId("pending-provider-banner")).toBeNull();
  });

  it("goes away, and forgets the choice, once any provider is configured", async () => {
    mocks.statuses = { data: { providers: [{ configured: true }] } };
    render(<PendingProviderBanner />);

    await waitFor(() => expect(mocks.clearPendingProvider).toHaveBeenCalled());
    expect(screen.queryByTestId("pending-provider-banner")).toBeNull();
  });

  it("can be dismissed, and then stays dismissed", async () => {
    render(<PendingProviderBanner />);
    fireEvent.click(await screen.findByRole("button", { name: /dismiss/i }));

    expect(mocks.clearPendingProvider).toHaveBeenCalledTimes(1);
    expect(screen.queryByTestId("pending-provider-banner")).toBeNull();
  });
});
