# WSL Development Setup

This is the developer setup for running Reliant from Ubuntu on WSL2.

## Why Use A Native WSL Clone

Do not run the Linux dev scripts from a Windows checkout mounted at `/mnt/c/...`.
That frequently causes line-ending and toolchain issues.

Use a Linux-native clone inside your WSL home directory instead, for example:

```bash
cd ~
git clone git@github.com:reliant-labs/reliant.git reliant-wsl
cd reliant-wsl
```

## Prerequisites (Ubuntu WSL2)

Install system dependencies:

```bash
sudo apt-get update
sudo apt-get install -y \
  build-essential \
  sqlite3 \
  libnspr4 \
  libnss3 \
  libatk-bridge2.0-0t64 \
  libgtk-3-0t64 \
  libgbm1 \
  libxss1 \
  libasound2t64
```

Install Go 1.25+ (required by `go.mod`) and verify:

```bash
go version
```

Install Node.js 18+ and verify:

```bash
node -v
npm -v
```

Install required Go dev tools:

```bash
go install github.com/air-verse/air@latest
```

Ensure Go bin is on PATH:

```bash
export PATH="$HOME/go/bin:$PATH"
```

## First-Time Project Setup

From the repo root:

```bash
task deps        # Go modules + the web, electron and protoc-gen-es npm packages
```

Generated code is committed, so there is nothing to generate on a fresh clone.
You also need [Task](https://taskfile.dev) 3.38+ and forge
(`go install github.com/reliant-labs/forge/cmd/forge@latest`) on `PATH`.

## Run The App In WSL

From the repo root:

```bash
export PATH="$HOME/go/bin:$PATH"
forge env up dev
```

This starts, as host processes under this checkout's `.forge/`:

- the api, worker and gateway server roles (air hot reload)
- a `reliant db migrate up` job
- forge host Postgres 16, NATS and the Temporal dev server
- the `frontends/web` Vite dev server

`./.reliant/tools/port-info.sh` prints where each one listens; the logs are in
`.forge/logs/dev/`. Sign in with `grep AUTH_API_KEY secrets/dev.yaml`. Stop it
with `forge env down dev`. The desktop app runs against control-plane's stack
(`electron/README.md`).

## Common Issues

`vite` fails with Node version errors:

- Upgrade Node to `22.12+`.

`air` not found:

- Re-run the `go install ...` commands above.
- Confirm `echo $PATH` includes `$HOME/go/bin`.

Electron fails with missing shared libraries (for example `libnspr4.so`):

- Re-run the `apt-get install` command in this guide.

You started from `/mnt/c/...` and bash scripts fail unexpectedly:

- Move to a Linux-native checkout under `~/...` and run there.