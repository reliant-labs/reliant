import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn(), useSearch: () => ({ start: "chat" }) }));
vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("../../OnboardingFlow/PendingProviderBanner", () => ({
  PendingProviderBanner: () => <div data-testid="pending-provider-banner" />,
}));
vi.mock("../../Chat/MainNewChatView", () => ({
  MainNewChatView: (props: { heading?: React.ReactNode }) => <div>{props.heading}</div>,
}));

import { HomeNewChat } from "../HomeNewChat";

describe("HomeNewChat — finishing a deferred sign-in", () => {
  it("shows the pending-provider prompt on the first chat, above the starter heading", () => {
    render(<HomeNewChat />);

    const banner = screen.getByTestId("pending-provider-banner");
    const heading = screen.getByTestId("home-build-heading");
    expect(banner.compareDocumentPosition(heading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});
