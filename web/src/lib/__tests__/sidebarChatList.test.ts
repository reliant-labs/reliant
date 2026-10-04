import { describe, expect, it } from "vitest";

import { ChatState } from "../../gen/reliant/v1/chat_pb";
import {
  isAdoptedAutomation,
  isAutomationLaunch,
  isUnadoptedAutomation,
  withOpenChatPinned,
} from "../sidebarChatList";

const listed = { id: "listed", projectId: "p1", state: ChatState.ACTIVE };
const run = { id: "run", projectId: "p1", state: ChatState.ACTIVE, launchKind: "schedule" };

describe("withOpenChatPinned", () => {
  it("appends the open chat when the server list omits it", () => {
    expect(withOpenChatPinned([listed], run, { projectId: "p1" }).map((c) => c.id)).toEqual(["listed", "run"]);
  });

  it("does not duplicate a chat the server already listed", () => {
    expect(withOpenChatPinned([listed, run], run, { projectId: "p1" })).toHaveLength(2);
  });

  it("leaves the list alone with no open chat, another project's chat, or an archived one", () => {
    expect(withOpenChatPinned([listed], undefined, { projectId: "p1" })).toEqual([listed]);
    expect(withOpenChatPinned([listed], { ...run, projectId: "p2" }, { projectId: "p1" })).toEqual([listed]);
    expect(withOpenChatPinned([listed], { ...run, state: ChatState.ARCHIVED }, { projectId: "p1" })).toEqual([
      listed,
    ]);
  });

  it("does not pin a chat the user just moved out of the list", () => {
    expect(withOpenChatPinned([listed], run, { projectId: "p1", releasedChatId: "run" })).toEqual([listed]);
  });
});

describe("launch classification", () => {
  it("reads null and chat.start as a chat, anything else as an automation", () => {
    expect(isAutomationLaunch(undefined)).toBe(false);
    expect(isAutomationLaunch("chat.start")).toBe(false);
    expect(isAutomationLaunch("schedule")).toBe(true);
    expect(isAutomationLaunch("agent.start_run")).toBe(true);
  });

  it("splits automations by adoption", () => {
    expect(isAdoptedAutomation({ ...run, adoptedAt: "2024-01-01T00:00:00Z" })).toBe(true);
    expect(isUnadoptedAutomation(run)).toBe(true);
    expect(isUnadoptedAutomation({ ...run, adoptedAt: "2024-01-01T00:00:00Z" })).toBe(false);
    expect(isUnadoptedAutomation(listed)).toBe(false);
  });
});
