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
    // No extra privileges for file://. The packaged renderer is served over
    // app:// (src/app-protocol.js), which is registered `standard` + `secure`
    // and carries its own origin — the app calls loadFile() nowhere, so
    // nothing loads from file:// at all.
    //
    // This was `true`, commented "required for loadFile to work", and that
    // stopped being true when v1.7.0 moved the renderer off file:// to fix the
    // blank-window bug. It granted a dead scheme the ability to reach other
    // file:// resources; turning it off narrows what a renderer compromise can
    // read from disk and costs nothing, because that scheme is unused.
    grantFileProtocolExtraPrivileges: false
  },

  files: [
    "src/**/*",
    "build/**/*"
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

  // Debian package hooks: symlink /usr/bin/reliant -> embedded backend so the
  // CLI is on $PATH immediately after `apt install` (no GUI launch needed).
  // See electron/build/deb-after-{install,remove}.sh.
  // AppImage users get the CLI installed at first GUI launch via
  // electron/src/cli-installer.js (~/.local/bin/reliant).
  deb: {
    afterInstall: "build/deb-after-install.sh",
    afterRemove: "build/deb-after-remove.sh"
  },

  nsis: {
    oneClick: false,
    allowToChangeInstallationDirectory: true,
    allowElevation: true,
    createDesktopShortcut: true,
    createStartMenuShortcut: true,
    shortcutName: "Reliant",
    // Custom installer hook: copies reliant-backend.exe to $INSTDIR\cli\reliant.exe
    // and adds $INSTDIR\cli to the user PATH so `reliant` works in cmd/PowerShell
    // without re-login. See electron/build/installer.nsh.
    include: "build/installer.nsh"
  },

  artifactName: "${productName}-${version}-${os}-${arch}.${ext}",

  // The portable build needs its OWN name. Both win targets are .exe for x64,
  // so the global artifactName above resolves them to the same
  // Reliant-<version>-win-x64.exe: portable overwrites the NSIS installer on
  // disk and then re-uploads that key to R2. The second PUT of a key already
  // being served has failed the publish step with `read ECONNRESET` three
  // times in a row on v1.7.13 — after the installers were uploaded but BEFORE
  // latest.yml was written, so Windows shipped binaries that no client was
  // ever offered (the feed stayed on 1.7.11).
  portable: {
    artifactName: "${productName}-${version}-${os}-${arch}-portable.${ext}"
  },

  dmg: {
    sign: false,
    writeUpdateInfo: true
  },

  afterSign: "build/notarize-safe.js"
};

module.exports = config;