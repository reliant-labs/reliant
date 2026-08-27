const common = require('./electron-builder.common.js');

// Local, unsigned macOS build for testing changes without cutting a release.
//
// Why this config exists separately from electron-builder.dev.js: that one
// pins `identity: "Reliant Labs, Inc (23P64LQTZD)"`. On a machine without that
// certificate in the keychain, electron-builder SKIPS signing and leaves the
// binary ad-hoc signed — but the hardened runtime and the ASAR-integrity fuse
// are still baked in, and they reject an ad-hoc signature at load. The app is
// SIGKILLed by the kernel before a single line of JS runs, with an empty log
// and a CODESIGNING / "Code Signature Invalid" crash report. It looks exactly
// like a silent startup hang, and it is not one.
//
// So this config turns off the things that require a real certificate:
//   - identity: null            -> don't attempt Developer ID signing
//   - hardenedRuntime: false    -> ad-hoc signature is acceptable at load
//   - integrity fuses off       -> no ASAR hash check against an unsigned bundle
//
// Everything else — the daemon binaries, the packaged web bundle, build-config
// endpoints, protocol registration — matches a production build, so this is a
// faithful target for testing the packaged-app code paths.
//
// NOT a release artifact: unsigned and un-notarized, so it will not pass
// Gatekeeper on any machine but the one that built it. Release builds continue
// to use electron-builder.js / .alpha.js.
const { afterSign: _afterSign, ...commonWithoutAfterSign } = common;

/**
 * @type {import('electron-builder').Configuration}
 */
const config = {
  ...commonWithoutAfterSign,

  // Distinct identity so this build can run ALONGSIDE an installed Reliant.
  //
  // main.js takes app.requestSingleInstanceLock() when packaged, and that lock
  // is keyed on appId. Sharing "com.reliantlabs.reliant" means whichever copy
  // starts second logs "Another instance is already running", focuses the
  // FIRST one's window, and exits — so you appear to launch the new build and
  // get the old one, with no visible error.
  //
  // productName also changes, which gives this build its own userData
  // (~/Library/Application Support/reliant-local) and its own log file. That
  // isolation is deliberate: testing a packaged build must not read or
  // overwrite the session of the Reliant you actually use.
  appId: "com.reliantlabs.reliant.local",
  productName: "Reliant Local",

  // Rename the app INSIDE the asar as well. Electron derives userData from
  // package.json "name" (not productName), and that file ships inside the
  // archive as "reliant" — so without this override a local build reads and
  // overwrites the installed app's stored session. With it, this build gets
  // ~/Library/Application Support/reliant-local and its own log file.
  extraMetadata: {
    name: "reliant-local"
  },

  electronFuses: {
    ...common.electronFuses,
    // Both of these validate the ASAR against the code signature. With an
    // ad-hoc signature there is nothing trustworthy to validate against, and
    // the process is killed at load rather than reporting an error.
    enableEmbeddedAsarIntegrityValidation: false,
    onlyLoadAppFromAsar: false,
  },

  mac: {
    ...common.mac,
    identity: null,
    hardenedRuntime: false,
    gatekeeperAssess: false,
    notarize: false,
    // Single arch: this is for testing on the build machine, and skipping the
    // x64 slice roughly halves the build.
    target: [
      { target: "dir", arch: ["arm64"] }
    ]
  },

  // Never publish a local build.
  publish: null
};

module.exports = config;
