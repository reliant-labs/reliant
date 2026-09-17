// notarize-safe.js — the afterSign hook used by RELEASE builds
// (electron/electron-builder.common.js).
//
// "safe" means credentials are REQUIRED: a release that silently skipped
// notarization would ship an app Gatekeeper refuses to open, and the build must
// stop instead. SKIP_NOTARIZATION=true is the only bypass, and it is for local
// testing only.
//
// All logic lives in notarize-core.js, which notarize.js also uses. The two
// files were near-duplicate copies for a long time, and the cost of that showed
// up in the v1.7.14 outage: the 403 "agreement missing" classification had to be
// fixed in both, so it was fixed in neither.

const { runNotarization } = require('./notarize-core.js');

exports.default = async function notarizing(context) {
  await runNotarization(context, { requireCredentials: true });
};
