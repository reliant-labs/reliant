import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render } from "@testing-library/react";

import { useComposerHandoffStore } from "../../../store/composerHandoffStore";

const seen = vi.hoisted(() => ({ props: [] as Array<{ composerPrefill?: { text: string; id: number }; onChatCreated?: (id: string) => void }> }));
vi.mock("../NewChatView", () => ({
  NewChatView: (props: { composerPrefill?: { text: string; id: number }; onChatCreated?: (id: string) => void }) => {
    seen.props.push(props);
    return null;
  },
}));
const project = vi.hoisted(() => ({ id: "p1" as string | undefined }));
vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (selector: (state: { currentProject: { id: string } | null }) => unknown) => selector({ currentProject: project.id ? { id: project.id } : null }),
}));

import { MainNewChatView } from "../MainNewChatView";

const last = () => seen.props[seen.props.length - 1];

describe("MainNewChatView", () => {
  beforeEach(() => {
    seen.props.length = 0;
    project.id = "p1";
    useComposerHandoffStore.setState({ pending: null });
  });

  it("passes the host's own prefill through when nothing was handed off", () => {
    render(<MainNewChatView tabId="t" composerPrefill={{ text: "host", id: 3 }} />);
    expect(last().composerPrefill).toEqual({ text: "host", id: 3 });
  });

  it("puts a handed-off prompt in the composer prefill and consumes it", () => {
    useComposerHandoffStore.getState().offer({ projectId: "p1", worktreeId: "w1", text: "Issue ISS-1 in environment prod." });
    render(<MainNewChatView tabId="t" />);
    expect(last().composerPrefill).toMatchObject({ text: "Issue ISS-1 in environment prod." });
    expect(useComposerHandoffStore.getState().pending).toBeNull();
  });

  it("takes an offer made after it mounted", () => {
    render(<MainNewChatView tabId="t" />);
    expect(last().composerPrefill).toBeUndefined();
    act(() => useComposerHandoffStore.getState().offer({ projectId: "p1", worktreeId: "w1", text: "late" }));
    expect(last().composerPrefill?.text).toBe("late");
  });

  it("leaves an offer for another project alone", () => {
    useComposerHandoffStore.getState().offer({ projectId: "p2", worktreeId: "w1", text: "not yours" });
    render(<MainNewChatView tabId="t" />);
    expect(last().composerPrefill).toBeUndefined();
    expect(useComposerHandoffStore.getState().pending?.projectId).toBe("p2");
  });

  it("reports the chat's id to whoever made the offer, and to the host, when the user sends", () => {
    const offered = vi.fn();
    const host = vi.fn();
    useComposerHandoffStore.getState().offer({ projectId: "p1", worktreeId: "w1", text: "x", onChatCreated: offered });
    render(<MainNewChatView tabId="t" onChatCreated={host} />);
    act(() => last().onChatCreated?.("chat-1"));
    expect(offered).toHaveBeenCalledWith("chat-1");
    expect(host).toHaveBeenCalledWith("chat-1");
  });

  it("stops applying the prompt once the chat is created", () => {
    useComposerHandoffStore.getState().offer({ projectId: "p1", worktreeId: "w1", text: "x" });
    render(<MainNewChatView tabId="t" />);
    act(() => last().onChatCreated?.("chat-1"));
    expect(last().composerPrefill).toBeUndefined();
  });
});
