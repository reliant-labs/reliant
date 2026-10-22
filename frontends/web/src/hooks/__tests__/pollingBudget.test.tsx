/**
 * Request budget for one open chat.
 *
 * The server log counted ListDaemons at ~26/min and ListQueuedAgentMessages at
 * ~47/min from one founder's dev stack. Per window, this harness measured the
 * code that produced them at 13/min and 24/min: a 5s daemon poll plus a second
 * cache of the same list (useDaemonList's own key, polled by the OOM banner),
 * and a 2.5s mailbox poll whenever an agent was working. Two open windows
 * account for the rest.
 *
 * Staggered readers do NOT multiply within one window — React Query re-arms
 * every observer's interval whenever the query fetches, so they share a phase.
 * A second cache key for the same RPC does, which is why the daemon list now
 * has one.
 *
 * These tests mount the readers an open chat mounts, staggered the way
 * navigation staggers them, and count the RPCs issued over one steady-state
 * minute. They pin the budget, not the mechanism: freshness comes from push
 * (see the push-wiring tests), and the fallback poll must cost at most one
 * call per minute no matter how many readers are on screen.
 */

import type { ReactNode } from "react";
import { act, cleanup, render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { listDaemonsMock, listQueuedMock } = vi.hoisted(() => ({
  listDaemonsMock: vi.fn(),
  listQueuedMock: vi.fn(),
}));

vi.mock("@/api/grpc-client", () => ({
  grpcClient: { daemonRegistry: () => ({ listDaemons: listDaemonsMock }) },
}));
vi.mock("../../api/grpc-client", () => ({
  grpcClient: { daemonRegistry: () => ({ listDaemons: listDaemonsMock }) },
}));
vi.mock("../../api/chat-grpc", () => ({
  chatGrpc: { listQueuedAgentMessages: listQueuedMock },
}));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));

import { useDaemonStatus } from "../useDaemonStatus";
import { useQueuedAgentMessages } from "../queued-agent-messages";
import { OomKillBanner } from "../../components/Chat/OomKillBanner";

const MINUTE = 60_000;

function DaemonReader() {
  useDaemonStatus();
  return null;
}

function MailboxReader({ running }: { running: boolean }) {
  useQueuedAgentMessages("chat-1", "thread-1", running);
  return null;
}

let client: QueryClient;

function mount(ui: ReactNode) {
  render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

async function advance(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

/** RPCs issued during one minute, measured after every reader has mounted. */
async function callsOverOneMinute(mock: ReturnType<typeof vi.fn>): Promise<number> {
  const before = mock.mock.calls.length;
  await advance(MINUTE);
  return mock.mock.calls.length - before;
}

beforeEach(() => {
  vi.useFakeTimers();
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  listDaemonsMock.mockReset();
  listDaemonsMock.mockResolvedValue({ daemons: [] });
  listQueuedMock.mockReset();
  listQueuedMock.mockResolvedValue({ messages: [] });
});

afterEach(() => {
  cleanup();
  client.clear();
  vi.useRealTimers();
});

describe("polling budget for one open idle chat", () => {
  it("ListDaemons costs at most one call per minute however many readers mount", async () => {
    // App shell: ModernApp and the status dot render together.
    mount(<DaemonReader />);
    mount(<DaemonReader />);
    // The chat view arrives a beat later with its OOM banner.
    await advance(1_300);
    mount(<OomKillBanner />);
    // Then the viewer panel and the detected-ports chip.
    await advance(1_400);
    mount(<DaemonReader />);
    mount(<DaemonReader />);

    expect(await callsOverOneMinute(listDaemonsMock)).toBeLessThanOrEqual(1);
  });

  it("ListQueuedAgentMessages costs at most one call per minute while the agent runs", async () => {
    // ChatPresenter (the strip) and ChatInput (the composer) both read the
    // same mailbox, and mount a render apart.
    mount(<MailboxReader running />);
    await advance(400);
    mount(<MailboxReader running />);

    expect(await callsOverOneMinute(listQueuedMock)).toBeLessThanOrEqual(1);
  });

  it("ListQueuedAgentMessages is not polled at all while the agent is idle", async () => {
    mount(<MailboxReader running={false} />);
    mount(<MailboxReader running={false} />);
    await advance(400);

    expect(await callsOverOneMinute(listQueuedMock)).toBe(0);
  });
});
