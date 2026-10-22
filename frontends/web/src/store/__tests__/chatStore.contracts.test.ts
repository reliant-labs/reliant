import { describe, expect, it } from 'vitest';

import type { ChatUpdate } from '../../types/streaming';

describe('chatStore message update contract', () => {
  const selectProtoMessageUpdates = (updates: ChatUpdate[]) =>
    updates
      .filter(
        (u): u is { update_type: 'message'; message: unknown } =>
          u.update_type === 'message' && 'message' in u,
      )
      .map((u) => u.message);

  it('accepts wrapped message updates (update_type + message payload)', () => {
    const updates = [
      {
        update_type: 'message',
        message: {
          id: 'msg-1',
          chatId: 'chat-1',
        },
      },
    ] as ChatUpdate[];

    const messageUpdates = selectProtoMessageUpdates(updates);

    expect(messageUpdates).toHaveLength(1);
    expect(messageUpdates[0]).toEqual(
      expect.objectContaining({
        id: 'msg-1',
        chatId: 'chat-1',
      }),
    );
  });

  it('rejects unwrapped message updates (regression shape that drops live updates)', () => {
    const updates = [
      {
        update_type: 'message',
        id: 'msg-2',
        role: 'assistant',
      },
    ] as unknown as ChatUpdate[];

    const messageUpdates = selectProtoMessageUpdates(updates);

    expect(messageUpdates).toHaveLength(0);
  });
});
