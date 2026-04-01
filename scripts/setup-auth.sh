#!/bin/bash

echo "🔐 Setting up Reliant CLI Authentication"
echo "========================================"

# Check if Supabase is running
if ! command -v supabase &> /dev/null; then
    echo "❌ Supabase CLI not found. Please install it first:"
    echo "   npm install -g supabase"
    exit 1
fi

# Get Supabase status
echo "📡 Getting Supabase configuration..."
SUPABASE_STATUS=$(supabase status 2>/dev/null)

if [ $? -ne 0 ]; then
    echo "❌ Supabase is not running. Starting it now..."
    supabase start
    SUPABASE_STATUS=$(supabase status)
fi

# Extract API URL and anon key
API_URL=$(echo "$SUPABASE_STATUS" | grep "API URL" | awk '{print $3}')
ANON_KEY=$(echo "$SUPABASE_STATUS" | grep "anon key" | awk '{print $3}')

if [ -z "$API_URL" ] || [ -z "$ANON_KEY" ]; then
    echo "❌ Failed to get Supabase configuration"
    echo "Please run 'supabase status' manually and check the output"
    exit 1
fi

echo "✅ Supabase is running:"
echo "   API URL: $API_URL"
echo "   Anon Key: ${ANON_KEY:0:20}..."

# Export environment variables
export SUPABASE_URL="$API_URL"
export SUPABASE_ANON_KEY="$ANON_KEY"

echo ""
echo "🚀 Environment configured! You can now use:"
echo "   export SUPABASE_URL=\"$API_URL\""
echo "   export SUPABASE_ANON_KEY=\"$ANON_KEY\""
echo ""
echo "   ./reliant_cli auth login"
echo "   ./reliant_cli auth status"
echo "   ./reliant_cli session start"
echo ""
echo "💡 Add these exports to your shell profile (.bashrc, .zshrc) for persistence"