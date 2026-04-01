#!/bin/bash

# Test setup script for authentication
# This script creates test users for manual testing

set -e

echo "🧪 Setting up test data for authentication..."

# Check if Supabase is running
if ! curl -s http://127.0.0.1:54331/rest/v1/ > /dev/null; then
    echo "❌ Supabase is not running. Please run 'supabase start' first."
    exit 1
fi

# Get Supabase configuration
SUPABASE_URL="http://127.0.0.1:54331"
SUPABASE_ANON_KEY="eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZS1kZW1vIiwicm9sZSI6ImFub24iLCJleHAiOjE5ODM4MTI5OTZ9.CRXP1A7WOeoJeXxjNni43kdQwgnWNReilDMblYTn_I0"

echo "📧 Creating test user via Supabase Auth API..."

# Try to create a test user
RESPONSE=$(curl -s -w "%{http_code}" -X POST "${SUPABASE_URL}/auth/v1/signup" \
  -H "apikey: ${SUPABASE_ANON_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "email": "test@reliant.ai",
    "password": "password123"
  }')

HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
BODY=$(echo "$RESPONSE" | head -n -1)

if [ "$HTTP_CODE" == "200" ]; then
    echo "✅ Test user created successfully"
    echo "   Email: test@reliant.ai"
    echo "   Password: password123"
elif [ "$HTTP_CODE" == "422" ] && echo "$BODY" | grep -q "already exists"; then
    echo "ℹ️  Test user already exists"
    echo "   Email: test@reliant.ai"
    echo "   Password: password123"
else
    echo "❌ Failed to create test user (HTTP $HTTP_CODE):"
    echo "$BODY"
    echo ""
    echo "This is likely due to database security triggers."
    echo "You can still test the CLI commands - they will show appropriate error messages."
fi

echo ""
echo "🧪 Test commands you can run:"
echo ""
echo "1. Test email/password authentication:"
echo "   export SUPABASE_URL=\"$SUPABASE_URL\""
echo "   export SUPABASE_ANON_KEY=\"$SUPABASE_ANON_KEY\""
echo "   ./reliant_cli auth login --method email"
echo ""
echo "2. Test OAuth authentication (requires OAuth setup):"
echo "   ./reliant_cli auth login --method github"
echo "   ./reliant_cli auth login --method google"
echo ""
echo "3. Test authentication status:"
echo "   ./reliant_cli auth status"
echo ""
echo "4. Test logout:"
echo "   ./reliant_cli auth logout"
echo ""
echo "📖 Run tests:"
echo "   go test ./internal/auth -v"
echo "   go test ./cmd -v"
echo ""
echo "   # Run integration tests (requires Supabase running):"
echo "   go test ./internal/auth -v -tags=integration"