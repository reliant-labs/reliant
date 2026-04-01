#!/usr/bin/env node

const { spawn } = require('child_process');
const net = require('net');

// Check if a port is available
function checkPort(port) {
  return new Promise((resolve) => {
    const server = net.createServer();
    server.listen(port, '127.0.0.1', () => {
      server.close(() => resolve(true));
    });
    server.on('error', () => resolve(false));
  });
}

// Find the first available port starting from a given port
async function findFreePort(startPort, maxAttempts = 100) {
  for (let i = 0; i < maxAttempts; i++) {
    const port = startPort + i;
    if (await checkPort(port)) {
      return port;
    }
  }
  throw new Error(`No free port found after ${maxAttempts} attempts starting from ${startPort}`);
}

async function main() {
  const startTime = Date.now();
  console.log('[dev-multi] Starting development environment...');

  // If ports are already set, use them
  let frontendPort = process.env.FRONTEND_PORT;
  let backendPort = process.env.BACKEND_PORT;

  if (!frontendPort || !backendPort) {
    const portStart = Date.now();
    console.log('[dev-multi] Finding available ports...');

    // Use random starting points to avoid collisions
    const frontendBase = 5173 + Math.floor(Math.random() * 20) * 10;
    const backendBase = 8080 + Math.floor(Math.random() * 20) * 10;

    frontendPort = await findFreePort(frontendBase);
    backendPort = await findFreePort(backendBase);
    console.log(`[dev-multi] ✓ Ports found in ${Date.now() - portStart}ms`);
  }

  console.log(`[dev-multi] Port Configuration:`);
  console.log(`[dev-multi]   Frontend: ${frontendPort}`);
  console.log(`[dev-multi]   Backend: ${backendPort}`);

  // Set environment variables for child processes
  const env = {
    ...process.env,
    FRONTEND_PORT: frontendPort.toString(),
    BACKEND_PORT: backendPort.toString(),
    DEV_START_TIME: startTime.toString()  // Pass start time to children
  };

  console.log(`[dev-multi] Launching Vite and Electron...`);
  const launchStart = Date.now();

  // Run concurrently with the proper environment
  const child = spawn('npx', [
    'concurrently',
    '-k',
    '-n', 'vite,electron',
    '-c', 'auto',
    'npm:dev:vite',
    'npm:dev:electron'
  ], {
    stdio: 'inherit',
    env,
    shell: true
  });

  console.log(`[dev-multi] ✓ Processes spawned in ${Date.now() - launchStart}ms`);

  // Handle signals to ensure proper propagation
  const handleSignal = (signal) => {
    console.log(`\nReceived ${signal}, forwarding to child processes...`);
    child.kill(signal);
  };

  process.on('SIGINT', () => handleSignal('SIGINT'));
  process.on('SIGTERM', () => handleSignal('SIGTERM'));

  child.on('exit', (code, signal) => {
    console.log(`Child process exited (code: ${code}, signal: ${signal})`);
    process.exit(code || 0);
  });
}

main().catch(console.error);