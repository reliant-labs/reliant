const common = require('./electron-builder.common.js');

// Dev packaging should not run notarization hooks.
// Production/alpha configs continue to use common.afterSign.
const { afterSign: _afterSign, ...commonWithoutAfterSign } = common;

/**
 * @type {import('electron-builder').Configuration}
 */
const config = {
  ...commonWithoutAfterSign,
  mac: {
    ...common.mac,
    identity: "Reliant Labs, Inc (23P64LQTZD)",
    target: [
      { target: "dmg", arch: ["arm64"] },
      { target: "zip", arch: ["arm64"] }
    ]
  },
  win: {
    ...common.win,
    target: [
      { target: "portable", arch: ["x64"] }
    ]
  },
  publish: [
    {
      provider: "s3",
      bucket: "downloads",
      region: "auto",
      endpoint: process.env.R2_ENDPOINT,
      acl: "public-read",
      channel: "dev"
    }
  ]
};

module.exports = config;