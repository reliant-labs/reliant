// Copyright (c) 2026 Reliant Labs

/**
 * GTM › Ideas over GtmService (a mock transport): the list reads the org's
 * ideas, the status tabs filter them, and "Ask the agent for ideas" opens a
 * chat with the prompt waiting, unsent, and creates no workflow. One idea's
 * page: Accept and Reject are SetIdeaStatus on the revision on screen, and a
 * draft edit writes the body back whole.
 */

import { create } from "@bufbuild/protobuf";
import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { GtmEvidenceSchema, GtmIdeaKind, GtmIdeaSchema, GtmIdeaSource, GtmIdeaStatus, GtmStrategyLibraryEntrySchema, GtmTailorAnswerSchema } from "@/gen/reliant/v1/gtm_pb";

import { renderGtm, TEST_ORG } from "../../shared/__tests__/gtmHarness";
import { IdeaDetailView } from "../IdeaDetailPage";
import { ASK_FOR_IDEAS_PROMPT } from "../AskForIdeas";
import { joinIdeaBody, splitIdeaBody } from "../ideaBody";
import { IdeasView } from "../IdeasPage";

const started = vi.hoisted(() => ({ calls: [] as unknown[] }));
vi.mock("../../../Forge/Observability/AskReliant/startAskReliantChat", () => ({
  startAskReliantChat: async (args: unknown) => {
    started.calls.push(args);
    return { worktreeId: "wt-1" };
  },
}));
vi.mock("@/store/projectStore", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/store/projectStore")>();
  return { ...actual, useProjectStore: Object.assign(() => null, { getState: () => ({ currentProject: { id: "p1", name: "site" } }) }) };
});
vi.mock("@/lib/toast-manager", () => ({ toast: { warning: vi.fn(), error: vi.fn(), success: vi.fn() } }));

afterEach(() => {
  cleanup();
  started.calls = [];
});

const proposed = create(GtmIdeaSchema, {
  id: "idea-1",
  orgId: TEST_ORG,
  kind: GtmIdeaKind.CONTENT,
  title: "Explain flows in one post",
  bodyMd: "Docs views are high but few create a flow.\n\n# The post\n\nBuild one in five minutes.",
  status: GtmIdeaStatus.PROPOSED,
  source: GtmIdeaSource.AGENT,
  revision: 2,
});
const accepted = create(GtmIdeaSchema, { ...proposed, id: "idea-2", title: "Launch post", status: GtmIdeaStatus.ACCEPTED, source: GtmIdeaSource.HUMAN });

const lists = {
  "GtmService.ListCampaigns": { response: { campaigns: [] } },
  "GtmService.ListItems": { response: { items: [] } },
  "GtmService.ListChannels": { response: { channels: [] } },
};

describe("ideaBody", () => {
  it("reads the first paragraph as why and the rest as the draft, and joins them back", () => {
    const body = splitIdeaBody(proposed.bodyMd);
    expect(body.why).toBe("Docs views are high but few create a flow.");
    expect(body.draft).toBe("# The post\n\nBuild one in five minutes.");
    expect(joinIdeaBody(body)).toBe(proposed.bodyMd);
    expect(splitIdeaBody("Only a reason.")).toEqual({ why: "Only a reason.", draft: "" });
  });
});

describe("IdeasView", () => {
  it("lists the org's ideas, and a status tab filters them", async () => {
    const list = vi.fn(() => ({ ideas: [proposed, accepted] }));
    renderGtm(<IdeasView />, { ...lists, "GtmService.ListIdeas": list }, { path: "/gtm/ideas?status=proposed", routePath: "/gtm/ideas" });
    expect(await screen.findByText("Explain flows in one post")).toBeInTheDocument();
    expect(screen.queryByText("Launch post")).not.toBeInTheDocument();
    expect(screen.getByText("Docs views are high but few create a flow.")).toBeInTheDocument();
    expect(list).toHaveBeenCalledWith(expect.objectContaining({ orgId: TEST_ORG }));
    expect(screen.queryByTestId("gtm-ideas-truncated")).not.toBeInTheDocument();
  });

  it("says when it shows only the first page", async () => {
    renderGtm(<IdeasView />, { ...lists, "GtmService.ListIdeas": { response: { ideas: [proposed], nextPageToken: "next" } } }, { path: "/gtm/ideas" });
    expect(await screen.findByTestId("gtm-ideas-truncated")).toHaveTextContent("Showing the first 200 ideas.");
  });

  it("asks the agent in a new chat, the prompt waiting unsent", async () => {
    renderGtm(<IdeasView />, { ...lists, "GtmService.ListIdeas": { response: { ideas: [] } } }, { path: "/gtm/ideas" });
    fireEvent.click(await screen.findByRole("button", { name: "Ask the agent for ideas" }));
    expect(screen.getByLabelText("Prompt")).toHaveValue(ASK_FOR_IDEAS_PROMPT);
    fireEvent.click(screen.getByRole("button", { name: "Open chat" }));
    await waitFor(() => expect(started.calls).toHaveLength(1));
    expect(started.calls[0]).toEqual(expect.objectContaining({ prompt: ASK_FOR_IDEAS_PROMPT, send: false, project: expect.objectContaining({ id: "p1" }) }));
  });
});

