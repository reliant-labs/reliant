import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
// PresetPicker reads a workflow's defaults through the query cache; these
// renders have no QueryClient and no defaults to show.
vi.mock("../../../hooks/preset-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../hooks/preset-queries")>()),
  useDefaultPresets: () => ({ defaults: {}, loading: false }),
}));

import { MobileWorkflowParamsView } from "../MobileWorkflowParamsView";
import { createInput } from "../../../lib/inputHelpers";
import type { Workflow } from "../../../types/workflow";
import type { Preset } from "../../../hooks/preset-queries";

function workflow(): Workflow {
  return {
    name: "demo",
    inputs: {
      mode: createInput("enum", {
        description: "Execution mode",
        enumValues: ["auto", "manual"],
        default: "auto",
      }),
      ask: createInput("boolean", { description: "Pause for review" }),
    },
  } as Workflow;
}

function preset(overrides: Partial<Preset> = {}): Preset {
  return {
    name: "fast",
    description: "",
    params: {},
    source: "builtin",
    ...overrides,
  };
}

describe("MobileWorkflowParamsView", () => {
  it("lists every declared input with its type and description", () => {
    render(<MobileWorkflowParamsView workflow={workflow()} presets={[]} />);
    expect(screen.getByText("mode")).toBeInTheDocument();
    expect(screen.getByText("enum")).toBeInTheDocument();
    expect(screen.getByText("Execution mode")).toBeInTheDocument();
    expect(screen.getByText("ask")).toBeInTheDocument();
    expect(screen.getByText("boolean")).toBeInTheDocument();
  });

  it("shows a default value when one is declared", () => {
    render(<MobileWorkflowParamsView workflow={workflow()} presets={[]} />);
    expect(screen.getByText("auto")).toBeInTheDocument();
  });

  it("reports when a workflow takes no parameters", () => {
    render(
      <MobileWorkflowParamsView workflow={{ name: "empty" } as Workflow} presets={[]} />,
    );
    expect(screen.getByText(/takes no parameters/i)).toBeInTheDocument();
  });

  it("never renders an editable field for a param", () => {
    render(<MobileWorkflowParamsView workflow={workflow()} presets={[]} />);
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  });

  it("omits the preset section when there are no presets", () => {
    render(<MobileWorkflowParamsView workflow={workflow()} presets={[]} />);
    expect(screen.queryByText(/preset/i)).not.toBeInTheDocument();
  });

  it("shows the preset picker when presets exist", () => {
    render(
      <MobileWorkflowParamsView workflow={workflow()} presets={[preset({ name: "fast" })]} />,
    );
    expect(screen.getByText("Preset")).toBeInTheDocument();
  });
});
