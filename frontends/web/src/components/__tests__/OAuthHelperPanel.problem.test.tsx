/**
 * A panel that cannot work says WHY and offers a way out.
 *
 * The helper runs on the user's own computer. For a browser user with no
 * machine on their account the old panel showed install steps and a Retry
 * button — and Retry could never succeed — while the app asked the server every
 * two seconds and was refused. Each reason now has its own sentence, a stopped
 * poll says so, and an alternative that needs no helper is offered inline.
 */
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { OAuthHelperPanel } from "../OAuthHelperPanel";

const base = {
  providerName: "Claude subscription",
  available: false,
  loading: false,
  connecting: false,
  onRetry: vi.fn(),
  onConnect: vi.fn(),
};

describe("OAuthHelperPanel — why it is unavailable", () => {
  it("no machine on the account: says the desktop app or CLI is needed", () => {
    render(<OAuthHelperPanel {...base} problem="no_machine" />);

    expect(screen.getByTestId("oauth-helper-problem")).toHaveTextContent(
      /no machine is connected to your account yet.*desktop app or CLI on your computer/i,
    );
  });

  it("machine offline: says to check that it is still running", () => {
    render(<OAuthHelperPanel {...base} problem="machine_offline" />);

    expect(screen.getByTestId("oauth-helper-problem")).toHaveTextContent(/not answering/i);
  });

  it("machine elsewhere: says the helper must run on THIS computer", () => {
    render(<OAuthHelperPanel {...base} problem="not_colocated" />);

    expect(screen.getByTestId("oauth-helper-problem")).toHaveTextContent(/not this computer/i);
  });

  it("when polling stopped, says so and points at Retry — which still works", () => {
    const onRetry = vi.fn();
    render(<OAuthHelperPanel {...base} onRetry={onRetry} problem="no_machine" stopped />);

    expect(screen.getByTestId("oauth-helper-problem")).toHaveTextContent(/stopped checking automatically/i);
    fireEvent.click(screen.getByRole("button", { name: /retry/i }));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("offers the alternative inline, so a user with no computer to run it on is not stuck", () => {
    const onClick = vi.fn();
    render(
      <OAuthHelperPanel {...base} problem="no_machine" alternative={{ label: "Use Reliant credit instead", onClick }} />,
    );

    fireEvent.click(screen.getByRole("button", { name: /use reliant credit instead/i }));
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("says nothing extra before the first answer, while loading, or once available", () => {
    const { rerender } = render(<OAuthHelperPanel {...base} />);
    expect(screen.queryByTestId("oauth-helper-problem")).toBeNull();

    rerender(<OAuthHelperPanel {...base} loading problem="no_machine" />);
    expect(screen.queryByTestId("oauth-helper-problem")).toBeNull();

    rerender(<OAuthHelperPanel {...base} available problem={null} />);
    expect(screen.queryByTestId("oauth-helper-problem")).toBeNull();
  });
});
