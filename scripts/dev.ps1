# PowerShell script for Windows development environment
# Equivalent to dev.sh for Unix systems
$ErrorActionPreference = "Stop"

# Colors for output
function Write-ColorOutput {
    param([string]$Message, [string]$Color = "White")
    Write-Host $Message -ForegroundColor $Color
}

$PROJECT_ROOT = Split-Path -Parent $PSScriptRoot
$WEB_DIR = Join-Path $PROJECT_ROOT "web"
$ELECTRON_DIR = Join-Path $PROJECT_ROOT "electron"
$DEV_DIR = Join-Path $PROJECT_ROOT "data"
$LOG_FILE = Join-Path $DEV_DIR "logs.txt"

# Ensure ./data directory exists
if (-not (Test-Path $DEV_DIR)) {
    New-Item -ItemType Directory -Path $DEV_DIR | Out-Null
}

# Clear/create log file. If locked by another process, fall back to a unique file.
try {
    Set-Content -Path $LOG_FILE -Value "" -Encoding UTF8
} catch {
    $fallbackName = "logs-$((Get-Date).ToString('yyyyMMdd-HHmmss')).txt"
    $LOG_FILE = Join-Path $DEV_DIR $fallbackName
    Set-Content -Path $LOG_FILE -Value "" -Encoding UTF8
}

Write-ColorOutput "Starting Reliant Development Environment (V2)" "Blue"
Write-ColorOutput "Logs are being written to: $LOG_FILE" "Blue"

function Import-EnvFile {
    param([string]$FilePath)

    if (-not (Test-Path $FilePath)) {
        return
    }

    Write-ColorOutput "Loading environment from $(Split-Path -Leaf $FilePath)" "Blue"

    Get-Content -Path $FilePath | ForEach-Object {
        $line = $_.Trim()
        if ([string]::IsNullOrWhiteSpace($line) -or $line.StartsWith("#")) {
            return
        }

        if ($line -match "^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=(.*)$") {
            $key = $Matches[1]
            $value = $Matches[2].Trim()

            if (($value.StartsWith('"') -and $value.EndsWith('"')) -or ($value.StartsWith("'") -and $value.EndsWith("'"))) {
                $value = $value.Substring(1, $value.Length - 2)
            }

            [Environment]::SetEnvironmentVariable($key, $value)
            Set-Item -Path "Env:$key" -Value $value
        }
    }
}

# Load root env files (.env first, then .env.local overrides)
Import-EnvFile (Join-Path $PROJECT_ROOT ".env")
Import-EnvFile (Join-Path $PROJECT_ROOT ".env.local")

function Write-Step {
    param([string]$Message)
    Write-ColorOutput "`n$Message" "Yellow"
    Add-Content -Path $LOG_FILE -Value "`n[$(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')] $Message" -Encoding UTF8
}

function Test-Command {
    param([string]$Command)
    $null = Get-Command $Command -ErrorAction SilentlyContinue
    return $?
}

function Get-FreePort {
    param([int]$StartPort)
    $port = $StartPort
    while ($port -lt ($StartPort + 1000)) {
        $tcpConnections = netstat -ano | Select-String ":$port\s"
        if (-not $tcpConnections) {
            return $port
        }
        $port++
    }
    throw "Could not find free port starting from $StartPort"
}

function Stop-ProcessOnPort {
    param([int]$Port)
    $connections = netstat -ano | Select-String ":$Port\s+.*LISTENING"
    if ($connections) {
        foreach ($conn in $connections) {
            if ($conn -match '\s+(\d+)\s*$') {
                $pid = $Matches[1]
                try {
                    Stop-Process -Id $pid -Force -ErrorAction SilentlyContinue
                } catch {}
            }
        }
    }
}

# Track processes for cleanup
$script:AIR_PROCESS = $null
$script:ELECTRON_PROCESS = $null
$script:TEMPORAL_CONTAINER_NAME = $null
$script:TEMPORAL_UI_STARTED = $false
$script:CLEANUP_DONE = $false

