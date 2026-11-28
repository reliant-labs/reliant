// Copyright (c) 2026 Reliant Labs

/**
 * GTM › Channels over GtmService (a mock transport): integrated and manual
 * channels apart, your connection for each integrated one and Connect or
 * Manage through the Connectors page, an agent-added channel badged, usage
 * counts; a new channel and an edit (on the revision read, channel types
 * whole), and a key another channel holds.
 */

import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  GtmCampaignSchema,
  GtmChannelMode,
  GtmChannelSchema,
  GtmChannelSource,
  GtmChannelStatus,
  GtmConnectionState,
  GtmItemSchema,
  type UpdateChannelRequest,
} from "@/gen/reliant/v1/gtm_pb";

import { renderGtm, TEST_ORG } from "../../shared/__tests__/gtmHarness";
import { ChannelsView } from "../ChannelsPage";

const toasts = vi.hoisted(() => ({ success: vi.fn(), warning: vi.fn(), error: vi.fn() }));
vi.mock("@/lib/toast-manager", () => ({ toast: toasts }));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const email = create(GtmChannelSchema, {
  id: "ch-email",
  orgId: TEST_ORG,
  key: "email",
  name: "Email",
  mode: GtmChannelMode.INTEGRATED,
  integrationKind: "gmail",
  status: GtmChannelStatus.ACTIVE,
  connectionState: GtmConnectionState.NOT_CONNECTED,
  source: GtmChannelSource.SEED,
  revision: 1,
});
const hn = create(GtmChannelSchema, {
  id: "ch-hn",
  orgId: TEST_ORG,
  key: "hacker-news",
  name: "Hacker News",
  mode: GtmChannelMode.MANUAL,
  manualInstructionsMd: "Post a Show HN by hand.",
  status: GtmChannelStatus.ACTIVE,
  source: GtmChannelSource.SEED,
  channelTypes: ["forum"],
  revision: 3,
});
const discord = create(GtmChannelSchema, {
  id: "ch-discord",
  orgId: TEST_ORG,
  key: "discord",
  name: "Discord",
  mode: GtmChannelMode.MANUAL,
  status: GtmChannelStatus.PAUSED,
  source: GtmChannelSource.AGENT,
  channelTypes: ["community"],
  revision: 1,
});

const reads = {
  "GtmService.ListChannels": { response: { channels: [email, hn, discord] } },
  "GtmService.ListItems": {
    response: {
      items: [
        create(GtmItemSchema, { id: "it-1", orgId: TEST_ORG, channelId: "ch-hn" }),
        create(GtmItemSchema, { id: "it-2", orgId: TEST_ORG, channelId: "ch-hn" }),
        create(GtmItemSchema, { id: "it-3", orgId: TEST_ORG, channelId: "ch-email" }),
      ],
    },
  },
  "GtmService.ListCampaigns": { response: { campaigns: [create(GtmCampaignSchema, { id: "cmp-1", orgId: TEST_ORG, plannedChannelIds: ["ch-hn", "ch-email"] })] } },
};

const at = { path: "/gtm/channels" };

describe("the channel lists", () => {
  it("splits integrated from manual, with your connection and Connect through the Connectors page", async () => {
    renderGtm(<ChannelsView />, reads, at);
    const integrated = await screen.findByTestId("gtm-channels-integrated");
    await within(integrated).findByText("Email");
    expect(within(integrated).queryByText("Hacker News")).not.toBeInTheDocument();
    expect(within(integrated).getByText("Not connected")).toBeInTheDocument();
    expect(within(integrated).getByText("Gmail")).toBeInTheDocument();
    expect(within(integrated).getByText("Only a person")).toBeInTheDocument();
    expect(within(integrated).getByRole("columnheader", { name: "Status" })).toBeInTheDocument();
    expect(within(integrated).getByText("Active")).toBeInTheDocument();
    const connect = within(integrated).getByRole("link", { name: "Connect Gmail for Email" });
    expect(connect.getAttribute("href")).toMatch(/\/connectors$/);

    const manual = screen.getByTestId("gtm-channels-manual");
    expect(within(manual).getByText("Hacker News")).toBeInTheDocument();
    expect(within(manual).getByText("Post a Show HN by hand.")).toBeInTheDocument();
    expect(within(manual).getByText("forum")).toBeInTheDocument();
    expect(within(manual).getByText("Paused")).toBeInTheDocument();
    expect(within(manual).queryByText("Email")).not.toBeInTheDocument();
    expect(await within(manual).findByText("2 items · 1 campaign")).toBeInTheDocument();
  });

  it("offers Manage once you are connected", async () => {
    const connected = { ...email, connectionState: GtmConnectionState.CONNECTED };
    renderGtm(<ChannelsView />, { ...reads, "GtmService.ListChannels": { response: { channels: [connected] } } }, at);
    const integrated = await screen.findByTestId("gtm-channels-integrated");
    expect(await within(integrated).findByText("Connected")).toBeInTheDocument();
    expect(within(integrated).getByRole("link", { name: "Manage Gmail for Email" }).getAttribute("href")).toMatch(/\/connectors$/);
    expect(within(integrated).queryByRole("link", { name: /^Connect/ })).not.toBeInTheDocument();
  });

  it("badges a channel an agent added", async () => {
    renderGtm(<ChannelsView />, reads, at);
    const manual = await screen.findByTestId("gtm-channels-manual");
    await within(manual).findByText("Discord");
    expect(within(manual).getAllByText("Added by agent")).toHaveLength(1);
    expect(within(manual).getAllByText("Default")).toHaveLength(1);
    // The tag sits on the name's line (the name truncates first), not on a line of its own.
    const name = within(manual).getByText("Discord");
    expect(name.parentElement).toContainElement(within(manual).getByText("Added by agent"));
  });
});

