/**
 * "Only from" edits the trigger's filter: adding and removing senders rewrites
 * its sender clause, "Me" adds the caller's own id from their connection, and
 * a filter that decides on the sender some other way is shown as custom and
 * never rewritten.
 *
 * On GitHub the list stores numeric user ids and shows logins: a typed login
 * is resolved to its id when it is added, ids are named from what is already
 * known (Me, the trigger's firings) and otherwise looked up, and a login left
 * from before is flagged as matching nobody.
 */

import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  ConnectionAuthKind,
  ConnectionSchema,
  ConnectionStatus,
  ListConnectionsResponseSchema,
} from "@/gen/reliant/v1/connection_pb";
import {
  ListTriggerEventsResponseSchema,
  ResolvedTriggerSenderSchema,
  ResolveTriggerSendersResponseSchema,
  TriggerEventSchema,
  TriggerSenderSchema,
} from "@/gen/reliant/v1/trigger_pb";
import { renderWithQuery } from "@/test/renderWithQuery";

const listConnections = vi.fn();
const resolveTriggerSenders = vi.fn();
const listTriggerEvents = vi.fn();
vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    connection: () => ({ listConnections }),
    trigger: () => ({ resolveTriggerSenders, listTriggerEvents }),
  },
  getGRPCBaseURLPublic: () => null,
}));

// Hosted GitHub: no connection row; the account comes from the control
// plane's git credential, which reports a login and (today) no user id.
const credential = vi.hoisted(() => ({ value: { hasToken: true, accountLogin: "OctoCat", accountId: undefined as string | undefined, isLoading: false } }));
vi.mock("@/hooks/useGitHubCredential", () => ({
  useGitHubCredential: () => credential.value,
}));

import { OnlyFromControl } from "../OnlyFromControl";

function connections(...items: Array<{ id: string; senderId: string; isDefault?: boolean; integrationId?: string; accountLabel?: string }>) {
  return create(ListConnectionsResponseSchema, {
    connections: items.map((item) =>
      create(ConnectionSchema, {
        id: item.id,
        integrationId: item.integrationId ?? "slack",
        authKind: ConnectionAuthKind.OAUTH2,
        name: item.id,
        accountLabel: item.accountLabel ?? "",
        senderId: item.senderId,
        status: ConnectionStatus.ACTIVE,
        isDefault: item.isDefault ?? false,
      }),
    ),
  });
}

const noConnections = () => create(ListConnectionsResponseSchema, { connections: [] });

function resolved(...people: Array<{ query: string; senderId: string; displayName: string }>) {
  return create(ResolveTriggerSendersResponseSchema, { senders: people.map((p) => create(ResolvedTriggerSenderSchema, p)) });
}

