/**
 * QuestionPrompt where it is answered on a phone: inside the transcript
 * (AskUserCard) and the Inbox, both of which already scroll. Inline, the form
 * is its natural height — a 40vh scroller nested in a scrolling page traps a
 * finger's scroll and hides Submit below the inner fold. Above the composer
 * it keeps its own scroller. Every control is touch-sized on a coarse pointer
 * (the `touch:` variant; e2e/mobile.spec.ts measures the pixels).
 */

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { QuestionPrompt } from "../QuestionPrompt";

const questions = [
  {
    question: "Which database?",
    options: [
      { label: "Postgres", description: "" },
      { label: "SQLite", description: "" },
    ],
    allowMultiple: false,
  },
  { question: "Which region?", options: [], allowMultiple: false },
];

function scroller(container: HTMLElement) {
  return container.firstElementChild as HTMLElement;
}

describe("QuestionPrompt sizing", () => {
  it("scrolls inside itself above the composer", () => {
    const { container } = render(<QuestionPrompt questions={questions} onSubmit={vi.fn()} />);
    expect(scroller(container)).toHaveClass("max-h-[40vh]", "overflow-y-auto");
  });

  it("takes its natural height inline, in a page that already scrolls", () => {
    const { container } = render(<QuestionPrompt inline questions={questions} onSubmit={vi.fn()} />);
    expect(scroller(container)).not.toHaveClass("max-h-[40vh]");
    expect(scroller(container)).not.toHaveClass("overflow-y-auto");
  });

  it("sizes options, the answer box and Next for a finger", () => {
    render(<QuestionPrompt inline questions={questions} onSubmit={vi.fn()} />);
    for (const option of screen.getAllByRole("radio")) expect(option).toHaveClass("touch:min-h-[44px]");
    expect(screen.getByPlaceholderText("Or type your own answer...")).toHaveClass("touch:min-h-[44px]");
    expect(screen.getByRole("button", { name: /Next/ })).toHaveClass("touch:min-h-[44px]");
  });
});
