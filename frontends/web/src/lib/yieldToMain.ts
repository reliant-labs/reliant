// Copyright (c) 2025 Reliant Labs

/**
 * Resolves in a new task, so the browser can paint and handle input in
 * between. Code that answers many reads in a row (the demo's in-browser
 * backend) awaits it before each one: otherwise every answer is computed in
 * the task of the one before, and a phone spends seconds in a single task.
 *
 * `scheduler.yield()` where the browser has it (its continuation runs ahead of
 * other queued tasks); a MessageChannel message elsewhere, which, unlike
 * setTimeout, is not clamped to 4 ms and is not faked by test timers.
 */
export function yieldToMain(): Promise<void> {
  const scheduler = (globalThis as { scheduler?: { yield?: () => Promise<void> } }).scheduler;
  if (typeof scheduler?.yield === "function") return scheduler.yield();
  if (typeof MessageChannel === "function") {
    return new Promise((resolve) => {
      const { port1, port2 } = new MessageChannel();
      port1.onmessage = () => {
        port1.close();
        resolve();
      };
      port2.postMessage(null);
    });
  }
  return new Promise((resolve) => setTimeout(resolve, 0));
}
