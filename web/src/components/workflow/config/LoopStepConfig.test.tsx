import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { LoopStep } from "../../../types/workflow";
import { celExpr, directCel } from "../../../lib/celAdapter";
import { LoopStepConfig } from "./LoopStepConfig";

function createLoopStep(overrides: Partial<LoopStep> = {}): LoopStep {
  return {
    id: "loop_1",
    type: "loop",
    args: {
      case: "loop",
      value: {
        args: {},
        presets: {},
        inline: {
          name: "inline-loop",
          entry: [],
          nodes: [],
          edges: [],
          outputs: {},
        },
      },
    },
    ...overrides,
  } as LoopStep;
}

describe("LoopStepConfig", () => {
  it("shows sequential fields by default and hides parallel-only fields", () => {
    render(<LoopStepConfig step={createLoopStep()} onUpdate={vi.fn()} />);

    expect(screen.getByRole("button", { name: "Sequential" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Parallel" })).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByLabelText("Continue while")).toBeInTheDocument();
    expect(screen.queryByLabelText("Items")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("On Failure")).not.toBeInTheDocument();
  });

  it("switches to parallel mode, clears while, and shows parallel-only fields", () => {
    const onUpdate = vi.fn();

    render(
      <LoopStepConfig
        step={createLoopStep({
          args: {
            case: "loop",
            value: {
              args: {},
              presets: {},

              inline: {
                name: "inline-loop",
                entry: [],
                nodes: [],
                edges: [],
                outputs: {},
              },
              while: directCel("outputs.done == true"),
            },
          },
        })}
        onUpdate={onUpdate}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Parallel" }));

    expect(onUpdate).toHaveBeenCalledTimes(1);
    expect(onUpdate.mock.calls[0][0].args.value).toMatchObject({
      parallel: { value: { case: "literal", value: true } },
      while: undefined,
    });
  });

  it("switches to sequential mode and clears parallel-only fields", () => {
    const onUpdate = vi.fn();

    render(
      <LoopStepConfig
        step={createLoopStep({
          args: {
            case: "loop",
            value: {
              args: {},
              presets: {},

              inline: {
                name: "inline-loop",
                entry: [],
                nodes: [],
                edges: [],
                outputs: {},
              },
              parallel: { value: { case: "literal", value: true } } as any,
              items: celExpr("{{inputs.items}}"),
              key: "{{iter.item.id}}",
              onFailure: "fail_fast",
            },
          },
        })}
        onUpdate={onUpdate}
      />,
    );

    expect(screen.getByRole("button", { name: "Sequential" })).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByRole("button", { name: "Parallel" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByLabelText("Items")).toBeInTheDocument();
    expect(screen.getByLabelText("On Failure")).toBeInTheDocument();
    expect(screen.queryByLabelText("Continue while")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Sequential" }));

    expect(onUpdate).toHaveBeenCalledTimes(1);
    expect(onUpdate.mock.calls[0][0].args.value).toMatchObject({
      parallel: undefined,
      items: undefined,
      key: undefined,
      onFailure: undefined,
    });
  });
});