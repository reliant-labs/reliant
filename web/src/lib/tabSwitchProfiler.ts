/**
 * Tab Switch Profiler
 * 
 * A simple profiling utility to trace the exact timing of tab/chat switching.
 * This helps identify performance bottlenecks from click to full message display.
 * 
 * USAGE:
 * - Profiler is enabled by default
 * - Switch tabs - timing will be logged to ~/Library/Logs/reliant/main.log
 * - Run window.tabSwitchProfiler.getReport() in DevTools for a summary
 */

import { logger } from './logger';

interface TimingEvent {
  name: string;
  timestamp: number;
  absoluteTime: number; // Date.now() for correlation
  data?: Record<string, unknown>;
}

interface TabSwitchSession {
  chatId: string;
  startTime: number;
  startAbsoluteTime: number;
  events: TimingEvent[];
  completed: boolean;
}

class TabSwitchProfiler {
  private currentSession: TabSwitchSession | null = null;
  private completedSessions: TabSwitchSession[] = [];
  private enabled = true;

  isEnabled(): boolean {
    return this.enabled;
  }

  enableProfiler() {
    this.enabled = true;
    logger.info('[TabSwitchProfiler] Enabled');
  }

  disableProfiler() {
    this.enabled = false;
    logger.info('[TabSwitchProfiler] Disabled');
  }

  /**
   * Start a new profiling session for a tab switch
   */
  startSession(chatId: string): void {
    if (!this.enabled) return;

    // If there's an existing incomplete session, log it as abandoned
    if (this.currentSession && !this.currentSession.completed) {
      const duration = performance.now() - this.currentSession.startTime;
      logger.warn(`[TabSwitchProfiler] Abandoned session for ${this.currentSession.chatId.slice(0, 8)}... after ${duration.toFixed(0)}ms`);
    }

    const now = performance.now();
    this.currentSession = {
      chatId,
      startTime: now,
      startAbsoluteTime: Date.now(),
      events: [],
      completed: false,
    };

    logger.info(`[TabSwitchProfiler] ========== SESSION START: ${chatId.slice(0, 8)}... ==========`);
    this.addEvent('session-start', { chatId });
  }

  /**
   * Add a timing event to the current session
   */
  mark(name: string, data?: Record<string, unknown>): void {
    if (!this.enabled || !this.currentSession) return;

    const now = performance.now();
    const elapsed = now - this.currentSession.startTime;

    this.addEvent(name, data);

    logger.info(`[TabSwitchProfiler] +${elapsed.toFixed(0)}ms - ${name}`, data || '');
  }

  /**
   * End the current session and log summary
   */
  endSession(): void {
    if (!this.enabled || !this.currentSession || this.currentSession.completed) return;

    const now = performance.now();
    const totalDuration = now - this.currentSession.startTime;

    this.addEvent('session-end', { totalDuration });
    this.currentSession.completed = true;

    // Log summary
    const sessionChatId = this.currentSession.chatId.slice(0, 8);
    logger.info(`[TabSwitchProfiler] ========== SESSION END: ${sessionChatId}... ==========`);
    logger.info(`[TabSwitchProfiler] TOTAL TIME: ${totalDuration.toFixed(0)}ms`);

    // Log each event with timing
    this.currentSession.events.forEach(event => {
      const elapsed = event.timestamp - this.currentSession!.startTime;
      logger.info(`[TabSwitchProfiler]   ${elapsed.toFixed(0).padStart(5)}ms - ${event.name}`);
    });

    if (totalDuration > 500) {
      logger.warn(`[TabSwitchProfiler] SLOW TAB SWITCH: ${totalDuration.toFixed(0)}ms for ${sessionChatId}...`);
    }

    // Clean up accumulated performance marks
    try {
      performance.clearMarks();
    } catch {
      // Ignore if clearMarks fails
    }

    // Store completed session
    this.completedSessions.push(this.currentSession);
    if (this.completedSessions.length > 20) {
      this.completedSessions.shift(); // Keep only last 20
    }

    this.currentSession = null;
  }

  private addEvent(name: string, data?: Record<string, unknown>): void {
    if (!this.currentSession) return;

    this.currentSession.events.push({
      name,
      timestamp: performance.now(),
      absoluteTime: Date.now(),
      data,
    });

    // Also add Chrome Performance marks
    try {
      performance.mark(`tab-switch:${name}`);
    } catch {
      // Ignore if performance.mark fails
    }
  }

  /**
   * Get a report of completed sessions
   */
  getReport(): void {
    if (this.completedSessions.length === 0) {
      console.log('[TabSwitchProfiler] No completed sessions');
      return;
    }

    console.log('[TabSwitchProfiler] === SESSION REPORT ===');
    
    const durations = this.completedSessions.map(s => {
      const endEvent = s.events.find(e => e.name === 'session-end');
      return endEvent ? endEvent.timestamp - s.startTime : 0;
    });

    console.log(`Sessions: ${this.completedSessions.length}`);
    console.log(`Average: ${(durations.reduce((a, b) => a + b, 0) / durations.length).toFixed(0)}ms`);
    console.log(`Min: ${Math.min(...durations).toFixed(0)}ms`);
    console.log(`Max: ${Math.max(...durations).toFixed(0)}ms`);

    console.log('\nLast 5 sessions:');
    this.completedSessions.slice(-5).forEach((session, i) => {
      const endEvent = session.events.find(e => e.name === 'session-end');
      const duration = endEvent ? endEvent.timestamp - session.startTime : 0;
      console.log(`  ${i + 1}. ${session.chatId.slice(0, 8)}...: ${duration.toFixed(0)}ms`);
    });
  }

  /**
   * Clear all sessions
   */
  clear(): void {
    this.currentSession = null;
    this.completedSessions = [];
    logger.info('[TabSwitchProfiler] Cleared');
  }

  /**
   * Check if there's an active session for a specific chatId
   */
  hasActiveSession(chatId?: string): boolean {
    if (!this.currentSession || this.currentSession.completed) return false;
    if (chatId) return this.currentSession.chatId === chatId;
    return true;
  }
}

// Create singleton instance
export const tabSwitchProfiler = new TabSwitchProfiler();

// Expose to window for debugging
declare global {
  interface Window {
    tabSwitchProfiler: TabSwitchProfiler;
  }
}

if (typeof window !== 'undefined') {
  window.tabSwitchProfiler = tabSwitchProfiler;
}
