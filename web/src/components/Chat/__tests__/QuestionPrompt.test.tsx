import { describe, it, expect, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QuestionPrompt, type QuestionItem, type QuestionAnswer } from "../QuestionPrompt";

// Mock MarkdownRenderer since it may have complex dependencies
vi.mock("../MarkdownRenderer", () => ({
  MarkdownRenderer: ({ content }: { content: string }) => (
    <div data-testid="markdown-preview">{content}</div>
  ),
}));

function makeQuestion(overrides: Partial<QuestionItem> = {}): QuestionItem {
  return {
    question: "Which approach?",
    options: [
      { label: "Option A", description: "First option" },
      { label: "Option B", description: "Second option" },
    ],
    allowMultiple: false,
    ...overrides,
  };
}

describe("QuestionPrompt", () => {
  describe("single question, single select", () => {
    it("renders the question text, options, and freetext input", () => {
      const onSubmit = vi.fn();
      render(
        <QuestionPrompt
          questions={[makeQuestion()]}
          onSubmit={onSubmit}
        />
      );

      expect(screen.getByText("Which approach?")).toBeInTheDocument();
      expect(screen.getByText("Option A")).toBeInTheDocument();
      expect(screen.getByText("Option B")).toBeInTheDocument();
      expect(screen.getByText("First option")).toBeInTheDocument();
      expect(screen.getByPlaceholderText("Or type your own answer...")).toBeInTheDocument();
    });

    it("submit button is disabled until an option is selected", () => {
      const onSubmit = vi.fn();
      render(
        <QuestionPrompt
          questions={[makeQuestion()]}
          onSubmit={onSubmit}
        />
      );

      const submitButton = screen.getByRole("button", { name: /submit/i });
      expect(submitButton).toBeDisabled();
    });

    it("selecting an option enables submit", async () => {
      const user = userEvent.setup();
      const onSubmit = vi.fn();
      render(
        <QuestionPrompt
          questions={[makeQuestion()]}
          onSubmit={onSubmit}
        />
      );

      await user.click(screen.getByText("Option A"));
      const submitButton = screen.getByRole("button", { name: /submit/i });
      expect(submitButton).toBeEnabled();
    });

    it("submits the selected answer", async () => {
      const user = userEvent.setup();
      const onSubmit = vi.fn();
      render(
        <QuestionPrompt
          questions={[makeQuestion()]}
          onSubmit={onSubmit}
        />
      );

      await user.click(screen.getByText("Option B"));
      await user.click(screen.getByRole("button", { name: /submit/i }));

      expect(onSubmit).toHaveBeenCalledOnce();
      const call = onSubmit.mock.calls[0][0] as { answers: QuestionAnswer[] };
      expect(call.answers).toHaveLength(1);
      expect(call.answers[0].question).toBe("Which approach?");
      expect(call.answers[0].selected).toEqual(["Option B"]);
      expect(call.answers[0].freetext).toBeUndefined();
    });

    it("single select deselects previous choice when a new one is picked", async () => {
      const user = userEvent.setup();
      const onSubmit = vi.fn();
      render(
        <QuestionPrompt
          questions={[makeQuestion()]}
          onSubmit={onSubmit}
        />
      );

      await user.click(screen.getByText("Option A"));
      await user.click(screen.getByText("Option B"));
      await user.click(screen.getByRole("button", { name: /submit/i }));

      const call = onSubmit.mock.calls[0][0] as { answers: QuestionAnswer[] };
      expect(call.answers[0].selected).toEqual(["Option B"]);
    });
  });

  describe("single question, multi select", () => {
    it("allows selecting multiple options", async () => {
      const user = userEvent.setup();
      const onSubmit = vi.fn();
      render(
        <QuestionPrompt
          questions={[makeQuestion({ allowMultiple: true })]}
          onSubmit={onSubmit}
        />
      );

      await user.click(screen.getByText("Option A"));
      await user.click(screen.getByText("Option B"));
      await user.click(screen.getByRole("button", { name: /submit/i }));

      const call = onSubmit.mock.calls[0][0] as { answers: QuestionAnswer[] };
      expect(call.answers[0].selected).toEqual(["Option A", "Option B"]);
    });

    it("can deselect a previously selected option", async () => {
      const user = userEvent.setup();
      const onSubmit = vi.fn();
      render(
        <QuestionPrompt
          questions={[makeQuestion({ allowMultiple: true })]}
          onSubmit={onSubmit}
        />
      );

      await user.click(screen.getByText("Option A"));
      await user.click(screen.getByText("Option B"));
      // Deselect A
      await user.click(screen.getByText("Option A"));
      await user.click(screen.getByRole("button", { name: /submit/i }));

      const call = onSubmit.mock.calls[0][0] as { answers: QuestionAnswer[] };
      expect(call.answers[0].selected).toEqual(["Option B"]);
    });
  });

  describe("freetext input", () => {
    it("freetext is always visible", () => {
      render(
        <QuestionPrompt
          questions={[makeQuestion()]}
          onSubmit={vi.fn()}
        />
      );

      expect(screen.getByPlaceholderText("Or type your own answer...")).toBeInTheDocument();
    });

    it("typing freetext enables submit and clears option selection (single-select)", async () => {
      const user = userEvent.setup();
      const onSubmit = vi.fn();
      render(
        <QuestionPrompt
          questions={[makeQuestion()]}
          onSubmit={onSubmit}
        />
      );

      // Select an option first
      await user.click(screen.getByText("Option A"));
      // Now type freetext — should clear the option
      const textarea = screen.getByPlaceholderText("Or type your own answer...");
      await user.type(textarea, "Custom answer");
      await user.click(screen.getByRole("button", { name: /submit/i }));

      const call = onSubmit.mock.calls[0][0] as { answers: QuestionAnswer[] };
      expect(call.answers[0].selected).toEqual([]);
      expect(call.answers[0].freetext).toBe("Custom answer");
    });

    it("selecting an option clears freetext (single-select)", async () => {
      const user = userEvent.setup();
      const onSubmit = vi.fn();
      render(
        <QuestionPrompt
          questions={[makeQuestion()]}
          onSubmit={onSubmit}
        />
      );

      // Type freetext first
      const textarea = screen.getByPlaceholderText("Or type your own answer...");
      await user.type(textarea, "Custom");
      // Select an option — should clear freetext
      await user.click(screen.getByText("Option B"));
      await user.click(screen.getByRole("button", { name: /submit/i }));

      const call = onSubmit.mock.calls[0][0] as { answers: QuestionAnswer[] };
      expect(call.answers[0].selected).toEqual(["Option B"]);
      expect(call.answers[0].freetext).toBeUndefined();
    });
  });

  describe("multi-step questions", () => {
    const multiQuestions: QuestionItem[] = [
      makeQuestion({ question: "Step 1?" }),
      makeQuestion({
        question: "Step 2?",
        options: [
          { label: "X", description: "X desc" },
          { label: "Y", description: "Y desc" },
        ],
        allowMultiple: true,
      }),
      makeQuestion({ question: "Step 3?" }),
    ];

    it("shows step indicator for multi-question", () => {
      render(
        <QuestionPrompt questions={multiQuestions} onSubmit={vi.fn()} />
      );

      expect(screen.getByText("1 / 3")).toBeInTheDocument();
      expect(screen.getByText("Step 1?")).toBeInTheDocument();
    });

    it("Next button advances to next question", async () => {
      const user = userEvent.setup();
      render(
        <QuestionPrompt questions={multiQuestions} onSubmit={vi.fn()} />
      );

      await user.click(screen.getByText("Option A"));
      await user.click(screen.getByRole("button", { name: /next/i }));

      expect(screen.getByText("2 / 3")).toBeInTheDocument();
      expect(screen.getByText("Step 2?")).toBeInTheDocument();
    });

    it("Back button returns to previous question", async () => {
      const user = userEvent.setup();
      render(
        <QuestionPrompt questions={multiQuestions} onSubmit={vi.fn()} />
      );

      // Go to step 2
      await user.click(screen.getByText("Option A"));
      await user.click(screen.getByRole("button", { name: /next/i }));
      expect(screen.getByText("Step 2?")).toBeInTheDocument();

      // Go back
      await user.click(screen.getByText(/back/i));
      expect(screen.getByText("Step 1?")).toBeInTheDocument();
      expect(screen.getByText("1 / 3")).toBeInTheDocument();
    });

    it("shows Submit on last step, not Next", async () => {
      const user = userEvent.setup();
      render(
        <QuestionPrompt questions={multiQuestions} onSubmit={vi.fn()} />
      );

      // Step 1
      await user.click(screen.getByText("Option A"));
      await user.click(screen.getByRole("button", { name: /next/i }));

      // Step 2
      await user.click(screen.getByText("X"));
      await user.click(screen.getByRole("button", { name: /next/i }));

      // Step 3 — should show Submit
      expect(screen.getByText("3 / 3")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: /submit/i })).toBeInTheDocument();
    });

    it("submits all answers from all steps", async () => {
      const user = userEvent.setup();
      const onSubmit = vi.fn();
      render(
        <QuestionPrompt questions={multiQuestions} onSubmit={onSubmit} />
      );

      // Step 1: select Option B
      await user.click(screen.getByText("Option B"));
      await user.click(screen.getByRole("button", { name: /next/i }));

      // Step 2: select X and Y (multi-select)
      await user.click(screen.getByText("X"));
      await user.click(screen.getByText("Y"));
      await user.click(screen.getByRole("button", { name: /next/i }));

      // Step 3: select Option A
      await user.click(screen.getByText("Option A"));
      await user.click(screen.getByRole("button", { name: /submit/i }));

      expect(onSubmit).toHaveBeenCalledOnce();
      const call = onSubmit.mock.calls[0][0] as { answers: QuestionAnswer[] };
      expect(call.answers).toHaveLength(3);

      expect(call.answers[0].question).toBe("Step 1?");
      expect(call.answers[0].selected).toEqual(["Option B"]);

      expect(call.answers[1].question).toBe("Step 2?");
      expect(call.answers[1].selected).toEqual(["X", "Y"]);

      expect(call.answers[2].question).toBe("Step 3?");
      expect(call.answers[2].selected).toEqual(["Option A"]);
    });

    it("does not show Back button on first step", () => {
      render(
        <QuestionPrompt questions={multiQuestions} onSubmit={vi.fn()} />
      );

      expect(screen.queryByText(/back/i)).not.toBeInTheDocument();
    });
  });
});