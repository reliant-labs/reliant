/**
 * Hook to derive loaded skills from chat message history.
 * Scans content blocks for `skill` tool calls with action=load.
 */

import { useMemo } from 'react';
import { useChatMessages } from '../store/chatStoreHooks';
import { ContentBlockType } from '../types/chat';

export function useLoadedSkills(chatId: string | undefined): string[] {
  const messages = useChatMessages(chatId);

  return useMemo(() => {
    const skills: string[] = [];
    const seen = new Set<string>();

    for (const message of messages) {
      for (const block of message.contentBlocks) {
        if (block.type !== ContentBlockType.TOOL_CALL) continue;
        if (block.toolName?.toLowerCase() !== 'skill') continue;

        let input: Record<string, unknown> | undefined;
        try {
          if (block.input) {
            input = JSON.parse(block.input);
          }
        } catch {
          continue;
        }

        if (input?.action !== 'load') continue;
        const name = (input.name as string) || (input.skill_name as string);
        if (name && !seen.has(name)) {
          seen.add(name);
          skills.push(name);
        }
      }
    }

    return skills;
  }, [messages]);
}
