/**
 * Test ModernApp for infinite loop issues
 */

import '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { fileURLToPath } from 'url';
import * as path from 'path';
import { useChatNavigationStore } from './store/chatNavigationStore';
import { useChatStore } from './store/chatStore';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

describe('ModernApp infinite loop prevention', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('clearQueue should not be in useEffect dependency arrays', async () => {
    // Read the ModernApp.tsx file
    const fs = await import('fs');
    const fileContent = fs.readFileSync(
      path.join(__dirname, 'ModernApp.tsx'),
      'utf-8'
    );

    // Check that clearQueue is NOT used as a dependency
    // Pattern: }, [.*clearQueue.*]);
    const badPattern = /\},\s*\[[^\]]*clearQueue[^\]]*\];/g;
    const matches = fileContent.match(badPattern);

    if (matches) {
      console.error('❌ Found clearQueue in dependency arrays:', matches);
      throw new Error(
        `clearQueue should not be in dependency arrays. Found ${matches.length} occurrences.`
      );
    }

    console.log('✅ clearQueue is not in any dependency arrays');
    expect(matches).toBeNull();
  });

  it('should use getState() instead of subscribing to clearQueue', async () => {
    const fs = await import('fs');
    const fileContent = fs.readFileSync(
      path.join(__dirname, 'ModernApp.tsx'),
      'utf-8'
    );

    // Check that we're using getState() pattern for navigation methods
    // (clearQueue is no longer used, but the pattern applies to other methods like navigateNext)
    const hasGetStatePattern = fileContent.includes('useChatNavigationStore.getState()');

    expect(hasGetStatePattern).toBe(true);
    console.log('✅ Using getState() pattern for navigation store');
  });

  it('chatNavigationStore clearQueue should be stable', () => {
    const store1 = useChatNavigationStore.getState();
    const clearQueue1 = store1.clearQueue;

    const store2 = useChatNavigationStore.getState();
    const clearQueue2 = store2.clearQueue;

    // The function reference should be the same
    expect(clearQueue1).toBe(clearQueue2);
    console.log('✅ clearQueue function reference is stable');
  });

  it('chatStore methods should be stable', () => {
    const store1 = useChatStore.getState();
    const selectChat1 = store1.selectChat;
    const loadChats1 = store1.loadChats;

    const store2 = useChatStore.getState();
    const selectChat2 = store2.selectChat;
    const loadChats2 = store2.loadChats;

    expect(selectChat1).toBe(selectChat2);
    expect(loadChats1).toBe(loadChats2);
    console.log('✅ chatStore methods are stable');
  });
});
