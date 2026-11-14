// Copyright (c) 2025 Reliant Labs
// One chat's detail stream (StreamChatUpdates): its snapshot or replay, the
// caught_up marker, then live messages, approvals, tool calls and streaming
// deltas. One connection at a time, for the chat on screen.
//
// It is separate from the user-wide stream (streaming-grpc.ts) on purpose: a
// chat switch cancels and reopens only this stream, so it never pays for a new
// user stream, its global replay, or the user-level events that would stall
// behind it.
//
// The stream and the decoding of its frames are @reliantlabs/sdk's ChatStream;
// this is the app's environment around it (its logger, the page-resume signal,
// the tab-switch profiler and the phases of the chat-open interaction).

import { ChatStream } from "@reliantlabs/sdk/client";
import { connectClient } from "../lib/connect";
import { logger } from "../lib/logger";
import { pageActivity } from "../lib/pageActivity";
import { tabSwitchProfiler } from "../lib/tabSwitchProfiler";
import { TRANSCRIPT_TOOL_BODIES } from "../lib/toolBodies";
import { markChatOpen, noteChatOpenSync } from "../lib/chatOpenInteraction";
import { StreamingService } from "../gen/reliant/v1/streaming_pb";
import type { ChatStreamCallbacks } from "../types/streaming";

export class ChatStreamingService extends ChatStream {
  constructor(callbacks: ChatStreamCallbacks, projectId?: string) {
    super({
      callbacks,
      projectId,
      // The snapshot and replay send finished calls' bodies elided; live
      // frames stay full (lib/toolBodies.ts).
      toolBodies: TRANSCRIPT_TOOL_BODIES,
      client: () => connectClient(StreamingService),
      logger,
      onConnectionLost: () => pageActivity().noteConnectionLost(),
      hooks: {
        onSubscribe: (chatId) => {
          markChatOpen(chatId, "subscribe");
          if (tabSwitchProfiler.isEnabled()) {
            tabSwitchProfiler.mark("grpc-stream-subscribe-chat", { chatId });
          }
        },
        onSnapshotReceived: ({ chatId, messages, otherUpdates }) => {
          noteChatOpenSync(chatId, "snapshot");
          markChatOpen(chatId, "snapshot_received", { messages, other_updates: otherUpdates });
          if (tabSwitchProfiler.isEnabled()) {
            tabSwitchProfiler.mark("grpc-stream-sync-snapshot-received", {
              chatId,
              messageCount: messages,
              otherUpdates,
            });
          }
        },
        onReplayStarted: ({ chatId, sinceSequence, updates }) => {
          noteChatOpenSync(chatId, "replay");
          markChatOpen(chatId, "replay_started", { since_sequence: sinceSequence, updates });
        },
        onCaughtUp: ({ chatId, sync, initialSync, replayedUpdates, latestSequence }) => {
          noteChatOpenSync(chatId, sync);
          markChatOpen(chatId, "caught_up", {
            sync,
            initial_sync: initialSync,
            replayed_updates: replayedUpdates,
            latest_sequence: latestSequence,
          });
        },
      },
    });
  }
}
