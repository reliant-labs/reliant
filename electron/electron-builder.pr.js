const common = require('./electron-builder.common.js');

/**
 * @type {import('electron-builder').Configuration}
 */
const config = {
  ...common,
  // Never run afterSign hooks (production config runs notarize-safe.js here)
  afterSign: null,
  
  // Ensure no publish attempts
  publish: null,
  
  mac: {
    ...common.mac,
    // Disable code signing for PR builds
    identity: null,
    hardenedRuntime: false,
    gatekeeperAssess: false
    // PR CI passes --dir, so we don't need installer targets here.
  }
};

module.exports = config;
