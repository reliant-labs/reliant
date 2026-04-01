#!/usr/bin/env node

const fs = require('fs');
const path = require('path');
const { execSync } = require('child_process');
const os = require('os');

// This script runs after the Electron app is installed
// It sets up the CLI command in the user's PATH

function installCLI() {
  const platform = os.platform();
  
  if (platform === 'darwin') {
    // macOS installation
    installMacOS();
  } else if (platform === 'linux') {
    // Linux installation
    installLinux();
  } else if (platform === 'win32') {
    // Windows installation
    installWindows();
  } else {
    console.log('Unsupported platform:', platform);
  }
}

function installMacOS() {
  try {
    // For macOS app bundle
    const appPath = '/Applications/Reliant.app';
    const cliSource = path.join(appPath, 'Contents', 'Resources', 'cli', 'reliant');
    const cliTarget = '/usr/local/bin/reliant';
    
    // Create /usr/local/bin if it doesn't exist
    if (!fs.existsSync('/usr/local/bin')) {
      execSync('sudo mkdir -p /usr/local/bin');
    }
    
    // Create symlink
    try {
      // Remove existing symlink if it exists
      if (fs.existsSync(cliTarget)) {
        fs.unlinkSync(cliTarget);
      }
      
      // Create new symlink
      fs.symlinkSync(cliSource, cliTarget);
      console.log('✓ Reliant CLI command installed successfully');
      console.log('  You can now use "reliant" command from Terminal');
    } catch (error) {
      // If permission denied, provide instructions
      console.log('Installing Reliant CLI command...');
      console.log('Please run the following command to complete installation:');
      console.log(`  sudo ln -sf "${cliSource}" ${cliTarget}`);
    }
  } catch (error) {
    console.error('Error installing CLI:', error.message);
  }
}

function installLinux() {
  try {
    // Detect installation path (could be AppImage, deb, rpm, etc)
    const possiblePaths = [
      '/opt/Reliant/resources/cli/reliant',
      '/usr/lib/reliant/resources/cli/reliant',
      path.join(process.resourcesPath, 'cli', 'reliant')
    ];
    
    let cliSource = null;
    for (const p of possiblePaths) {
      if (fs.existsSync(p)) {
        cliSource = p;
        break;
      }
    }
    
    if (!cliSource) {
      console.log('Could not find Reliant CLI script');
      return;
    }
    
    const cliTarget = '/usr/local/bin/reliant';
    
    try {
      if (fs.existsSync(cliTarget)) {
        fs.unlinkSync(cliTarget);
      }
      fs.symlinkSync(cliSource, cliTarget);
      console.log('✓ Reliant CLI command installed successfully');
    } catch (error) {
      console.log('Please run the following command to complete installation:');
      console.log(`  sudo ln -sf "${cliSource}" ${cliTarget}`);
    }
  } catch (error) {
    console.error('Error installing CLI:', error.message);
  }
}

function installWindows() {
  try {
    // For Windows, we'll add a batch file to the PATH
    const appPath = path.join(process.env.LOCALAPPDATA, 'Programs', 'reliant');
    const cliSource = path.join(appPath, 'resources', 'cli', 'reliant.bat');
    
    // Create batch file wrapper
    const batchContent = `@echo off
set TARGET_DIR=%1
if "%TARGET_DIR%"=="" set TARGET_DIR=%cd%
start "" "reliant://open?path=%TARGET_DIR%"
`;
    
    fs.writeFileSync(cliSource, batchContent);
    
    console.log('✓ Reliant CLI command prepared');
    console.log('To use the "reliant" command from Command Prompt or PowerShell,');
    console.log(`add the following directory to your PATH environment variable:`);
    console.log(`  ${path.dirname(cliSource)}`);
  } catch (error) {
    console.error('Error installing CLI:', error.message);
  }
}

// Run installation if this script is executed directly
if (require.main === module) {
  installCLI();
}

module.exports = { installCLI };