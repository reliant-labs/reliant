# CLI Hybrid Architecture Patterns: Cloud + Local

Research into how developer tools structure their CLI for cloud+local hybrid architectures.

---

## 1. Claude Code CLI

**Architecture**: Pure CLI process → Cloud API. No local daemon.

### Auth
- **OAuth 2.0 with PKCE** (Authorization Code + Proof Key for Code Exchange)
- Two distinct auth paths:
  - **Claude.ai** (consumer subscription — Pro/Max/Teams)
  - **Anthropic Console** (API key billing)
- **Token storage**:
  - **macOS**: OS Keychain (`Keychain.app`, item named `"Claude Code-credentials"`)
  - **Linux/Windows**: `~/.claude/.credentials.json` (plaintext JSON)
- **Auth precedence** (highest to lowest):
  1. Cloud provider env vars (`CLAUDE_CODE_USE_BEDROCK`, `CLAUDE_CODE_USE_VERTEX`, `CLAUDE_CODE_USE_FOUNDRY`)
  2. `ANTHROPIC_AUTH_TOKEN` env var (sent as `Authorization: Bearer` — for LLM gateways)
  3. `ANTHROPIC_API_KEY` env var
  4. `apiKeyHelper` script (external program that returns a key)
  5. OAuth stored credentials
- **OAuth flow**: Opens browser → localhost redirect → receives auth code → exchanges for tokens → stores in keychain
- **Token refresh**: Background refresh cycle for OAuth tokens
- **CI/CD**: `claude setup-token` generates a long-lived `CLAUDE_CODE_OAUTH_TOKEN` for non-interactive environments

### Project Context
- **`CLAUDE.md`** files at project root, nested directories, and `~/.claude/CLAUDE.md` (global)
- Hierarchical: global → project root → subdirectory
- Git-aware: detects repo root, branch, recent commits
- No daemon — each `claude` invocation is a fresh process that reads context at startup

### Tool Execution
- **All local**: File reads, edits, bash commands execute in the local shell process
- **Cloud**: Only the LLM inference call goes to cloud APIs
- Tools are hardcoded primitives: `Read`, `Write`, `Edit`, `Bash`, `Grep`, `Glob`
- MCP servers are spawned as child processes of the CLI

### Key Pattern
> **Stateless CLI + Cloud API + OS Keychain**. No daemon. Each invocation bootstraps from filesystem context. Auth tokens in the OS credential store, with env var overrides for CI/CD and gateways.

---

## 2. OpenClaw

**Architecture**: CLI → Local Gateway Daemon (WebSocket) → Cloud AI APIs. Persistent daemon.

### Auth
- **`auth-profiles.json`** file in workspace — centralized "token sink"
- Multiple auth sources per provider:
  - API keys (env vars or config)
  - OAuth (PKCE) for subscription-based access (Anthropic, OpenAI)
  - `setup-token` flow: run provider CLI login, paste token into OpenClaw
