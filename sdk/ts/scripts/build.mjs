// Build dist/ — the .js and .d.ts this package publishes.
//
// Runs from `prepack`, so the tarball carries dist/ and `npm publish` can never
// ship without it. It is not `prepare`: in the reliant repo this package is a
// pnpm workspace member of frontends/web, whose gates install with
// --ignore-scripts and consume the TypeScript sources directly, so no install
// should pay for a build nobody reads.
import { spawnSync } from "node:child_process";
import { existsSync, rmSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const pkgDir = dirname(dirname(fileURLToPath(import.meta.url)));
const tsc = join(pkgDir, "node_modules", "typescript", "bin", "tsc");

if (!existsSync(tsc)) {
  console.error("@reliantlabs/sdk: typescript is not installed here; run `pnpm install` in frontends/web (this package is a member of that workspace) or `npm install` in sdk/ts first");
  process.exit(1);
}

// A stale dist/ would let a deleted module keep shipping.
rmSync(join(pkgDir, "dist"), { recursive: true, force: true });

const result = spawnSync(process.execPath, [tsc, "-p", "tsconfig.build.json"], {
  cwd: pkgDir,
  stdio: "inherit",
});
process.exit(result.status ?? 1);
