#!/usr/bin/env node
/**
 * Cross-platform dev script runner
 * Runs dev.ps1 on Windows, dev.sh on Unix
 */
const { spawn } = require('child_process');
const path = require('path');
const os = require('os');

const scriptsDir = __dirname;
const isWindows = os.platform() === 'win32';

let child;

if (isWindows) {
  // Run PowerShell script on Windows
  const ps1Path = path.join(scriptsDir, 'dev.ps1');
  console.log('Running Windows development environment (PowerShell)...');
  child = spawn('powershell.exe', ['-ExecutionPolicy', 'Bypass', '-File', ps1Path], {
    stdio: 'inherit',
    cwd: path.dirname(scriptsDir),
  });
} else {
  // Run bash script on Unix
  const shPath = path.join(scriptsDir, 'dev.sh');
  console.log('Running Unix development environment (Bash)...');
  child = spawn('bash', [shPath], {
    stdio: 'inherit',
    cwd: path.dirname(scriptsDir),
  });
}

// Forward signals to child process
['SIGINT', 'SIGTERM', 'SIGHUP'].forEach((signal) => {
  process.on(signal, () => {
    if (child && !child.killed) {
      child.kill(signal);
    }
  });
});

child.on('error', (err) => {
  console.error('Failed to start development environment:', err.message);
  process.exit(1);
});

child.on('exit', (code) => {
  process.exit(code || 0);
});
