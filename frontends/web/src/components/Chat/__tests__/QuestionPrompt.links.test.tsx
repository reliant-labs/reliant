/**
 * URLs in an ask_user question are links: in the question text and in each
 * option's label and description. A link opens through openLink (the system
 * browser in Electron) and never selects the option it sits in.
 */
import { render, screen, fireEvent } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { QuestionPrompt, type QuestionItem } from "../QuestionPrompt";

const openLink = vi.fn();
vi.mock("../../../lib/open-link", () => ({
  openLink: (url: string, worktreeId?: string) => openLink(url, worktreeId),
}));

const question: QuestionItem = {
  question: "Merge https://github.com/reliant-labs/reliant/pull/1 now?",
  options: [
    { label: "Yes", description: "Its checks: https://example.com/checks." },
    { label: "Docs at https://example.com/docs", description: "" },
  ],
  allowMultiple: false,
};

beforeEach(() => openLink.mockClear());

describe("QuestionPrompt links", () => {
  it("links the URLs in the question, option labels and descriptions", () => {
    render(<QuestionPrompt questions={[question]} onSubmit={vi.fn()} />);
    const hrefs = screen.getAllByRole("link").map((a) => a.getAttribute("href"));
    expect(hrefs).toEqual([
      "https://github.com/reliant-labs/reliant/pull/1",
      "https://example.com/checks",
      "https://example.com/docs",
    ]);
    for (const link of screen.getAllByRole("link")) {
      expect(link).toHaveAttribute("target", "_blank");
      expect(link).toHaveAttribute("rel", "noopener noreferrer");
      // MarkdownRenderer's link styling.
      expect(link).toHaveClass("text-primary-ink", "underline");
    }
    // The text around a link is still there.
    expect(screen.getByText(/Its checks:/)).toHaveTextContent("Its checks: https://example.com/checks.");
  });

  it("opens a link through openLink without selecting its option", async () => {
    const user = userEvent.setup();
    render(<QuestionPrompt questions={[question]} onSubmit={vi.fn()} />);
    const [yes] = screen.getAllByRole("radio");

    await user.click(screen.getByRole("link", { name: "https://example.com/checks" }));

    expect(openLink).toHaveBeenCalledWith("https://example.com/checks", undefined);
    expect(yes).toHaveAttribute("aria-checked", "false");
    expect(screen.getByRole("button", { name: /submit/i })).toBeDisabled();
  });

  it("still selects an option by click, Enter and Space", async () => {
    const user = userEvent.setup();
    render(<QuestionPrompt questions={[question]} onSubmit={vi.fn()} />);
    const [yes, docs] = screen.getAllByRole("radio");

    await user.click(yes!);
    expect(yes).toHaveAttribute("aria-checked", "true");

    docs!.focus();
    await user.keyboard("{Enter}");
    expect(docs).toHaveAttribute("aria-checked", "true");
    expect(yes).toHaveAttribute("aria-checked", "false");

    yes!.focus();
    await user.keyboard(" ");
    expect(yes).toHaveAttribute("aria-checked", "true");
  });

  it("does not select an option on Enter in a link inside it", () => {
    render(<QuestionPrompt questions={[question]} onSubmit={vi.fn()} />);
    const docs = screen.getAllByRole("radio")[1]!;
    fireEvent.keyDown(screen.getByRole("link", { name: "https://example.com/docs" }), { key: "Enter" });
    expect(docs).toHaveAttribute("aria-checked", "false");
  });
});
