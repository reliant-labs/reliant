// Metro bundler configuration — https://docs.expo.dev/guides/customizing-metro/
//
// Two departures from the Expo defaults. Both are load-bearing for the API
// client; delete either and `expo start` fails to resolve a module.
//
//   1. unstable_enablePackageExports. @connectrpc/connect, @bufbuild/protobuf
//      and @reliantlabs/forge-web-runtime declare their entry points ONLY through
//      package.json "exports" — no physical file at the old path. Metro in
//      this Expo SDK still defaults that resolution off, so without the flag
//      the first RPC import dies on `Unable to resolve module
//      @bufbuild/protobuf/wire`. A later Expo SDK turns it on by default.
//
//   2. The WORKSPACE ROOT's node_modules. npm workspaces HOIST shared
//      dependencies to the root, and Metro resolves only against the paths it
//      is given — so a package installed at <root>/node_modules is invisible
//      to an app at <root>/frontends/<name>. That is not a hypothetical: it
//      broke the scaffolded mobile app outright, because expo-router reaches
//      for `query-string` from inside its own build output and gets hoisted
//      away from it:
//
//        Unable to resolve module query-string from
//        node_modules/expo-router/build/fork/getPathFromState.js
//
//      The failure names a package the app never imports, three layers down
//      in somebody else's file, which is why it reads as an upstream bug
//      rather than a resolver configuration gap.
//
//   3. watchFolders / nodeModulesPaths, for the local runtime bridge. When
//      the forge binary is a dev build, `forge generate` symlinks its own
//      checkout of @reliantlabs/forge-web-runtime into node_modules so edits land
//      here with nothing published. Metro crawls only the project root, so a
//      symlink pointing outside it has to be watched explicitly — and the
//      helpers its transformed files pull in (@babel/runtime) have to resolve
//      back against THIS app's node_modules. Both lines are inert once the
//      package is installed normally from the registry.
const path = require("node:path");

const { getDefaultConfig } = require("expo/metro-config");

const config = getDefaultConfig(__dirname);

config.resolver.unstable_enablePackageExports = true;

const projectNodeModules = path.join(__dirname, "node_modules");
config.resolver.nodeModulesPaths = [
  ...(config.resolver.nodeModulesPaths ?? []),
  projectNodeModules,
];

// The workspace root, when this app is part of one. Found by walking up for a
// package.json that declares "workspaces" rather than by assuming a fixed
// depth, so it keeps working if the app moves or is scaffolded standalone —
// in which case nothing is added and the app resolves entirely locally.
const workspaceRoot = (() => {
  let dir = path.dirname(__dirname);
  for (let i = 0; i < 5; i += 1) {
    const manifest = path.join(dir, "package.json");
    try {
      if (require(manifest).workspaces) return dir;
    } catch {
      // No package.json here, or unreadable: keep walking.
    }
    const parent = path.dirname(dir);
    if (parent === dir) break;
    dir = parent;
  }
  return null;
})();

if (workspaceRoot) {
  config.resolver.nodeModulesPaths.push(
    path.join(workspaceRoot, "node_modules"),
  );
  // Metro must also WATCH the root, or a hoisted package it can now resolve
  // still fails to transform: resolution and the file crawler are separate
  // concerns, and satisfying only the first yields a confusing partial fix.
  config.watchFolders = [...(config.watchFolders ?? []), workspaceRoot];
}

try {
  // require.resolve follows symlinks, so this is the runtime's REAL location.
  const runtimeDir = path.dirname(
    require.resolve("@reliantlabs/forge-web-runtime/package.json", {
      paths: [__dirname],
    }),
  );
  if (!runtimeDir.startsWith(projectNodeModules)) {
    config.watchFolders = [...(config.watchFolders ?? []), runtimeDir];
  }
} catch {
  // Not installed yet (pre-`npm install`). Metro will report the missing
  // import itself, with a better message than a config-time crash.
}

module.exports = config;
