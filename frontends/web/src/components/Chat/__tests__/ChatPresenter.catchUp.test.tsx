/**
 * Opening a chat renders whatever is cached, then the stream delivers what
 * changed (a snapshot, or a replay) and ends with chat_caught_up. Until then
 * the timeline is covered: a "Loading conversation…" indicator over the
 * blurred, non-interactive stale transcript. It waits before appearing, so a
 * cached switch that catches up at once shows nothing, and once up it holds
 * long enough not to flicker. A caught-up chat — streaming or not — never
 * shows it.
 */

import { act, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactElement } from "react";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { ChatPresenter } from "../ChatPresenter";
import type { Message } from "../../../api/client";
import {
  CATCH_UP_MIN_VISIBLE_MS,
  CATCH_UP_SHOW_DELAY_MS,
} from "../../../lib/timelineCatchUp";

const scrollTimelineToBottom = vi.fn();

vi.mock("../ChatInputWrapper", () => ({
  ChatInputWrapper: () => <div data-testid="chat-input" />,
}));
vi.mock("../ChatThinkingIndicator", () => ({ ChatThinkingIndicator: () => null }));
vi.mock("../ScrollToBottomButton", () => ({ ScrollToBottomButton: () => null }));
vi.mock("../PermissionsPanelWrapper", () => ({
  PermissionsPanelWrapper: ({ children }: { children?: React.ReactNode }) => (
    <>{children}</>
  ),
}));
vi.mock("../PermissionsPanel", () => ({ PermissionsPanel: () => null }));
vi.mock("../ChatHeader", () => ({ ChatHeader: () => <div data-testid="chat-header" /> }));
vi.mock("../ResumeDaemonPill", () => ({ ResumeDaemonPill: () => null }));
vi.mock("../OomKillBanner", () => ({ OomKillBanner: () => null }));
vi.mock("../DiskPressureBanner", () => ({ DiskPressureBanner: () => null }));
vi.mock("../BackgroundWorkPill", () => ({ BackgroundWorkPill: () => null }));

// ChatPresenter reads the chat's pending questions to tell whether the thread
// in view is waiting on the user; none is pending here.
vi.mock("../../../hooks/approval-queries", async () => {
  const actual = await vi.importActual<
    typeof import("../../../hooks/approval-queries")
  >("../../../hooks/approval-queries");
  return { ...actual, usePendingQuestions: () => ({ data: [] }) };
});
// A running chat reads its machine; none is needed here.
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useDaemonList: () => ({ data: [], isLoading: false }),
}));
vi.mock("../QueuedMessages", () => ({ QueuedMessages: () => null }));
vi.mock("../../../hooks/queued-agent-messages", () => ({
  useQueuedAgentMessages: () => ({ messages: [], refresh: vi.fn(), forget: vi.fn() }),
}));
// The real timeline hands the presenter its "jump to the end" callback.
vi.mock("../thread-views", () => ({
  InterleavedTimeline: ({ onResumeFollow }: { onResumeFollow?: (cb: () => void) => void }) => {
    onResumeFollow?.(scrollTimelineToBottom);
    return <button data-testid="timeline">transcript</button>;
  },
}));
// The presenter reads the chat row; this suite needs none of it.
vi.mock("../../../hooks/chat-queries", async () => {
  const actual = await vi.importActual<
    typeof import("../../../hooks/chat-queries")
  >("../../../hooks/chat-queries");
  return { ...actual, useChat: () => ({ data: undefined }) };
});
vi.mock("../../workflow/WorkflowViewerPanel", () => ({
  WorkflowViewerPanel: () => null,
}));

const cached = [{ id: "m1" } as Message];

interface Props {
  catchingUp: boolean;
  messages?: Message[];
  busy?: boolean;
}

function presenter({ catchingUp, messages = cached, busy = false }: Props): ReactElement {
  return (
    <SurfaceProvider surface="desktop">
      <ChatPresenter
        messages={messages}
        approvals={[]}
        errorEvents={[]}
        infoEvents={[]}
        runOutputs={[]}
        chatId="chat-1"
        isChatBusy={busy}
        pendingApprovals={[]}
        connectionStatus="connected"
        projectId="project-1"
        onSendMessage={vi.fn(async () => {})}
        onStopStreaming={vi.fn(async () => {})}
        isTimelineCatchingUp={catchingUp}
      />
    </SurfaceProvider>
  );
}

