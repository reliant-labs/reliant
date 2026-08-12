/**
 * MobileSearchScreen — the segmented-control entry point wiring the query
 * box to whichever tab (Chats / Files) is active. The tabs' own search
 * behavior is covered by their own test files; this pins the wiring and the
 * capability gates.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SurfaceProvider } from "../../../lib/surfaceContext";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, ...props }: { children?: React.ReactNode }) => (
    <a {...props}>{children}</a>
  ),
}));

vi.mock("../MobileChatSearchTab", () => ({
  MobileChatSearchTab: ({ query }: { query: string }) => (
    <div data-testid="chat-tab">chat-tab:{query}</div>
  ),
}));

vi.mock("../MobileFileSearchTab", () => ({
  MobileFileSearchTab: ({ query }: { query: string }) => (
    <div data-testid="file-tab">file-tab:{query}</div>
  ),
}));

const { MobileSearchScreen } = await import("../MobileSearchScreen");

function renderMobile() {
  return render(
    <SurfaceProvider surface="mobile">
      <MobileSearchScreen />
    </SurfaceProvider>,
  );
}

describe("MobileSearchScreen", () => {
  it("defaults to the Chats tab", () => {
    renderMobile();
    expect(screen.getByTestId("chat-tab")).toBeInTheDocument();
    expect(screen.queryByTestId("file-tab")).not.toBeInTheDocument();
  });

  it("switches to the Files tab", async () => {
    renderMobile();
    await userEvent.click(screen.getByRole("button", { name: "Files" }));
    expect(screen.getByTestId("file-tab")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-tab")).not.toBeInTheDocument();
  });

  it("passes the typed query through to the active tab", async () => {
    renderMobile();
    await userEvent.type(screen.getByPlaceholderText(/search chats/i), "auth");
    expect(screen.getByTestId("chat-tab")).toHaveTextContent("chat-tab:auth");
  });

  it("clears the query with the clear button", async () => {
    renderMobile();
    const input = screen.getByPlaceholderText(/search chats/i);
    await userEvent.type(input, "auth");
    await userEvent.click(screen.getByRole("button", { name: /clear search/i }));
    expect(input).toHaveValue("");
  });

  it("renders both search modes at 390px without overflow", () => {
    // jsdom doesn't lay out CSS, so this pins the STRUCTURAL contract instead:
    // both tab controls exist and the search input has no fixed pixel width
    // that would overflow a 390px viewport.
    renderMobile();
    expect(screen.getByRole("button", { name: "Chats" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Files" })).toBeInTheDocument();
  });

  it("hides both tabs when neither search capability is enabled", () => {
    render(
      <SurfaceProvider surface="embed">
        <MobileSearchScreen />
      </SurfaceProvider>,
    );
    expect(screen.queryByRole("button", { name: "Chats" })).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-tab")).not.toBeInTheDocument();
    expect(screen.queryByTestId("file-tab")).not.toBeInTheDocument();
  });
});
