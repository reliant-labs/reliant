#!/bin/bash
set -eo pipefail

# Ensure TERM is set (may be missing when run from GUI apps)
export TERM=${TERM:-xterm-256color}

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEB_DIR="$PROJECT_ROOT/web"
ELECTRON_DIR="$PROJECT_ROOT/electron"
DEV_DIR="$PROJECT_ROOT/data"
LOG_FILE="$DEV_DIR/logs.txt"

# Ensure ./data directory exists
mkdir -p "$DEV_DIR"

# Clear/create log file
> "$LOG_FILE"

echo -e "${BLUE}Starting Reliant Development Environment (V2)${NC}"
echo -e "${BLUE}Logs are being written to: ${YELLOW}$LOG_FILE${NC}"

# Function to print step
print_step() {
    echo -e "\n${YELLOW}🚀 $1${NC}"
    echo -e "\n[$(date '+%Y-%m-%d %H:%M:%S')] 🚀 $1" >> "$LOG_FILE"
}

# Function to check if command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

load_env_file() {
    local env_file="$1"
    if [ ! -f "$env_file" ]; then
        return
    fi

    echo -e "${BLUE}Loading environment from ${YELLOW}$(basename "$env_file")${NC}"

    # Export variables defined in the env file for child processes.
    # Supports lines like `KEY=value` and `export KEY=value`.
    set -a
    # shellcheck disable=SC1090
    source "$env_file"
    set +a
}

# Load root env files (.env first, then .env.local overrides)
load_env_file "$PROJECT_ROOT/.env"
load_env_file "$PROJECT_ROOT/.env.local"

hash_short() {
    local input="$1"
    if command_exists shasum; then
        printf "%s" "$input" | shasum | awk '{print substr($1,1,8)}'
        return
    fi
    if command_exists sha1sum; then
        printf "%s" "$input" | sha1sum | awk '{print substr($1,1,8)}'
        return
    fi
    if command_exists openssl; then
        printf "%s" "$input" | openssl sha1 | awk '{print substr($NF,1,8)}'
        return
    fi

    # Last resort (deterministic enough fallback)
    printf "%s" "$input" | tr -cd 'a-zA-Z0-9' | head -c 8
}

