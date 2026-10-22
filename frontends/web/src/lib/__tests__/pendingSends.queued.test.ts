import { afterEach, describe, expect, it } from "vitest";
import {
  beginQueuedSend,
  endQueuedSend,
  type QueuedRow,
  withQueuedSendsInFlight,
} from "../pendingSends";

const started: string[] = [];

function row(id: string, body: string): QueuedRow {
  return {
    id,
    body,
    created_at: "2026-01-01T00:00:00Z",
    sender_kind: 5,
    attachments: [],
  };
}

function begin(queuedRow: QueuedRow, chatId = "c1", thread = "t1") {
  beginQueuedSend({ chatId, thread, row: queuedRow });
  started.push(queuedRow.id);
}

afterEach(() => {
  for (const id of started.splice(0)) endQueuedSend(id);
});

describe("withQueuedSendsInFlight", () => {
  it("keeps an in-flight send that the mailbox has not stored yet", () => {
    begin(row("client-1", "ship it"));

    expect(withQueuedSendsInFlight([], "c1", "t1").map((message) => message.id)).toEqual([
      "client-1",
    ]);
  });

  it("reconciles only the server row with the same client message ID", () => {
    begin(row("client-1", "same text"));
    begin(row("client-2", "same text"));

    const messages = withQueuedSendsInFlight([row("client-1", "same text")], "c1", "t1");

    expect(messages.map((message) => message.id)).toEqual(["client-1", "client-2"]);
  });

  it("does not let an identical server body claim a different optimistic send", () => {
    begin(row("client-1", "same text"));

    const messages = withQueuedSendsInFlight([row("server-1", "same text")], "c1", "t1");

    expect(messages.map((message) => message.id)).toEqual(["server-1", "client-1"]);
  });
});
