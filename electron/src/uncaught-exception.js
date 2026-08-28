/**
 * Policy for process-level uncaught exceptions.
 *
 * Extracted from main.js so it can be tested without booting Electron. The
 * EPIPE branch is the whole reason this file exists — see below.
 */

/**
 * Decide what to do with an uncaught exception.
 *
 * EPIPE means the stdout/stderr pipe the logger writes to is gone: the parent
 * shell exited, `forge env up` was stopped, or a terminal closed while the app
 * kept running.
 *
 * It MUST NOT be routed through the logger. electron-log's console transport
 * writes to that same dead pipe, so logging an EPIPE throws another EPIPE,
 * which re-enters this handler, which logs again. Observed in
 * .reliant/logs/: 100MB across ten 10MB archives in 23 seconds, which then
 * evicted every previous archive (retention caps at 10 files) and destroyed
 * the real history someone would have wanted.
 *
 * Losing the console sink is not fatal — the FILE transport is unaffected — so
 * the right move is to disable the broken console transport and keep running,
 * not to take the app down over a closed pipe.
 *
 * @param {unknown} error the thrown value
 * @param {{
 *   disableConsoleTransport: () => void,
 *   logError: (err: unknown) => void,
 *   shutdown: () => void,
 * }} deps
 * @returns {"console-disabled" | "fatal"} which branch ran (for tests/callers)
 */
function handleUncaughtException(error, deps) {
  if (error && error.code === "EPIPE") {
    try {
      deps.disableConsoleTransport();
    } catch {
      // Nothing safe is left to do — swallowing still beats re-entering the
      // loop this function exists to prevent.
    }
    return "console-disabled";
  }

  deps.logError(error);
  deps.shutdown();
  return "fatal";
}

module.exports = { handleUncaughtException };
