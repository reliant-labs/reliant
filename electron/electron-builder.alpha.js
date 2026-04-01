const common = require('./electron-builder.common.js');

/**
 * @type {import('electron-builder').Configuration}
 */
const config = {
  ...common,
  linux: {
    ...common.linux,
    description: "AI-powered coding assistant with intelligent agents (Alpha Build)"
  },
  publish: [
    {
      provider: "s3",
      bucket: "downloads",
      region: "auto",
      endpoint: process.env.R2_ENDPOINT,
      acl: "public-read",
      channel: "alpha"
    }
  ]
};

module.exports = config;