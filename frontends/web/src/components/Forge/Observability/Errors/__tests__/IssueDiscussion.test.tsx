// Copyright (c) 2025 Reliant Labs

/**
 * The discussion on an issue against the issues fixture: the assignee control
 * and its conflict path, the one timeline (comments with edit and delete,
 * assignments, linked chats, a merged issue's entries labelled with their
 * origin), the composer, the assignee filter on the list, and the incident.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { createFixtureApi } from "../../__fixtures__/fixtureApi";
import { ENV_PLACE, PROJECT_PLACE, renderPanel } from "../../__tests__/obsTestUtils";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("../../__tests__/obsMocks")).obsMocks.virtual());

const calls = (onCall: ReturnType<typeof vi.fn>, rpc: string) => onCall.mock.calls.filter(([name]) => name === rpc).map(([, req]) => req);
const rowIds = () => screen.getAllByTestId(/^obs-issue-iss-/).map((row) => row.getAttribute("data-testid")!.replace("obs-issue-", ""));

describe("assigning", () => {
  it("shows the assignee, and 'Assign to me' echoes the version read", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { view: "errors", issue: "iss-worker-conns" }, ENV_PLACE);
    expect(await screen.findByTestId("obs-issue-assignee-name")).toHaveTextContent("Unassigned");

    await user.click(await screen.findByTestId("obs-issue-assign-me"));
    expect(calls(onCall, "AssignIssue")[0]).toEqual({ issueId: "iss-worker-conns", assigneeUserId: "user-ada", expectedVersion: 1 });
    await waitFor(() => expect(screen.getByTestId("obs-issue-assignee-name")).toHaveTextContent("ada@example.com (you)"));
    expect(screen.getByTestId("obs-issue-triage")).toHaveAttribute("data-version", "2");
    expect(screen.queryByTestId("obs-issue-assign-me")).not.toBeInTheDocument();
  });

  it("assigns a member from the picker, and unassigns", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { view: "errors", issue: "iss-orders-deadline" }, ENV_PLACE);
    expect(await screen.findByTestId("obs-issue-assignee-name")).toHaveTextContent("grace@example.com");
    const pick = screen.getByTestId("obs-issue-assignee-pick");
    await waitFor(() => expect(within(pick).getByRole("option", { name: "alan@example.com" })).toBeInTheDocument());
    await user.selectOptions(pick, "user-alan");
    await waitFor(() => expect(screen.getByTestId("obs-issue-assignee-name")).toHaveTextContent("alan@example.com"));
    expect(calls(onCall, "AssignIssue")[0]).toMatchObject({ assigneeUserId: "user-alan", expectedVersion: 1 });

    await user.click(screen.getByTestId("obs-issue-unassign"));
    await waitFor(() => expect(screen.getByTestId("obs-issue-assignee-name")).toHaveTextContent("Unassigned"));
    expect(calls(onCall, "AssignIssue")[1]).toMatchObject({ assigneeUserId: "", expectedVersion: 2 });
  });

  it("on a stale version, re-reads the issue and says who has it now — and never re-sends", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall, assignConflict: true }), { view: "errors", issue: "iss-worker-conns" }, ENV_PLACE);
    await user.click(await screen.findByTestId("obs-issue-assign-me"));
    const conflict = await screen.findByTestId("obs-issue-assignee-conflict");
    expect(conflict).toHaveTextContent("Someone changed this issue while you were looking, so the assignee was not changed.");
    await waitFor(() => expect(conflict).toHaveTextContent("It is assigned to alan@example.com now, not unassigned."));
    // The other person's assignment stands; the reader's was neither applied nor retried.
    expect(screen.getByTestId("obs-issue-assignee-name")).toHaveTextContent("alan@example.com");
    expect(calls(onCall, "AssignIssue")).toHaveLength(1);
  });

  it("still offers 'me' and the current assignee when the member list is closed to the reader", async () => {
    renderPanel(createFixtureApi({ rosterDenied: true }), { view: "errors", issue: "iss-orders-deadline" }, ENV_PLACE);
    expect(await screen.findByTestId("obs-issue-assignee-roster-closed")).toBeInTheDocument();
    const pick = screen.getByTestId("obs-issue-assignee-pick");
    expect(within(pick).getByRole("option", { name: "ada@example.com (you)" })).toBeInTheDocument();
    expect(within(pick).getByRole("option", { name: "grace@example.com" })).toBeInTheDocument();
    expect(within(pick).queryByRole("option", { name: "alan@example.com" })).not.toBeInTheDocument();
  });
});

describe("the timeline", () => {
  it("merges status changes, comments, an assignment and a linked chat, oldest first", async () => {
    renderPanel(createFixtureApi(), { view: "errors", issue: "iss-orders-deadline" }, ENV_PLACE);
    const list = await screen.findByTestId("obs-issue-timeline-list");
    const kinds = within(list).getAllByRole("listitem").map((li) => li.getAttribute("data-kind"));
    expect(kinds).toEqual(["status", "assignment", "comment", "chat"]);
    expect(within(list).getAllByRole("listitem")[1]).toHaveTextContent("assigned it to grace@example.com");
    expect(screen.getByTestId("obs-timeline-chat-chat-4f2a91c07b")).toHaveTextContent("Investigate CreateOrder timeouts");
  });

  it("renders a comment's markdown but never its raw HTML", async () => {
    renderPanel(createFixtureApi(), { view: "errors", issue: "iss-checkout-total" }, ENV_PLACE);
    const bodies = await screen.findAllByTestId("obs-comment-body");
    const body = bodies[0];
    expect(within(body).getByText("v1.8.3").tagName).toBe("STRONG");
    expect(body.querySelector("script")).toBeNull();
    const link = within(body).getByRole("link", { name: "the fix" });
    expect(link).toHaveAttribute("href", "https://example.com/pr/4821");
    expect(link).toHaveAttribute("rel", expect.stringContaining("noopener"));
    expect(link).toHaveAttribute("target", "_blank");
  });

  it("adds a comment, which lands at the end of the timeline", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { view: "errors", issue: "iss-worker-conns" }, ENV_PLACE);
    await screen.findByTestId("obs-issue-timeline-list");
    expect(screen.getByTestId("obs-comment-submit")).toBeDisabled();
    await user.type(screen.getByTestId("obs-comment-box"), "Raised max_connections to 200");
    await user.click(screen.getByTestId("obs-comment-submit"));
    expect(calls(onCall, "AddIssueComment")[0]).toEqual({ issueId: "iss-worker-conns", body: "Raised max_connections to 200" });
    await waitFor(() => expect(within(screen.getByTestId("obs-issue-timeline-list")).getAllByRole("listitem").at(-1)).toHaveTextContent("Raised max_connections to 200"));
    expect(screen.getByTestId("obs-comment-box")).toHaveValue("");
  });

  it("keeps what was typed when the comment is refused", async () => {
    const user = userEvent.setup();
    renderPanel(createFixtureApi({ issueReader: "none" }), { view: "errors", issue: "iss-worker-conns" }, ENV_PLACE);
    await user.type(await screen.findByTestId("obs-comment-box"), "Still here");
    await user.click(screen.getByTestId("obs-comment-submit"));
    expect(await screen.findByTestId("obs-comment-add-error")).toHaveTextContent("cannot comment");
    expect(screen.getByTestId("obs-comment-box")).toHaveValue("Still here");
  });

  it("lets the author edit a comment, and shows it edited", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall, issueReader: "grace" }), { view: "errors", issue: "iss-orders-deadline" }, ENV_PLACE);
    const edit = await screen.findByTestId(/^obs-comment-edit-cmt-/);
    await user.click(edit);
    const box = screen.getByTestId(/^obs-comment-edit-box-cmt-/);
    await user.clear(box);
    await user.type(box, "Pool exhausted; see the worker issue.");
    await user.click(screen.getByTestId(/^obs-comment-save-cmt-/));
    expect(calls(onCall, "UpdateIssueComment")[0]).toMatchObject({ issueId: "iss-orders-deadline", body: "Pool exhausted; see the worker issue." });
    await waitFor(() => expect(screen.getByTestId("obs-issue-timeline-list")).toHaveTextContent("Pool exhausted; see the worker issue."));
    expect(screen.getByTestId("obs-issue-timeline-list")).toHaveTextContent("(edited)");
  });

  it("offers edit to the author only, and delete to the author or an admin", async () => {
    // grace wrote the comment on iss-orders-deadline; ada (an admin) did not.
    const { unmount } = renderPanel(createFixtureApi({ issueReader: "ada" }), { view: "errors", issue: "iss-orders-deadline" }, ENV_PLACE);
    await screen.findByTestId("obs-issue-timeline-list");
    await waitFor(() => expect(screen.getByTestId(/^obs-comment-delete-cmt-/)).toBeInTheDocument());
    expect(screen.queryByTestId(/^obs-comment-edit-cmt-/)).not.toBeInTheDocument();
    unmount();

    // alan is a plain member and wrote nothing here.
    // (The fixture has no 'alan' reader, so use a member who is not the author: grace on ada's comment.)
    renderPanel(createFixtureApi({ issueReader: "grace" }), { view: "errors", issue: "iss-checkout-total" }, ENV_PLACE);
    await screen.findByTestId("obs-issue-timeline-list");
    expect(screen.queryByTestId(/^obs-comment-edit-cmt-/)).not.toBeInTheDocument();
    expect(screen.queryByTestId(/^obs-comment-delete-cmt-/)).not.toBeInTheDocument();
  });

  it("deletes a comment after a confirmation, and it leaves the timeline", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall, issueReader: "ada" }), { view: "errors", issue: "iss-orders-deadline" }, ENV_PLACE);
    await user.click(await screen.findByTestId(/^obs-comment-delete-cmt-/));
    expect(calls(onCall, "DeleteIssueComment")).toHaveLength(0);
    await user.click(screen.getByTestId(/^obs-comment-confirm-delete-cmt-/));
    expect(calls(onCall, "DeleteIssueComment")).toHaveLength(1);
    await waitFor(() => expect(screen.getByTestId("obs-issue-timeline-list")).not.toHaveTextContent("Looks like the pool is exhausted"));
  });

  it("labels the entries of a merged-away issue with where they came from", async () => {
    // iss-checkout-total was merged into iss-orders-deadline: opening either shows the target's timeline.
    renderPanel(createFixtureApi({ mergedIssue: true }), { view: "errors", issue: "iss-checkout-total" }, ENV_PLACE);
    const list = await screen.findByTestId("obs-issue-timeline-list");
    expect(screen.getByTestId("obs-issue-timeline")).toHaveAttribute("data-issue", "iss-orders-deadline");
    const rows = within(list).getAllByRole("listitem");
    const fromMerged = rows.filter((row) => row.getAttribute("data-issue") === "iss-checkout-total");
    const ownRows = rows.filter((row) => row.getAttribute("data-issue") === "iss-orders-deadline");
    expect(fromMerged.length).toBeGreaterThan(0);
    for (const row of fromMerged) expect(within(row).getByTestId("obs-timeline-origin")).toHaveTextContent("on this issue, before it was merged");
    for (const row of ownRows) expect(within(row).queryByTestId("obs-timeline-origin")).not.toBeInTheDocument();
  });

  it("explains a refused write on a merged issue, naming the target", async () => {
    const user = userEvent.setup();
    renderPanel(createFixtureApi({ mergedIssue: true }), { view: "errors", issue: "iss-checkout-total" }, ENV_PLACE);
    // The composer writes to the issue the timeline is of, which accepts it; the merged issue itself refuses.
    await screen.findByTestId("obs-issue-timeline-list");
    await user.click(await screen.findByTestId("obs-issue-assign-me"));
    expect(await screen.findByTestId("obs-issue-assignee-error")).toHaveTextContent("merged into iss-orders-deadline");
  });
});

describe("the list", () => {
  it("shows each issue's assignee", async () => {
    renderPanel(createFixtureApi(), { view: "errors" }, ENV_PLACE);
    await screen.findByTestId("obs-errors-list");
    expect(screen.getByTestId("obs-issue-assignee-iss-orders-deadline")).toHaveTextContent("grace@example.com");
    expect(screen.getByTestId("obs-issue-assignee-iss-worker-conns")).toHaveTextContent("—");
  });

  it("filters to mine: resolves who the reader is, then asks the server for their user id", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    const harness = renderPanel(createFixtureApi({ onCall, issueReader: "grace" }), { view: "errors" }, ENV_PLACE);
    await screen.findByTestId("obs-errors-list");
    await user.selectOptions(screen.getByTestId("obs-errors-assignee"), "me");
    expect(harness.current().assignee).toBe("me");
    await waitFor(() => expect(rowIds()).toEqual(["iss-orders-deadline"]));
    const last = calls(onCall, "ListIssues").at(-1);
    expect(last.assignee).toBe("user-grace");
    // It never listed everyone's while waiting to learn who "me" is.
    expect(calls(onCall, "ListIssues").filter((r) => r.assignee === undefined)).toHaveLength(1);
  });

  it("filters to a named person on the server, and offers no 'unassigned' (the server cannot filter to it yet)", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    const harness = renderPanel(createFixtureApi({ onCall }), { view: "errors" }, ENV_PLACE);
    await screen.findByTestId("obs-errors-list");
    const pick = screen.getByTestId("obs-errors-assignee");
    expect(within(pick).queryByRole("option", { name: "Unassigned" })).not.toBeInTheDocument();
    await waitFor(() => expect(within(pick).getByRole("option", { name: "grace@example.com" })).toBeInTheDocument());
    await user.selectOptions(pick, "user-grace");
    expect(harness.current().assignee).toBe("user-grace");
    await waitFor(() => expect(rowIds()).toEqual(["iss-orders-deadline"]));
    expect(calls(onCall, "ListIssues").at(-1).assignee).toBe("user-grace");
  });

  it("clears the assignee filter with the other filters", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi(), { view: "errors", assignee: "user-nobody" }, ENV_PLACE);
    await user.click(await screen.findByTestId("obs-clear-filters"));
    expect(harness.current().assignee).toBeUndefined();
  });
});

describe("incidents", () => {
  it("says how many other errors share the incident, and opens each from the issue", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi(), { view: "errors" }, PROJECT_PLACE);
    await screen.findByTestId("obs-errors-list");
    expect(screen.getByTestId("obs-issue-related-count-iss-orders-deadline")).toHaveTextContent("+1 related");
    await user.click(screen.getByTestId("obs-issue-open-iss-orders-deadline"));
    const incident = await screen.findByTestId("obs-issue-incident");
    expect(incident).toHaveTextContent("1 related error (same incident)");
    await user.click(within(incident).getByTestId("obs-issue-incident-iss-worker-conns"));
    expect(harness.current().issue).toBe("iss-worker-conns");
  });

  it("shows nothing for an issue that is part of no incident", async () => {
    renderPanel(createFixtureApi(), { view: "errors", issue: "iss-checkout-total" }, ENV_PLACE);
    await screen.findByTestId("obs-issue-triage");
    expect(screen.queryByTestId("obs-issue-incident")).not.toBeInTheDocument();
  });
});
