#!/usr/bin/env node

const net = require('net');

/**
 * Check if a port is free
 */
function isPortFree(port) {
  return new Promise((resolve) => {
    const server = net.createServer();
    server.listen(port, '127.0.0.1', () => {
      server.close(() => resolve(true));
    });
    server.on('error', () => resolve(false));
  });
}

/**
 * Find a free port starting from basePort
 */
async function findFreePort(basePort) {
  for (let port = basePort; port < basePort + 200; port++) {
    if (await isPortFree(port)) {
      return port;
    }
  }
  throw new Error(`No free port found starting from ${basePort}`);
}

/**
 * Find N consecutive free ports
 */
async function findConsecutivePorts(basePort, count) {
  for (let start = basePort; start < basePort + 200; start++) {
    let allFree = true;
    for (let i = 0; i < count; i++) {
      if (!(await isPortFree(start + i))) {
        allFree = false;
        break;
      }
    }
    if (allFree) {
      return start;
    }
  }
  throw new Error(`No ${count} consecutive free ports found starting from ${basePort}`);
}

async function main() {
  // Random offset (0-99) to reduce collision when multiple instances start simultaneously
  const offset = Math.floor(Math.random() * 100);

  const frontend = await findFreePort(3000 + offset);
  const backend = await findFreePort(8080 + offset);
  const grpc = await findFreePort(9090 + offset);
  const toolsDaemon = await findFreePort(9190 + offset);
  const daemonFrontend = await findFreePort(9290 + offset);
  const temporalFrontend = await findConsecutivePorts(7000 + offset, 4);
  const temporalUI = await findFreePort(8233 + offset);
  const pprof = await findFreePort(6060 + offset);

  // Output for shell to eval
  console.log(`FRONTEND_PORT=${frontend}`);
  console.log(`BACKEND_PORT=${backend}`);
  console.log(`GRPC_PORT=${grpc}`);
  console.log(`TOOLS_DAEMON_PORT=${toolsDaemon}`);
  console.log(`DAEMON_FRONTEND_PORT=${daemonFrontend}`);
  console.log(`TEMPORAL_FRONTEND_PORT=${temporalFrontend}`);
  console.log(`TEMPORAL_UI_PORT=${temporalUI}`);
  console.log(`PPROF_PORT=${pprof}`);
}

main().catch(err => {
  console.error('Failed to find ports:', err.message);
  process.exit(1);
});
