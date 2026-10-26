/**
 * "Connect a machine" (research/NO_MACHINE_CHATS.md §2.3) and the
 * request_machine card that opens it (§3).
 *
 * Connecting is SetChatDaemon through chatStore.connectChatToMachine; the
 * server clears no_machine in the same write. The card shows the model's
 * reason until then, and says the machine is connected afterwards.
 */

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  daemons: [] as Array<{ daemonId: string; hostname: string; status: number }>,
  chat: undefined as { id: string; noMachine: boolean } | undefined,
}));
const store = vi.hoisted(() => ({ connectChatToMachine: vi.fn(async () => ({})) }));

vi.mock("@/hooks/useOnboardingQueries", () => ({
  useDaemonList: () => ({ data: state.daemons, isLoading: false }),
}));
vi.mock("@/hooks/chat-queries", () => ({
  useChat: () => ({ data: state.chat }),
}));
vi.mock("@/store/chatStore", () => ({
  useChatStore: Object.assign(vi.fn(), { getState: () => store }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("../../Machines/AddMachineSheet", () => ({
  AddMachineSheet: ({ open, onReady }: { open: boolean; onReady?: (m: { daemonId: string; name: string }) => void }) =>
    open ? (
      <button type="button" onClick={() => onReady?.({ daemonId: "new-machine", name: "cloud-small" })}>
        Set up your machine
      </button>
    ) : null,
}));

const { ConnectMachineDialog } = await import("../ConnectMachineDialog");
const { RequestMachineCard, requestMachineReason } = await import("../tool-renderers/RequestMachineToolRenderer");
const { NoMachinePill } = await import("../NoMachine");

// DaemonStatus: ACTIVE = 1, DISCONNECTED = 3, SUSPENDED = 5.
beforeEach(() => {
  store.connectChatToMachine.mockClear();
  state.daemons = [
    { daemonId: "d-off", hostname: "old-box", status: 3 },
    { daemonId: "d-sleep", hostname: "cloud", status: 5 },
    { daemonId: "d-on", hostname: "laptop", status: 1 },
  ];
  state.chat = { id: "chat-1", noMachine: true };
});

describe("ConnectMachineDialog", () => {
  it("preselects the best machine and connects the chat to it", async () => {
    const onClose = vi.fn();
    render(<ConnectMachineDialog chatId="chat-1" open onClose={onClose} />);

    const radios = screen.getAllByRole("radio");
    expect(radios.map((r) => r.textContent)).toEqual([
      expect.stringContaining("laptop"),
      expect.stringContaining("cloud"),
      expect.stringContaining("old-box"),
    ]);
    expect(radios[0]).toHaveAttribute("aria-checked", "true");

    fireEvent.click(screen.getByTestId("connect-machine-confirm"));
    await waitFor(() => expect(store.connectChatToMachine).toHaveBeenCalledWith("chat-1", "d-on"));
    expect(onClose).toHaveBeenCalled();
  });

  it("connects the machine the user picks, an asleep one included", async () => {
    render(<ConnectMachineDialog chatId="chat-1" open onClose={vi.fn()} />);
    fireEvent.click(screen.getByRole("radio", { name: /cloud/ }));
    expect(screen.getByRole("radio", { name: /cloud/ })).toHaveTextContent("wakes when you send");
    fireEvent.click(screen.getByTestId("connect-machine-confirm"));
    await waitFor(() => expect(store.connectChatToMachine).toHaveBeenCalledWith("chat-1", "d-sleep"));
  });

  it("says it is one-way", () => {
    render(<ConnectMachineDialog chatId="chat-1" open onClose={vi.fn()} />);
    expect(screen.getByRole("dialog")).toHaveTextContent("can't go back to no machine");
  });

  it("offers Add machine when the user has none, and connects the chat to the new one", async () => {
    state.daemons = [];
    render(<ConnectMachineDialog chatId="chat-1" open onClose={vi.fn()} />);
    expect(screen.queryByTestId("connect-machine-confirm")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Add machine" }));
    // The sheet hands back the machine it made; the chat moves onto it.
    fireEvent.click(screen.getByText("Set up your machine"));
    await vi.waitFor(() => expect(store.connectChatToMachine).toHaveBeenCalledWith("chat-1", "new-machine"));
  });
});

describe("request_machine card", () => {
  it("shows the model's reason and opens Connect a machine", () => {
    render(<RequestMachineCard chatId="chat-1" reason="Running the tests needs a checkout." />);
    expect(screen.getByTestId("request-machine-card")).toHaveTextContent("This needs a machine");
    expect(screen.getByText("Running the tests needs a checkout.")).toBeInTheDocument();

    fireEvent.click(screen.getByTestId("request-machine-connect"));
    expect(screen.getByRole("dialog")).toHaveTextContent("Connect a machine");
  });

  it("says the machine is connected once the chat has one, and drops the button", () => {
    state.chat = { id: "chat-1", noMachine: false };
    render(<RequestMachineCard chatId="chat-1" reason="Needs files." />);
    expect(screen.getByTestId("request-machine-card")).toHaveTextContent("Machine connected");
    expect(screen.queryByTestId("request-machine-connect")).toBeNull();
  });

  it("reads the reason from the call's input", () => {
    expect(requestMachineReason({ reason: "  edit the repo  " })).toBe("edit the repo");
    expect(requestMachineReason({})).toBe("");
    expect(requestMachineReason("{}")).toBe("");
  });
});

describe("No machine header pill", () => {
  it("names what the chat can use and opens Connect a machine", () => {
    render(<NoMachinePill chatId="chat-1" />);
    const pill = screen.getByTestId("no-machine-pill");
    expect(pill).toHaveTextContent("No machine · web & integrations");
    fireEvent.click(pill);
    expect(screen.getByRole("dialog")).toHaveTextContent("Connect a machine");
  });
});
