/**
 * @type {import('electron-builder').Configuration}
 * @see https://www.electron.build/configuration/configuration
 */
const azureSignOptions = process.env.AZURE_TRUSTED_SIGNING_ENDPOINT ? {
  publisherName: process.env.AZURE_TRUSTED_SIGNING_PUBLISHER_NAME,
  endpoint: process.env.AZURE_TRUSTED_SIGNING_ENDPOINT,
  certificateProfileName: process.env.AZURE_TRUSTED_SIGNING_CERTIFICATE_PROFILE_NAME,
  codeSigningAccountName: process.env.AZURE_TRUSTED_SIGNING_ACCOUNT_NAME
} : null;

const config = {
  appId: "com.reliantlabs.reliant",
  productName: "Reliant",
  directories: {
    output: "dist",
    buildResources: "build"
  },
  
  // ASAR configuration with integrity validation
  asar: true,
  asarUnpack: [
    "**/*.node",
    "**/node_modules/sharp/**/*",
    "**/node_modules/@img/**/*"
  ],

  // Electron Fuses - Security hardening
  // https://www.electronjs.org/docs/latest/tutorial/fuses
  electronFuses: {
    // Validate ASAR archive integrity at runtime (tamper detection)
    enableEmbeddedAsarIntegrityValidation: true,
    // Only load app from app.asar (prevents code injection)
    onlyLoadAppFromAsar: true,
    // Encrypt cookies on disk using OS-level cryptography
    enableCookieEncryption: true,
    // Disable ELECTRON_RUN_AS_NODE for security
    runAsNode: false,
    // Disable NODE_OPTIONS environment variable
    enableNodeOptionsEnvironmentVariable: false,
    // Disable --inspect and similar debug flags in production
    enableNodeCliInspectArguments: false,
    // Enable file:// protocol privileges (required for loadFile to work)
    grantFileProtocolExtraPrivileges: true
  },

  files: [
    "src/**/*",
    "build/**/*",
    "cli/**/*"
  ],

  extraResources: [
    {
      from: "resources/server",
      to: "server",
      filter: ["**/*"]
    },
    {
      from: "../web/dist",
      to: "web",
      filter: ["**/*"]
    },
    {
      from: "cli",
      to: "cli",
      filter: ["**/*"]
    },
    {
      from: "src/preload.js",
      to: "preload.js"
    },
    {
      from: "../LICENSE",
      to: "LICENSE"
    },
    {
      from: "../LICENSES.txt",
      to: "LICENSES.txt"
    },
    {
      from: "src/update-helper.sh",
      to: "update-helper.sh"
    }
  ],

  protocols: [
    {
      name: "reliant",
      schemes: ["reliant"]
    }
  ],

  mac: {
    category: "public.app-category.developer-tools",
    hardenedRuntime: true,
    gatekeeperAssess: false,
    entitlements: "build/entitlements.mac.plist",
    entitlementsInherit: "build/entitlements.mac.plist",
    target: [
      { target: "dmg", arch: ["arm64", "x64"] },
      { target: "zip", arch: ["arm64", "x64"] }
    ],
    notarize: false
  },

  win: {
    target: [
      { target: "nsis", arch: ["x64", "arm64"] },
      { target: "portable", arch: ["x64"] }
    ],
    verifyUpdateCodeSignature: false,
    // Azure Trusted Signing (Artifact Signing)
    // Auth is provided via AZURE_* environment variables in GitHub Actions.
    // publisherName must match the certificate Common Name (CN) exactly.
    // Only applied if endpoint is defined (i.e. in CI/CD)
    ...(azureSignOptions ? { azureSignOptions } : {})
  },

  linux: {
    target: [
      { target: "AppImage", arch: ["x64", "arm64"] },
      { target: "deb", arch: ["x64", "arm64"] }
    ],
    category: "Development",
    description: "AI-powered coding assistant with intelligent agents",
    maintainer: "Reliant Labs <support@reliantlabs.io>",
    vendor: "Reliant Labs"
  },

  nsis: {
    oneClick: false,
    allowToChangeInstallationDirectory: true,
    allowElevation: true,
    createDesktopShortcut: true,
    createStartMenuShortcut: true,
    shortcutName: "Reliant"
  },

  artifactName: "${productName}-${version}-${os}-${arch}.${ext}",

  dmg: {
    sign: false,
    writeUpdateInfo: true
  },

  afterSign: "build/notarize-safe.js"
};

module.exports = config;
