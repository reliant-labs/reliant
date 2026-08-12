/**
 * Signal forwarding for dev-loop supervisors that spawn a long-running child.
 *
 * A supervisor that spawns a child but installs no signal handlers dies on
 * Node's default SIGTERM action, and the child is reparented to PID 1. In the
 * Electron dev chain that orphan is the Electron main process, which is what
 * owns gracefulShutdown() — so the tools-daemon it spawned outlives the dev
 * session and holds its port. Forwarding keeps the whole chain tearing itself
 * down in order.
 *
 * NOTE: `task` does not participate in this. go-task intercepts SIGINT/SIGTERM
 * and only logs them (see its signals.go InterceptInterruptSignals), expecting
 * the caller to signal the whole process group instead. Forwarding here fixes
 * the chain BELOW task; it cannot make `kill <task-pid>` work.
 */

const TERMINATION_SIGNALS = ["SIGINT", "SIGTERM", "SIGHUP"];

/**
 * Translate a child's (code, signal) exit into a process exit code.
 *
 * A child killed by a signal reports code === null. Passing that straight to
 * process.exit() yields 0, which reports a killed dev session as a clean run;
 * the shell convention of 128 + signal number keeps it distinguishable.
 *
 * @param {number|null} code - Exit code, or null if terminated by a signal.
 * @param {string|null} signal - Signal name, if terminated by one.
 * @returns {number} Exit code to hand to process.exit().
 */
function exitCodeFor(code, signal) {
  if (typeof code === "number") return code;
  if (signal) {
    const signalNumber = require("os").constants.signals[signal];
    if (typeof signalNumber === "number") return 128 + signalNumber;
    return 1;
  }
  return 0;
}

/**
 * Forward termination signals from this process to a spawned child.
 *
 * Idempotent per signal and inert once the child has exited, so a second
 * Ctrl-C (or a group signal that reaches both parent and child) can't throw
 * ESRCH on an already-dead pid.
 *
 * @param {import('child_process').ChildProcess} child - The spawned child.
 * @param {object} [options]
 * @param {string[]} [options.signals] - Signals to forward.
 * @param {NodeJS.EventEmitter} [options.parent] - Emitter to listen on (test seam).
 * @param {(message: string) => void} [options.onForward] - Called when a signal is forwarded.
 * @returns {{ forward: (signal: string) => boolean }} Handle, mainly for tests.
 */
function forwardSignals(child, options = {}) {
  const {
    signals = TERMINATION_SIGNALS,
    parent = process,
    onForward = () => {},
  } = options;

  let childExited = false;
  child.on("exit", () => {
    childExited = true;
  });

  const forward = (signal) => {
    if (childExited) return false;
    try {
      child.kill(signal);
      onForward(`forwarding ${signal} to child pid ${child.pid}`);
      return true;
    } catch (error) {
      // ESRCH just means the child beat us to it — not worth failing over.
      if (error && error.code !== "ESRCH") {
        onForward(`failed to forward ${signal}: ${error.message}`);
      }
      return false;
    }
  };

  for (const signal of signals) {
    parent.on(signal, () => forward(signal));
  }

  return { forward };
}

module.exports = { forwardSignals, exitCodeFor, TERMINATION_SIGNALS };
