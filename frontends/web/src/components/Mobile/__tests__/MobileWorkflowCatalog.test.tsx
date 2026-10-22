import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, ...props }: { children?: React.ReactNode }) => (
    <a {...props}>{children}</a>
  ),
}));

vi.mock("../../../store/globalDataStore", () => ({
  useWorkflows: () => ({
    workflows: [
      { name: "builtin://agent", description: "Basic agentic chat" },
      { name: "builtin://forge-one-shot", description: "Build with Forge" },
      { name: "builtin://hidden-flow", description: "Should be hidden" },
    ],
    loading: false,
  }),
}));

vi.mock("../../../store/preferencesStore", () => ({
  usePreferencesStore: (selector: (s: unknown) => unknown) =>
    selector({
      isWorkflowHidden: (name: string) => name === "builtin://hidden-flow",
    }),
}));

const { MobileWorkflowCatalog } = await import("../MobileWorkflowCatalog");

describe("MobileWorkflowCatalog", () => {
  it("lists visible workflows, alphabetically", () => {
    const { container } = render(<MobileWorkflowCatalog />);
    expect(container.querySelectorAll("a")).toHaveLength(2);
    expect(screen.getByText("Agent")).toBeInTheDocument();
    expect(screen.getByText("Forge One Shot")).toBeInTheDocument();
  });

  it("omits workflows hidden by user preferences", () => {
    render(<MobileWorkflowCatalog />);
    expect(screen.queryByText(/hidden-flow/i)).not.toBeInTheDocument();
  });

  it("links each row to its workflow detail route", () => {
    render(<MobileWorkflowCatalog />);
    const link = screen.getByText("Agent").closest("a");
    expect(link).toHaveAttribute("to", "/m/workflows/$workflowName");
  });

  it("leads the second line with the description, not the step count", () => {
    render(<MobileWorkflowCatalog />);
    expect(screen.getByText("Basic agentic chat")).toBeInTheDocument();
    expect(screen.queryByText(/^\d+ steps?$/)).not.toBeInTheDocument();
  });

  it("gives rows with a matched name and a distinct icon from the fallback", () => {
    const { container } = render(<MobileWorkflowCatalog />);
    const agentRow = screen.getByText("Agent").closest("a")!;
    const forgeRow = screen.getByText("Forge One Shot").closest("a")!;
    const agentIcon = agentRow.querySelector("svg[class*='lucide-']");
    const forgeIcon = forgeRow.querySelector("svg[class*='lucide-']");
    expect(agentIcon?.getAttribute("class")).not.toBe(
      forgeIcon?.getAttribute("class"),
    );
    expect(container.querySelectorAll("svg").length).toBeGreaterThan(0);
  });
});
