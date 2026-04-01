#!/usr/bin/env node

const { spawn, execSync } = require('child_process');
const fs = require('fs');
const path = require('path');
const net = require('net');

// Colors for output
const colors = {
  red: '\x1b[31m',
  green: '\x1b[32m',
  yellow: '\x1b[33m',
  blue: '\x1b[34m',
  reset: '\x1b[0m'
};

const PROJECT_ROOT = path.resolve(__dirname, '..');
const WEB_DIR = path.join(PROJECT_ROOT, 'web');
const ELECTRON_DIR = path.join(PROJECT_ROOT, 'electron');

// Track Docker container name for cleanup
let temporalContainerName = null;

// Function to print colored messages
function print(message, color = 'reset') {
  console.log(`${colors[color]}${message}${colors.reset}`);
}

function printStep(message) {
  console.log(`\n${colors.yellow}🚀 ${message}${colors.reset}`);
}

// Function to check if command exists
function commandExists(command) {
  try {
    const cmd = process.platform === 'win32' ? 'where' : 'which';
    execSync(`${cmd} ${command}`, { stdio: 'ignore' });
    return true;
  } catch (error) {
    return false;
  }
}

// Function to check if port is available
function isPortAvailable(port) {
  return new Promise((resolve) => {
    const server = net.createServer();
    server.once('error', () => resolve(false));
    server.once('listening', () => {
      server.close();
      resolve(true);
    });
    server.listen(port, '127.0.0.1');
  });
}

// Function to check if port is used by Docker
function isPortUsedByDocker(port) {
  if (!commandExists('docker')) {
    return false;
  }
  try {
    const output = execSync(`docker ps --format "{{.Ports}}"`, { encoding: 'utf8' });
    return output.includes(`:${port}-`);
  } catch (error) {
    return false;
  }
}

// Function to find a free port starting from a given port
async function findFreePort(startPort, maxPort = startPort + 1000) {
  for (let port = startPort; port <= maxPort; port++) {
    if (await isPortAvailable(port) && !isPortUsedByDocker(port)) {
      return port;
    }
  }
  throw new Error(`Could not find free port between ${startPort} and ${maxPort}`);
}

// Function to find consecutive free ports
async function findConsecutivePorts(startPort, count, maxPort = startPort + 1000) {
  for (let port = startPort; port <= maxPort - count + 1; port++) {
    let allFree = true;
    for (let i = 0; i < count; i++) {
      if (!(await isPortAvailable(port + i))) {
        allFree = false;
        break;
      }
    }
    if (allFree) {
      return port;
    }
  }
  throw new Error(`Could not find ${count} consecutive free ports`);
}

// Function to run command and return output
function runCommand(command, args = [], options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      stdio: 'inherit',
      shell: true,
      ...options
    });

    child.on('close', (code) => {
      if (code !== 0 && !options.ignoreErrors) {
        reject(new Error(`Command failed with exit code ${code}`));
      } else {
        resolve(code);
      }
    });

    child.on('error', reject);
  });
}

// Function to run command and capture output
function runCommandCapture(command, args = [], options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      shell: true,
      ...options
    });

    let stdout = '';
    let stderr = '';

    if (child.stdout) {
      child.stdout.on('data', (data) => {
        stdout += data.toString();
      });
    }

    if (child.stderr) {
      child.stderr.on('data', (data) => {
        stderr += data.toString();
      });
    }

    child.on('close', (code) => {
      if (code !== 0 && !options.ignoreErrors) {
        reject(new Error(`Command failed: ${stderr}`));
      } else {
        resolve({ stdout, stderr, code });
      }
    });

    child.on('error', reject);
  });
}

// Cleanup function
let cleanupInProgress = false;
async function cleanup() {
  // Prevent multiple cleanup runs
  if (cleanupInProgress) return;
  cleanupInProgress = true;

  print('\nReceived shutdown signal, cleaning up...', 'blue');

  if (temporalContainerName && commandExists('docker')) {
    try {
      print(`Stopping Temporal UI container ${temporalContainerName}...`, 'blue');
      // Use shorter timeout (5s) since Temporal UI doesn't need graceful shutdown
      execSync(`docker stop -t 5 ${temporalContainerName}`, { stdio: 'pipe' });
    } catch (error) {
      // docker stop failed, try force removal
      print('Warning: docker stop failed, forcing removal...', 'yellow');
      try {
        execSync(`docker rm -f ${temporalContainerName}`, { stdio: 'ignore' });
      } catch (rmError) {
        // Ignore - container may already be gone
      }
    }
  }

  process.exit(0);
}

// Register cleanup handlers
process.on('SIGINT', cleanup);
process.on('SIGTERM', cleanup);
process.on('exit', cleanup);

