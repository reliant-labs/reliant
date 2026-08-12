import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { MarkdownRenderer } from "../MarkdownRenderer";

const openLink = vi.fn();
vi.mock("../../../lib/open-link", () => ({
  openLink: (url: string, worktreeId?: string) => openLink(url, worktreeId),
}));

// FileLink pulls in the file-opener stack; stand it in with a marker so these
// tests stay about link routing.
vi.mock("../FileLink", () => ({
  FileLink: ({ path }: { path: string }) => (
    <span data-testid="file-link">{path}</span>
  ),
}));

beforeEach(() => {
  openLink.mockClear();
});

describe("MarkdownRenderer links", () => {
  it("autolinks a bare URL via remark-gfm", () => {
    render(<MarkdownRenderer content="see https://example.com/foo for details" />);
    expect(screen.getByRole("link")).toHaveAttribute(
      "href",
      "https://example.com/foo",
    );
  });

  it("colors a bare autolink with the link color", () => {
    render(<MarkdownRenderer content="see https://example.com/foo for details" />);
    expect(screen.getByRole("link").className).toContain("text-info");
  });

  it("links a URL wrapped in backticks", () => {
    render(<MarkdownRenderer content="open `https://example.com/foo` please" />);
    expect(screen.getByRole("link")).toHaveAttribute(
      "href",
      "https://example.com/foo",
    );
  });

  it("keeps a backticked URL styled as code, not as a link", () => {
    render(<MarkdownRenderer content="open `https://example.com/foo` please" />);
    const link = screen.getByRole("link");
    expect(link.className).toContain("text-foreground");
    expect(link.className).not.toContain("text-info");
    // Still discoverable as clickable on hover.
    expect(link.className).toContain("hover:underline");
    expect(link.className).toContain("cursor-pointer");
  });

  it("routes a backticked URL through openLink instead of navigating", async () => {
    const user = userEvent.setup();
    render(
      <MarkdownRenderer
        content="open `https://example.com/foo`"
        worktreeId="worktree-1"
      />,
    );

    await user.click(screen.getByRole("link"));

    expect(openLink).toHaveBeenCalledWith("https://example.com/foo", "worktree-1");
  });

  it("leaves a backticked non-URL as plain code", () => {
    const { container } = render(
      <MarkdownRenderer content="run `npm install` first" />,
    );
    expect(container.querySelector("a")).toBeNull();
    expect(container.querySelector("code")?.textContent).toBe("npm install");
  });

  it("leaves a backticked scheme-less host as plain code", () => {
    const { container } = render(
      <MarkdownRenderer content="server on `localhost:3000` now" />,
    );
    expect(container.querySelector("a")).toBeNull();
  });

  it("still renders a backticked file path as a file link", () => {
    render(<MarkdownRenderer content="edit `./src/app.tsx` now" />);
    expect(screen.getByTestId("file-link")).toBeInTheDocument();
  });

  it("does not link URLs inside a fenced code block", () => {
    const { container } = render(
      <MarkdownRenderer content={"```\ncurl https://example.com/api\n```"} />,
    );
    expect(container.querySelector("a")).toBeNull();
  });
});