function Test-DockerDaemonRunning {
    if (-not (Test-Command "docker")) {
        return $false
    }

    $prevErrorAction = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    $null = docker ps 2>&1
    $dockerExitCode = $LASTEXITCODE
    $ErrorActionPreference = $prevErrorAction
    return ($dockerExitCode -eq 0)
}

function Get-ShortHash {
    param([string]$InputText)

    $sha1 = [System.Security.Cryptography.SHA1]::Create()
    try {
        $bytes = [System.Text.Encoding]::UTF8.GetBytes($InputText)
        $hashBytes = $sha1.ComputeHash($bytes)
        $hash = [System.BitConverter]::ToString($hashBytes).Replace("-", "").ToLower()
        return $hash.Substring(0, 8)
    } finally {
        $sha1.Dispose()
    }
}

function Get-WorktreePostgresDbName {
    param([string]$ProjectRoot)

    $repoToken = ""
    $worktreeToken = ""
    $normalizedPath = $ProjectRoot -replace "\\", "/"

    if ($normalizedPath -match "/\.reliant/worktrees/([^/]+)/([^/]+)") {
        $repoToken = $Matches[1]
        $worktreeToken = $Matches[2]
    } else {
        $repoTop = ""
        try {
            $repoTop = (git rev-parse --show-toplevel 2>$null).Trim()
        } catch {
            $repoTop = $ProjectRoot
        }
        if ([string]::IsNullOrWhiteSpace($repoTop)) {
            $repoTop = $ProjectRoot
        }
        $repoToken = Split-Path -Leaf $repoTop
        $worktreeToken = Split-Path -Leaf $ProjectRoot
    }

    $raw = "reliant_${repoToken}_${worktreeToken}".ToLower()
    $sanitized = [System.Text.RegularExpressions.Regex]::Replace($raw, "[^a-z0-9]+", "_")
    $sanitized = [System.Text.RegularExpressions.Regex]::Replace($sanitized, "_+", "_")
    $sanitized = $sanitized.Trim("_")

    if ([string]::IsNullOrWhiteSpace($sanitized)) {
        $sanitized = "reliant_dev"
    }

    if ($sanitized.Length -gt 63) {
        $suffix = Get-ShortHash -InputText $sanitized
        $prefixLen = 63 - 1 - $suffix.Length
        $sanitized = $sanitized.Substring(0, $prefixLen) + "_" + $suffix
    }

    return $sanitized
}

