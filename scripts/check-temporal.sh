#!/bin/bash
# Check Temporal setup

echo "🔍 Checking Temporal Setup..."
echo ""

# Check if Temporal server is running
echo "1. Checking Temporal Server (port 7235)..."
if nc -z localhost 7235 2>/dev/null; then
    echo "   ✅ Temporal server is running"
else
    echo "   ❌ Temporal server is NOT running"
    echo "      Start with: npm run dev:electron:v2"
fi

echo ""

# Check if Temporal UI is running
echo "2. Checking Temporal UI (port 8233)..."
if nc -z localhost 8233 2>/dev/null; then
    echo "   ✅ Temporal UI is running"
    echo "      Open: http://localhost:8233"
else
    echo "   ❌ Temporal UI is NOT running"
    echo "      Start with: npm run temporal:ui"
fi

echo ""

# Check if Docker is available
echo "3. Checking Docker..."
if command -v docker &> /dev/null; then
    if docker ps &> /dev/null; then
        echo "   ✅ Docker is available and running"
    else
        echo "   ⚠️  Docker is installed but not running"
        echo "      Start Docker Desktop"
    fi
else
    echo "   ❌ Docker is not installed"
    echo "      Install from: https://www.docker.com/products/docker-desktop"
fi

echo ""

# Check if Temporal CLI is available
echo "4. Checking Temporal CLI..."
if command -v temporal &> /dev/null; then
    echo "   ✅ Temporal CLI is installed"
    TEMPORAL_VERSION=$(temporal version 2>/dev/null | head -1)
    echo "      Version: $TEMPORAL_VERSION"
else
    echo "   ⚠️  Temporal CLI is not installed (optional)"
    echo "      Install with: brew install temporal"
fi

echo ""
echo "================================"
echo "Summary:"
echo "================================"

if nc -z localhost 7235 2>/dev/null && nc -z localhost 8233 2>/dev/null; then
    echo "✅ Everything is running! Open http://localhost:8233"
elif nc -z localhost 7235 2>/dev/null; then
    echo "⚠️  Temporal server is running, but UI is not"
    echo "   Run: npm run temporal:ui"
else
    echo "❌ Temporal server is not running"
    echo "   Run: npm run dev:electron:v2"
fi