describe("writing a channel", () => {
  it("adds a new manual channel, its key from its name", async () => {
    const createC = vi.fn(() => ({ channel: { ...discord, id: "ch-new" } }));
    renderGtm(<ChannelsView />, { ...reads, "GtmService.CreateChannel": createC }, at);
    fireEvent.click(await screen.findByRole("button", { name: "New channel" }));
    const form = await screen.findByTestId("gtm-channel-form");
    fireEvent.change(within(form).getByLabelText("Name"), { target: { value: "Indie Hackers!" } });
    expect(within(form).getByLabelText("Key")).toHaveValue("indie-hackers");
    fireEvent.change(within(form).getByLabelText("Channel types"), { target: { value: "forum, community" } });
    fireEvent.click(screen.getByRole("button", { name: "Add channel" }));
    await waitFor(() => expect(createC).toHaveBeenCalled());
    expect(createC).toHaveBeenCalledWith(
      expect.objectContaining({ orgId: TEST_ORG, name: "Indie Hackers!", key: "indie-hackers", mode: GtmChannelMode.MANUAL, channelTypes: ["forum", "community"] }),
    );
  });

  it("edits on the revision read and sends the channel types whole", async () => {
    const update = vi.fn(() => ({ channel: hn }));
    renderGtm(<ChannelsView />, { ...reads, "GtmService.UpdateChannel": update }, at);
    fireEvent.click(await screen.findByRole("button", { name: "Edit Hacker News" }));
    const form = await screen.findByTestId("gtm-channel-form");
    expect(within(form).getByLabelText("Key")).toHaveValue("hacker-news");
    fireEvent.change(within(form).getByLabelText("Channel types"), { target: { value: "forum news" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(update).toHaveBeenCalledWith(
      expect.objectContaining({ orgId: TEST_ORG, id: "ch-hn", expectedRevision: 3, key: "hacker-news", channelTypes: expect.objectContaining({ types: ["forum", "news"] }) }),
    );
  });

  it("an integrated channel asks for its integration", async () => {
    renderGtm(<ChannelsView />, reads, at);
    fireEvent.click(await screen.findByRole("button", { name: "New channel" }));
    const form = await screen.findByTestId("gtm-channel-form");
    fireEvent.change(within(form).getByLabelText("Name"), { target: { value: "Newsletter" } });
    fireEvent.change(within(form).getByLabelText("How it goes out"), { target: { value: String(GtmChannelMode.INTEGRATED) } });
    expect(within(form).getByTestId("gtm-channel-integration")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add channel" })).toBeDisabled();
    fireEvent.change(within(form).getByLabelText("Integration"), { target: { value: "gmail" } });
    expect(screen.getByRole("button", { name: "Add channel" })).toBeEnabled();
  });

  it("after a conflict, edits the channel as it is now, on its current revision", async () => {
    let server = hn;
    const update = vi.fn((req: unknown) => {
      if ((req as UpdateChannelRequest).expectedRevision === 3) {
        server = create(GtmChannelSchema, { ...hn, manualInstructionsMd: "Post a Show HN, then reply to comments.", revision: 4 });
        throw new ConnectError("revision moved", Code.Aborted);
      }
      return { channel: server };
    });
    renderGtm(<ChannelsView />, { ...reads, "GtmService.ListChannels": () => ({ channels: [email, server, discord] }), "GtmService.UpdateChannel": update }, at);
    fireEvent.click(await screen.findByRole("button", { name: "Edit Hacker News" }));
    fireEvent.change(within(await screen.findByTestId("gtm-channel-form")).getByLabelText("Channel types"), { target: { value: "forum news" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(toasts.warning).toHaveBeenCalled());
    await waitFor(() =>
      expect(within(screen.getByTestId("gtm-channel-form")).getByLabelText("How to post on it")).toHaveValue("Post a Show HN, then reply to comments."),
    );
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(update).toHaveBeenCalledTimes(2));
    expect(update).toHaveBeenLastCalledWith(expect.objectContaining({ expectedRevision: 4, manualInstructionsMd: "Post a Show HN, then reply to comments." }));
  });

  it("says so when another channel holds the key", async () => {
    renderGtm(<ChannelsView />, { ...reads, "GtmService.CreateChannel": { error: new ConnectError("key taken", Code.AlreadyExists) } }, at);
    fireEvent.click(await screen.findByRole("button", { name: "New channel" }));
    const form = await screen.findByTestId("gtm-channel-form");
    fireEvent.change(within(form).getByLabelText("Name"), { target: { value: "Hacker News" } });
    fireEvent.click(screen.getByRole("button", { name: "Add channel" }));
    expect(await within(form).findByRole("alert")).toHaveTextContent("Another channel already has the key hacker-news");
    expect(toasts.error).not.toHaveBeenCalled();
  });
});
