/**
 * WebSocket Message Replay Console Script
 *
 * Usage:
 * 1. Open browser console (F12)
 * 2. Paste this entire script
 * 3. Run: await initReplay()
 * 4. Use: replayNext(), replayAll(), replayAt(n), etc.
 */

(function() {
  // Global replay state
  window.REPLAY_STATE = {
    messages: [],
    currentIndex: -1,
    autoPlayInterval: null,
  };

  /**
   * Parse messages.txt format into structured messages
   * Format: JSON\tBYTE_COUNT\nTIMESTAMP\n
   */
  function parseMessages(text) {
    const lines = text.split('\n').filter(l => l.trim());
    const messages = [];

    for (let i = 0; i < lines.length; i += 2) {
      const dataLine = lines[i];
      const timestamp = lines[i + 1];

      if (!dataLine || !timestamp) continue;

      try {
        // Extract JSON part (before the tab separator)
        const parts = dataLine.split('\t');
        const jsonPart = parts[0];

        const parsed = JSON.parse(jsonPart);
        messages.push({
          index: messages.length,
          timestamp,
          parsed,
          updates: parsed.updates || [],
          latestSequence: parsed.latest_sequence,
        });
      } catch (e) {
        console.error(`Failed to parse message at line ${i + 1}:`, e);
      }
    }

    return messages;
  }

  /**
   * Initialize replay by loading messages.txt
   */
  window.initReplay = async function() {
    console.log('[Replay] Loading messages.txt...');

    try {
      const response = await fetch('/messages.txt');
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const text = await response.text();
      window.REPLAY_STATE.messages = parseMessages(text);
      window.REPLAY_STATE.currentIndex = -1;

      console.log(`[Replay] ✅ Loaded ${window.REPLAY_STATE.messages.length} messages`);
      console.log('[Replay] Available commands:');
      console.log('  - replayNext()           Step forward one message');
      console.log('  - replayPrev()           Step backward (marker only)');
      console.log('  - replayAt(n)            Jump to message index n');
      console.log('  - replayAll(delay)       Replay all messages with delay (ms)');
      console.log('  - replayReset()          Reset to beginning');
      console.log('  - replayStatus()         Show current status');
      console.log('  - replayInspect(n)       Inspect message at index n');
      console.log('  - takeScreenshot(label)  Capture screenshot (if handler exists)');

      return window.REPLAY_STATE.messages;
    } catch (error) {
      console.error('[Replay] ❌ Failed to load messages.txt:', error);
      console.error('[Replay] Make sure messages.txt is in the public folder');
      throw error;
    }
  };

  /**
   * Get the Zustand chat store
   */
  function getChatStore() {
    // Access Zustand store from window
    // The store might be exposed differently depending on setup

    // Try to find the store from React DevTools or window
    if (window.__ZUSTAND_STORES__?.chatStore) {
      return window.__ZUSTAND_STORES__.chatStore;
    }

    console.warn('[Replay] Could not find chat store on window');
    console.warn('[Replay] Messages will be logged but not processed');
    return null;
  }

  /**
   * Send a message update to the chat store
   */
  function sendUpdate(message) {
    console.group(`[Replay] 📨 Message #${message.index}`);
    console.log('Timestamp:', message.timestamp);
    console.log('Latest Sequence:', message.latestSequence);
    console.log('Updates:', message.updates.length);

    // Log each update with detailed info
    message.updates.forEach((update, idx) => {
      console.log(`[Replay] Update #${idx}:`, {
        update_type: update.update_type,
        sequence_number: update.sequence_number,
        id: update.id,
        role: update.role,
        streaming_state: update.streaming_state,
        content_blocks: update.content_blocks?.length,
        status: update.status,
        full: update,
      });
    });

    // Get current store state before update
    const store = getChatStore();
    if (store) {
      const state = store.getState();
      console.log('[Replay] State BEFORE:', {
        streamingMessages: state.streamingMessages?.size || 0,
        hasExecutingTools: state.hasExecutingTools,
        isChatBusy: state.isChatBusy,
        messagesCount: state.messages?.length || 0,
      });
    }

    // Trigger the update
    // Method 1: Try to call store's onUpdate callback directly
    if (store) {
      try {
        // This is a hack - we're trying to access internal WebSocket callback
        // In production, you'd need to expose this properly
        console.log('[Replay] ⚠️ Direct store update not implemented yet');
        console.log('[Replay] Updates logged above - manually verify UI state');
      } catch (e) {
        console.error('[Replay] Failed to send update:', e);
      }
    }

    // Method 2: Dispatch custom event
    window.dispatchEvent(new CustomEvent('replay-update', {
      detail: {
        type: 'updates',
        updates: message.updates,
        latest_sequence: message.latestSequence,
      }
    }));

    console.groupEnd();

    // Log state after update
    if (store) {
      // Small delay to let state update
      setTimeout(() => {
        const state = store.getState();
        console.log('[Replay] State AFTER:', {
          streamingMessages: state.streamingMessages?.size || 0,
          hasExecutingTools: state.hasExecutingTools,
          isChatBusy: state.isChatBusy,
          messagesCount: state.messages?.length || 0,
        });
      }, 50);
    }
  }

  /**
   * Replay next message
   */
  window.replayNext = function() {
    const { messages, currentIndex } = window.REPLAY_STATE;

    if (messages.length === 0) {
      console.error('[Replay] No messages loaded. Run initReplay() first');
      return;
    }

    if (currentIndex >= messages.length - 1) {
      console.log('[Replay] ⏭️ Already at last message');
      return;
    }

    const nextIndex = currentIndex + 1;
    window.REPLAY_STATE.currentIndex = nextIndex;

    const message = messages[nextIndex];
    sendUpdate(message);

    return message;
  };

  /**
   * Step backward (doesn't "undo" the message, just moves marker)
   */
  window.replayPrev = function() {
    if (window.REPLAY_STATE.currentIndex <= -1) {
      console.log('[Replay] ⏮️ Already at beginning');
      return;
    }

    window.REPLAY_STATE.currentIndex--;
    console.log(`[Replay] Moved to index ${window.REPLAY_STATE.currentIndex}`);
  };

  /**
   * Jump to specific message index
   */
  window.replayAt = function(index) {
    const { messages } = window.REPLAY_STATE;

    if (messages.length === 0) {
      console.error('[Replay] No messages loaded. Run initReplay() first');
      return;
    }

    if (index < 0 || index >= messages.length) {
      console.error(`[Replay] Index ${index} out of range (0-${messages.length - 1})`);
      return;
    }

    window.REPLAY_STATE.currentIndex = index;
    const message = messages[index];
    sendUpdate(message);

    return message;
  };

  /**
   * Replay all messages with delay
   */
  window.replayAll = async function(delayMs = 100) {
    const { messages } = window.REPLAY_STATE;

    if (messages.length === 0) {
      console.error('[Replay] No messages loaded. Run initReplay() first');
      return;
    }

    console.log(`[Replay] ▶️ Playing all ${messages.length} messages with ${delayMs}ms delay`);
    console.log('[Replay] Use replayStop() to stop playback');

    // Stop any existing playback
    if (window.REPLAY_STATE.autoPlayInterval) {
      clearInterval(window.REPLAY_STATE.autoPlayInterval);
    }

    // Auto-play
    window.REPLAY_STATE.autoPlayInterval = setInterval(() => {
      const next = window.replayNext();

      if (!next) {
        console.log('[Replay] ⏹️ Playback complete');
        clearInterval(window.REPLAY_STATE.autoPlayInterval);
        window.REPLAY_STATE.autoPlayInterval = null;
      }
    }, delayMs);
  };

  /**
   * Stop auto-playback
   */
  window.replayStop = function() {
    if (window.REPLAY_STATE.autoPlayInterval) {
      clearInterval(window.REPLAY_STATE.autoPlayInterval);
      window.REPLAY_STATE.autoPlayInterval = null;
      console.log('[Replay] ⏹️ Playback stopped');
    } else {
      console.log('[Replay] No playback in progress');
    }
  };

  /**
   * Reset to beginning
   */
  window.replayReset = function() {
    window.replayStop();
    window.REPLAY_STATE.currentIndex = -1;
    console.log('[Replay] 🔄 Reset to beginning');
  };

  /**
   * Show current status
   */
  window.replayStatus = function() {
    const { messages, currentIndex } = window.REPLAY_STATE;

    console.log('[Replay] Status:');
    console.log(`  Total messages: ${messages.length}`);
    console.log(`  Current index: ${currentIndex}`);
    console.log(`  Progress: ${currentIndex + 1} / ${messages.length}`);

    if (currentIndex >= 0 && currentIndex < messages.length) {
      const current = messages[currentIndex];
      console.log(`  Current message:`, current);
    }

    // Show store state if available
    const store = getChatStore();
    if (store) {
      const state = store.getState();
      console.log('[Replay] Chat Store State:');
      console.log(`  Streaming messages: ${state.streamingMessages?.size || 0}`);
      console.log(`  Has executing tools: ${state.hasExecutingTools}`);
      console.log(`  Is chat busy: ${state.isChatBusy}`);
      console.log(`  Messages count: ${state.messages?.length || 0}`);
    }
  };

  /**
   * Inspect a specific message
   */
  window.replayInspect = function(index) {
    const { messages } = window.REPLAY_STATE;

    if (messages.length === 0) {
      console.error('[Replay] No messages loaded. Run initReplay() first');
      return;
    }

    if (index < 0 || index >= messages.length) {
      console.error(`[Replay] Index ${index} out of range (0-${messages.length - 1})`);
      return;
    }

    const message = messages[index];
    console.log(`[Replay] Message #${index}:`, message);

    return message;
  };

  /**
   * Take a screenshot (requires integration with screenshot tool)
   */
  window.takeScreenshot = function(label = 'screenshot') {
    console.log(`[Replay] 📷 Screenshot: ${label}`);

    // Trigger custom event that screenshot tool can listen to
    window.dispatchEvent(new CustomEvent('replay-screenshot', {
      detail: { label }
    }));

    // If running in Playwright/Puppeteer, they can listen for this
    console.log(`[Replay] To capture this in Playwright, use page.screenshot()`);
  };

  console.log('[Replay Console Script] ✅ Loaded');
  console.log('[Replay Console Script] Run: await initReplay()');
})();
