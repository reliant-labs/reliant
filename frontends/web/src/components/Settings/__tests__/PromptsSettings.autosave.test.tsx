/**
 * Prompts save themselves: a pause in typing saves the set and says Saved; a
 * failed save says so with a Retry. With no project, the page says to pick
 * one rather than silently disabling New.
 */
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  save: vi.fn(),
  prompts: [] as Array<{ id: string; name: string; content: string; description: string }>,
  project: { id: "p1", name: "shopfront" } as { id: string; name: string } | null,
}));
vi.mock("../../../hooks/project-queries", () => ({
  useProjectPrompts: () => ({ prompts: mocks.prompts, isPending: false, error: null }),
  useSaveProjectPrompts: () => ({ mutateAsync: mocks.save, isPending: false }),
}));
vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (select: (s: { currentProject: unknown }) => unknown) => select({ currentProject: mocks.project }),
}));
vi.mock("../SettingsProjectScope", () => ({ SettingsProjectScope: () => null }));
vi.mock("../../../lib/toast-manager", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const { PromptsSettings } = await import("../PromptsSettings");

beforeEach(() => {
  vi.useFakeTimers();
  mocks.save.mockReset();
  mocks.prompts = [{ id: "a", name: "Review", content: "Check the tests", description: "QA" }];
  mocks.project = { id: "p1", name: "shopfront" };
});
afterEach(() => vi.useRealTimers());

describe("Prompts autosave", () => {
  it("saves after a pause in typing, keeping the category", async () => {
    mocks.save.mockResolvedValue({});
    render(<PromptsSettings />);
    // The category round-trips (it is saved as the description).
    expect(screen.getByLabelText("Category")).toHaveValue("QA");

    fireEvent.change(screen.getByLabelText("Prompt content"), { target: { value: "Check the tests twice" } });
    expect(mocks.save).not.toHaveBeenCalled();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(800);
    });
    expect(mocks.save).toHaveBeenCalledWith({
      projectId: "p1",
      prompts: [{ id: "a", name: "Review", content: "Check the tests twice", description: "QA" }],
    });
    expect(screen.getByRole("status")).toHaveTextContent("Saved");
  });

  it("says when a save failed, and retries", async () => {
    mocks.save.mockRejectedValueOnce(new Error("down")).mockResolvedValueOnce({});
    render(<PromptsSettings />);
    fireEvent.change(screen.getByLabelText("Prompt title"), { target: { value: "Review v2" } });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(800);
    });
    expect(screen.getByRole("status")).toHaveTextContent("Couldn't save");
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    });
    expect(mocks.save).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("status")).toHaveTextContent("Saved");
  });

  it("never sends a newer set while an older one is in flight", async () => {
    let finishFirst: (v: unknown) => void = () => {};
    mocks.save
      .mockImplementationOnce(() => new Promise((resolve) => (finishFirst = resolve)))
      .mockImplementation(async ({ prompts }) => {
        mocks.prompts = prompts; // the saved set becomes the cache, as onSuccess does
        return {};
      });
    render(<PromptsSettings />);
    fireEvent.change(screen.getByLabelText("Prompt content"), { target: { value: "A" } });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(800);
    });
    fireEvent.change(screen.getByLabelText("Prompt content"), { target: { value: "B" } });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(800);
    });
    // B waits for A, so the server cannot apply B and then A.
    expect(mocks.save).toHaveBeenCalledTimes(1);
    await act(async () => {
      finishFirst({});
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(mocks.save).toHaveBeenCalledTimes(2);
    expect(mocks.save.mock.calls[1][0].prompts[0].content).toBe("B");
    expect(screen.getByLabelText("Prompt content")).toHaveValue("B");
    expect(screen.getByRole("status")).toHaveTextContent("Saved");
  });

  it("saves an unsaved edit to its own project when the project changes", async () => {
    mocks.save.mockResolvedValue({});
    const { rerender } = render(<PromptsSettings projectId="p1" />);
    fireEvent.change(screen.getByLabelText("Prompt content"), { target: { value: "Edited" } });
    rerender(<PromptsSettings projectId="p2" />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(800);
    });
    expect(mocks.save).toHaveBeenCalledTimes(1);
    expect(mocks.save.mock.calls[0][0]).toMatchObject({ projectId: "p1", prompts: [{ content: "Edited" }] });
    // The new project's page is not marked as saved by the old project's save.
    expect(screen.getByRole("status")).toBeEmptyDOMElement();
  });

  it("asks for a project rather than silently disabling New", () => {
    mocks.project = null;
    render(<PromptsSettings />);
    expect(screen.getByText(/pick a project above/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /new prompt/i })).toHaveAttribute("title", expect.stringMatching(/pick a project/i));
  });
});
