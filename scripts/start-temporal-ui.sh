#!/bin/bash
set -e

# Start Temporal UI using Docker

# Use host.docker.internal to access host's localhost from inside Docker
TEMPORAL_ADDRESS=${TEMPORAL_ADDRESS:-host.docker.internal:7234}
UI_PORT=${UI_PORT:-8233}

# Check if Docker is installed
if ! command -v docker >/dev/null 2>&1; then
  echo "❌ Docker is not installed"
  echo "   Install Docker Desktop: https://www.docker.com/products/docker-desktop"
  exit 1
fi

# Check if Docker daemon is running
if ! docker ps >/dev/null 2>&1; then
  echo "❌ Docker daemon is not running"
  echo "   Start Docker Desktop and try again"
  exit 1
fi

echo "Starting Temporal UI..."
echo "  Temporal Address: $TEMPORAL_ADDRESS"
echo "  UI Port: $UI_PORT"

# Use --add-host to ensure host.docker.internal works on all platforms
docker run --rm -it \
  --name temporal-ui \
  --add-host=host.docker.internal:host-gateway \
  -p $UI_PORT:8080 \
  -e TEMPORAL_ADDRESS=$TEMPORAL_ADDRESS \
  -e TEMPORAL_CORS_ORIGINS=http://localhost:3000 \
  temporalio/ui:latest
