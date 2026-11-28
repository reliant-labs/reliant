/**
 * The to-do form (ask_user task: true): each item is marked Done or Not done,
 * with an optional note on why not, and sent in the ask_user answers shape.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TaskPrompt } from "../TaskPrompt";

describe("TaskPrompt, one item", () => {
  it("sends Done at once on Mark done", async () => {
    const onSubmit = vi.fn();
    const user = userEvent.setup();
    render(<TaskPrompt tasks={["Run `gcloud auth login`"]} onSubmit={onSubmit} />);

    expect(screen.getByText("Run `gcloud auth login`")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Mark done" }));

    expect(onSubmit).toHaveBeenCalledTimes(1);
    expect(onSubmit).toHaveBeenCalledWith({
      answers: [{ question: "Run `gcloud auth login`", selected: ["Done"] }],
    });
    // Locked once sent: a second click sends nothing.
    expect(screen.getByRole("button", { name: /Sending/ })).toBeDisabled();
  });

  it("sends Not done with the note, from the note's Send", async () => {
    const onSubmit = vi.fn();
    const user = userEvent.setup();
    render(<TaskPrompt tasks={["Approve the deploy"]} onSubmit={onSubmit} />);

    await user.click(screen.getByRole("button", { name: "Not done" }));
    const note = screen.getByLabelText("Why not? (optional)");
    expect(note).toHaveFocus();
    await user.type(note, "I have no access");
    await user.click(screen.getByRole("button", { name: "Send" }));

    expect(onSubmit).toHaveBeenCalledWith({
      answers: [{ question: "Approve the deploy", selected: ["Not done"], freetext: "I have no access" }],
    });
  });

  it("sends Not done without a note, and Enter in the note sends it", async () => {
    const onSubmit = vi.fn();
    const user = userEvent.setup();
    render(<TaskPrompt tasks={["Approve the deploy"]} onSubmit={onSubmit} />);

    await user.click(screen.getByRole("button", { name: "Not done" }));
    await user.keyboard("{Enter}");

    expect(onSubmit).toHaveBeenCalledWith({
      answers: [{ question: "Approve the deploy", selected: ["Not done"] }],
    });
  });

  it("goes back from the note to Mark done", async () => {
    const onSubmit = vi.fn();
    const user = userEvent.setup();
    render(<TaskPrompt tasks={["Approve the deploy"]} onSubmit={onSubmit} />);

    await user.click(screen.getByRole("button", { name: "Not done" }));
    await user.click(screen.getByRole("button", { name: "Back" }));
    expect(screen.queryByLabelText("Why not? (optional)")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Mark done" }));
    expect(onSubmit).toHaveBeenCalledWith({ answers: [{ question: "Approve the deploy", selected: ["Done"] }] });
  });
});

describe("TaskPrompt, several items", () => {
  it("sends every item's mark together, once all are marked", async () => {
    const onSubmit = vi.fn();
    const user = userEvent.setup();
    render(<TaskPrompt tasks={["Sign in", "Approve the PR"]} onSubmit={onSubmit} />);

    const submit = screen.getByRole("button", { name: "Submit" });
    expect(submit).toBeDisabled();
    expect(screen.getByText("0 of 2 marked")).toBeInTheDocument();

    const [signIn, approve] = screen.getAllByRole("group");
    const done = signIn!.querySelector("button")!;
    await user.click(done);
    expect(done).toHaveAttribute("aria-pressed", "true");
    // Marking one item done does not send: the other is still unmarked.
    expect(onSubmit).not.toHaveBeenCalled();
    expect(submit).toBeDisabled();

    await user.click(approve!.querySelectorAll("button")[1]!);
    await user.type(screen.getByLabelText("Why not? (optional)"), "CI is red");
    expect(screen.getByText("2 of 2 marked")).toBeInTheDocument();
    await user.click(submit);

    expect(onSubmit).toHaveBeenCalledWith({
      answers: [
        { question: "Sign in", selected: ["Done"] },
        { question: "Approve the PR", selected: ["Not done"], freetext: "CI is red" },
      ],
    });
  });

  it("names each item's marks after the item", () => {
    render(<TaskPrompt tasks={["Sign in", "Approve the PR"]} onSubmit={vi.fn()} />);
    expect(screen.getByRole("group", { name: "Sign in" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Approve the PR" })).toBeInTheDocument();
  });
});
