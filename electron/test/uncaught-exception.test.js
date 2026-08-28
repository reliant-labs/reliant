const test = require('node:test');
const assert = require('node:assert');

const { handleUncaughtException } = require('../src/uncaught-exception');

function makeDeps(overrides = {}) {
  const calls = { disabled: 0, logged: [], shutdowns: 0 };
  return {
    calls,
    deps: {
      disableConsoleTransport: () => {
        calls.disabled++;
      },
      logError: (err) => {
        calls.logged.push(err);
      },
      shutdown: () => {
        calls.shutdowns++;
      },
      ...overrides,
    },
  };
}

function epipe() {
  const err = new Error('write EPIPE');
  err.code = 'EPIPE';
  return err;
}

// THE BUG THIS GUARDS: logging an EPIPE writes to the very pipe that is
// broken, which throws EPIPE, which re-enters the handler. Observed writing
// 100MB across ten 10MB archives in 23 seconds and evicting all real log
// history (retention caps at 10 files).
test('an EPIPE is never routed through the logger', () => {
  const { calls, deps } = makeDeps();

  const outcome = handleUncaughtException(epipe(), deps);

  assert.strictEqual(outcome, 'console-disabled');
  assert.deepStrictEqual(calls.logged, [], 'logging an EPIPE re-enters the handler');
  assert.strictEqual(calls.disabled, 1, 'the broken console transport must be disabled');
});

// A dead stdout pipe costs us the console sink, not the app. The file
// transport still works, so shutting down would turn a closed terminal into
// an outage.
test('an EPIPE does not shut the app down', () => {
  const { calls, deps } = makeDeps();

  handleUncaughtException(epipe(), deps);

  assert.strictEqual(calls.shutdowns, 0);
});

// The loop is driven by the handler being re-entered. Feeding its own failure
// back in must converge rather than recurse.
test('repeated EPIPEs converge instead of looping', () => {
  const { calls, deps } = makeDeps({
    disableConsoleTransport: () => {
      throw epipe();
    },
  });

  // Even when disabling the transport ITSELF throws EPIPE, the handler must
  // return rather than fall through to the logging path.
  for (let i = 0; i < 50; i++) {
    assert.strictEqual(handleUncaughtException(epipe(), deps), 'console-disabled');
  }
  assert.deepStrictEqual(calls.logged, []);
  assert.strictEqual(calls.shutdowns, 0);
});

// Everything that is NOT a dead pipe keeps the original behavior: log it and
// bring the app down. Silencing real crashes would be a worse bug than the one
// being fixed.
test('a non-EPIPE exception is logged and shuts down', () => {
  const { calls, deps } = makeDeps();
  const boom = new Error('boom');

  const outcome = handleUncaughtException(boom, deps);

  assert.strictEqual(outcome, 'fatal');
  assert.deepStrictEqual(calls.logged, [boom]);
  assert.strictEqual(calls.shutdowns, 1);
  assert.strictEqual(calls.disabled, 0);
});

// A thrown non-Error (or undefined) must not crash the handler itself on a
// `.code` read.
test('a null or non-Error throw is treated as fatal, not a crash', () => {
  for (const value of [null, undefined, 'a string', 42]) {
    const { calls, deps } = makeDeps();
    assert.strictEqual(handleUncaughtException(value, deps), 'fatal');
    assert.strictEqual(calls.shutdowns, 1);
  }
});
