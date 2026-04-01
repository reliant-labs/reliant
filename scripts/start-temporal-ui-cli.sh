#!/bin/bash
# Start Temporal UI using Temporal CLI

TEMPORAL_ADDRESS=${TEMPORAL_ADDRESS:-localhost:7235}
UI_PORT=${UI_PORT:-8233}

echo "Starting Temporal UI with CLI..."
echo "  Temporal Address: $TEMPORAL_ADDRESS"
echo "  UI Port: $UI_PORT"
echo ""
echo "Installing/updating Temporal CLI if needed..."

# Check if temporal CLI is installed
if ! command -v temporal &> /dev/null; then
    echo "Temporal CLI not found. Installing..."
    if [[ "$OSTYPE" == "darwin"* ]]; then
        # macOS
        brew install temporal
    else
        echo "Please install Temporal CLI manually: https://docs.temporal.io/cli#install"
        exit 1
    fi
fi

echo "Starting UI server..."
temporal server start-dev --ui-port $UI_PORT --port 0 --db-filename "" --headless &
TEMPORAL_PID=$!

# Wait a moment for server to start
sleep 2

echo ""
echo "✅ Temporal UI started!"
echo "   Open: http://localhost:$UI_PORT"
echo ""
echo "Press Ctrl+C to stop"

# Wait for interrupt
trap "kill $TEMPORAL_PID 2>/dev/null" EXIT
wait $TEMPORAL_PID
