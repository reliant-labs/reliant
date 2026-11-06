#!/usr/bin/env node

const { spawn } = require('child_process');
const { waitForPort } = require('../src/wait-for-port');
const fs = require('fs');
const path = require('path');
const { forwardSignals, exitCodeFor } = require('../src/child-signals');

// Load .env file from project root
const envPath = path.join(__dirname, '../../.env');
if (fs.existsSync(envPath)) {
  const envContent = fs.readFileSync(envPath, 'utf8');
  envContent.split('\n').forEach(line => {
    const trimmed = line.trim();
    if (trimmed && !trimmed.startsWith('#')) {
      const match = trimmed.match(/^([^=]+)=(.*)$/);
      if (match) {
        const key = match[1].trim();
        const value = match[2].trim();
        if (!process.env[key]) {
          process.env[key] = value;
        }
      }
    }
  });
}

// Get ports from environment or use defaults
const FRONTEND_PORT = process.env.FRONTEND_PORT || '5173';
const BACKEND_PORT = process.env.BACKEND_PORT || '8080';

console.log(`FRONTEND_PORT=${FRONTEND_PORT}`);
console.log(`BACKEND_PORT=${BACKEND_PORT}`);

// CLI-only override keeps standalone startup bounded at its historical 30s.
function parseOptions(args) {
  let timeoutMs = 30000;
  for (let i = 0; i < args.length; i++) {
    if (args[i] !== '--wait-timeout-ms') throw new Error(`Unknown option: ${args[i]}`);
    const value = args[++i];
    if (!/^\d+$/.test(value || '') || Number(value) < 1 || Number(value) > 2147483647) {
      throw new Error('--wait-timeout-ms must be an integer between 1 and 2147483647');
    }
    timeoutMs = Number(value);
  }
  return { timeoutMs };
}

async function main({ args = process.argv.slice(2), parent = process, spawnChild = spawn, wait = waitForPort } = {}) {
  const startTime = Date.now();
  console.log('[wait-and-start] Starting Electron development...');

  const controller = new AbortController();
  let cancelledSignal;
  const cancelListeners = ['SIGINT', 'SIGTERM', 'SIGHUP'].map(signal => {
    const listener = () => {
      cancelledSignal = signal;
      controller.abort(new Error(`Startup cancelled by ${signal}`));
    };
    parent.on(signal, listener);
    return [signal, listener];
  });
  const stopCancellation = () => {
    for (const [signal, listener] of cancelListeners) parent.removeListener(signal, listener);
  };
  try {
    const { timeoutMs } = parseOptions(args);
    // Wait for frontend to be ready
    const frontendWaitStart = Date.now();
    console.log(`[wait-and-start] Waiting for frontend on port ${FRONTEND_PORT}...`);
    await wait(Number(FRONTEND_PORT), { timeoutMs, signal: controller.signal });
    controller.signal.throwIfAborted();
    console.log(`[wait-and-start] ✓ Frontend ready in ${Date.now() - frontendWaitStart}ms`);

    // Start electron with ports
    const electronStartTime = Date.now();
    console.log('[wait-and-start] Starting Electron...');

    // Use the local electron binary from node_modules
    // On Windows, use .cmd extension; on Unix, no extension
    const electronBinary = process.platform === 'win32' ? 'electron.cmd' : 'electron';
    const electronPath = path.join(__dirname, '../node_modules/.bin', electronBinary);
    // Avoid a Unix shell between us and Electron: signals must reach the app.
    const electron = spawnChild(electronPath, ['.'], {
      stdio: 'inherit',
      shell: process.platform === 'win32',
      windowsHide: true,  // Prevent console window from appearing on Windows
      env: {
        ...process.env,
        NODE_ENV: 'development',
        FRONTEND_PORT,
        BACKEND_PORT
      }
    });

    electron.once('spawn', () => {
      console.log(`[wait-and-start] Electron process started in ${Date.now() - electronStartTime}ms (renderer readiness is not implied)`);
      console.log(`[wait-and-start] Total launch time: ${Date.now() - startTime}ms`);
    });

    // Without this, SIGTERM kills THIS process via Node's default action and
    // reparents Electron to PID 1 — orphaning the one process that runs
    // gracefulShutdown(), so the tools-daemon it spawned outlives the session
    // and keeps its port. Whoever launched us (forge env up's host runner)
    // signals us; we forward the rest of the way down.
    const forwarding = forwardSignals(electron, {
      parent,
      onForward: (message) => console.log(`[wait-and-start] ${message}`),
    });
    stopCancellation();
    try {
      return await new Promise((resolve, reject) => {
        electron.once('error', reject);
        electron.once('exit', (code, signal) => resolve(exitCodeFor(code, signal)));
      });
    } finally {
      forwarding.dispose();
    }
  } catch (error) {
    console.error('[wait-and-start] Error:', error.message);
    return cancelledSignal ? exitCodeFor(null, cancelledSignal) : 1;
  } finally {
    stopCancellation();
  }
}

if (require.main === module) main().then(code => { process.exitCode = code; });
module.exports = { main, parseOptions };