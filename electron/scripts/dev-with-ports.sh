#!/bin/bash

# Set default ports if not provided
export FRONTEND_PORT=${FRONTEND_PORT:-5173}
export BACKEND_PORT=${BACKEND_PORT:-8080}

echo "Starting Electron dev with ports:"
echo "  Frontend: $FRONTEND_PORT"
echo "  Backend: $BACKEND_PORT"

# Run the actual dev command
exec npm run dev