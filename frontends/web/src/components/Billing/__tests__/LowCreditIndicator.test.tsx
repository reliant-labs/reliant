/**
 * The ambient indicator: what it says, and — more importantly — when it says
 * nothing at all.
 *
 * The silence cases are the ones worth pinning. An indicator that renders for a
 * healthy user, or for a user on their own API key who has no wallet, is not a
 * cosmetic bug: it is a permanent piece of furniture that trains people to
 * ignore the spot where the real warning appears.
 */
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const mockNavigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => mockNavigate,
}));

vi.mock("@/components/ui/Tooltip", () => ({
  Tooltip: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));

/** The status the hook reports, set per test. */
let mockStatus: {
  status: { urgency: "healthy" | "low" | "empty"; runwayDays: number | null };
  formattedBalance: string | null;
  available: boolean;
  dailySpendUsd: number | null;
};

vi.mock("../useLowCreditStatus", () => ({
  useLowCreditStatus: () => mockStatus,
}));

import { LowCreditIndicator } from "../LowCreditIndicator";

describe("the indicator is invisible to anyone who does not need it", () => {
  it("renders nothing for a healthy balance", () => {
    mockStatus = {
      status: { urgency: "healthy", runwayDays: 40 },
      formattedBalance: "$40.00",
      available: true,
      dailySpendUsd: 1,
    };
    const { container } = render(<LowCreditIndicator />);
    expect(container).toBeEmptyDOMElement();
  });

  /**
   * A user on their own API key has no Reliant balance at all. Telling them
   * their credit is low would be both wrong and alarming — and there is no
   * action they could take in response.
   */
  it("renders nothing for a user not spending Reliant credit", () => {
    mockStatus = {
      // Even with an empty wallet: no entitlement means this is not their
      // funding source and none of it applies to them.
      status: { urgency: "empty", runwayDays: null },
      formattedBalance: "$0.00",
      available: false,
      dailySpendUsd: null,
    };
    const { container } = render(<LowCreditIndicator />);
    expect(container).toBeEmptyDOMElement();
  });
});

describe("the indicator states a real quantity and offers a way out", () => {
  it("shows the days remaining, not the word 'low'", () => {
    mockStatus = {
      status: { urgency: "low", runwayDays: 3 },
      formattedBalance: "$6.00",
      available: true,
      dailySpendUsd: 2,
    };
    render(<LowCreditIndicator />);

    const indicator = screen.getByTestId("low-credit-indicator");
    expect(indicator).toHaveTextContent("~3 days of credit left");
  });

  it("falls back to the balance when the runway is unknowable", () => {
    mockStatus = {
      status: { urgency: "low", runwayDays: null },
      formattedBalance: "$1.20",
      available: true,
      dailySpendUsd: null,
    };
    render(<LowCreditIndicator />);
    expect(screen.getByTestId("low-credit-indicator")).toHaveTextContent(
      "$1.20 credit left",
    );
  });

  it("says plainly when the balance is gone", () => {
    mockStatus = {
      status: { urgency: "empty", runwayDays: null },
      formattedBalance: "$0.00",
      available: true,
      dailySpendUsd: 5,
    };
    render(<LowCreditIndicator />);
    expect(screen.getByTestId("low-credit-indicator")).toHaveTextContent(
      "Out of credit",
    );
  });

  /**
   * Click-through to where the problem can actually be fixed. An indicator that
   * reports a problem and offers no route to solving it is a nag.
   */
  it("navigates to billing when clicked", async () => {
    mockStatus = {
      status: { urgency: "low", runwayDays: 2 },
      formattedBalance: "$4.00",
      available: true,
      dailySpendUsd: 2,
    };
    render(<LowCreditIndicator />);

    screen.getByTestId("low-credit-indicator").click();
    expect(mockNavigate).toHaveBeenCalledWith({
      to: "/settings/$section",
      params: { section: "billing" },
    });
  });
});
