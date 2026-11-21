#!/usr/bin/env bash
# Self-test: every `pnpm run dist ...` scripts/build-unified.sh runs reaches
# electron-builder with exactly the switches it names (--publish=never and the
# platform flags), with no `--` in front of them.
#
# npm run drops the `--` that separates its own options from the script's;
# pnpm run forwards it to the script. Through dist -> build -> the release-config
# wrapper -> pack, a forwarded `--` lands in electron-builder's argv before
# --publish=never and the platform switches, which then no longer apply.
#
# Hermetic: a temp copy of electron/package.json keeps its real dist, build and
# pack scripts; build:config and build:web become no-ops, build-electron.sh and
# with-release-config.mjs are stubs (the real wrapper forwards its argv
# verbatim through spawnSync), and electron-builder is a stub that records its
# argv. Needs node and pnpm on PATH.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

mkdir -p "$TMP/electron/scripts" "$TMP/electron/node_modules/.bin" "$TMP/scripts"
node -e '
const fs = require("fs");
const pkg = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
pkg.scripts["build:config"] = "true";
pkg.scripts["build:web"] = "true";
delete pkg.packageManager;
fs.writeFileSync(process.argv[2], JSON.stringify({ name: "t", private: true, scripts: pkg.scripts }));
' "$ROOT/electron/package.json" "$TMP/electron/package.json"

cat >"$TMP/electron/scripts/with-release-config.mjs" <<'EOF'
import { spawnSync } from "node:child_process";
const [command, ...args] = process.argv.slice(2);
process.exit(spawnSync(command, args, { stdio: "inherit" }).status ?? 1);
EOF
printf '#!/usr/bin/env bash\nexit 0\n' >"$TMP/scripts/build-electron.sh"
cat >"$TMP/electron/node_modules/.bin/electron-builder" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$@" >"$TMP/argv"
EOF
chmod +x "$TMP/scripts/build-electron.sh" "$TMP/electron/node_modules/.bin/electron-builder"

mapfile -t invocations < <(sed -n 's/^[[:space:]]*run_cmd pnpm run dist\(.*\)$/\1/p' "$ROOT/scripts/build-unified.sh")
if [[ ${#invocations[@]} -eq 0 ]]; then
    echo "FAIL: no 'run_cmd pnpm run dist' lines found in scripts/build-unified.sh" >&2
    exit 1
fi

fail=0
for rest in "${invocations[@]}"; do
    read -r -a args <<<"$rest"
    rm -f "$TMP/argv"
    (cd "$TMP/electron" && npm_config_manage_package_manager_versions=false \
        pnpm run --silent dist "${args[@]}" >/dev/null 2>&1) || true
    if [[ ! -f "$TMP/argv" ]]; then
        echo "FAIL: pnpm run dist$rest never reached electron-builder" >&2
        fail=1
        continue
    fi
    mapfile -t got <"$TMP/argv"
    # pack's own `--config electron-builder.js`, then exactly the requested switches.
    want=(--config electron-builder.js)
    for a in "${args[@]}"; do [[ "$a" == "--" ]] || want+=("$a"); done
    if [[ "${got[*]}" != "${want[*]}" ]]; then
        echo "FAIL: pnpm run dist$rest -> electron-builder ${got[*]} (want ${want[*]})" >&2
        fail=1
        continue
    fi
    if [[ " ${got[*]} " != *" --publish=never "* ]]; then
        echo "FAIL: pnpm run dist$rest -> electron-builder ${got[*]}: no --publish=never" >&2
        fail=1
        continue
    fi
    echo "ok: pnpm run dist$rest -> electron-builder ${got[*]}"
done
exit "$fail"
