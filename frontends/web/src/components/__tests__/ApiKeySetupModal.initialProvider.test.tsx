import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";

vi.mock("../../hooks/useOAuthAvailability", () => ({
  useOAuthAvailability: () => ({ available: true, loading: false, recheck: vi.fn(), problem: null, stopped: false }),
}));
vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));

import { ApiKeySetupModal } from "../ApiKeySetupModal";

const renderModal = (initialProvider?: "claude" | "anthropic") =>
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ApiKeySetupModal isOpen onClose={vi.fn()} initialProvider={initialProvider} />
    </QueryClientProvider>,
  );

describe("ApiKeySetupModal — opens on the provider it was asked to", () => {
  it("defaults to the Claude subscription", () => {
    renderModal();
    expect(screen.getByRole("button", { name: /login with claude subscription/i })).toBeInTheDocument();
  });

  it("opens on the API key when the caller says so (the pending-provider banner's alternative)", () => {
    renderModal("anthropic");
    expect(screen.getByPlaceholderText("sk-ant-...")).toBeInTheDocument();
  });
});
