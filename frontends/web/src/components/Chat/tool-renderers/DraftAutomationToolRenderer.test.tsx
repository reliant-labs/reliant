// Copyright (c) 2025 Reliant Labs

/**
 * draft_automation's card: the draft in words and one action, "Review &
 * create", which opens the automation dialog prefilled. It never creates.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

vi.mock("@/hooks/chat-queries", () => ({ useChat: () => ({ data: { projectId: "proj-7" } }) }));

import { useAutomationDialogStore, closeAutomationDialog } from "../../Automations/dialog/openAutomationDialog";
import { DraftAutomationCard, draftFromToolResult, isDraftAutomationTool } from "./DraftAutomationToolRenderer";

const metadata = JSON.stringify({
  draft: {
    definition: { message: "Review the PR.", integration: { integration: "github", events: ["pull_request.opened"] } },
    summary: "When a pull request is opened, review it",
  },
});

beforeEach(() => closeAutomationDialog());

describe("DraftAutomationCard", () => {
  it("reads the draft from the result metadata, and nothing else", () => {
    expect(draftFromToolResult(metadata)?.summary).toBe("When a pull request is opened, review it");
    expect(draftFromToolResult(undefined)).toBeUndefined();
    expect(draftFromToolResult("not json")).toBeUndefined();
    expect(isDraftAutomationTool("draft_automation")).toBe(true);
    expect(isDraftAutomationTool("mcp__reliant__draft_automation")).toBe(true);
    expect(isDraftAutomationTool("list_triggers")).toBe(false);
  });

  it("opens the automation dialog prefilled, in the chat's project", () => {
    render(<DraftAutomationCard chatId="chat-1" metadata={metadata} />);
    expect(screen.getByText("When a pull request is opened, review it")).toBeInTheDocument();
    expect(screen.getByText("Nothing runs until you review it and press Create.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Review & create" }));

    const request = useAutomationDialogStore.getState().request;
    expect(request).toMatchObject({ projectId: "proj-7", prefill: { message: "Review the PR.", projectId: "proj-7" } });
    expect(request?.prefill?.source).toMatchObject({ kind: "passthrough", arm: { case: "integration" } });
  });

  it("renders nothing for a failed draft", () => {
    const { container } = render(<DraftAutomationCard chatId="chat-1" metadata={undefined} />);
    expect(container).toBeEmptyDOMElement();
  });
});
