/**
 * "Only from" edits the trigger's filter: adding and removing senders rewrites
 * its sender clause, "Me" adds the caller's own id from their connection, and
 * a filter that decides on the sender some other way is shown as custom and
 * never rewritten.
 */

import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";

import {
  ConnectionAuthKind,
  ConnectionSchema,
  ConnectionStatus,
  ListConnectionsResponseSchema,
} from "@/gen/reliant/v1/connection_pb";
import { renderWithQuery } from "@/test/renderWithQuery";

const listConnections = vi.fn();
vi.mock("@/api/grpc-client", () => ({
  grpcClient: { connection: () => ({ listConnections }) },
  getGRPCBaseURLPublic: () => null,
}));

// Hosted GitHub: no connection row, the login comes from the control plane's
// git credential.
vi.mock("@/hooks/useGitHubCredential", () => ({
  useGitHubCredential: () => ({ hasToken: true, accountLogin: "OctoCat", isLoading: false }),
}));

import { OnlyFromControl } from "../OnlyFromControl";

function connections(...items: Array<{ id: string; senderId: string; isDefault?: boolean }>) {
  return create(ListConnectionsResponseSchema, {
    connections: items.map((item) =>
      create(ConnectionSchema, {
        id: item.id,
        integrationId: "slack",
        authKind: ConnectionAuthKind.OAUTH2,
        name: item.id,
        senderId: item.senderId,
        status: ConnectionStatus.ACTIVE,
        isDefault: item.isDefault ?? false,
      }),
    ),
  });
}

/** The control over real filter state, reporting every write. */
function Harness({ integration, initial, onWrite }: { integration: string; initial: string; onWrite?: (filter: string) => void }) {
  const [filter, setFilter] = useState(initial);
  return (
    <>
      <OnlyFromControl
        integration={integration}
        filter={filter}
        onChange={(next) => {
          setFilter(next);
          onWrite?.(next);
        }}
      />
      <output aria-label="filter">{filter}</output>
    </>
  );
}

const filterText = () => screen.getByLabelText("filter").textContent;

describe("OnlyFromControl", () => {
  beforeEach(() => {
    listConnections.mockReset();
    listConnections.mockResolvedValue(connections({ id: "conn_work", senderId: "U0ME", isDefault: true }));
  });

  it("adds senders as a verified allowlist ANDed after the existing filter, and removes them", async () => {
    const user = userEvent.setup();
    renderWithQuery(<Harness integration="slack" initial="trigger.payload.data.channel == 'C0GEN'" />);

    await user.type(screen.getByLabelText("Add a sender"), "U123{Enter}");
    expect(filterText()).toBe(`(trigger.payload.data.channel == 'C0GEN') && trigger.sender.verified && trigger.sender.id in ["U123"]`);

    await user.type(screen.getByLabelText("Add a sender"), " U456 ");
    await user.click(screen.getByRole("button", { name: "Add" }));
    expect(filterText()).toBe(`(trigger.payload.data.channel == 'C0GEN') && trigger.sender.verified && trigger.sender.id in ["U123", "U456"]`);
    expect(screen.getByRole("list", { name: "Allowed senders" })).toHaveTextContent("U123U456");

    await user.click(screen.getByRole("button", { name: "Remove U123" }));
    await user.click(screen.getByRole("button", { name: "Remove U456" }));
    expect(filterText()).toBe("trigger.payload.data.channel == 'C0GEN'");
    expect(screen.getByText("Anyone. Add people to run only for them.")).toBeInTheDocument();
  });

  it("reads back the list it wrote", () => {
    renderWithQuery(<Harness integration="slack" initial={`trigger.sender.verified && trigger.sender.id in ["U1", "U2"]`} />);
    expect(screen.getByRole("list", { name: "Allowed senders" })).toHaveTextContent("U1U2");
  });

  it("'Me' adds the caller's sender id from their default connection", async () => {
    const user = userEvent.setup();
    listConnections.mockResolvedValue(
      connections({ id: "conn_other", senderId: "U0OTHER" }, { id: "conn_work", senderId: "U0ME", isDefault: true }),
    );
    renderWithQuery(<Harness integration="slack" initial="" />);

    const me = await screen.findByRole("button", { name: "Add me (U0ME)" });
    await user.click(me);
    expect(filterText()).toBe(`trigger.sender.verified && trigger.sender.id in ["U0ME"]`);
    expect(me).toBeDisabled();
    expect(listConnections.mock.calls[0]![0]).toMatchObject({ integrationId: "slack" });
  });

  it("'Me' says why when the connection does not know who you are", async () => {
    listConnections.mockResolvedValue(connections({ id: "conn_old", senderId: "", isDefault: true }));
    renderWithQuery(<Harness integration="slack" initial="" />);
    expect(await screen.findByText("Reconnect Slack to add yourself.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add me" })).toBeDisabled();
  });

  it("'Me' on GitHub falls back to the hosted account's login when there is no connection", async () => {
    const user = userEvent.setup();
    listConnections.mockResolvedValue(create(ListConnectionsResponseSchema, { connections: [] }));
    renderWithQuery(<Harness integration="github" initial="" />);
    await user.click(await screen.findByRole("button", { name: "Add me (octocat)" }));
    expect(filterText()).toBe(`trigger.sender.verified && trigger.sender.id in ["octocat"]`);
  });

  it("lowercases GitHub logins and email addresses the way trigger.sender carries them", async () => {
    const user = userEvent.setup();
    renderWithQuery(<Harness integration="gmail" initial="" />);
    await user.type(screen.getByLabelText("Add a sender"), "Boss@Example.com{Enter}");
    expect(filterText()).toBe(`trigger.sender.verified && trigger.sender.id in ["boss@example.com"]`);
  });

  it("shows a hand-written sender filter as custom and never rewrites it", async () => {
    const onWrite = vi.fn();
    renderWithQuery(<Harness integration="slack" initial={`trigger.sender.id == "U1"`} onWrite={onWrite} />);
    expect(screen.getByText(/Custom filter/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Add a sender")).not.toBeInTheDocument();
    await waitFor(() => expect(onWrite).not.toHaveBeenCalled());
    expect(filterText()).toBe(`trigger.sender.id == "U1"`);
  });

  it("explains Twilio instead of offering an allowlist SMS cannot back", () => {
    renderWithQuery(<Harness integration="twilio" initial="" />);
    expect(screen.getByText(/Twilio can't verify who sent a message/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Add a sender")).not.toBeInTheDocument();
  });

  it("renders nothing for a source with no person behind it", () => {
    renderWithQuery(<Harness integration="test_feed" initial="" />);
    expect(screen.queryByText("Only from")).not.toBeInTheDocument();
  });
});
