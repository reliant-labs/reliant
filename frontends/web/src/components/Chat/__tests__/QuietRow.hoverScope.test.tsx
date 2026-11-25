import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { QuietRow } from "../QuietRow";

// A row's hover-revealed actions key off the nearest `group/row`. When the
// group was the whole row, a group whose open detail held rows of its own (a
// SubAgentGroup of twelve agents, a fold of tool calls) revealed EVERY child's
// Open thread and Cancel while the pointer was anywhere over the group.
describe("QuietRow hover scope", () => {
  it("scopes the hover group to the header line, not the open detail", () => {
    render(
      <QuietRow
        icon={null}
        expanded
        onToggle={() => {}}
        ariaLabel="12 agents"
        data-testid="outer"
        detail={
          <QuietRow icon={null} expanded={false} onToggle={() => {}} ariaLabel="Agent one" data-testid="inner">
            Agent one
          </QuietRow>
        }
      >
        12 agents
      </QuietRow>,
    );
    const outerHeader = screen.getByRole("button", { name: "12 agents" });
    const innerHeader = screen.getByRole("button", { name: "Agent one" });

    expect(outerHeader.className).toContain("group/row");
    expect(innerHeader.className).toContain("group/row");
    // The inner row is not inside the outer row's hover group.
    expect(outerHeader).not.toContainElement(innerHeader);
    expect(screen.getByTestId("outer").className).not.toContain("group/row");
  });
});