async function main() {
  try {
    const V2_BACKEND = process.env.V2_BACKEND === 'true';

    print('Starting Reliant Electron Development Environment', 'blue');

    // Check prerequisites
    printStep('Checking prerequisites...');

    if (!commandExists('go')) {
      print('❌ Go is not installed', 'red');
      process.exit(1);
    }

    if (!commandExists('npm')) {
      print('❌ npm is not installed', 'red');
      process.exit(1);
    }

    if (!commandExists('node')) {
      print('❌ Node.js is not installed', 'red');
      process.exit(1);
    }

    print('✅ Prerequisites check passed', 'green');

    // Install dependencies if needed
    printStep('Installing dependencies...');

    if (!fs.existsSync(path.join(WEB_DIR, 'node_modules'))) {
      print('Installing web dependencies...');
      process.chdir(WEB_DIR);
      await runCommand('npm', ['ci', '--legacy-peer-deps']);
    }

    if (!fs.existsSync(path.join(ELECTRON_DIR, 'node_modules'))) {
      print('Installing Electron dependencies...');
      process.chdir(ELECTRON_DIR);
      await runCommand('npm', ['ci']);
    }

    print('✅ Dependencies ready', 'green');

    // Build backend if V2_BACKEND is set
    if (V2_BACKEND) {
      printStep('Building V2 backend...');
      process.chdir(PROJECT_ROOT);

      const distDir = path.join(PROJECT_ROOT, 'dist');
      if (!fs.existsSync(distDir)) {
        fs.mkdirSync(distDir, { recursive: true });
      }

      const outputPath = path.join(distDir, process.platform === 'win32' ? 'reliant.exe' : 'reliant');
      await runCommand('go', ['build', '-buildvcs=false', '-o', outputPath, './cmd/reliant/']);

      print('✅ V2 backend built successfully', 'green');
    }

    // Find available ports
    printStep('Finding available ports...');
    process.chdir(PROJECT_ROOT);

    // Run the port finder
    const { stdout: portOutput } = await runCommandCapture('node', ['scripts/find-ports.js']);

    // Parse ports from output
    const frontendMatch = portOutput.match(/FRONTEND_PORT=(\d+)/);
    const backendMatch = portOutput.match(/BACKEND_PORT=(\d+)/);
    const grpcMatch = portOutput.match(/GRPC_PORT=(\d+)/);
    const toolsDaemonMatch = portOutput.match(/TOOLS_DAEMON_PORT=(\d+)/);
    const temporalFrontendMatch = portOutput.match(/TEMPORAL_FRONTEND_PORT=(\d+)/);
    const temporalUIMatch = portOutput.match(/TEMPORAL_UI_PORT=(\d+)/);

    if (!frontendMatch || !backendMatch || !grpcMatch || !toolsDaemonMatch || !temporalFrontendMatch || !temporalUIMatch) {
      print('❌ Failed to parse port output', 'red');
      process.exit(1);
    }

    const FRONTEND_PORT = frontendMatch[1];
    const BACKEND_PORT = backendMatch[1];
    const GRPC_PORT = grpcMatch[1];
    const TOOLS_DAEMON_PORT = toolsDaemonMatch[1];
    const TEMPORAL_FRONTEND_PORT = temporalFrontendMatch[1];
    const TEMPORAL_UI_PORT = temporalUIMatch[1];

    print('✅ Found available ports', 'green');
    print(`  Frontend: ${colors.blue}${FRONTEND_PORT}${colors.reset}`);
    print(`  Backend: ${colors.blue}${BACKEND_PORT}${colors.reset}`);
    print(`  gRPC: ${colors.blue}${GRPC_PORT}${colors.reset}`);
    print(`  Tools daemon gRPC: ${colors.blue}${TOOLS_DAEMON_PORT}${colors.reset}`);
    print('  Temporal:');
    print(`    Frontend (client API):  ${colors.blue}${TEMPORAL_FRONTEND_PORT}${colors.reset}`);
    print(`    History:                ${colors.blue}${TEMPORAL_FRONTEND_PORT + 1}${colors.reset}`);
    print(`    Matching:               ${colors.blue}${TEMPORAL_FRONTEND_PORT + 2}${colors.reset}`);
    print(`    Worker:                 ${colors.blue}${TEMPORAL_FRONTEND_PORT + 3}${colors.reset}`);
    print(`  Temporal UI: ${colors.blue}${TEMPORAL_UI_PORT}${colors.reset}`);

    // Start development environment
    printStep('Starting development environment...');

    print('This will start:', 'green');
    if (V2_BACKEND) {
      print(`  ${colors.blue}1. Go V2 backend server on port ${BACKEND_PORT}${colors.reset}`);
      print(`  ${colors.blue}2. Embedded Temporal server:${colors.reset}`);
      print(`       Frontend: ${TEMPORAL_FRONTEND_PORT}, History: ${TEMPORAL_FRONTEND_PORT + 1}, Matching: ${TEMPORAL_FRONTEND_PORT + 2}, Worker: ${TEMPORAL_FRONTEND_PORT + 3}`);
      print(`  ${colors.blue}3. Temporal UI (web UI on port ${TEMPORAL_UI_PORT})${colors.reset}`);
      print(`  ${colors.blue}4. Vite dev server on port ${FRONTEND_PORT}${colors.reset}`);
      print(`  ${colors.blue}5. Electron app${colors.reset}`);
    } else {
      print(`  ${colors.blue}1. Go backend server on port ${BACKEND_PORT}${colors.reset}`);
      print(`  ${colors.blue}2. Vite dev server on port ${FRONTEND_PORT}${colors.reset}`);
      print(`  ${colors.blue}3. Electron app${colors.reset}`);
    }

    // Export environment variables
    process.env.NODE_ENV = 'development';
    process.env.FRONTEND_PORT = FRONTEND_PORT;
    process.env.BACKEND_PORT = BACKEND_PORT;
    process.env.API_PORT = BACKEND_PORT;
    process.env.GRPC_PORT = GRPC_PORT;
    process.env.TOOLS_DAEMON_PORT = TOOLS_DAEMON_PORT;
    process.env.TEMPORAL_FRONTEND_PORT = TEMPORAL_FRONTEND_PORT.toString();
    process.env.TEMPORAL_UI_PORT = TEMPORAL_UI_PORT.toString();
    process.env.USE_DEV_SERVER = 'true';

    // Write ports to file
    const portsContent = `export FRONTEND_PORT=${FRONTEND_PORT}\nexport BACKEND_PORT=${BACKEND_PORT}\nexport API_PORT=${BACKEND_PORT}\nexport GRPC_PORT=${GRPC_PORT}\nexport TOOLS_DAEMON_PORT=${TOOLS_DAEMON_PORT}\nexport TEMPORAL_FRONTEND_PORT=${TEMPORAL_FRONTEND_PORT}\nexport TEMPORAL_UI_PORT=${TEMPORAL_UI_PORT}\n`;
    fs.writeFileSync(path.join(PROJECT_ROOT, '.env.ports'), portsContent);

    // Start Temporal UI if running V2 backend
    if (V2_BACKEND) {
      const TEMPORAL_ADDRESS = `host.docker.internal:${TEMPORAL_FRONTEND_PORT}`;
      temporalContainerName = `temporal-ui-dev-${BACKEND_PORT}`;

      print('ℹ️  V2 Backend detected - Temporal UI is required', 'blue');

      // Check if Docker is installed
      if (!commandExists('docker')) {
        print('❌ Docker is required for V2 backend (Temporal UI)', 'red');
        print('   Install Docker Desktop: https://www.docker.com/products/docker-desktop', 'red');
        process.exit(1);
      }

      // Check if Docker daemon is running
      try {
        execSync('docker ps', { stdio: 'ignore' });
      } catch (error) {
        print('❌ Docker daemon is not running', 'red');
        print('   Start Docker Desktop and try again', 'red');
        process.exit(1);
      }

      // Stop any existing container with this name
      try {
        const containerCheck = execSync(`docker ps -a --filter "name=${temporalContainerName}" --format "{{.Names}}"`, { encoding: 'utf8' });
        if (containerCheck.trim() === temporalContainerName) {
          print(`Stopping existing Temporal UI container ${temporalContainerName}...`, 'blue');
          execSync(`docker stop ${temporalContainerName}`, { stdio: 'ignore' });
          execSync(`docker rm ${temporalContainerName}`, { stdio: 'ignore' });
        }
      } catch (error) {
        // Ignore errors if container doesn't exist
      }

      print(`Starting Temporal UI with Docker on port ${TEMPORAL_UI_PORT}...`, 'green');

      // Start Temporal UI in background
      try {
        const dockerCmd = `docker run -d --name ${temporalContainerName} --rm --add-host=host.docker.internal:host-gateway -p ${TEMPORAL_UI_PORT}:8080 -e TEMPORAL_ADDRESS=${TEMPORAL_ADDRESS} -e TEMPORAL_CORS_ORIGINS=http://localhost:3000 temporalio/ui:latest`;
        execSync(dockerCmd, { encoding: 'utf8' });
      } catch (error) {
        print('❌ Failed to start Temporal UI container', 'red');
        print(`   Error: ${error.message}`, 'red');
        if (error.stderr) {
          print(`   Docker stderr: ${error.stderr}`, 'red');
        }
        print('', 'reset');
        print('Troubleshooting steps:', 'yellow');
        print('  1. Check if Docker Desktop is running', 'yellow');
        print('  2. Check if the port is already in use:', 'yellow');
        print(`     lsof -i :${TEMPORAL_UI_PORT}`, 'yellow');
        print('  3. Check for existing Temporal UI containers:', 'yellow');
        print('     docker ps -a | grep temporal', 'yellow');
        print('  4. Clean up old containers:', 'yellow');
        print('     docker rm -f $(docker ps -a -q --filter "name=temporal-ui-dev")', 'yellow');
        process.exit(1);
      }

      print(`✅ Temporal UI started on http://localhost:${TEMPORAL_UI_PORT}`, 'green');
      print(`   Container: ${temporalContainerName}`, 'blue');
      print('   Access Temporal UI via dev button in app header', 'blue');
    }

    printStep('Launching Vite + Electron (Ctrl-C to stop all)');
    process.chdir(ELECTRON_DIR);

    // Run npm run dev
    await runCommand('npm', ['run', 'dev']);

  } catch (error) {
    print(`❌ Error: ${error.message}`, 'red');
    process.exit(1);
  }
}

main();