describe("IdeaDetailView", () => {
  const at = { path: "/gtm/ideas/idea-1", routePath: "/gtm/ideas/$ideaId" };

  it("accepts on the revision on screen", async () => {
    const setStatus = vi.fn(() => ({ idea: { ...proposed, status: GtmIdeaStatus.ACCEPTED, revision: 3 } }));
    renderGtm(<IdeaDetailView />, { ...lists, "GtmService.GetIdea": { response: { idea: proposed } }, "GtmService.SetIdeaStatus": setStatus }, at);
    fireEvent.click(await screen.findByRole("button", { name: "Accept" }));
    await waitFor(() => expect(setStatus).toHaveBeenCalled());
    expect(setStatus).toHaveBeenCalledWith(expect.objectContaining({ orgId: TEST_ORG, id: "idea-1", expectedRevision: 2, status: GtmIdeaStatus.ACCEPTED }));
  });

  it("rejects after a confirm", async () => {
    const setStatus = vi.fn(() => ({ idea: { ...proposed, status: GtmIdeaStatus.REJECTED } }));
    renderGtm(<IdeaDetailView />, { ...lists, "GtmService.GetIdea": { response: { idea: proposed } }, "GtmService.SetIdeaStatus": setStatus }, at);
    fireEvent.click(await screen.findByRole("button", { name: "Reject" }));
    const confirm = await screen.findAllByRole("button", { name: "Reject" });
    fireEvent.click(confirm[confirm.length - 1]!);
    await waitFor(() => expect(setStatus).toHaveBeenCalledWith(expect.objectContaining({ status: GtmIdeaStatus.REJECTED, expectedRevision: 2 })));
  });

  it("saves a draft edit with the rationale kept", async () => {
    const update = vi.fn(() => ({ idea: proposed }));
    renderGtm(<IdeaDetailView />, { ...lists, "GtmService.GetIdea": { response: { idea: proposed } }, "GtmService.UpdateIdea": update }, at);
    expect(await screen.findByText("Docs views are high but few create a flow.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    fireEvent.change(screen.getByLabelText("Draft"), { target: { value: "A new draft." } });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(update).toHaveBeenCalledWith(
      expect.objectContaining({ orgId: TEST_ORG, id: "idea-1", expectedRevision: 2, bodyMd: "Docs views are high but few create a flow.\n\nA new draft." }),
    );
  });

  it("marks an accepted idea done", async () => {
    const setStatus = vi.fn(() => ({ idea: { ...accepted, status: GtmIdeaStatus.DONE } }));
    renderGtm(<IdeaDetailView />, { ...lists, "GtmService.GetIdea": { response: { idea: accepted } }, "GtmService.SetIdeaStatus": setStatus }, at);
    fireEvent.click(await screen.findByRole("button", { name: "Mark done" }));
    await waitFor(() => expect(setStatus).toHaveBeenCalledWith(expect.objectContaining({ id: "idea-2", expectedRevision: 2, status: GtmIdeaStatus.DONE })));
  });

  it("shows evidence's markdown as text with emphasis, never raw markers or markup, and links only http(s)", async () => {
    const withEvidence = create(GtmIdeaSchema, {
      ...proposed,
      evidence: [
        create(GtmEvidenceSchema, { title: "Signups (sample)", summaryMd: "**1 in 5** came from _search_ <img src=x>", url: "https://analytics.example/r/1" }),
        create(GtmEvidenceSchema, { title: "A note", summaryMd: "unpaired ** stays", url: "javascript:alert(1)" }),
      ],
    });
    renderGtm(<IdeaDetailView />, { ...lists, "GtmService.GetIdea": { response: { idea: withEvidence } } }, at);
    const bold = await screen.findByText("1 in 5");
    expect(bold.tagName).toBe("STRONG");
    expect(screen.getByText("search").tagName).toBe("EM");
    expect(screen.queryByText(/\*\*1 in 5/)).not.toBeInTheDocument();
    expect(document.querySelector("img[src='x']")).toBeNull();
    expect(screen.getByText(/came from/).textContent).toContain("<img src=x>");
    expect(screen.getByText("unpaired ** stays")).toBeInTheDocument();
    const link = screen.getByRole("link", { name: "Signups (sample)" });
    expect(link).toHaveAttribute("href", "https://analytics.example/r/1");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    expect(screen.queryByRole("link", { name: "A note" })).not.toBeInTheDocument();
  });

  it("says when the items it looked through were only the newest page", async () => {
    renderGtm(
      <IdeaDetailView />,
      { ...lists, "GtmService.ListItems": { response: { items: [], nextPageToken: "more" } }, "GtmService.GetIdea": { response: { idea: accepted } } },
      at,
    );
    expect(await screen.findByTestId("gtm-idea-items-truncated")).toHaveTextContent("newest 500");
  });

  it("shows the library entry it started from, the tailor answers, and the suggested workflow", async () => {
    const fromBank = create(GtmIdeaSchema, {
      ...accepted,
      libraryKey: "sample-local-meetup",
      tailorAnswers: [create(GtmTailorAnswerSchema, { question: "Where do your users gather?", answer: "Two coworking spaces downtown." })],
      suggestedWorkflowMd: "Each month, draft the meetup invitation for a person to approve.",
    });
    const entry = create(GtmStrategyLibraryEntrySchema, { key: "sample-local-meetup", name: "Host a local meetup" });
    renderGtm(
      <IdeaDetailView />,
      { ...lists, "GtmService.GetIdea": { response: { idea: fromBank } }, "GtmService.GetStrategyLibraryEntry": { response: { entry } } },
      at,
    );
    const card = await screen.findByTestId("gtm-idea-library");
    const link = await within(card).findByRole("link", { name: "Host a local meetup" });
    expect(link).toHaveAttribute("href", "/gtm/strategy?view=library&entry=sample-local-meetup");
    expect(within(card).getByText("Where do your users gather?")).toBeInTheDocument();
    expect(within(card).getByText("Two coworking spaces downtown.")).toBeInTheDocument();
    expect(within(within(card).getByTestId("gtm-suggested-workflow")).getByRole("button", { name: "Create this workflow" })).toBeInTheDocument();
  });

  it("shows the raw key when the library no longer has the entry", async () => {
    const { ConnectError, Code } = await import("@connectrpc/connect");
    const gone = create(GtmIdeaSchema, { ...accepted, libraryKey: "sample-retired-play" });
    renderGtm(
      <IdeaDetailView />,
      { ...lists, "GtmService.GetIdea": { response: { idea: gone } }, "GtmService.GetStrategyLibraryEntry": { error: new ConnectError("no", Code.NotFound) } },
      at,
    );
    const entry = await screen.findByTestId("gtm-idea-library-entry");
    expect(await within(entry).findByText("sample-retired-play")).toBeInTheDocument();
    expect(within(entry).getByText("(no longer in the library)")).toBeInTheDocument();
    expect(within(entry).queryByRole("link")).not.toBeInTheDocument();
  });

  it("shows no library card for an idea that did not come from it", async () => {
    renderGtm(<IdeaDetailView />, { ...lists, "GtmService.GetIdea": { response: { idea: accepted } } }, at);
    await screen.findByRole("button", { name: "Mark done" });
    expect(screen.queryByTestId("gtm-idea-library")).not.toBeInTheDocument();
  });

  it("says when the idea is not there", async () => {
    const { ConnectError, Code } = await import("@connectrpc/connect");
    renderGtm(<IdeaDetailView />, { ...lists, "GtmService.GetIdea": { error: new ConnectError("no", Code.NotFound) } }, at);
    expect(await screen.findByText("Idea not found")).toBeInTheDocument();
  });
});
