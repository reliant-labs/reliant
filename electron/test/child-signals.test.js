const test = require("node:test");
const assert = require("node:assert");
const { EventEmitter } = require("node:events");
const { spawn } = require("node:child_process");
const path = require("node:path");
const os = require("node:os");

const {
  forwardSignals,
  exitCodeFor,
  TERMINATION_SIGNALS,
} = require("../src/child-signals");

function fakeChild(pid = 4242) {
  const child = new EventEmitter();
  child.pid = pid;
  child.killed = [];
  child.kill = (signal) => {
    child.killed.push(signal);
    return true;
  };
  return child;
}

test("forwards each termination signal to the child", () => {
  const child = fakeChild();
  const parent = new EventEmitter();
  forwardSignals(child, { parent });

  for (const signal of TERMINATION_SIGNALS) {
    parent.emit(signal);
  }

  assert.deepStrictEqual(child.killed, TERMINATION_SIGNALS);
});

test("stops forwarding once the child has exited", () => {
  // Guards the double-Ctrl-C / process-group case, where the parent gets a
  // signal after the child is already reaped. Killing a dead pid throws ESRCH.
  const child = fakeChild();
  const parent = new EventEmitter();
  forwardSignals(child, { parent });

  child.emit("exit", 0, null);
  parent.emit("SIGTERM");

  assert.deepStrictEqual(child.killed, []);
});

test("a kill that throws ESRCH does not propagate", () => {
  const child = fakeChild();
  child.kill = () => {
    const error = new Error("no such process");
    error.code = "ESRCH";
    throw error;
  };
  const parent = new EventEmitter();
  const { forward } = forwardSignals(child, { parent });

  assert.doesNotThrow(() => parent.emit("SIGTERM"));
  assert.strictEqual(forward("SIGTERM"), false);
});

test("exitCodeFor preserves a real exit code", () => {
  assert.strictEqual(exitCodeFor(0, null), 0);
  assert.strictEqual(exitCodeFor(3, null), 3);
});

test("exitCodeFor maps a signal death to 128 + signum, not 0", () => {
  // process.exit(code) with code === null exits 0, which reports a killed dev
  // session as a clean run. That is the bug this mapping exists to prevent.
  assert.strictEqual(exitCodeFor(null, "SIGTERM"), 128 + os.constants.signals.SIGTERM);
  assert.strictEqual(exitCodeFor(null, "SIGINT"), 128 + os.constants.signals.SIGINT);
  assert.notStrictEqual(exitCodeFor(null, "SIGTERM"), 0);
});

// End-to-end: the regression this whole module exists for. Spawns the real
// wait-and-start supervisor shape and asserts SIGTERM reaches the grandchild
// instead of orphaning it to PID 1.
test("SIGTERM to the supervisor reaches the spawned grandchild", async () => {
  const fixtureDir = path.join(os.tmpdir(), `reliant-signal-fixture-${process.pid}`);
  const fs = require("node:fs");
  fs.mkdirSync(fixtureDir, { recursive: true });

  const leafPath = path.join(fixtureDir, "leaf.js");
  const supervisorPath = path.join(fixtureDir, "supervisor.js");
  const modulePath = path.join(__dirname, "../src/child-signals.js");

  fs.writeFileSync(
    leafPath,
    `process.on("SIGTERM", () => { console.log("LEAF_GOT_SIGTERM"); process.exit(0); });
     console.log("LEAF_READY");
     setInterval(() => {}, 1000);`,
  );

  fs.writeFileSync(
    supervisorPath,
    `const { spawn } = require("child_process");
     const { forwardSignals, exitCodeFor } = require(${JSON.stringify(modulePath)});
     const child = spawn(process.execPath, [${JSON.stringify(leafPath)}], { stdio: "inherit" });
     forwardSignals(child);
     child.on("exit", (code, signal) => process.exit(exitCodeFor(code, signal)));`,
  );

  const supervisor = spawn(process.execPath, [supervisorPath], {
    stdio: ["ignore", "pipe", "pipe"],
  });

  let output = "";
  supervisor.stdout.on("data", (chunk) => {
    output += chunk.toString();
  });

  await new Promise((resolve) => {
    const check = () => (output.includes("LEAF_READY") ? resolve() : setTimeout(check, 50));
    check();
  });

  supervisor.kill("SIGTERM");

  const exitCode = await new Promise((resolve) => {
    supervisor.on("exit", (code) => resolve(code));
  });

  assert.ok(
    output.includes("LEAF_GOT_SIGTERM"),
    `grandchild was orphaned instead of signalled. output: ${output}`,
  );
  assert.strictEqual(exitCode, 0, "supervisor should exit with the leaf's clean code");

  fs.rmSync(fixtureDir, { recursive: true, force: true });
});
