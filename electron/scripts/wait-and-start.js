#!/usr/bin/env node

const { spawn, execSync } = require('child_process');
const net = require('net');
const fs = require('fs');
const path = require('path');

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

// Function to check if port is available
function waitForPort(port, host = '127.0.0.1', timeoutMs = 30000) {
  const start = Date.now();
  return new Promise((resolve, reject) => {
    const tryOnce = () => {
      const socket = net.connect(port, host);
      socket.once('connect', () => {
        socket.destroy();
        resolve(true);
      });
      socket.once('error', () => {
        socket.destroy();
        if (Date.now() - start > timeoutMs) {
          reject(new Error(`Timeout waiting for port ${port}`));
        } else {
          setTimeout(tryOnce, 200);
        }
      });
    };
    tryOnce();
  });
}

async function main() {
  const startTime = Date.now();
  console.log('[wait-and-start] Starting Electron development...');

  try {
    // Wait for frontend to be ready
    const frontendWaitStart = Date.now();
    console.log(`[wait-and-start] Waiting for frontend on port ${FRONTEND_PORT}...`);
    await waitForPort(parseInt(FRONTEND_PORT));
    console.log(`[wait-and-start] ✓ Frontend ready in ${Date.now() - frontendWaitStart}ms`);

    // Start electron with ports
    const electronStartTime = Date.now();
    console.log('[wait-and-start] Starting Electron...');

    // Use the local electron binary from node_modules
    // On Windows, use .cmd extension; on Unix, no extension
    const electronBinary = process.platform === 'win32' ? 'electron.cmd' : 'electron';
    const electronPath = path.join(__dirname, '../node_modules/.bin', electronBinary);
    const electron = spawn(electronPath, ['.'], {
      stdio: 'inherit',
      shell: true,
      windowsHide: true,  // Prevent console window from appearing on Windows
      env: {
        ...process.env,
        NODE_ENV: 'development',
        FRONTEND_PORT,
        BACKEND_PORT
      }
    });

    console.log(`[wait-and-start] ✓ Electron spawned in ${Date.now() - electronStartTime}ms`);
    console.log(`[wait-and-start] ✓✓✓ Total wait-and-start time: ${Date.now() - startTime}ms`);

    electron.on('exit', (code) => {
      process.exit(code);
    });

  } catch (error) {
    console.error('[wait-and-start] Error:', error.message);
    process.exit(1);
  }
}

main();