#!/usr/bin/env node

const net = require('net');
const fs = require('fs');
const path = require('path');

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

// Find the first available port in a range
async function findFreePort(startPort, endPort) {
  for (let port = startPort; port <= endPort; port++) {
    if (await checkPort(port)) {
      return port;
    }
  }
  throw new Error(`No free port found between ${startPort} and ${endPort}`);
}

async function setupPorts() {
  // Check if we're in the electron directory
  const isElectronDir = process.cwd().endsWith('/electron');

  // If running from root with dev:multi, ports should already be set
  if (process.env.FRONTEND_PORT && process.env.BACKEND_PORT) {
    console.log(`Using existing port configuration:`);
    console.log(`  Frontend: ${process.env.FRONTEND_PORT}`);
    console.log(`  Backend: ${process.env.BACKEND_PORT}`);
    return;
  }

  // Check for parent directory's port config if in electron dir
  let portsConfig = null;
  const parentPortsFile = path.join(__dirname, '../../.ports.json');
  const localPortsFile = path.join(process.cwd(), '.ports.json');

  if (isElectronDir && fs.existsSync(parentPortsFile)) {
    portsConfig = JSON.parse(fs.readFileSync(parentPortsFile, 'utf8'));
    console.log(`Found parent directory port configuration`);
  } else if (fs.existsSync(localPortsFile)) {
    portsConfig = JSON.parse(fs.readFileSync(localPortsFile, 'utf8'));
    console.log(`Found local port configuration`);
  }

  if (portsConfig) {
    // Verify ports are available
    const frontendAvailable = await checkPort(portsConfig.frontend);
    const backendAvailable = await checkPort(portsConfig.backend);

    if (frontendAvailable && backendAvailable) {
      process.env.FRONTEND_PORT = portsConfig.frontend.toString();
      process.env.BACKEND_PORT = portsConfig.backend.toString();
      console.log(`Using saved port configuration:`);
      console.log(`  Frontend: ${process.env.FRONTEND_PORT}`);
      console.log(`  Backend: ${process.env.BACKEND_PORT}`);
      return;
    } else {
      console.log(`Saved ports are in use, finding new ones...`);
    }
  }

  // Find new available ports
  const frontendPort = await findFreePort(5173, 5273);
  const backendPort = await findFreePort(8080, 8180);

  process.env.FRONTEND_PORT = frontendPort.toString();
  process.env.BACKEND_PORT = backendPort.toString();

  // Save the configuration
  const config = {
    frontend: frontendPort,
    backend: backendPort,
    timestamp: new Date().toISOString()
  };

  const saveLocation = isElectronDir ? parentPortsFile : localPortsFile;
  fs.writeFileSync(saveLocation, JSON.stringify(config, null, 2));

  console.log(`Port Configuration:`);
  console.log(`  Frontend: ${process.env.FRONTEND_PORT}`);
  console.log(`  Backend: ${process.env.BACKEND_PORT}`);
}

setupPorts().catch(console.error);