derive_worktree_postgres_db_name() {
    local project_path="$1"
    local repo_token=""
    local worktree_token=""

    # Typical Reliant worktree path: ~/.reliant/worktrees/<repo_id>/<worktree_name>
    if [[ "$project_path" =~ \.reliant/worktrees/([^/]+)/([^/]+) ]]; then
        repo_token="${BASH_REMATCH[1]}"
        worktree_token="${BASH_REMATCH[2]}"
    else
        repo_token="$(basename "$(git rev-parse --show-toplevel 2>/dev/null || echo "$project_path")")"
        worktree_token="$(basename "$project_path")"
    fi

    local raw="reliant_${repo_token}_${worktree_token}"
    local sanitized
    sanitized="$(echo "$raw" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9]+/_/g; s/_+/_/g; s/^_+//; s/_+$//')"

    if [ -z "$sanitized" ]; then
        sanitized="reliant_dev"
    fi

    # Postgres identifier max length is 63 bytes.
    if [ ${#sanitized} -gt 63 ]; then
        local suffix
        suffix="$(hash_short "$sanitized")"
        local prefix_len=$((63 - 1 - ${#suffix}))
        sanitized="${sanitized:0:$prefix_len}_$suffix"
    fi

    echo "$sanitized"
}

bootstrap_postgres_for_worktree() {
    local project_root="$1"
    local log_file="$2"
    local postgres_compose_project="${RELIANT_POSTGRES_COMPOSE_PROJECT:-reliant}"

    # Use RELIANT_POSTGRES_* vars for explicit overrides.
    # Do NOT read PG* here to avoid accidental cross-worktree leakage from parent shells.
    local pg_host="${RELIANT_POSTGRES_HOST:-localhost}"
    local pg_port="${RELIANT_POSTGRES_PORT:-5433}"
    local pg_user="${RELIANT_POSTGRES_USER:-postgres}"
    local pg_password="${RELIANT_POSTGRES_PASSWORD:-postgres}"
    local pg_sslmode="${RELIANT_POSTGRES_SSLMODE:-disable}"
    RELIANT_POSTGRES_PORT="$pg_port"
    export RELIANT_POSTGRES_PORT
    local db_name
    db_name="$(derive_worktree_postgres_db_name "$project_root")"

    echo -e "${BLUE}Postgres dev mode enabled${NC}"
    echo -e "  ${BLUE}Worktree database: ${YELLOW}$db_name${NC}"
    echo -e "  ${BLUE}Server: ${YELLOW}$pg_host:$pg_port${NC}"

    # We currently only automate local Docker-backed Postgres bootstrap.
    if [ "$pg_host" != "localhost" ] && [ "$pg_host" != "127.0.0.1" ]; then
        echo -e "${RED}❌ Automatic per-worktree Postgres bootstrap only supports localhost/127.0.0.1 currently${NC}"
        echo -e "${RED}   Received host: $pg_host${NC}"
        echo -e "${RED}   Set DATABASE_URL manually to a pre-created database, or use local docker-compose Postgres.${NC}"
        exit 1
    fi

    if ! command_exists docker; then
        echo -e "${RED}❌ Docker is required for DATABASE_DRIVER=postgres local dev bootstrap${NC}"
        exit 1
    fi
    if ! docker ps >/dev/null 2>&1; then
        echo -e "${RED}❌ Docker daemon is not running${NC}"
        echo -e "${RED}   Start Docker Desktop and try again${NC}"
        exit 1
    fi

    echo -e "${BLUE}Ensuring local Postgres container is running...${NC}"
    if ! docker compose -p "$postgres_compose_project" up -d postgres >> "$log_file" 2>&1; then
        echo -e "${RED}❌ Failed to start local Postgres container${NC}"
        echo -e "${RED}   Check $log_file for details${NC}"
        exit 1
    fi

    local ready=0
    for _ in {1..20}; do
        if docker compose -p "$postgres_compose_project" exec -T postgres pg_isready -U "$pg_user" -d postgres >/dev/null 2>&1; then
            ready=1
            break
        fi
        sleep 1
    done
    if [ "$ready" -ne 1 ]; then
        echo -e "${RED}❌ Postgres container is not ready${NC}"
        echo -e "${RED}   Service: postgres${NC}"
        exit 1
    fi

    local db_exists
    db_exists="$(docker compose -p "$postgres_compose_project" exec -T -e PGPASSWORD="$pg_password" postgres \
        psql -U "$pg_user" -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='${db_name}'" 2>/dev/null | tr -d '[:space:]')"

    if [ "$db_exists" != "1" ]; then
        echo -e "${BLUE}Creating worktree Postgres database ${YELLOW}$db_name${NC}"
        if ! docker compose -p "$postgres_compose_project" exec -T -e PGPASSWORD="$pg_password" postgres \
            psql -U "$pg_user" -d postgres -c "CREATE DATABASE \"$db_name\";" >> "$log_file" 2>&1; then
            echo -e "${RED}❌ Failed to create worktree database: $db_name${NC}"
            echo -e "${RED}   Check $log_file for details${NC}"
            exit 1
        fi
    else
        echo -e "${GREEN}✅ Worktree Postgres database already exists: $db_name${NC}"
    fi

    # Build runtime DATABASE_URL for this worktree
    local encoded_password
    encoded_password="$(node -e 'console.log(encodeURIComponent(process.argv[1] || ""))' "$pg_password")"
    if [ -n "$encoded_password" ]; then
        DATABASE_URL="postgres://${pg_user}:${encoded_password}@${pg_host}:${pg_port}/${db_name}?sslmode=${pg_sslmode}"
    else
        DATABASE_URL="postgres://${pg_user}@${pg_host}:${pg_port}/${db_name}?sslmode=${pg_sslmode}"
    fi

    export DATABASE_DRIVER=postgres
    export DATABASE_URL
    export PGDATABASE="$db_name"
    # Force PG* to match selected local dev database to avoid inherited shell overrides
    # influencing pgx/libpq resolution in child processes.
    export PGHOST="$pg_host"
    export PGPORT="$pg_port"
    export PGUSER="$pg_user"
    export PGSSLMODE="$pg_sslmode"

    echo -e "${GREEN}✅ Postgres per-worktree database ready${NC}"
    echo -e "  ${BLUE}DATABASE_DRIVER=${YELLOW}$DATABASE_DRIVER${NC}"
    echo -e "  ${BLUE}PGDATABASE=${YELLOW}$PGDATABASE${NC}"
    echo -e "  ${BLUE}DATABASE_URL=${YELLOW}$DATABASE_URL${NC}"
}

# Setup Proxyman (for debugging HTTP traffic) - disabled by default
# Enable with: RELIANT_PROXYMAN=1 npm run dev
if [ "${RELIANT_PROXYMAN:-}" = "1" ]; then
    if [ -t 0 ] && [ -t 1 ]; then
        print_step "Setting up Proxyman..."
        PROXYMAN_SCRIPT="$HOME/Library/Application Support/com.proxyman.NSProxy/app-data/proxyman_env_automatic_setup.sh"
        if [ -f "$PROXYMAN_SCRIPT" ]; then
            # Extract only the export lines from Proxyman script (skip clear, echo, etc.)
            while IFS= read -r line; do
                if [[ "$line" =~ ^export\ .+=.+ ]]; then
                    eval "$line" 2>/dev/null || true
                fi
            done < "$PROXYMAN_SCRIPT"
            # Enable TLS skip for Go HTTP clients when using Proxyman (for debugging API calls)
            export RELIANT_SKIP_TLS_VERIFY=1
            echo -e "${GREEN}✅ Proxyman environment loaded${NC}"
            echo -e "${YELLOW}⚠️  TLS verification disabled for debugging (RELIANT_SKIP_TLS_VERIFY=1)${NC}"
        else
            echo -e "${YELLOW}⚠️  Proxyman script not found, skipping...${NC}"
        fi
    else
        echo -e "${BLUE}ℹ️  Proxyman skipped (non-interactive environment)${NC}"
    fi
else
    echo -e "${BLUE}ℹ️  Proxyman disabled (enable with RELIANT_PROXYMAN=1)${NC}"
fi

# Check prerequisites
print_step "Checking prerequisites..."

if ! command_exists go; then
    echo -e "${RED}❌ Go is not installed${NC}"
    exit 1
fi

if ! command_exists npm; then
    echo -e "${RED}❌ npm is not installed${NC}"
    exit 1
fi

if ! command_exists node; then
    echo -e "${RED}❌ Node.js is not installed${NC}"
    exit 1
fi

if ! command_exists air; then
    echo -e "${RED}❌ Air is not installed${NC}"
    echo -e "${RED}   Install with: go install github.com/air-verse/air@latest${NC}"
    exit 1
fi

echo -e "${GREEN}✅ Prerequisites check passed${NC}"

# Install dependencies if needed
print_step "Installing dependencies..."

cd "$WEB_DIR"
echo "Installing web dependencies..."
if ! npm install --legacy-peer-deps >> "$LOG_FILE" 2>&1; then
    echo -e "${RED}❌ Failed to install web dependencies. Check $LOG_FILE for details${NC}"
    exit 1
fi

cd "$ELECTRON_DIR"
echo "Installing Electron dependencies..."
if ! npm install >> "$LOG_FILE" 2>&1; then
    echo -e "${RED}❌ Failed to install Electron dependencies. Check $LOG_FILE for details${NC}"
    exit 1
fi

echo -e "${GREEN}✅ Dependencies ready${NC}"

# Verify protoc-gen-es was installed (required for buf generate)
if [ ! -x "$WEB_DIR/node_modules/.bin/protoc-gen-es" ]; then
    echo -e "${RED}❌ protoc-gen-es not found in web/node_modules/.bin${NC}"
    echo -e "${RED}   Try: cd web && npm install @bufbuild/protoc-gen-es${NC}"
    exit 1
fi

# Generate protobuf code
print_step "Generating protobuf code..."
cd "$PROJECT_ROOT"
if command_exists buf; then
    # Temporarily unset proxy variables for buf generate (npx doesn't play nice with Proxyman)
    # Add web/node_modules/.bin to PATH for protoc-gen-es
    BUF_PATH="$PROJECT_ROOT/web/node_modules/.bin:$(command -v buf | xargs dirname):$PATH"
    # Run buf in a clean environment to avoid Proxyman proxy/TLS vars interfering
    env -i HOME="$HOME" USER="${USER:-$(whoami)}" TMPDIR="${TMPDIR:-/tmp}" \
        PATH="$BUF_PATH" \
        buf generate 2>&1 | tee -a "$LOG_FILE"
    if [ ${PIPESTATUS[0]} -ne 0 ]; then
        echo -e "${RED}❌ Failed to generate protobuf code${NC}"
        exit 1
    fi
    echo -e "${GREEN}✅ Protobuf code generated${NC}"
else
    echo -e "${YELLOW}⚠️  buf not installed, skipping proto generation...${NC}"
    echo "buf not found, skipping proto generation" >> "$LOG_FILE"
fi

# Generate docs, shortcuts, and presets from sources of truth
# Source: config/shortcuts.yaml -> web/src/store/shortcutsData.generated.ts
# Source: internal/llm/tools/*.go -> generated/docs/tools-reference.md
# Source: generated reference docs -> docs/reference/*.mdx and workflow-builder SKILL.md
print_step "Generating docs and shortcuts..."
cd "$PROJECT_ROOT"
if make generate >> "$LOG_FILE" 2>&1; then
    echo -e "${GREEN}✅ Generated files up to date${NC}"
else
    echo -e "${YELLOW}⚠️  Failed to generate docs (non-fatal). Check $LOG_FILE for details${NC}"
fi

# Find available ports
print_step "Finding available ports..."
cd "$PROJECT_ROOT"

PORT_OUTPUT=$(node scripts/find-ports.js 2>&1)
if [ $? -ne 0 ]; then
    echo -e "${RED}❌ Failed to find available ports${NC}"
    echo "$PORT_OUTPUT" >&2
    exit 1
fi

# Parse all port assignments from the Node.js script output
eval "$(echo "$PORT_OUTPUT" | grep '^FRONTEND_PORT=' | tail -1)"
eval "$(echo "$PORT_OUTPUT" | grep '^BACKEND_PORT=' | tail -1)"
eval "$(echo "$PORT_OUTPUT" | grep '^GRPC_PORT=' | tail -1)"
eval "$(echo "$PORT_OUTPUT" | grep '^TOOLS_DAEMON_PORT=' | tail -1)"
eval "$(echo "$PORT_OUTPUT" | grep '^DAEMON_FRONTEND_PORT=' | tail -1)"
eval "$(echo "$PORT_OUTPUT" | grep '^TEMPORAL_FRONTEND_PORT=' | tail -1)"
eval "$(echo "$PORT_OUTPUT" | grep '^TEMPORAL_UI_PORT=' | tail -1)"
eval "$(echo "$PORT_OUTPUT" | grep '^PPROF_PORT=' | tail -1)"

# Verify all ports were found
if [ -z "$FRONTEND_PORT" ] || [ -z "$BACKEND_PORT" ] || [ -z "$GRPC_PORT" ] || [ -z "$TOOLS_DAEMON_PORT" ] || [ -z "$DAEMON_FRONTEND_PORT" ] || [ -z "$TEMPORAL_FRONTEND_PORT" ] || [ -z "$TEMPORAL_UI_PORT" ] || [ -z "$PPROF_PORT" ]; then
    echo -e "${RED}❌ Failed to parse all required ports${NC}"
    echo "FRONTEND_PORT=$FRONTEND_PORT, BACKEND_PORT=$BACKEND_PORT, GRPC_PORT=$GRPC_PORT, TOOLS_DAEMON_PORT=$TOOLS_DAEMON_PORT, TEMPORAL_FRONTEND_PORT=$TEMPORAL_FRONTEND_PORT, TEMPORAL_UI_PORT=$TEMPORAL_UI_PORT" >&2
    exit 1
fi

# If postgres mode is enabled, bootstrap an isolated DB for this worktree.
if [ "${DATABASE_DRIVER:-sqlite}" = "postgres" ]; then
    print_step "Bootstrapping per-worktree Postgres database..."
    bootstrap_postgres_for_worktree "$PROJECT_ROOT" "$LOG_FILE"
fi

echo -e "${GREEN}✅ Found available ports${NC}"
echo -e "  Frontend: ${BLUE}$FRONTEND_PORT${NC}"
echo -e "  Backend: ${BLUE}$BACKEND_PORT${NC}"
echo -e "  gRPC: ${BLUE}$GRPC_PORT${NC}"
echo -e "  Tools daemon gRPC: ${BLUE}$TOOLS_DAEMON_PORT${NC}"
echo -e "  Daemon frontend: ${BLUE}$DAEMON_FRONTEND_PORT${NC}"
echo -e "  Temporal:"
echo -e "    Frontend (client API):  ${BLUE}$TEMPORAL_FRONTEND_PORT${NC}"
echo -e "    History:                ${BLUE}$((TEMPORAL_FRONTEND_PORT + 1))${NC}"
echo -e "    Matching:               ${BLUE}$((TEMPORAL_FRONTEND_PORT + 2))${NC}"
echo -e "    Worker:                 ${BLUE}$((TEMPORAL_FRONTEND_PORT + 3))${NC}"
echo -e "  Temporal UI: ${BLUE}$TEMPORAL_UI_PORT${NC}"
echo -e "  pprof: ${BLUE}$PPROF_PORT${NC}"

# Export ports for all child processes
export NODE_ENV=development
export RELIANT_ENV=dev  # Canonical env detection for Go backend (used by config.IsDevelopmentEnvironment())
export FRONTEND_PORT="$FRONTEND_PORT"
export BACKEND_PORT="$BACKEND_PORT"
export API_PORT="$BACKEND_PORT"  # V2 backend uses API_PORT
export GRPC_PORT="$GRPC_PORT"  # gRPC/Connect server port
export TOOLS_DAEMON_PORT="$TOOLS_DAEMON_PORT"  # Dedicated daemon gRPC listener
export DAEMON_FRONTEND_PORT="$DAEMON_FRONTEND_PORT"
export VITE_GRPC_PORT="$GRPC_PORT"  # Expose to Vite/frontend
export TEMPORAL_FRONTEND_PORT="$TEMPORAL_FRONTEND_PORT"
export TEMPORAL_UI_PORT="$TEMPORAL_UI_PORT"
export PPROF_PORT="$PPROF_PORT"
export USE_DEV_SERVER=true
export RELIANT_EXTERNAL_BACKEND=true  # Tell Electron to use external backend (Air)

# Ensure ports are available to npm scripts
# Write .env.ports for processes that source it
echo "export FRONTEND_PORT=$FRONTEND_PORT" > .env.ports
echo "export BACKEND_PORT=$BACKEND_PORT" >> .env.ports
echo "export GRPC_PORT=$GRPC_PORT" >> .env.ports
echo "export TOOLS_DAEMON_PORT=$TOOLS_DAEMON_PORT" >> .env.ports
echo "export DAEMON_FRONTEND_PORT=$DAEMON_FRONTEND_PORT" >> .env.ports
echo "export TEMPORAL_FRONTEND_PORT=$TEMPORAL_FRONTEND_PORT" >> .env.ports
echo "export TEMPORAL_UI_PORT=$TEMPORAL_UI_PORT" >> .env.ports
echo "export PPROF_PORT=$PPROF_PORT" >> .env.ports
if [ "${DATABASE_DRIVER:-sqlite}" = "postgres" ]; then
    echo "export DATABASE_DRIVER=postgres" >> .env.ports
    echo "export DATABASE_URL=$DATABASE_URL" >> .env.ports
    echo "export PGDATABASE=$PGDATABASE" >> .env.ports
    echo "export PGHOST=$PGHOST" >> .env.ports
    echo "export PGPORT=$PGPORT" >> .env.ports
    echo "export PGUSER=$PGUSER" >> .env.ports
    echo "export PGSSLMODE=$PGSSLMODE" >> .env.ports
    echo "export RELIANT_POSTGRES_PORT=$RELIANT_POSTGRES_PORT" >> .env.ports
fi

# Start Temporal UI
print_step "Starting Temporal UI..."
TEMPORAL_UI_STARTED=false
# Use host.docker.internal to access host's localhost from inside Docker
TEMPORAL_ADDRESS="host.docker.internal:$TEMPORAL_FRONTEND_PORT"

# Use backend port as unique identifier for container name (allows multiple instances)
TEMPORAL_CONTAINER_NAME="temporal-ui-dev-$BACKEND_PORT"

# Check if Docker is installed
if ! command_exists docker; then
    echo -e "${RED}❌ Docker is required for V2 backend (Temporal UI)${NC}"
    echo -e "${RED}   Install Docker Desktop: https://www.docker.com/products/docker-desktop${NC}"
    exit 1
fi

# Check if Docker daemon is running
if ! docker ps >/dev/null 2>&1; then
    echo -e "${RED}❌ Docker daemon is not running${NC}"
    echo -e "${RED}   Start Docker Desktop and try again${NC}"
    exit 1
fi

# Stop any existing container with this name (in case of restart)
if docker ps -a --filter "name=$TEMPORAL_CONTAINER_NAME" --format "{{.Names}}" | grep -q "$TEMPORAL_CONTAINER_NAME"; then
    echo -e "${BLUE}Stopping existing Temporal UI container $TEMPORAL_CONTAINER_NAME...${NC}"
    docker stop "$TEMPORAL_CONTAINER_NAME" >/dev/null 2>&1 || true
    docker rm "$TEMPORAL_CONTAINER_NAME" >/dev/null 2>&1 || true
fi

echo -e "${GREEN}Starting Temporal UI with Docker on port $TEMPORAL_UI_PORT...${NC}"

# Start Temporal UI in background
# Use --add-host to ensure host.docker.internal works on all platforms
TEMPORAL_UI_STARTED=false
temporal_ui_run_output="$(docker run -d \
  --name "$TEMPORAL_CONTAINER_NAME" \
  --rm \
  --add-host=host.docker.internal:host-gateway \
  -p $TEMPORAL_UI_PORT:8080 \
  -e TEMPORAL_ADDRESS=$TEMPORAL_ADDRESS \
  -e TEMPORAL_CORS_ORIGINS=http://localhost:3000 \
  temporalio/ui:latest 2>&1)"
temporal_ui_run_status=$?
echo "$temporal_ui_run_output" >> "$LOG_FILE"

if [ $temporal_ui_run_status -ne 0 ]; then
  echo -e "${YELLOW}⚠️  Failed to start Temporal UI container (continuing without UI)${NC}"
  if echo "$temporal_ui_run_output" | grep -q "port is already allocated"; then
    echo -e "${YELLOW}   Port $TEMPORAL_UI_PORT is already in use by another container/process.${NC}"
    echo -e "${YELLOW}   Backend will still run. If needed, rerun to pick a new dynamic port.${NC}"
  else
    echo -e "${YELLOW}   See $LOG_FILE for Docker error details.${NC}"
  fi
else
  TEMPORAL_UI_STARTED=true
  echo -e "${GREEN}✅ Temporal UI started on http://localhost:$TEMPORAL_UI_PORT${NC}"
  echo -e "${BLUE}   Container: $TEMPORAL_CONTAINER_NAME${NC}"
fi

# Cleanup function
cleanup() {
  # Prevent cleanup from being interrupted by subsequent signals
  trap '' INT TERM

  echo -e "\n${BLUE}Received shutdown signal, cleaning up...${NC}"

  # Stop Air (and its child reliant process)
  if [ -n "$AIR_PID" ]; then
    echo -e "${BLUE}Stopping Air (PID: $AIR_PID)...${NC}"
    kill -TERM $AIR_PID 2>/dev/null || true
    # Wait for graceful shutdown (Air kill_delay is 5s + buffer)
    echo -e "${BLUE}Waiting for graceful shutdown (max 7s)...${NC}"
    for i in {1..7}; do
      if ! kill -0 $AIR_PID 2>/dev/null; then
        echo -e "${GREEN}✅ Air exited gracefully after ${i}s${NC}"
        break
      fi
      sleep 1
    done
    # Force kill if still running after timeout
    if kill -0 $AIR_PID 2>/dev/null; then
      echo -e "${YELLOW}⚠️  Air did not exit gracefully, force killing...${NC}"
      kill -KILL $AIR_PID 2>/dev/null || true
    fi
    wait $AIR_PID 2>/dev/null || true
  fi

  # Note: We intentionally do NOT use pgrep to find "orphaned" reliant processes
  # because that would kill processes from other dev sessions/worktrees.



  # Stop Temporal UI Docker container (only if we started it)
  if command_exists docker && [ "$TEMPORAL_UI_STARTED" = "true" ] && [ -n "$TEMPORAL_CONTAINER_NAME" ]; then
    echo -e "${BLUE}Stopping Temporal UI container $TEMPORAL_CONTAINER_NAME...${NC}"
    # Use shorter timeout (5s) since Temporal UI doesn't need graceful shutdown
    if ! docker stop -t 5 "$TEMPORAL_CONTAINER_NAME" 2>&1; then
      echo -e "${YELLOW}Warning: docker stop failed, forcing removal...${NC}"
      docker rm -f "$TEMPORAL_CONTAINER_NAME" 2>/dev/null || true
    fi
  fi

  # Stop Electron/Vite (if npm is running)
  if [ -n "$ELECTRON_PID" ]; then
    echo -e "${BLUE}Stopping Electron/Vite (PID: $ELECTRON_PID)...${NC}"
    kill $ELECTRON_PID 2>/dev/null || true
  fi

  echo -e "${GREEN}✅ Cleanup complete${NC}"
}
trap cleanup EXIT INT TERM

print_step "Starting backend with Air for hot reload..."
cd "$PROJECT_ROOT"

# If a previous dev run left an Air process alive for this same worktree,
# it can keep restarting backend with stale DATABASE_URL/PG* env values.
# Kill only Air processes whose CWD matches this PROJECT_ROOT (safe in multi-worktree setups).
existing_air_pids=()
while IFS= read -r pid; do
  [ -z "$pid" ] && continue
  if lsof -a -p "$pid" -d cwd -Fn 2>/dev/null | grep -Fq "n$PROJECT_ROOT"; then
    existing_air_pids+=("$pid")
  fi
done < <(ps -Ao pid=,command= | awk 'index($0, "air -c .air.toml") { print $1 }')

if [ ${#existing_air_pids[@]} -gt 0 ]; then
  echo -e "${YELLOW}⚠️  Found stale Air process(es) for this worktree: ${existing_air_pids[*]}${NC}"
  echo -e "${YELLOW}   Stopping stale Air process(es) to avoid stale DB env drift...${NC}"
  for stale_pid in "${existing_air_pids[@]}"; do
    kill -TERM "$stale_pid" 2>/dev/null || true
  done

  # Wait for graceful stop, then force kill if needed.
  sleep 1
  for stale_pid in "${existing_air_pids[@]}"; do
    if kill -0 "$stale_pid" 2>/dev/null; then
      kill -KILL "$stale_pid" 2>/dev/null || true
    fi
  done
fi

# Use the standard Air config
AIR_CONFIG=".air.toml"

echo -e "[dev.sh] Air startup env: DATABASE_DRIVER=${DATABASE_DRIVER:-unset} PGHOST=${PGHOST:-unset} PGPORT=${PGPORT:-unset} PGDATABASE=${PGDATABASE:-unset}" >> "$LOG_FILE"
echo -e "[dev.sh] Air startup env: DATABASE_URL=${DATABASE_URL:-unset}" >> "$LOG_FILE"

# Start Air in the background and capture its PID
# Air will watch for Go file changes and rebuild automatically
# With stop_on_error=true, it won't run old binary on build failure
air -c "$AIR_CONFIG" >> "$LOG_FILE" 2>&1 &
AIR_PID=$!

echo -e "${GREEN}✅ Backend started with Air (PID: $AIR_PID)${NC}"
echo -e "${BLUE}   Air will auto-reload on Go file changes${NC}"
echo -e "${BLUE}   Build errors will be logged to: ${YELLOW}./data/build-errors.log${NC}"

# Give backend a moment to start
sleep 2

print_step "Starting Vite + Electron..."
cd "$ELECTRON_DIR"

echo -e "${GREEN}This will start:${NC}"
echo -e "  ${BLUE}1. Go V2 backend server on port $BACKEND_PORT (with Air hot reload)${NC}"
echo -e "  ${BLUE}2. Embedded Temporal server:${NC}"
echo -e "       Frontend: $TEMPORAL_FRONTEND_PORT, History: $((TEMPORAL_FRONTEND_PORT + 1)), Matching: $((TEMPORAL_FRONTEND_PORT + 2)), Worker: $((TEMPORAL_FRONTEND_PORT + 3))"
echo -e "  ${BLUE}3. Temporal UI (web UI on port $TEMPORAL_UI_PORT)${NC}"
echo -e "  ${BLUE}4. Vite dev server on port $FRONTEND_PORT${NC}"
echo -e "  ${BLUE}5. Electron app${NC}"

# Start Electron/Vite
npm run dev >> "$LOG_FILE" 2>&1 &
ELECTRON_PID=$!

echo -e "${GREEN}✅ Electron/Vite started (PID: $ELECTRON_PID)${NC}"
echo -e "\n${GREEN}Development environment is running!${NC}"
echo -e "${BLUE}📝 All logs: ${YELLOW}$LOG_FILE${NC}"
echo -e "${BLUE}🔧 Build errors: ${YELLOW}./data/build-errors.log${NC}"
echo -e "${YELLOW}Press Ctrl-C to stop all services${NC}\n"

# Wait for Electron to finish (user closes it)
wait $ELECTRON_PID
RETVAL=$?

# Cleanup will be called by trap
exit $RETVAL