function setup(initial: Props) {
  const view = renderWithQuery(presenter(initial));
  return { rerender: (next: Props) => view.rerender(presenter(next)) };
}

function advance(ms: number) {
  act(() => {
    vi.advanceTimersByTime(ms);
  });
}

const overlay = () => screen.queryByTestId("timeline-catchup-overlay");
const frame = () => screen.getByTestId("chat-messages");
const status = () => screen.getByTestId("timeline-catchup-status");

beforeEach(() => {
  vi.useFakeTimers();
  scrollTimelineToBottom.mockClear();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("ChatPresenter timeline catch-up overlay", () => {
  it("shows nothing for a cached switch that catches up before the delay", () => {
    const { rerender } = setup({ catchingUp: true });

    advance(CATCH_UP_SHOW_DELAY_MS - 50);
    rerender({ catchingUp: false });
    advance(1000);

    expect(overlay()).not.toBeInTheDocument();
    expect(frame()).not.toHaveAttribute("aria-busy");
    expect(status()).toBeEmptyDOMElement();
  });

  it("appears after the delay while catching up, over a blurred, inert transcript", () => {
    setup({ catchingUp: true });
    expect(overlay()).not.toBeInTheDocument();

    advance(CATCH_UP_SHOW_DELAY_MS);

    expect(overlay()).toBeInTheDocument();
    expect(overlay()).toHaveClass("backdrop-blur-xs");
    expect(frame()).toHaveAttribute("aria-busy", "true");
    expect(status()).toHaveTextContent("Loading conversation…");
    // The stale transcript stays rendered underneath, but out of reach.
    const transcript = screen.getByTestId("timeline");
    expect(transcript.closest("[inert]")).not.toBeNull();
    // The composer is not covered.
    expect(screen.getByTestId("chat-input").closest("[inert]")).toBeNull();
  });

  it("shows only the indicator for a cold open with nothing to blur", () => {
    setup({ catchingUp: true, messages: [] });

    advance(CATCH_UP_SHOW_DELAY_MS);

    expect(overlay()).toBeInTheDocument();
    expect(overlay()).not.toHaveClass("backdrop-blur-xs");
  });

  it("lifts once caught up, after holding long enough not to flicker, and lands at the bottom", () => {
    const { rerender } = setup({ catchingUp: true });
    advance(CATCH_UP_SHOW_DELAY_MS);
    expect(overlay()).toBeInTheDocument();

    rerender({ catchingUp: false });
    advance(CATCH_UP_MIN_VISIBLE_MS - 50);
    expect(overlay()).toBeInTheDocument();

    advance(50);
    expect(overlay()).not.toBeInTheDocument();
    expect(frame()).not.toHaveAttribute("aria-busy");
    expect(screen.getByTestId("timeline").closest("[inert]")).toBeNull();
    expect(status()).toBeEmptyDOMElement();
    expect(scrollTimelineToBottom).toHaveBeenCalledTimes(1);
  });

  it("stays up without a gap when catch-up resumes during the hold", () => {
    const { rerender } = setup({ catchingUp: true });
    advance(CATCH_UP_SHOW_DELAY_MS);

    rerender({ catchingUp: false });
    advance(100);
    rerender({ catchingUp: true });
    advance(1000);

    expect(overlay()).toBeInTheDocument();
    expect(scrollTimelineToBottom).not.toHaveBeenCalled();
  });

  it("never appears while a caught-up chat streams", () => {
    const { rerender } = setup({ catchingUp: false, busy: true });

    // Tokens and tool events keep landing; the chat stays caught up.
    for (let i = 2; i < 12; i++) {
      rerender({
        catchingUp: false,
        busy: true,
        messages: [...cached, { id: `m${i}` } as Message],
      });
      advance(100);
    }

    expect(overlay()).not.toBeInTheDocument();
    expect(frame()).not.toHaveAttribute("aria-busy");
    expect(scrollTimelineToBottom).not.toHaveBeenCalled();
  });
});
