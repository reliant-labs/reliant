#!/usr/bin/env node
"use strict";
// `npx @reliantlabs/forge <args>` runs the forge CLI. The binary matching this
// package's version is downloaded from the GitHub release on first use, checked
// against the release's sha256 sums, cached, and executed with stdio inherited.

const { spawn } = require("node:child_process");
const { version } = require("../package.json");
const { ensureBinary } = require("../lib/install.js");

ensureBinary(version)
  .then((binary) => {
    const child = spawn(binary, process.argv.slice(2), { stdio: "inherit" });
    for (const sig of ["SIGINT", "SIGTERM", "SIGHUP"]) {
      process.on(sig, () => child.kill(sig));
    }
    child.on("error", (err) => {
      process.stderr.write(`forge: could not run ${binary}: ${err.message}\n`);
      process.exit(1);
    });
    child.on("exit", (code, signal) => {
      if (signal) {
        process.removeAllListeners(signal);
        process.kill(process.pid, signal);
        return;
      }
      process.exit(code === null ? 1 : code);
    });
  })
  .catch((err) => {
    process.stderr.write(`forge: ${err.message}\n`);
    process.exit(1);
  });