- **Token sink pattern**: OpenClaw consolidates tokens from multiple sources to prevent "logout wars" (when multiple tools share the same OAuth client, refreshing one invalidates the other's refresh token)
- **Bidirectional sync**: When OpenClaw refreshes an Anthropic token, it writes back to Claude Code's storage (`~/.claude/.credentials.json` or macOS Keychain) so both tools stay in sync
- **Gateway auth**: WebSocket connections to the Gateway require a token when binding to non-loopback addresses. Local (127.0.0.1) connections are unauthenticated by default

### Gateway Daemon Architecture
- **Single long-lived Node.js process** (Node 22+, TypeScript ESM)
- Binds HTTP + WebSocket on port **18789** (configurable)
- **Everything is WebSocket RPC** — no REST API. 80+ RPC methods
- All clients (CLI, Control UI web app, iOS/macOS companion apps) speak the same WS protocol
- **Bootstrap sequence**:
  1. `src/entry.ts` → process title, env setup
  2. CLI argument parsing → determines if this is a `gateway` command or a client command
  3. If gateway: start HTTP server, bind WebSocket, load channels, start agent runtime
  4. If client: connect to existing gateway via WebSocket
- **Daemon management**:
  - `openclaw gateway` — start in foreground
  - `openclaw gateway status` — health check via WebSocket
  - `openclaw gateway restart` — restart
  - `openclaw gateway install --force` — install as launchd/systemd service
  - Health: heartbeat events over WS, also in the `hello-ok` handshake
  - Auto-restart via launchd (macOS) / systemd (Linux)

### Project Context
- **Workspace files**: `SOUL.md` (personality), `AGENTS.md` (agent config), `USER.md` (user context), `TOOLS.md` (available tools)
- Injected into system prompt under a "Project Context" block at session start
- **`openclaw.json`** — main config file, hot-reloaded via file watcher
- Per-project config via workspace directories

### Tool Execution (Skills)
- **Skills** are plugins that run inside the Gateway process
- Skills communicate back through the WebSocket control plane
- **Browser control** via headless Playwright inside the Gateway
- **Channel bridges** route messages from Telegram/Discord/Slack/etc through the Gateway
- Skills are installed from **ClawHub** (marketplace) or local directories

### Key Pattern
> **CLI ↔ Local WebSocket Daemon ↔ Cloud APIs**. The Gateway owns everything: agent sessions, channel connections, node registry, cron, browser control. CLI commands are thin WebSocket RPC clients. Config via JSON with hot-reload.

---

## 3. Cursor / Windsurf

**Architecture**: VS Code fork (Electron) → Cloud proxy → AI Models. Extension host for local execution.

### Auth
- **Account-based**: Sign in via browser OAuth, token stored in Electron's credential store
- Cursor uses its own proxy layer — all AI requests route through `api2.cursor.sh`
- API keys never go directly to model providers; Cursor's cloud proxy handles routing

### Local vs Cloud Split
- **Local (Extension Host)**:
  - File system access, git operations
  - Language server protocol (LSP) for code intelligence
  - Terminal/shell execution
  - Code indexing (local vector embeddings for codebase search)
- **Cloud (Cursor Proxy)**:
  - LLM inference (Claude, GPT-4, custom models)
  - The proxy selects which model to use based on task type
  - Cursor uses a multi-model architecture:
    - **Small model** ("cursor-small"): Tab completions, fast inline suggestions
    - **Large model** (Claude/GPT-4): Chat, agent mode, complex reasoning
    - **Speculative edits model**: Applies diffs back to code
  - Background indexing results sent to cloud for context retrieval
- **Windsurf (Codeium)** differs:
  - "Cascade" agent runs server-side — the cloud does more orchestration
  - Local sends file context, cloud returns multi-step plans
  - Cloud holds conversation state, local executes tool calls

### Project Context
- Automatic codebase indexing on project open
- `.cursorrules` / `.cursor/rules` files for project-specific instructions
- `@codebase` context — queries the local vector index
- Windsurf uses `.windsurfrules`

### Key Pattern
> **Fat client (Electron) + Cloud proxy for AI**. All file/tool operations stay local in the extension host. AI inference always routes through the vendor's proxy, which handles model selection, rate limiting, and billing. No local daemon beyond the editor process itself.

---

## 4. GitHub CLI (`gh`)

**Architecture**: Stateless CLI → GitHub REST/GraphQL APIs. No daemon.

### Auth
- **Two OAuth flows**:
  1. **Browser flow** (default): Opens browser → OAuth callback on localhost → token exchange
  2. **Device code flow** (`--web`): Displays a code → user enters at github.com/login/device → CLI polls for completion. Essential for headless/SSH environments
- **Token storage**:
  - **macOS**: Keychain (`keyring`)
  - **Linux**: System keyring if available, else `~/.config/gh/hosts.yml` (plaintext YAML)
  - **Fallback**: `--insecure-storage` flag forces plaintext storage
  - `GH_TOKEN` env var overrides all stored credentials
- **`hosts.yml` structure**:
  ```yaml
  github.com:
    oauth_token: gho_xxxx
    user: username
    git_protocol: https
  ```
- **Multi-account**: `gh auth switch` toggles between accounts for the same host
- **Token scopes**: Requested at login time via `-s` flag (e.g., `repo`, `read:org`, `gist`)
- **Refresh**: `gh auth refresh` re-authenticates to add scopes

### Project Context
- **Git remote detection**: `gh` reads `.git/config` to determine the GitHub repo
- No project config file — context is inferred from git
- `GH_REPO` env var overrides repo detection

### Key Pattern
> **Stateless CLI + OS Keyring + Device Code Flow**. The device code flow is the gold standard for CLI OAuth — works headless, no localhost server needed. Git remotes provide implicit project context. `GH_TOKEN` env var as the universal escape hatch.

---

## 5. Docker CLI

**Architecture**: CLI → REST API over Unix socket → Docker Daemon. Classic client-server.

### The Socket Pattern
- **Default**: Unix socket at `/var/run/docker.sock`
- **Windows**: Named pipe `//./pipe/docker_engine`
- **Remote**: TCP socket (e.g., `tcp://remote-host:2376`)
- **Protocol**: HTTP REST API over the socket — the CLI literally sends HTTP requests through a Unix socket
- **TLS**: Required for TCP connections (`--tlsverify`, certs in `~/.docker/`)
- **API versioning**: Client and daemon negotiate API version. Client sends `API-Version` header; daemon responds with its version. Client auto-downgrades to match daemon.

### Docker Contexts
- **`docker context`** — manages multiple daemon endpoints
  ```bash
  docker context create remote --docker "host=ssh://user@remote-host"
  docker context use remote
  # Now all docker commands go to the remote daemon
  ```
- Context stores: endpoint URL, TLS config, description
- **Stored in**: `~/.docker/contexts/`
- **`DOCKER_HOST`** env var overrides the active context
- **Docker Desktop**: Uses a context-like abstraction internally to route to its VM daemon

### Auth (Registry)
- **`docker login`** stores credentials in `~/.docker/config.json`
- **Credential helpers**: External programs for secure storage
  ```json
  {
    "credStore": "osxkeychain",
    "credHelpers": {
      "gcr.io": "gcloud",
      "aws_account.dkr.ecr.region.amazonaws.com": "ecr-login"
    }
  }
  ```
- **Credential helper protocol**: Docker calls `docker-credential-{name}` binary with `store`, `get`, `erase`, `list` commands via stdin/stdout JSON

### Key Pattern
> **CLI → REST-over-Unix-socket → Daemon**. The socket is the fundamental IPC mechanism. Docker Contexts let one CLI target multiple daemons. Credential helpers are an extensible protocol for external secret storage. API version negotiation handles client/server version skew.

---

## 6. Terraform CLI

**Architecture**: Stateless CLI with pluggable backends and providers. No daemon.

### Auth
- **`terraform login`**: OAuth flow for Terraform Cloud/Enterprise
  - Launches browser → consent page → localhost callback → receives API token
  - Token saved to `~/.terraform.d/credentials.tfrc.json`:
    ```json
    {
      "credentials": {
        "app.terraform.io": {
          "token": "xxxxxx.atlasv1.xxxxxxxxx"
        }
      }
    }
    ```
- **Credentials helpers**: External programs for secure storage
  - Named `terraform-credentials-{name}` (e.g., `terraform-credentials-op` for 1Password)
  - Protocol: CLI calls helper with `get`, `store`, `forget` subcommands
  - Helper receives hostname on stdin, returns JSON with token on stdout
  - Configured in `.terraformrc`:
    ```hcl
    credentials_helper "op" {
      args = ["--account", "my-org"]
    }
    ```
- **Provider auth**: Each provider has its own auth mechanism
  - AWS: env vars (`AWS_ACCESS_KEY_ID`), shared credentials file, IAM roles
  - GCP: `gcloud auth application-default login`, service account JSON
  - Azure: `az login`, service principal, managed identity
  - Pattern: providers delegate to the cloud's own CLI/SDK auth chain

### State Management (the "Backend" pattern)
- **Local state**: `terraform.tfstate` in working directory (default)
- **Remote backends**: S3, GCS, Azure Blob, Consul, Terraform Cloud
- **State locking**: DynamoDB (AWS), native (GCS/Azure), built-in (TF Cloud)
- Backend configured in HCL:
  ```hcl
  terraform {
    backend "s3" {
      bucket = "my-tf-state"
      key    = "prod/terraform.tfstate"
      region = "us-east-1"
    }
  }
  ```
- **`terraform init`** is the critical bootstrap — downloads providers, configures backend, migrates state

### Plugin/Provider Architecture
- Providers are separate binaries downloaded during `terraform init`
- Stored in `.terraform/providers/`
- Communication: **gRPC** between Terraform core and provider plugins
- Provider registry: `registry.terraform.io` — discovery protocol for finding provider binaries
- **Lock file**: `.terraform.lock.hcl` pins provider versions (like a lockfile)

### Key Pattern
> **Stateless CLI + Pluggable Backends + Credential Helpers**. Auth is split: `terraform login` for TF Cloud, provider-specific auth chains for infrastructure. Credential helpers use a simple stdin/stdout protocol for extensible secret management. State backends decouple state storage from the CLI. Providers are gRPC plugin binaries.

---

## 7. VS Code Remote

**Architecture**: Local editor UI ↔ SSH/tunnel ↔ Remote VS Code Server. Split extension execution.

### The Split
- **Local (client)**:
  - Full editor UI, input handling, rendering
  - UI extensions (themes, keybindings, snippets)
  - SSH connection management
- **Remote (server)**:
  - File system access
  - Terminal/shell
  - Workspace extensions (language servers, linters, formatters, debuggers)
  - Git operations
  - Extension host process

### How It Connects
- **SSH**: `Remote-SSH` extension establishes SSH tunnel
  - Auto-installs VS Code Server on first connection (downloads into `~/.vscode-server/`)
  - All extension traffic flows through the SSH tunnel
- **Tunnels**: `code tunnel` CLI command creates a secure tunnel without SSH
  - Uses Microsoft's dev tunnels service as relay
  - Client connects via browser or local VS Code
- **Dev Containers**: Docker container as the "remote" — same architecture, different transport

### Extension Classification
- Extensions declare their execution environment:
  - `"ui"` — runs locally (themes, UI customization)
  - `"workspace"` — runs on remote (language servers, debuggers)
  - Default: workspace (most extensions run remote)
- This split is invisible to the user — extensions just work in their correct context

### Key Pattern
> **Thin client + Remote server auto-provisioned over SSH**. The server is installed automatically on first connection. Extensions are classified as UI (local) or workspace (remote) and run in the correct place. The SSH tunnel is the transport layer — no custom protocol needed.

---

## Synthesis: Architectural Patterns for Reliant

### Pattern 1: Auth Token Storage (Tiered)
Every tool follows the same layered approach:
1. **OS Keychain** as primary (Claude Code, `gh`, Docker credential helpers)
2. **Plaintext JSON/YAML fallback** for environments without keychain (`~/.config/tool/credentials.json`)
3. **Env var override** for CI/CD (`GH_TOKEN`, `ANTHROPIC_API_KEY`, `DOCKER_HOST`)
4. **Credential helper protocol** for extensibility (Terraform, Docker — external program with stdin/stdout JSON)

**Recommendation for Reliant**:
```
Auth precedence:
1. Env vars (RELIANT_API_KEY, RELIANT_AUTH_TOKEN) — CI/CD, scripting
2. OS Keychain (macOS Keychain, Linux libsecret) — interactive use
3. ~/.reliant/credentials.json — fallback / insecure-storage flag
4. credential-helper protocol — enterprise integrations
```

### Pattern 2: OAuth from CLI
Two proven flows:
1. **Browser redirect** (Claude Code, Terraform, `gh` default): Open browser → localhost server catches callback → exchange code for token
2. **Device code flow** (`gh --web`, headless environments): Display code → user enters at website → CLI polls for completion

**Recommendation for Reliant**:
- **Primary**: Browser redirect with localhost callback (better UX for desktop)
- **Fallback**: Device code flow for headless/SSH (detected automatically when no browser available)
- Use **PKCE** always (Claude Code pattern)

### Pattern 3: CLI ↔ Daemon Communication
Three proven patterns:

| Pattern | Example | Transport | When to use |
|---------|---------|-----------|-------------|
| No daemon | Claude Code, `gh`, Terraform | Direct HTTP to cloud | Simple CLI, stateless |
| Unix socket + REST | Docker | HTTP over Unix socket | Local daemon, need process isolation |
| WebSocket RPC | OpenClaw | WS on localhost port | Persistent daemon, bidirectional events, multiple clients |

**Recommendation for Reliant** (given we already have a local server):
- **WebSocket or HTTP on localhost** with dynamic port (already our pattern)
- **Port discovery via `.dev-ports.sh`** (already our pattern)
- Docker's context pattern is worth adopting: `reliant context` to switch between local/remote instances

### Pattern 4: Project Context Discovery
| Tool | Mechanism |
|------|-----------|
| Claude Code | `CLAUDE.md` files (hierarchical) |
| OpenClaw | `SOUL.md`, `AGENTS.md`, `openclaw.json` |
| Cursor | `.cursorrules`, automatic codebase indexing |
| `gh` | Git remote detection |
| Docker | `docker-compose.yml`, `Dockerfile` |
| Terraform | `*.tf` files in working directory |

**Common theme**: Walk up from CWD to find project root marker, then read config files.

**Recommendation for Reliant**:
- Git root detection (already done)
- `RELIANT.md` / `reliant.yaml` for project config
- Hierarchical: `~/.reliant/reliant.md` (global) → project root → subdirectory

### Pattern 5: Daemon Lifecycle Management
| Tool | Discovery | Start | Health | Restart |
|------|-----------|-------|--------|---------|
| Docker | Socket file existence | `dockerd` / Docker Desktop | `docker info` | systemd/launchd |
| OpenClaw | WebSocket connect to port 18789 | `openclaw gateway` | Heartbeat over WS | launchd/systemd |
| VS Code Remote | SSH connection | Auto-install on first connect | Process check | Reconnect logic |

**Recommendation for Reliant**:
- **Discovery**: Check `.dev-ports.sh` → attempt HTTP health check
- **Auto-start**: If daemon not running, start it (like Docker Desktop)
- **Health**: HTTP `/health` endpoint (simple, debuggable with curl)
- **Process supervision**: launchd plist on macOS, systemd unit on Linux

### Pattern 6: Tool Execution Delegation
| Tool | Local execution | Cloud execution |
|------|----------------|-----------------|
| Claude Code | File ops, bash, grep | LLM inference only |
| Cursor | File ops, LSP, terminal, indexing | LLM inference via proxy |
| OpenClaw | Skills, browser (Playwright), channels | LLM inference |
| VS Code Remote | UI rendering | Everything else (file ops, terminal, extensions) |

**Key insight**: Every tool keeps **file system operations local** and sends only **LLM inference** to the cloud. The exception is VS Code Remote, which flips this — but it's solving a different problem (remote filesystem access).

**Recommendation for Reliant**:
- CLI tool execution is always local (bash, file ops, git)
- Only LLM inference goes to cloud
- MCP servers run as local child processes (Claude Code pattern)

### Pattern 7: Credential Helper Protocol
Both Docker and Terraform use a **simple external program protocol**:

```
# Docker: docker-credential-{name}
echo '{"ServerURL":"registry.example.com"}' | docker-credential-osxkeychain get
# Returns: {"Username":"user","Secret":"token"}

# Terraform: terraform-credentials-{name}
echo '{"hostname":"app.terraform.io"}' | terraform-credentials-op get
# Returns: {"token":"xxxxx"}
```

**Protocol**: Named binary on PATH, receives JSON on stdin, returns JSON on stdout.
Subcommands: `get`, `store`, `erase` (Docker) or `get`, `store`, `forget` (Terraform).

**Recommendation for Reliant**:
- `reliant-credentials-{name}` helper protocol
- Default helpers: `keychain` (OS), `plaintext` (fallback)
- Enterprise can plug in Vault, 1Password, etc.

---

## Summary Table

| Concern | Claude Code | OpenClaw | Cursor | gh | Docker | Terraform | VS Code Remote |
|---------|------------|----------|--------|-----|--------|-----------|---------------|
| **Daemon** | None | Gateway (WS :18789) | Electron process | None | dockerd (socket) | None | VS Code Server (SSH) |
| **Auth storage** | OS Keychain | auth-profiles.json | Electron cred store | OS Keyring / hosts.yml | config.json + helpers | credentials.tfrc.json + helpers | SSH keys |
| **OAuth flow** | PKCE + browser redirect | PKCE + setup-token | Browser OAuth | Device code + browser | N/A (registry login) | Browser redirect | N/A |
| **Project context** | CLAUDE.md | SOUL.md + openclaw.json | .cursorrules + indexing | Git remotes | docker-compose.yml | *.tf files | Remote filesystem |
| **CLI↔Daemon IPC** | N/A | WebSocket RPC | Electron IPC | N/A | REST over Unix socket | N/A | SSH tunnel |
| **Cloud calls** | LLM inference | LLM inference | LLM via proxy | GitHub API | Registry pulls | Provider APIs | Dev tunnels relay |
| **Env var override** | ANTHROPIC_API_KEY | Per-provider env vars | N/A | GH_TOKEN | DOCKER_HOST | TF_TOKEN_* | N/A |
