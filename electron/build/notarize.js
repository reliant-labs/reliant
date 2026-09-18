// notarize.js — a LENIENT afterSign hook: missing credentials warn and skip
// rather than failing the build.
//
// No electron-builder config currently points here (release builds use
// notarize-safe.js), so this is the variant to reach for in a local or
// experimental config where an unnotarized --dir build is an acceptable output.
// If you want a build to FAIL when credentials are absent, use notarize-safe.js.
//
// All logic lives in notarize-core.js, shared with notarize-safe.js.

const { runNotarization } = require('./notarize-core.js');

exports.default = async function notarizing(context) {
  await runNotarization(context, { requireCredentials: false });
};
