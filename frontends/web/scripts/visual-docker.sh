#!/usr/bin/env bash
# Run the visual baseline (e2e/visual) inside the pinned Playwright Linux
# image, so the screenshots are the same pixels on every host: a Mac, a
# Linux laptop and a cloud machine all compare against one set of baselines.
#
#   scripts/visual-docker.sh [playwright args]   e.g. --update-snapshots, --grep chat
#   scripts/visual-docker.sh --pull              pull the pinned image and exit
#                                                (scripts/visual-gate.sh pulls it
#                                                while the renderer builds)
#
# The image is pinned to the exact @playwright/test the lockfile installs:
# the library and the browser in the image must be one release. Bumping
# Playwright means bumping PLAYWRIGHT_VERSION here too (the check below
# fails until you do) and re-capturing the baselines.
#
# It serves the existing build in dist/ (`task test:web:visual` builds it
# first) and mounts the checkout at the same path, so node_modules and the
# sibling sdk/ resolve inside the container. It runs as your user, so the
# baselines and reports it writes are yours.
set -euo pipefail

PLAYWRIGHT_VERSION="1.58.0"
# The digest pins the bytes, not just the tag.
IMAGE="mcr.microsoft.com/playwright:v${PLAYWRIGHT_VERSION}-noble@sha256:35c7d48b4ccaf3aca5018f5f1bf7f50c7da7d61d176c530741f4f2e9ca336c34"

WEB="$(cd "$(dirname "$0")/.." && pwd)"
REPO="$(cd "$WEB/../.." && pwd)"

die() { echo "visual-docker: $*" >&2; exit 2; }

command -v docker >/dev/null 2>&1 ||
  die "docker is required. The visual baseline runs in the pinned Playwright image ($IMAGE) so its pixels match on every host. Install Docker, or debug one surface on this host with VISUAL_ALLOW_HOST=1 npx playwright test -c playwright.visual.config.ts (not comparable with the baselines)."
docker info >/dev/null 2>&1 || die "docker is installed but its daemon is not reachable (docker info failed). Start Docker and re-run."
if [ "${1:-}" = "--pull" ]; then
  exec docker pull --quiet "$IMAGE"
fi

installed="$(node -p "require('$WEB/node_modules/@playwright/test/package.json').version" 2>/dev/null)" ||
  die "frontends/web/node_modules has no @playwright/test: run task install:web first."
[ "$installed" = "$PLAYWRIGHT_VERSION" ] ||
  die "the lockfile installs @playwright/test $installed but this script pins the $PLAYWRIGHT_VERSION image. Set PLAYWRIGHT_VERSION=$installed in $0 and re-capture the baselines (-- --update-snapshots)."
[ -f "$WEB/dist/index.html" ] || die "no build in frontends/web/dist: run task test:web:visual, which builds it."

# The settings footer prints the build's version. A build of a commit, a tag
# or a dirty tree each prints a different one, so the baselines are captured
# from a build whose version is pinned (Taskfile.yml's test:web:visual builds
# with RELEASE_VERSION set to this).
VISUAL_RELEASE_VERSION="0.0.0-visual"
built="$(node -p "require('$WEB/dist/version.json').version" 2>/dev/null || echo "none")"
[ "$built" = "$VISUAL_RELEASE_VERSION" ] ||
  die "frontends/web/dist was built as version '$built', not the visual lane's pinned '$VISUAL_RELEASE_VERSION'. Rebuild it: task test:web:visual (without VISUAL_SKIP_BUILD), or RELEASE_VERSION=$VISUAL_RELEASE_VERSION npm run build:alpha."

tty=()
[ -t 1 ] && tty=(-t)
# ${tty[@]+…}: macOS's bash 3.2 calls an empty array unbound under set -u.
exec docker run --rm -i ${tty[@]+"${tty[@]}"} --init --ipc=host \
  --user "$(id -u):$(id -g)" \
  -e HOME=/tmp -e NPM_CONFIG_UPDATE_NOTIFIER=false -e CI -e VISUAL_IN_CONTAINER=1 -e VISUAL_FXLOG -e VISUAL_WORKERS -e VISUAL_PORT -e VISUAL_JSON \
  -v "$REPO:$REPO" -w "$WEB" \
  "$IMAGE" \
  npx --no-install playwright test -c playwright.visual.config.ts "$@"
