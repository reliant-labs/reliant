#!/usr/bin/env node

const { execSync } = require('child_process');
const fs = require('fs');
const path = require('path');

function killPort(port) {
  try {
    if (process.platform === 'darwin' || process.platform === 'linux') {
      // Use lsof on macOS/Linux
      try {
        const pid = execSync(`lsof -ti tcp:${port}`, { encoding: 'utf8' }).trim();
        if (pid) {
          execSync(`kill -9 ${pid}`);
          console.log(`Killed process on port ${port} (PID: ${pid})`);
        }
      } catch (e) {
        // Port not in use or no permission
      }
    } else if (process.platform === 'win32') {
      // Use netstat on Windows
      try {
        execSync(`netstat -ano | findstr :${port} | findstr LISTENING`);
        execSync(`for /f "tokens=5" %a in ('netstat -ano ^| findstr :${port} ^| findstr LISTENING') do taskkill /PID %a /F`);
        console.log(`Killed process on port ${port}`);
      } catch (e) {
        // Port not in use
      }
    }
  } catch (error) {
    // Ignore errors (port might not be in use)
  }
}

// Check for port configuration
const parentPortsFile = path.join(__dirname, '../../.ports.json');
const localPortsFile = path.join(process.cwd(), '.ports.json');

let portsToKill = [];

// Get ports from config or use defaults
if (fs.existsSync(parentPortsFile)) {
  const config = JSON.parse(fs.readFileSync(parentPortsFile, 'utf8'));
  portsToKill = [config.frontend, config.backend];
} else if (fs.existsSync(localPortsFile)) {
  const config = JSON.parse(fs.readFileSync(localPortsFile, 'utf8'));
  portsToKill = [config.frontend, config.backend];
} else {
  // Use default ports
  portsToKill = [5173, 8080];
}

console.log('Cleaning up ports...');
portsToKill.forEach(port => killPort(port));
console.log('Ports cleaned up');