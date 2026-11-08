/**
 * The builder header keeps Run, Inputs, Save and Publish visible; YAML, Tests,
 * Duplicate and Unpublish live in the "More" menu. The workflows journey's
 * anchors must still resolve on the real markup.
 */
import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { BuilderHeaderActions, type BuilderHeaderActionsProps } from "../BuilderHeaderActions";
import { workflowsJourney } from "../../Onboarding/journeys/workflows";

function props(overrides: Partial<BuilderHeaderActionsProps> = {}): BuilderHeaderActionsProps {
  return {
    showRun: true,
    runOpen: false,
    onToggleRun: vi.fn(),
    onOpenInputs: vi.fn(),
    onOpenYaml: vi.fn(),
    onOpenTests: vi.fn(),
    onDuplicate: vi.fn(),
    onSave: vi.fn(),
    saveDisabled: false,
    saving: false,
    saveIsPrimary: false,
    publish: { onPublish: vi.fn(), reasons: [], hasModifications: false },
    classes: { button: "b", secondary: "s", primary: "p" },
    ...overrides,
  };
}

const visibleButtons = () => screen.getAllByRole("button").map((b) => b.textContent?.trim());

describe("builder header: four actions and More", () => {
  it("a draft shows Run, Inputs, More, Save and Publish, and nothing else", () => {
    render(<BuilderHeaderActions {...props()} />);
    expect(visibleButtons()).toEqual(["Run", "Inputs", "More", "Save", "Publish"]);
    for (const label of ["YAML", "Tests", "Duplicate", "Unpublish"]) {
      expect(screen.queryByRole("button", { name: label })).toBeNull();
    }
  });

  it("More holds YAML, Tests and Duplicate; Unpublish only while published", async () => {
    const user = userEvent.setup();
    const { rerender } = render(<BuilderHeaderActions {...props()} />);
    await user.click(screen.getByRole("button", { name: /More/ }));
    expect(screen.getAllByRole("menuitem").map((i) => i.textContent)).toEqual(["YAML", "Tests", "Duplicate"]);

    await user.keyboard("{Escape}");
    rerender(<BuilderHeaderActions {...props({ onUnpublish: vi.fn(), publish: undefined })} />);
    await user.click(screen.getByRole("button", { name: /More/ }));
    const menu = screen.getByRole("menu");
    expect(within(menu).getAllByRole("menuitem").map((i) => i.textContent)).toEqual([
      "YAML",
      "Tests",
      "Duplicate",
      "UnpublishStops it running",
    ]);
    expect(screen.queryByTestId("workflow-publish")).toBeNull();
  });

  it("is operable from the keyboard: Enter opens, arrows move, Enter chooses, Escape returns focus", async () => {
    const user = userEvent.setup();
    const onOpenTests = vi.fn();
    render(<BuilderHeaderActions {...props({ onOpenTests })} />);
    const more = screen.getByRole("button", { name: /More/ });
    more.focus();
    await user.keyboard("{Enter}");
    expect(screen.getByRole("menuitem", { name: "YAML" })).toHaveFocus();
    await user.keyboard("{ArrowDown}{Enter}");
    expect(onOpenTests).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("menu")).toBeNull();
    expect(more).toHaveFocus();

    await user.keyboard("{Enter}");
    expect(screen.getByRole("menu")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).toBeNull();
    expect(more).toHaveFocus();
  });

  it("each menu item runs its action", async () => {
    const user = userEvent.setup();
    const p = props({ onUnpublish: vi.fn(), publish: undefined });
    render(<BuilderHeaderActions {...p} />);
    for (const [label, fn] of [
      ["YAML", p.onOpenYaml],
      ["Tests", p.onOpenTests],
      ["Duplicate", p.onDuplicate],
      ["Unpublish", p.onUnpublish],
    ] as const) {
      await user.click(screen.getByRole("button", { name: /More/ }));
      await user.click(screen.getByRole("menuitem", { name: new RegExp(`^${label}`) }));
      expect(fn).toHaveBeenCalledTimes(1);
    }
  });

  it("Run is hidden when the workflow can't be test-run", () => {
    render(<BuilderHeaderActions {...props({ showRun: false })} />);
    expect(visibleButtons()).toEqual(["Inputs", "More", "Save", "Publish"]);
  });
});

describe("builder header: workflows journey anchors still resolve", () => {
  const builderTargets = () =>
    workflowsJourney.steps
      .flatMap((s) => [s.target, ...(s.phases ?? []).map((p) => p.target)])
      .filter((t): t is string => typeof t === "string" && /workflow-(save|inputs|publish|unpublish|more)/.test(t));

  it("the journey still names the Inputs anchor", () => {
    expect(builderTargets()).toEqual(expect.arrayContaining(["[data-tour='workflow-inputs']"]));
  });

  it("every header anchor the journey uses is on a visible control, never inside More", () => {
    const { container } = render(<BuilderHeaderActions {...props()} />);
    for (const selector of builderTargets()) {
      const el = container.querySelector(selector);
      expect(el, selector).not.toBeNull();
      expect(el!.closest("[role='menu']")).toBeNull();
    }
    expect(container.querySelector("[data-testid='workflow-publish']")).not.toBeNull();
  });

  it("workflow-unpublish moves onto the menu item", async () => {
    const user = userEvent.setup();
    render(<BuilderHeaderActions {...props({ onUnpublish: vi.fn(), publish: undefined })} />);
    expect(screen.queryByTestId("workflow-unpublish")).toBeNull();
    await user.click(screen.getByRole("button", { name: /More/ }));
    expect(screen.getByTestId("workflow-unpublish")).toHaveAttribute("role", "menuitem");
  });
});
