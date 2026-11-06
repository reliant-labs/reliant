const net = require('net');

// A deadline independent of socket events also bounds black-holed connections.
function waitForPort(port, {
  host = '127.0.0.1', timeoutMs = 30000, attemptTimeoutMs = 1000,
  retryMs = 200, signal, connect = net.connect,
} = {}) {
  return new Promise((resolve, reject) => {
    let socket, deadline, attempt, retry;
    let done = false;
    const cleanupAttempt = () => {
      clearTimeout(attempt);
      if (socket) {
        socket.removeListener('connect', connected);
        socket.removeListener('error', failed);
        socket.destroy();
        socket = undefined;
      }
    };
    const finish = (error) => {
      if (done) return;
      done = true;
      clearTimeout(deadline);
      clearTimeout(retry);
      cleanupAttempt();
      signal?.removeEventListener('abort', aborted);
      if (error) reject(error); else resolve(true);
    };
    const aborted = () => finish(signal.reason || new Error('Startup cancelled'));
    const connected = () => finish();
    const failed = () => {
      if (done) return;
      cleanupAttempt();
      retry = setTimeout(tryOnce, retryMs);
    };
    const tryOnce = () => {
      if (done) return;
      try {
        socket = connect(port, host);
        socket.once('connect', connected);
        socket.once('error', failed);
        attempt = setTimeout(failed, attemptTimeoutMs);
      } catch (error) {
        finish(error);
      }
    };
    if (signal?.aborted) return aborted();
    signal?.addEventListener('abort', aborted, { once: true });
    deadline = setTimeout(() => finish(new Error(`Timeout waiting for port ${port} after ${timeoutMs}ms`)), timeoutMs);
    tryOnce();
  });
}

module.exports = { waitForPort };
