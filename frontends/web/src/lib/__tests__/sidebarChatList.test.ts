import { describe, expect, it } from "vitest";

import { ChatOrigin, ChatState } from "../../gen/reliant/v1/chat_pb";
import {
  chatListRows,
  isAdoptedAutomation,
  isAutomationLaunch,
  isListedInChatList,
  isRun,
  isUnadoptedAutomation,
  withOpenChatPinned,
} from "../sidebarChatList";

const listed = { id: "listed", projectId: "p1", state: ChatState.IDLE };
const run = { id: "run", projectId: "p1", state: ChatState.IDLE, launchKind: "schedule" };

describe("withOpenChatPinned", () => {
  it("appends the open chat when the server list omits it", () => {
    expect(withOpenChatPinned([listed], run, { scope: "p1" }).map((c) => c.id)).toEqual(["listed", "run"]);
  });

  it("does not duplicate a chat the server already listed", () => {
    expect(withOpenChatPinned([listed, run], run, { scope: "p1" })).toHaveLength(2);
  });

  it("leaves the list alone with no open chat, another project's chat, or an archived one", () => {
    expect(withOpenChatPinned([listed], undefined, { scope: "p1" })).toEqual([listed]);
    expect(withOpenChatPinned([listed], { ...run, projectId: "p2" }, { scope: "p1" })).toEqual([listed]);
    expect(withOpenChatPinned([listed], { ...run, state: ChatState.ARCHIVED }, { scope: "p1" })).toEqual([
      listed,
    ]);
  });

  it("pins a chat from any project into the all-projects list", () => {
    expect(withOpenChatPinned([listed], { ...run, projectId: "p2" }, { scope: null }).map((c) => c.id)).toEqual([
      "listed",
      "run",
    ]);
  });

  it("does not pin a chat the user just moved out of the list", () => {
    expect(withOpenChatPinned([listed], run, { scope: "p1", releasedChatId: "run" })).toEqual([listed]);
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

describe("origin policy", () => {
  it("treats every origin but USER as a run", () => {
    expect(isRun({ origin: ChatOrigin.USER })).toBe(false);
    for (const origin of [ChatOrigin.AUTOMATION, ChatOrigin.AGENT, ChatOrigin.MANUAL_RUN, ChatOrigin.BUILDER_TEST]) {
      expect(isRun({ origin })).toBe(true);
    }
  });

  it("prefers origin to the launch kind, and falls back to it only when origin is unset", () => {
    expect(isRun({ origin: ChatOrigin.AUTOMATION, launchKind: "chat.start" })).toBe(true);
    expect(isRun({ origin: ChatOrigin.USER, launchKind: "schedule" })).toBe(false);
    expect(isRun({ origin: ChatOrigin.UNSPECIFIED, launchKind: "schedule" })).toBe(true);
    expect(isRun({ launchKind: undefined })).toBe(false);
  });

  it("lists a person's chat and an adopted run, never an unadopted run", () => {
    const user = { id: "u", origin: ChatOrigin.USER };
    const automation = { id: "a", origin: ChatOrigin.AUTOMATION };
    const adopted = { id: "ad", origin: ChatOrigin.AUTOMATION, adoptedAt: "2024-01-01T00:00:00Z" };
    expect(isListedInChatList(user)).toBe(true);
    expect(isListedInChatList(automation)).toBe(false);
    expect(isListedInChatList(adopted)).toBe(true);
    expect(chatListRows([user, automation, adopted]).map((c) => c.id)).toEqual(["u", "ad"]);
  });

  it("drops an unadopted run from the pinned rows unless it is the open chat", () => {
    const user = { id: "u", projectId: "p1", state: ChatState.IDLE, origin: ChatOrigin.USER };
    const automation = { id: "a", projectId: "p1", state: ChatState.IDLE, origin: ChatOrigin.AUTOMATION };
    expect(withOpenChatPinned([user, automation], undefined, { scope: "p1" }).map((c) => c.id)).toEqual(["u"]);
    expect(withOpenChatPinned([user, automation], automation, { scope: "p1" }).map((c) => c.id)).toEqual(["u", "a"]);
    expect(withOpenChatPinned([user, automation], automation, { scope: "p1", releasedChatId: "a" }).map((c) => c.id)).toEqual(["u"]);
  });
});
