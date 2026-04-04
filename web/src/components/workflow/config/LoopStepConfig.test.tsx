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
        yield: "",
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

    expect(screen.getByLabelText("Mode")).toHaveValue("sequential");
    expect(screen.getByLabelText("While (optional)")).toBeInTheDocument();
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
              yield: "",
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

    fireEvent.change(screen.getByLabelText("Mode"), {
      target: { value: "parallel" },
    });

    expect(onUpdate).toHaveBeenCalledTimes(1);
    expect(onUpdate.mock.calls[0][0].args.value).toMatchObject({
      parallel: true,
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
              yield: "",
              inline: {
                name: "inline-loop",
                entry: [],
                nodes: [],
                edges: [],
                outputs: {},
              },
              parallel: true,
              items: celExpr("{{inputs.items}}"),
              key: "{{iter.item.id}}",
              onFailure: "fail_fast",
            },
          },
        })}
        onUpdate={onUpdate}
      />,
    );

    expect(screen.getByLabelText("Mode")).toHaveValue("parallel");
    expect(screen.getByLabelText("Items")).toBeInTheDocument();
    expect(screen.getByLabelText("On Failure")).toBeInTheDocument();
    expect(screen.queryByLabelText("While (optional)")).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Mode"), {
      target: { value: "sequential" },
    });

    expect(onUpdate).toHaveBeenCalledTimes(1);
    expect(onUpdate.mock.calls[0][0].args.value).toMatchObject({
      parallel: undefined,
      items: undefined,
      key: undefined,
      onFailure: undefined,
    });
  });
});
