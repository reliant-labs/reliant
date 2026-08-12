/**
 * MobileWorkspacePreferencesScreen renders MobileWorkspacePreferencesPanel
 * (mobile-native) behind the shared section header.
 */

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

vi.mock("../../../api/client", () => ({
  api: {
    settings: {
      getPreferences: vi.fn().mockResolvedValue({}),
      updatePreferences: vi.fn(),
    },
  },
}));

const { MobileWorkspacePreferencesScreen } = await import(
  "../MobileWorkspacePreferencesScreen"
);

function renderScreen(onBack = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MobileWorkspacePreferencesScreen onBack={onBack} />
    </QueryClientProvider>,
  );
}

describe("MobileWorkspacePreferencesScreen", () => {
  it("renders the mobile-native workspace preferences panel content", async () => {
    renderScreen();
    expect(await screen.findByText(/ask every time/i)).toBeInTheDocument();
    expect(screen.getByText(/^clean up$/i)).toBeInTheDocument();
  });

  it("calls onBack from the header", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    const onBack = vi.fn();
    renderScreen(onBack);
    await userEvent.setup().click(
      screen.getByRole("button", { name: /back to settings/i }),
    );
    expect(onBack).toHaveBeenCalled();
  });
});
