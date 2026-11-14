// Copyright (c) 2025 Reliant Labs
// The org feed (StreamOrgUpdates): coarse events about the chats and projects
// an org's members shared (control-plane docs/design/org-sharing.md §5.2).
// It keeps the "Shared with the org" list current; a shared chat's detail
// comes from its own chat stream, opened only when someone opens it.
//
// The feed is app-only (proto/public-api.txt withholds StreamingService), so
// it lives here on the SDK's ReconnectingStream rather than in the SDK.

import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, type Client } from "@connectrpc/connect";
import { ReconnectingStream } from "@reliantlabs/sdk/client";
import {
  StreamingService,
  StreamOrgUpdatesRequestSchema,
  type OrgStreamEvent,
  type OrgUpdate,
} from "../gen/reliant/v1/streaming_pb";
import { connectClient } from "../lib/connect";
import { logger } from "../lib/logger";
import { pageActivity } from "../lib/pageActivity";

export interface OrgFeedCallbacks {
  /** Rows after the cursor, in feed order. Never the caller's own. */
  onUpdates: (updates: OrgUpdate[]) => void;
  /** The replay is over; everything up to `latestSequence` was delivered. */
  onCaughtUp: (latestSequence: bigint) => void;
  /**
   * The server will not stream this org to the caller: not a live member, or
   * not a person. Retrying would be refused the same way, so the feed stops.
   */
  onRefused: (error: ConnectError) => void;
}

/** A refusal every retry would repeat (see OrgFeedCallbacks.onRefused). */
export function isOrgFeedRefusal(error: unknown): error is ConnectError {
  if (!(error instanceof ConnectError)) return false;
  return (
    error.code === Code.PermissionDenied ||
    error.code === Code.NotFound ||
    error.code === Code.Unauthenticated
  );
}

/** What the feed needs of the StreamingService client (a fake in tests). */
export type OrgFeedClient = Pick<Client<typeof StreamingService>, "streamOrgUpdates">;

export class OrgFeedStream extends ReconnectingStream<OrgStreamEvent> {
  private readonly callbacks: OrgFeedCallbacks;
  private readonly client: () => OrgFeedClient;
  private orgId = "";
  // The resume cursor: advanced only past rows this client processed (a
  // batch's latest_sequence, or caught_up), so a reconnect mid-replay
  // re-reads what it had not handled.
  private cursor = 0n;

  constructor(callbacks: OrgFeedCallbacks, client: () => OrgFeedClient = () => connectClient(StreamingService)) {
    super("[📡 OrgFeed]", {}, { logger, onConnectionLost: () => pageActivity().noteConnectionLost() });
    this.callbacks = callbacks;
    this.client = client;
  }

  /** Follow `orgId` from now on: nothing is replayed (the list is read fresh). */
  start(orgId: string): void {
    this.orgId = orgId;
    this.cursor = 0n;
    this.reconnectAttempts = 0;
    this.open();
  }

  stop(): void {
    this.close(true);
  }

  protected openStream(signal: AbortSignal): AsyncIterable<OrgStreamEvent> {
    const request = create(StreamOrgUpdatesRequestSchema, { orgId: this.orgId, sinceSeq: this.cursor });
    return this.client().streamOrgUpdates(request, { signal });
  }

  protected describe(): Record<string, unknown> {
    return { orgId: this.orgId, sinceSeq: Number(this.cursor) };
  }

  protected handleConnectFailure(error: unknown): boolean {
    if (!isOrgFeedRefusal(error)) return false;
    this.log.warn("Server refused the org feed; stopping it", { code: error.code });
    this.close(true);
    this.callbacks.onRefused(error);
    return true;
  }

  protected handleEvent(event: OrgStreamEvent): void {
    switch (event.event.case) {
      case "updates": {
        const batch = event.event.value;
        if (batch.updates.length > 0) this.callbacks.onUpdates(batch.updates);
        if (batch.latestSequence > this.cursor) this.cursor = batch.latestSequence;
        break;
      }
      case "caughtUp": {
        const latest = event.event.value.latestSequence;
        if (latest > this.cursor) this.cursor = latest;
        this.callbacks.onCaughtUp(latest);
        break;
      }
      case "heartbeat":
        break;
      default:
        this.log.warn("Unknown org feed event", { case: event.event.case });
    }
  }
}
