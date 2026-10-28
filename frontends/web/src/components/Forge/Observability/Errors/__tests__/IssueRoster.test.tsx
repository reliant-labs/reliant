// Copyright (c) 2025 Reliant Labs

import { act, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { peopleKeys } from "@/hooks/observability-queries";
import { createTestQueryClient } from "@/test/renderWithQuery";
import { ObservabilityApiProvider } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { IssueRoster } from "@/services/controlPlane/observability/membersApi";
import { createFixtureApi } from "../../__fixtures__/fixtureApi";
import { IssueAssignee } from "../IssueAssignee";
import { IssueTimeline } from "../IssueTimeline";

async function rig(reader: "ada" | "grace" = "ada") {
  const api = createFixtureApi({ issueReader: reader });
  const detail = await api.getIssue({ issueId: "iss-orders-deadline" });
  const client = createTestQueryClient();
  client.setDefaultOptions({ queries: { ...client.getDefaultOptions().queries, gcTime: Infinity } });
  const view = (orgId: string) => (
    <QueryClientProvider client={client}>
      <ObservabilityApiProvider api={api}>
        <IssueAssignee issue={detail.issue} orgId={orgId} />
        <IssueTimeline issueId={detail.issue.id} orgId={orgId} />
      </ObservabilityApiProvider>
    </QueryClientProvider>
  );
  return { api, client, view };
}

const roster = (org: string, callerRole: string | null): IssueRoster => ({ users: [{ id: `${org}-person`, name: `${org} Person` }], callerRole });

describe("issue roster org isolation and role controls", () => {
  it("switches A to B without showing A's people or borrowing A's admin role, even when A finishes late", async () => {
    const r = await rig();
    let finishA!: (value: IssueRoster) => void;
    let finishB!: (value: IssueRoster) => void;
    const list = vi.fn((orgId: string) => new Promise<IssueRoster>((resolve) => { if (orgId === "a") finishA = resolve; else finishB = resolve; }));
    r.api.listOrgMembers = list;
    const rendered = render(r.view("a"));
    await waitFor(() => expect(finishA).toBeTypeOf("function"));
    rendered.rerender(r.view("b"));
    await waitFor(() => expect(finishB).toBeTypeOf("function"));
    await act(async () => { finishA(roster("a", "admin")); });
    expect(screen.queryByRole("option", { name: "a Person" })).not.toBeInTheDocument();
    expect(screen.queryByTestId(/^obs-comment-delete-/)).not.toBeInTheDocument();
    await act(async () => { finishB(roster("b", "member")); });
    expect(await screen.findByRole("option", { name: "b Person" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "a Person" })).not.toBeInTheDocument();
    expect(screen.queryByTestId(/^obs-comment-delete-/)).not.toBeInTheDocument();
    expect(list.mock.calls).toEqual([["a"], ["b"]]);
    expect(r.client.getQueryData(peopleKeys.members("a"))).toEqual(roster("a", "admin"));
    expect(r.client.getQueryData(peopleKeys.members("b"))).toEqual(roster("b", "member"));
  });

  it.each(["admin", "owner"])("allows deleting another author's comment for the issue org's %s", async (role) => {
    const r = await rig();
    r.api.listOrgMembers = async () => roster("b", role);
    render(r.view("b"));
    expect(await screen.findByTestId(/^obs-comment-delete-/)).toBeInTheDocument();
  });

  it("keeps assignable members but fails closed for admin controls if caller role is unknown", async () => {
    const r = await rig();
    r.api.listOrgMembers = async () => roster("b", null);
    render(r.view("b"));
    expect(await screen.findByRole("option", { name: "b Person" })).toBeInTheDocument();
    await screen.findByTestId("obs-issue-timeline-list");
    expect(screen.queryByTestId(/^obs-comment-delete-/)).not.toBeInTheDocument();
  });

  it("hides cached roster and borrowed admin controls after a refused refresh", async () => {
    const r = await rig();
    r.api.listOrgMembers = async () => roster("b", "admin");
    render(r.view("b"));
    await screen.findByTestId(/^obs-comment-delete-/);
    r.api.listOrgMembers = async () => { throw new ConnectError("revoked", Code.PermissionDenied); };
    await act(async () => { await r.client.invalidateQueries({ queryKey: peopleKeys.members("b") }); });
    await waitFor(() => expect(screen.queryByTestId(/^obs-comment-delete-/)).not.toBeInTheDocument());
    expect(screen.queryByRole("option", { name: "b Person" })).not.toBeInTheDocument();
    expect(within(screen.getByTestId("obs-issue-assignee-pick")).getByRole("option", { name: /grace/ })).toBeInTheDocument();
  });

  it("keeps author delete available when membership role lookup is unknown", async () => {
    const r = await rig("grace");
    r.api.listOrgMembers = async () => roster("b", null);
    render(r.view("b"));
    expect(await screen.findByTestId(/^obs-comment-delete-/)).toBeInTheDocument();
    expect(await screen.findByTestId(/^obs-comment-edit-cmt-/)).toBeInTheDocument();
  });

  it("does not request a default org when explicit scope is absent", async () => {
    const r = await rig();
    const list = vi.spyOn(r.api, "listOrgMembers");
    render(r.view(""));
    await screen.findByTestId("obs-issue-timeline-list");
    expect(list).not.toHaveBeenCalled();
    expect(screen.queryByTestId(/^obs-comment-delete-/)).not.toBeInTheDocument();
  });
});
