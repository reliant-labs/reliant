const common = require('./electron-builder.common.js');

/**
 * @type {import('electron-builder').Configuration}
 */
const config = {
  ...common,
  publish: [
    {
      provider: "s3",
      bucket: "downloads",
      region: "auto",
      endpoint: process.env.R2_ENDPOINT,
      acl: "public-read",
      channel: "latest"
    }
  ]
};

module.exports = config;