/** The control over real filter state, reporting every write. */
function Harness({
  integration,
  initial,
  onWrite,
  triggerId,
}: {
  integration: string;
  initial: string;
  onWrite?: (filter: string) => void;
  triggerId?: string;
}) {
  const [filter, setFilter] = useState(initial);
  return (
    <>
      <OnlyFromControl
        integration={integration}
        filter={filter}
        triggerId={triggerId}
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
const allowed = () => screen.getByRole("list", { name: "Allowed senders" });

describe("OnlyFromControl", () => {
  beforeEach(() => {
    listConnections.mockReset();
    resolveTriggerSenders.mockReset();
    listTriggerEvents.mockReset();
    listConnections.mockResolvedValue(connections({ id: "conn_work", senderId: "U0ME", isDefault: true }));
    resolveTriggerSenders.mockResolvedValue(resolved());
    listTriggerEvents.mockResolvedValue(create(ListTriggerEventsResponseSchema, { events: [] }));
    credential.value = { hasToken: true, accountLogin: "OctoCat", accountId: undefined, isLoading: false };
  });

  it("adds senders as a verified allowlist ANDed after the existing filter, and removes them", async () => {
    const user = userEvent.setup();
    renderWithQuery(<Harness integration="slack" initial="trigger.payload.data.channel == 'C0GEN'" />);

    await user.type(screen.getByLabelText("Add a sender"), "U123{Enter}");
    expect(filterText()).toBe(`(trigger.payload.data.channel == 'C0GEN') && trigger.sender.verified && trigger.sender.id in ["U123"]`);

    await user.type(screen.getByLabelText("Add a sender"), " U456 ");
    await user.click(screen.getByRole("button", { name: "Add" }));
    expect(filterText()).toBe(`(trigger.payload.data.channel == 'C0GEN') && trigger.sender.verified && trigger.sender.id in ["U123", "U456"]`);
    expect(allowed()).toHaveTextContent("U123U456");

    await user.click(screen.getByRole("button", { name: "Remove U123" }));
    await user.click(screen.getByRole("button", { name: "Remove U456" }));
    expect(filterText()).toBe("trigger.payload.data.channel == 'C0GEN'");
    expect(screen.getByText("Anyone. Add people to run only for them.")).toBeInTheDocument();
    expect(resolveTriggerSenders).not.toHaveBeenCalled();
  });

  it("reads back the list it wrote", () => {
    renderWithQuery(<Harness integration="slack" initial={`trigger.sender.verified && trigger.sender.id in ["U1", "U2"]`} />);
    expect(allowed()).toHaveTextContent("U1U2");
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

  it("lowercases email addresses the way trigger.sender carries them", async () => {
    const user = userEvent.setup();
    renderWithQuery(<Harness integration="gmail" initial="" />);
    await user.type(screen.getByLabelText("Add a sender"), "Boss@Example.com{Enter}");
    expect(filterText()).toBe(`trigger.sender.verified && trigger.sender.id in ["boss@example.com"]`);
  });

  describe("on GitHub", () => {
    it("stores user ids and shows the people by login", async () => {
      const user = userEvent.setup();
      listConnections.mockResolvedValue(noConnections());
      resolveTriggerSenders.mockResolvedValue(resolved({ query: "583231", senderId: "583231", displayName: "octocat" }));
      renderWithQuery(<Harness integration="github" initial={`trigger.sender.verified && trigger.sender.id in ["583231"]`} />);

      expect(await screen.findByText("octocat")).toBeInTheDocument();
      expect(allowed()).not.toHaveTextContent("583231");
      expect(resolveTriggerSenders.mock.calls[0]![0]).toMatchObject({ integration: "github", handles: [], senderIds: ["583231"] });

      // Removing by the name shown takes the id out of the filter.
      await user.click(screen.getByRole("button", { name: "Remove octocat" }));
      expect(filterText()).toBe("");
    });

    it("resolves a typed login to its user id when it is added, and shows the login", async () => {
      const user = userEvent.setup();
      listConnections.mockResolvedValue(noConnections());
      resolveTriggerSenders.mockResolvedValue(resolved({ query: "OctoCat", senderId: "583231", displayName: "octocat" }));
      renderWithQuery(<Harness integration="github" initial="" />);

      await user.type(screen.getByLabelText("Add a sender"), "@OctoCat{Enter}");
      await waitFor(() => expect(filterText()).toBe(`trigger.sender.verified && trigger.sender.id in ["583231"]`));
      expect(resolveTriggerSenders.mock.calls[0]![0]).toMatchObject({ integration: "github", handles: ["OctoCat"], senderIds: [] });
      expect(allowed()).toHaveTextContent("octocat");
      expect(screen.getByLabelText("Add a sender")).toHaveValue("");
      // The name came with the id: nothing more to look up.
      expect(resolveTriggerSenders).toHaveBeenCalledTimes(1);
    });

    it("adds nobody for a login GitHub does not know, and says so", async () => {
      const user = userEvent.setup();
      listConnections.mockResolvedValue(noConnections());
      renderWithQuery(<Harness integration="github" initial="" />);

      await user.type(screen.getByLabelText("Add a sender"), "no-such-person{Enter}");
      expect(await screen.findByRole("alert")).toHaveTextContent("No GitHub user is named no-such-person.");
      expect(filterText()).toBe("");
      expect(screen.getByLabelText("Add a sender")).toHaveValue("no-such-person");
    });

    it("shows why a login cannot be looked up, and adds nobody", async () => {
      const user = userEvent.setup();
      listConnections.mockResolvedValue(noConnections());
      resolveTriggerSenders.mockRejectedValue(
        new ConnectError("connect GitHub (or reconnect it) in Settings to look people up by their GitHub login", Code.FailedPrecondition),
      );
      renderWithQuery(<Harness integration="github" initial="" />);

      await user.type(screen.getByLabelText("Add a sender"), "octocat{Enter}");
      expect(await screen.findByRole("alert")).toHaveTextContent(/connect GitHub/);
      expect(filterText()).toBe("");
    });

    it("'Me' is the connection's user id, shown by its login", async () => {
      const user = userEvent.setup();
      listConnections.mockResolvedValue(
        connections({ id: "conn_gh", integrationId: "github", senderId: "583231", accountLabel: "octocat", isDefault: true }),
      );
      renderWithQuery(<Harness integration="github" initial="" />);

      await user.click(await screen.findByRole("button", { name: "Add me (octocat)" }));
      expect(filterText()).toBe(`trigger.sender.verified && trigger.sender.id in ["583231"]`);
      expect(allowed()).toHaveTextContent("octocat");
      expect(resolveTriggerSenders).not.toHaveBeenCalled();
    });

    it("'Me' is not a connection's stored login from before senders were ids", async () => {
      listConnections.mockResolvedValue(connections({ id: "conn_gh", integrationId: "github", senderId: "octocat", isDefault: true }));
      credential.value = { hasToken: false, accountLogin: undefined as unknown as string, accountId: undefined, isLoading: false };
      renderWithQuery(<Harness integration="github" initial="" />);
      expect(await screen.findByText("Reconnect GitHub to add yourself.")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Add me" })).toBeDisabled();
    });

    it("'Me' is not offered from a hosted account whose user id control-plane does not report", async () => {
      listConnections.mockResolvedValue(noConnections());
      renderWithQuery(<Harness integration="github" initial="" />);
      expect(await screen.findByText(/doesn't report your GitHub user id yet/)).toHaveTextContent("Add your login (OctoCat) instead.");
      expect(screen.getByRole("button", { name: "Add me" })).toBeDisabled();
    });

    it("'Me' is the hosted account's user id once control-plane reports it", async () => {
      const user = userEvent.setup();
      listConnections.mockResolvedValue(noConnections());
      credential.value = { hasToken: true, accountLogin: "OctoCat", accountId: "583231", isLoading: false };
      renderWithQuery(<Harness integration="github" initial="" />);
      await user.click(await screen.findByRole("button", { name: "Add me (OctoCat)" }));
      expect(filterText()).toBe(`trigger.sender.verified && trigger.sender.id in ["583231"]`);
    });

    it("names ids from the trigger's own firings before asking GitHub", async () => {
      listConnections.mockResolvedValue(noConnections());
      listTriggerEvents.mockResolvedValue(
        create(ListTriggerEventsResponseSchema, {
          events: [
            create(TriggerEventSchema, {
              id: "ev1",
              sender: create(TriggerSenderSchema, { kind: "github", id: "583231", displayName: "octocat", verified: true }),
            }),
          ],
        }),
      );
      renderWithQuery(<Harness integration="github" triggerId="trig-1" initial={`trigger.sender.verified && trigger.sender.id in ["583231"]`} />);
      expect(await screen.findByText("octocat")).toBeInTheDocument();
      expect(listTriggerEvents.mock.calls[0]![0]).toMatchObject({ triggerId: "trig-1" });
      expect(resolveTriggerSenders).not.toHaveBeenCalled();
    });

    it("flags a login left in the list from before as matching nobody", async () => {
      listConnections.mockResolvedValue(noConnections());
      renderWithQuery(<Harness integration="github" initial={`trigger.sender.verified && trigger.sender.id in ["octocat"]`} />);
      expect(allowed()).toHaveTextContent("@octocat");
      expect(screen.getByText(/the logins marked here match nobody/)).toBeInTheDocument();
      await waitFor(() => expect(listConnections).toHaveBeenCalled());
      expect(resolveTriggerSenders).not.toHaveBeenCalled();
    });
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
