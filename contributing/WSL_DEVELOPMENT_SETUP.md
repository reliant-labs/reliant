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
go install github.com/pressly/goose/v3/cmd/goose@latest
go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
```

Ensure Go bin is on PATH:

```bash
export PATH="$HOME/go/bin:$PATH"
```

## First-Time Project Setup

From the repo root:

```bash
npm install
make generate
npm run verify:dev
```

## Run The App In WSL

From the repo root:

```bash
export PATH="$HOME/go/bin:$PATH"
npm run dev
```

This starts:

- Backend (Air hot reload)
- Embedded Temporal services
- Temporal UI container
- Vite dev server
- Electron app

Stop with `Ctrl+C`.

## Common Issues

`vite` fails with Node version errors:

- Upgrade Node to `22.12+`.

`air`, `goose`, or `sqlc` not found:

- Re-run the `go install ...` commands above.
- Confirm `echo $PATH` includes `$HOME/go/bin`.

Electron fails with missing shared libraries (for example `libnspr4.so`):

- Re-run the `apt-get install` command in this guide.

You started from `/mnt/c/...` and bash scripts fail unexpectedly:

- Move to a Linux-native checkout under `~/...` and run there.