/**
 * The live card a backgrounded shell call renders: the process an agent
 * started, where it started it — its state, the newest lines of its output,
 * and Stop, which used to be the agent's alone (shell_kill).
 */

import { fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { MessageInitShape } from "@bufbuild/protobuf";
import type { BackgroundProcessSchema } from "../../../gen/reliant/v1/background_pb";
import { BackgroundProcessStatus } from "../../../hooks/background-queries";
import { setTransport } from "../../../lib/connect";
import { mockTransport } from "../../../lib/test-utils";
import { queryClient } from "../../../lib/query-client";

const { streamMock } = vi.hoisted(() => ({ streamMock: vi.fn() }));
vi.mock("../../../hooks/useProcessOutputStream", () => ({
  useProcessOutputStream: (...args: unknown[]) => streamMock(...args),
}));

import { LiveProcessCard, LIVE_CARD_TAIL_LINES } from "../LiveProcessCard";

let processes: MessageInitShape<typeof BackgroundProcessSchema>[] = [];
const killProcess = vi.fn((_req: unknown) => ({ message: "ok" }));

function renderCard(processId = "proc-1") {
  // The card's refresh and the stream's cache patches use the app's client.
  return render(
    <QueryClientProvider client={queryClient}>
      <LiveProcessCard processId={processId} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  processes = [];
  killProcess.mockClear();
  streamMock.mockReturnValue({ lines: [], isLoading: false, error: null, isComplete: false, restart: vi.fn() });
  setTransport(
    mockTransport({
      "BackgroundService.ListProcesses": () => ({ processes }),
      "BackgroundService.KillProcess": (req: unknown) => killProcess(req),
    }),
  );
});

afterEach(() => {
  queryClient.clear();
});

describe("LiveProcessCard", () => {
  it("shows a running process with its newest output, read as plain text", async () => {
    processes = [
      {
        id: "proc-1",
        command: "npm run dev",
        status: BackgroundProcessStatus.RUNNING,
        startTime: new Date().toISOString(),
        chatId: "chat-1",
        daemonId: "machine-2",
        ports: [{ port: 5173, protocol: "tcp", state: "LISTEN", address: "" }],
      },
    ];
    streamMock.mockReturnValue({
      lines: [
        { type: "stdout", text: "\u001b[32mVITE\u001b[0m ready", sequence: 1 },
        { type: "stderr", text: "warning: x", sequence: 2 },
      ],
      isLoading: false,
      error: null,
      isComplete: false,
      restart: vi.fn(),
    });

    renderCard();

    expect(await screen.findByTestId("live-process-card")).toHaveTextContent("Running");
    expect(screen.getByText("VITE ready")).toBeInTheDocument();
    expect(screen.getByText("warning: x")).toBeInTheDocument();
    // The tail streams from the process's own machine, a few lines only.
    expect(streamMock).toHaveBeenLastCalledWith("proc-1", expect.objectContaining({ daemonId: "machine-2", maxLines: LIVE_CARD_TAIL_LINES, enabled: true }));
  });

  it("stops the process on the machine it runs on", async () => {
    processes = [
      { id: "proc-1", command: "npm run dev", status: BackgroundProcessStatus.RUNNING, startTime: new Date().toISOString(), daemonId: "machine-2" },
    ];

    renderCard();
    fireEvent.click(await screen.findByRole("button", { name: "Stop process" }));

    await waitFor(() => expect(killProcess).toHaveBeenCalled());
    expect(killProcess.mock.calls[0]![0]).toMatchObject({ processId: "proc-1", daemonId: "machine-2" });
  });

  it("offers no Stop for a finished process and says how it ended", async () => {
    processes = [
      {
        id: "proc-1",
        command: "go test ./...",
        status: BackgroundProcessStatus.COMPLETED,
        exitCode: 1,
        startTime: new Date().toISOString(),
        endTime: new Date().toISOString(),
      },
    ];

    renderCard();

    expect(await screen.findByTestId("live-process-card")).toHaveTextContent("Exit 1");
    expect(screen.queryByRole("button", { name: "Stop process" })).toBeNull();
  });

  // The list may predate the process (it started after the last read): the
  // card reads it again once before saying the process is gone.
  it("says a process its machine no longer tracks is gone, after reading the list again", async () => {
    let lists = 0;
    setTransport(
      mockTransport({
        "BackgroundService.ListProcesses": () => {
          lists++;
          return { processes: [] };
        },
      }),
    );

    renderCard("gone-123456");

    expect(await screen.findByText(/no longer tracked/)).toBeInTheDocument();
    expect(lists).toBeGreaterThanOrEqual(2);
    expect(streamMock).toHaveBeenLastCalledWith("gone-123456", expect.objectContaining({ enabled: false }));
  });
});