function Initialize-PostgresWorktreeDatabase {
    param(
        [string]$ProjectRoot,
        [string]$LogFile
    )

    $postgresComposeProject = if ([string]::IsNullOrWhiteSpace($env:RELIANT_POSTGRES_COMPOSE_PROJECT)) { "reliant" } else { $env:RELIANT_POSTGRES_COMPOSE_PROJECT }

    # Use RELIANT_POSTGRES_* vars for explicit overrides.
    # Do NOT read PG* here to avoid accidental cross-worktree leakage from parent shells.
    $dbName = Get-WorktreePostgresDbName -ProjectRoot $ProjectRoot
    $pgHost = if ([string]::IsNullOrWhiteSpace($env:RELIANT_POSTGRES_HOST)) { "localhost" } else { $env:RELIANT_POSTGRES_HOST }
    $pgPort = if ([string]::IsNullOrWhiteSpace($env:RELIANT_POSTGRES_PORT)) { "5433" } else { $env:RELIANT_POSTGRES_PORT }
    $pgUser = if ([string]::IsNullOrWhiteSpace($env:RELIANT_POSTGRES_USER)) { "postgres" } else { $env:RELIANT_POSTGRES_USER }
    $pgPassword = if ($null -eq $env:RELIANT_POSTGRES_PASSWORD) { "postgres" } else { $env:RELIANT_POSTGRES_PASSWORD }
    $pgSslMode = if ([string]::IsNullOrWhiteSpace($env:RELIANT_POSTGRES_SSLMODE)) { "disable" } else { $env:RELIANT_POSTGRES_SSLMODE }
    $env:RELIANT_POSTGRES_PORT = $pgPort

    Write-ColorOutput "Postgres dev mode enabled" "Blue"
    Write-ColorOutput "  Worktree database: $dbName" "Blue"
    Write-ColorOutput "  Server: ${pgHost}:${pgPort}" "Blue"

    if ($pgHost -ne "localhost" -and $pgHost -ne "127.0.0.1") {
        Write-ColorOutput "Automatic per-worktree Postgres bootstrap only supports localhost/127.0.0.1 currently" "Red"
        Write-ColorOutput "Received host: $pgHost" "Red"
        Write-ColorOutput "Set DATABASE_URL manually to a pre-created database, or use local docker-compose Postgres." "Red"
        exit 1
    }

    if (-not (Test-Command "docker")) {
        Write-ColorOutput "Docker is required for DATABASE_DRIVER=postgres local dev bootstrap" "Red"
        exit 1
    }
    if (-not (Test-DockerDaemonRunning)) {
        Write-ColorOutput "Docker daemon is not running" "Red"
        Write-ColorOutput "Start Docker Desktop and try again" "Red"
        exit 1
    }

    Write-ColorOutput "Ensuring local Postgres container is running..." "Blue"
    $prevErrorAction = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    docker compose -p $postgresComposeProject up -d postgres 2>&1 | Out-File -Append $LogFile -Encoding UTF8
    $composeExitCode = $LASTEXITCODE
    $ErrorActionPreference = $prevErrorAction
    if ($composeExitCode -ne 0) {
        Write-ColorOutput "Failed to start local Postgres container" "Red"
        Write-ColorOutput "Check $LogFile for details" "Red"
        exit 1
    }

    $ready = $false
    for ($i = 0; $i -lt 20; $i++) {
        $prevErrorAction = $ErrorActionPreference
        $ErrorActionPreference = "Continue"
        docker compose -p $postgresComposeProject exec -T postgres pg_isready -U $pgUser -d postgres 2>&1 | Out-Null
        $readyExitCode = $LASTEXITCODE
        $ErrorActionPreference = $prevErrorAction
        if ($readyExitCode -eq 0) {
            $ready = $true
            break
        }
        Start-Sleep -Seconds 1
    }

    if (-not $ready) {
        Write-ColorOutput "Postgres service is not ready: postgres" "Red"
        exit 1
    }

    $query = "SELECT 1 FROM pg_database WHERE datname='${dbName}'"
    $prevErrorAction = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    $dbExists = docker compose -p $postgresComposeProject exec -T -e "PGPASSWORD=$pgPassword" postgres psql -U $pgUser -d postgres -tAc $query 2>$null
    $existsExitCode = $LASTEXITCODE
    $ErrorActionPreference = $prevErrorAction
    $dbExists = ($dbExists | Out-String).Trim()

    if ($existsExitCode -ne 0) {
        Write-ColorOutput "Failed to verify worktree database existence" "Red"
        exit 1
    }

    if ($dbExists -ne "1") {
        Write-ColorOutput "Creating worktree Postgres database $dbName" "Blue"
        $createSql = "CREATE DATABASE `"$dbName`";"
        $prevErrorAction = $ErrorActionPreference
        $ErrorActionPreference = "Continue"
        docker compose -p $postgresComposeProject exec -T -e "PGPASSWORD=$pgPassword" postgres psql -U $pgUser -d postgres -c $createSql 2>&1 | Out-File -Append $LogFile -Encoding UTF8
        $createExitCode = $LASTEXITCODE
        $ErrorActionPreference = $prevErrorAction
        if ($createExitCode -ne 0) {
            Write-ColorOutput "Failed to create worktree database: $dbName" "Red"
            Write-ColorOutput "Check $LogFile for details" "Red"
            exit 1
        }
    } else {
        Write-ColorOutput "Worktree Postgres database already exists: $dbName" "Green"
    }

    $encodedPassword = [System.Uri]::EscapeDataString($pgPassword)
    if ([string]::IsNullOrEmpty($encodedPassword)) {
        $env:DATABASE_URL = "postgres://${pgUser}@${pgHost}:${pgPort}/${dbName}?sslmode=${pgSslMode}"
    } else {
        $env:DATABASE_URL = "postgres://${pgUser}:${encodedPassword}@${pgHost}:${pgPort}/${dbName}?sslmode=${pgSslMode}"
    }
    $env:DATABASE_DRIVER = "postgres"
    $env:PGDATABASE = $dbName
    # Force PG* to match selected local dev database to avoid inherited shell overrides
    # influencing pgx/libpq resolution in child processes.
    $env:PGHOST = $pgHost
    $env:PGPORT = $pgPort
    $env:PGUSER = $pgUser
    $env:PGSSLMODE = $pgSslMode

    Write-ColorOutput "Postgres per-worktree database ready" "Green"
    Write-ColorOutput "  DATABASE_DRIVER=$($env:DATABASE_DRIVER)" "Blue"
    Write-ColorOutput "  PGDATABASE=$($env:PGDATABASE)" "Blue"
    Write-ColorOutput "  DATABASE_URL=$($env:DATABASE_URL)" "Blue"
}

function Invoke-Cleanup {
    if ($script:CLEANUP_DONE) {
        return
    }
    $script:CLEANUP_DONE = $true

    Write-ColorOutput "`nReceived shutdown signal, cleaning up..." "Blue"

    if ($script:AIR_PROCESS -and -not $script:AIR_PROCESS.HasExited) {
        Write-ColorOutput "Stopping Air gracefully..." "Blue"
        # Try graceful shutdown first (sends SIGTERM to Air, which forwards to child)
        Stop-Process -Id $script:AIR_PROCESS.Id -ErrorAction SilentlyContinue
        # Wait up to 5 seconds for graceful shutdown
        $waited = 0
        while ($waited -lt 5000 -and -not $script:AIR_PROCESS.HasExited) {
            Start-Sleep -Milliseconds 500
            $waited += 500
        }
        # Force kill if still running
        if (-not $script:AIR_PROCESS.HasExited) {
            Write-ColorOutput "Air didn't stop gracefully, forcing..." "Yellow"
            Stop-Process -Id $script:AIR_PROCESS.Id -Force -ErrorAction SilentlyContinue
        }
    }

    if ($script:TEMPORAL_UI_STARTED -and $script:TEMPORAL_CONTAINER_NAME -and (Test-DockerDaemonRunning)) {
        Write-ColorOutput "Stopping Temporal UI container $script:TEMPORAL_CONTAINER_NAME..." "Blue"
        # Use shorter timeout (5s) since Temporal UI doesn't need graceful shutdown
        $prevErrorAction = $ErrorActionPreference
        $ErrorActionPreference = "Continue"
        $stopResult = docker stop -t 5 $script:TEMPORAL_CONTAINER_NAME 2>&1
        $ErrorActionPreference = $prevErrorAction
        if ($LASTEXITCODE -ne 0) {
            Write-ColorOutput "Warning: docker stop failed, forcing removal..." "Yellow"
            $prevErrorAction = $ErrorActionPreference
            $ErrorActionPreference = "Continue"
            docker rm -f $script:TEMPORAL_CONTAINER_NAME 2>$null
            $ErrorActionPreference = $prevErrorAction
        }
    }

    if ($script:ELECTRON_PROCESS -and -not $script:ELECTRON_PROCESS.HasExited) {
        Write-ColorOutput "Stopping Electron/Vite..." "Blue"
        Stop-Process -Id $script:ELECTRON_PROCESS.Id -Force -ErrorAction SilentlyContinue
    }

    Write-ColorOutput "Cleanup complete" "Green"
}

# Register cleanup on exit
Register-EngineEvent -SourceIdentifier PowerShell.Exiting -Action { Invoke-Cleanup } | Out-Null

try {
    # Check prerequisites
    Write-Step "Checking prerequisites..."

    if (-not (Test-Command "go")) {
        Write-ColorOutput "Go is not installed" "Red"
        exit 1
    }

    if (-not (Test-Command "npm")) {
        Write-ColorOutput "npm is not installed" "Red"
        exit 1
    }

    if (-not (Test-Command "node")) {
        Write-ColorOutput "Node.js is not installed" "Red"
        exit 1
    }

    if (-not (Test-Command "air")) {
        Write-ColorOutput "Air is not installed" "Red"
        Write-ColorOutput "Install with: go install github.com/air-verse/air@latest" "Red"
        exit 1
    }

    Write-ColorOutput "Prerequisites check passed" "Green"

    # Install dependencies if needed
    Write-Step "Installing dependencies..."

    Push-Location $WEB_DIR
    if (-not (Test-Path "node_modules")) {
        Write-Host "Installing web dependencies..."
        # npm emits some warnings on stderr; treat only non-zero exit as failure
        $prevErrorAction = $ErrorActionPreference
        $ErrorActionPreference = "Continue"
        npm ci --legacy-peer-deps 2>&1 | Out-File -Append $LOG_FILE -Encoding UTF8
        $npmExitCode = $LASTEXITCODE
        $ErrorActionPreference = $prevErrorAction
        if ($npmExitCode -ne 0) {
            Write-ColorOutput "Failed to install web dependencies. Check $LOG_FILE for details" "Red"
            exit 1
        }
    }
    Pop-Location

    Push-Location $ELECTRON_DIR
    if (-not (Test-Path "node_modules")) {
        Write-Host "Installing Electron dependencies..."
        # npm emits some warnings on stderr; treat only non-zero exit as failure
        $prevErrorAction = $ErrorActionPreference
        $ErrorActionPreference = "Continue"
        npm ci 2>&1 | Out-File -Append $LOG_FILE -Encoding UTF8
        $npmExitCode = $LASTEXITCODE
        $ErrorActionPreference = $prevErrorAction
        if ($npmExitCode -ne 0) {
            Write-ColorOutput "Failed to install Electron dependencies. Check $LOG_FILE for details" "Red"
            exit 1
        }
    }
    Pop-Location

    Write-ColorOutput "Dependencies ready" "Green"

    # Generate protobuf code
    Write-Step "Generating protobuf code..."
    Push-Location $PROJECT_ROOT
    if (Test-Command "buf") {
        # Add Go bin and node_modules/.bin to PATH for protoc plugins
        $goBin = Join-Path $env:USERPROFILE "go\bin"
        $nodeBin = "$WEB_DIR\node_modules\.bin"
        $env:PATH = "$goBin;$nodeBin;$env:PATH"

        # Run buf generate - temporarily allow errors since buf outputs info to stderr
        $prevErrorAction = $ErrorActionPreference
        $ErrorActionPreference = "Continue"
        & buf generate 2>&1 | Out-File -Append $LOG_FILE -Encoding UTF8
        $bufExitCode = $LASTEXITCODE
        $ErrorActionPreference = $prevErrorAction
        if ($bufExitCode -ne 0) {
            Write-ColorOutput "Failed to generate protobuf code. Check $LOG_FILE for details" "Red"
            Get-Content $LOG_FILE -Tail 20
            exit 1
        }
        Write-ColorOutput "Protobuf code generated" "Green"
    } else {
        Write-ColorOutput "buf not installed, skipping proto generation..." "Yellow"
    }
    Pop-Location

    # Find available ports
    Write-Step "Finding available ports..."
    Push-Location $PROJECT_ROOT

    # Run the consolidated port finder (handles all ports with locking)
    # This script finds Frontend, Backend, gRPC, Temporal (4 consecutive), and Temporal UI ports
    # It uses a global lock (~/.reliant/ports.lock) to prevent race conditions across instances
    $prevErrorAction = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    $portOutput = node scripts/find-ports.js 2>&1 | Out-String
    $portExitCode = $LASTEXITCODE
    $ErrorActionPreference = $prevErrorAction
    if ($portExitCode -ne 0) {
        Write-ColorOutput "Failed to find available ports" "Red"
        Write-ColorOutput $portOutput "Red"
        exit 1
    }

    # Parse all port assignments from the Node.js script output
    $FRONTEND_PORT = $null
    $BACKEND_PORT = $null
    $GRPC_PORT = $null
    $TOOLS_DAEMON_PORT = $null
    $DAEMON_FRONTEND_PORT = $null
    $TEMPORAL_FRONTEND_PORT = $null
    $TEMPORAL_UI_PORT = $null
    foreach ($line in $portOutput -split "`n") {
        if ($line -match "^FRONTEND_PORT=(\d+)") { $FRONTEND_PORT = [int]$Matches[1] }
        if ($line -match "^BACKEND_PORT=(\d+)") { $BACKEND_PORT = [int]$Matches[1] }
        if ($line -match "^GRPC_PORT=(\d+)") { $GRPC_PORT = [int]$Matches[1] }
        if ($line -match "^TOOLS_DAEMON_PORT=(\d+)") { $TOOLS_DAEMON_PORT = [int]$Matches[1] }
        if ($line -match "^DAEMON_FRONTEND_PORT=(\d+)") { $DAEMON_FRONTEND_PORT = [int]$Matches[1] }
        if ($line -match "^TEMPORAL_FRONTEND_PORT=(\d+)") { $TEMPORAL_FRONTEND_PORT = [int]$Matches[1] }
        if ($line -match "^TEMPORAL_UI_PORT=(\d+)") { $TEMPORAL_UI_PORT = [int]$Matches[1] }
        if ($line -match "^PPROF_PORT=(\d+)") { $PPROF_PORT = [int]$Matches[1] }
    }

    # Verify all ports were found
    if (-not $FRONTEND_PORT -or -not $BACKEND_PORT -or -not $GRPC_PORT -or -not $TOOLS_DAEMON_PORT -or -not $DAEMON_FRONTEND_PORT -or -not $TEMPORAL_FRONTEND_PORT -or -not $TEMPORAL_UI_PORT -or -not $PPROF_PORT) {
        Write-ColorOutput "Failed to parse all required ports" "Red"
        Write-ColorOutput "FRONTEND_PORT=$FRONTEND_PORT, BACKEND_PORT=$BACKEND_PORT, GRPC_PORT=$GRPC_PORT, TOOLS_DAEMON_PORT=$TOOLS_DAEMON_PORT, TEMPORAL_FRONTEND_PORT=$TEMPORAL_FRONTEND_PORT, TEMPORAL_UI_PORT=$TEMPORAL_UI_PORT" "Red"
        exit 1
    }

    $effectiveDbDriver = if ([string]::IsNullOrWhiteSpace($env:DATABASE_DRIVER)) { "sqlite" } else { $env:DATABASE_DRIVER }
    if ($effectiveDbDriver -eq "postgres") {
        Write-Step "Bootstrapping per-worktree Postgres database..."
        Initialize-PostgresWorktreeDatabase -ProjectRoot $PROJECT_ROOT -LogFile $LOG_FILE
    }

    Write-ColorOutput "Found available ports" "Green"
    Write-ColorOutput "  Frontend: $FRONTEND_PORT" "Blue"
    Write-ColorOutput "  Backend: $BACKEND_PORT" "Blue"
    Write-ColorOutput "  gRPC: $GRPC_PORT" "Blue"
    Write-ColorOutput "  Tools daemon gRPC: $TOOLS_DAEMON_PORT" "Blue"
    Write-ColorOutput "  Daemon frontend: $DAEMON_FRONTEND_PORT" "Blue"
    Write-ColorOutput "  Temporal:" "White"
    Write-ColorOutput "    Frontend (client API):  $TEMPORAL_FRONTEND_PORT" "Blue"
    Write-ColorOutput "    History:                $($TEMPORAL_FRONTEND_PORT + 1)" "Blue"
    Write-ColorOutput "    Matching:               $($TEMPORAL_FRONTEND_PORT + 2)" "Blue"
    Write-ColorOutput "    Worker:                 $($TEMPORAL_FRONTEND_PORT + 3)" "Blue"
    Write-ColorOutput "  Temporal UI: $TEMPORAL_UI_PORT" "Blue"
    Write-ColorOutput "  pprof: $PPROF_PORT" "Blue"

    # Set environment variables
    $env:NODE_ENV = "development"
    $env:RELIANT_ENV = "dev"  # Canonical env detection for Go backend (used by config.IsDevelopmentEnvironment())
    $env:FRONTEND_PORT = $FRONTEND_PORT
    $env:BACKEND_PORT = $BACKEND_PORT
    $env:API_PORT = $BACKEND_PORT
    $env:GRPC_PORT = $GRPC_PORT
    $env:TOOLS_DAEMON_PORT = $TOOLS_DAEMON_PORT
    $env:DAEMON_FRONTEND_PORT = $DAEMON_FRONTEND_PORT
    $env:VITE_GRPC_PORT = $GRPC_PORT
    $env:TEMPORAL_FRONTEND_PORT = $TEMPORAL_FRONTEND_PORT
    $env:TEMPORAL_UI_PORT = $TEMPORAL_UI_PORT
    $env:PPROF_PORT = $PPROF_PORT
    $env:USE_DEV_SERVER = "true"
    $env:RELIANT_EXTERNAL_BACKEND = "true"

    # Save ports to file
    @"
export FRONTEND_PORT=$FRONTEND_PORT
export BACKEND_PORT=$BACKEND_PORT
export GRPC_PORT=$GRPC_PORT
export TOOLS_DAEMON_PORT=$TOOLS_DAEMON_PORT
export DAEMON_FRONTEND_PORT=$DAEMON_FRONTEND_PORT
export TEMPORAL_FRONTEND_PORT=$TEMPORAL_FRONTEND_PORT
export TEMPORAL_UI_PORT=$TEMPORAL_UI_PORT
export PPROF_PORT=$PPROF_PORT
$(if ($effectiveDbDriver -eq "postgres") { "export DATABASE_DRIVER=postgres`nexport DATABASE_URL=$($env:DATABASE_URL)`nexport PGDATABASE=$($env:PGDATABASE)`nexport PGHOST=$($env:PGHOST)`nexport PGPORT=$($env:PGPORT)`nexport PGUSER=$($env:PGUSER)`nexport PGSSLMODE=$($env:PGSSLMODE)`nexport RELIANT_POSTGRES_PORT=$($env:RELIANT_POSTGRES_PORT)" })
"@ | Out-File -FilePath ".dev-ports.sh" -Encoding UTF8

    # Kill any old processes on these ports
    Write-Step "Killing any old processes on these ports..."
    Stop-ProcessOnPort -Port $FRONTEND_PORT
    Stop-ProcessOnPort -Port $BACKEND_PORT
    Write-ColorOutput "Ports are clear" "Green"

    # Start Temporal UI
    Write-Step "Starting Temporal UI..."
    $TEMPORAL_ADDRESS = "host.docker.internal:$TEMPORAL_FRONTEND_PORT"
    $script:TEMPORAL_CONTAINER_NAME = "temporal-ui-dev-$BACKEND_PORT"

    if (-not (Test-Command "docker")) {
        Write-ColorOutput "Docker is required for V2 backend (Temporal UI)" "Red"
        Write-ColorOutput "Install Docker Desktop: https://www.docker.com/products/docker-desktop" "Red"
        exit 1
    }

    # Check if Docker daemon is running
    $prevErrorAction = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    $null = docker ps 2>&1
    $dockerExitCode = $LASTEXITCODE
    $ErrorActionPreference = $prevErrorAction
    if ($dockerExitCode -ne 0) {
        Write-ColorOutput "Docker daemon is not running" "Red"
        Write-ColorOutput "Start Docker Desktop and try again" "Red"
        exit 1
    }

    # Stop any existing container
    $ErrorActionPreference = "Continue"
    $existingContainer = docker ps -a --filter "name=$script:TEMPORAL_CONTAINER_NAME" --format "{{.Names}}" 2>&1 | Out-String
    if ($existingContainer -match $script:TEMPORAL_CONTAINER_NAME) {
        Write-ColorOutput "Stopping existing Temporal UI container..." "Blue"
        docker stop $script:TEMPORAL_CONTAINER_NAME 2>&1 | Out-Null
        docker rm $script:TEMPORAL_CONTAINER_NAME 2>&1 | Out-Null
    }
    $ErrorActionPreference = "Stop"

    Write-ColorOutput "Starting Temporal UI with Docker on port $TEMPORAL_UI_PORT..." "Green"
    $script:TEMPORAL_UI_STARTED = $false
    $ErrorActionPreference = "Continue"
    $dockerRunOutput = docker run -d `
        --name $script:TEMPORAL_CONTAINER_NAME `
        --rm `
        -p "${TEMPORAL_UI_PORT}:8080" `
        -e "TEMPORAL_ADDRESS=$TEMPORAL_ADDRESS" `
        -e "TEMPORAL_CORS_ORIGINS=http://localhost:3000" `
        temporalio/ui:latest 2>&1
    $dockerRunExitCode = $LASTEXITCODE
    $dockerRunOutput | Out-File -Append $LOG_FILE -Encoding UTF8
    $ErrorActionPreference = "Stop"

    if ($dockerRunExitCode -ne 0) {
        Write-ColorOutput "Failed to start Temporal UI container (continuing without UI)" "Yellow"
        if (($dockerRunOutput | Out-String) -match "port is already allocated") {
            Write-ColorOutput "Port $TEMPORAL_UI_PORT is already in use by another container/process." "Yellow"
            Write-ColorOutput "Backend will still run. If needed, rerun to pick a new dynamic port." "Yellow"
        } else {
            Write-ColorOutput "See $LOG_FILE for Docker error details." "Yellow"
        }
    } else {
        $script:TEMPORAL_UI_STARTED = $true
        Write-ColorOutput "Temporal UI started on http://localhost:$TEMPORAL_UI_PORT" "Green"
    }

    # Start backend with Air
    Write-Step "Starting backend with Air for hot reload..."
    Push-Location $PROJECT_ROOT

    $airLogFile = Join-Path $DEV_DIR "air-stdout.log"
    # Use Windows-specific Air config that uses Windows commands (move instead of mv)
    $script:AIR_PROCESS = Start-Process -FilePath "air" -ArgumentList "-c", ".air.windows.toml" `
        -RedirectStandardOutput $airLogFile `
        -RedirectStandardError (Join-Path $DEV_DIR "air-stderr.log") `
        -PassThru -NoNewWindow

    Write-ColorOutput "Backend started with Air (PID: $($script:AIR_PROCESS.Id))" "Green"
    Write-ColorOutput "   Air will auto-reload on Go file changes" "Blue"

    # Give backend a moment to start
    Start-Sleep -Seconds 2

    # Start Vite + Electron
    Write-Step "Starting Vite + Electron..."
    Push-Location $ELECTRON_DIR

    Write-ColorOutput "This will start:" "Green"
    Write-ColorOutput "  1. Go V2 backend server on port $BACKEND_PORT (with Air hot reload)" "Blue"
    Write-ColorOutput "  2. Embedded Temporal server:" "Blue"
    Write-ColorOutput "       Frontend: $TEMPORAL_FRONTEND_PORT, History: $($TEMPORAL_FRONTEND_PORT + 1), Matching: $($TEMPORAL_FRONTEND_PORT + 2), Worker: $($TEMPORAL_FRONTEND_PORT + 3)" "Blue"
    Write-ColorOutput "  3. Temporal UI (web UI on port $TEMPORAL_UI_PORT)" "Blue"
    Write-ColorOutput "  4. Vite dev server on port $FRONTEND_PORT" "Blue"
    Write-ColorOutput "  5. Electron app" "Blue"

    $electronLogFile = Join-Path $DEV_DIR "electron-stdout.log"
    # Use cmd.exe to run npm since npm is a script on Windows
    $script:ELECTRON_PROCESS = Start-Process -FilePath "cmd.exe" -ArgumentList "/c", "npm run dev" `
        -RedirectStandardOutput $electronLogFile `
        -RedirectStandardError (Join-Path $DEV_DIR "electron-stderr.log") `
        -PassThru -NoNewWindow

    Write-ColorOutput "Electron/Vite started (PID: $($script:ELECTRON_PROCESS.Id))" "Green"
    Write-ColorOutput "`nDevelopment environment is running!" "Green"
    Write-ColorOutput "All logs: $LOG_FILE" "Blue"
    Write-ColorOutput "Air logs: $airLogFile" "Blue"
    Write-ColorOutput "Electron logs: $electronLogFile" "Blue"
    Write-ColorOutput "Press Ctrl-C to stop all services`n" "Yellow"

    Pop-Location
    Pop-Location

    # Wait for Electron to finish
    $script:ELECTRON_PROCESS.WaitForExit()

} finally {
    Invoke-Cleanup
}
