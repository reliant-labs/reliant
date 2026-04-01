#!/bin/bash
# Generate schema.sql from database migrations
# This ensures schema.sql stays in sync with migrations

set -e

MIGRATIONS_DIR="internal/db/migrations/sqlite"
SCHEMA_FILE="internal/db/schema.sql"
DB_FILE="data/reliant.db"
TEMP_DB="/tmp/reliant-schema-gen.db"

echo "🔄 Generating schema.sql from migrations..."

# Clean up temp DB if it exists
rm -f "$TEMP_DB"

# Apply all migrations to temp database
echo "📦 Applying migrations to temporary database..."
goose -dir "$MIGRATIONS_DIR" sqlite3 "$TEMP_DB" up

# Generate schema from temp database
echo "📝 Generating schema.sql..."
sqlite3 "$TEMP_DB" ".schema" > "$SCHEMA_FILE"

# Clean up temp database
rm -f "$TEMP_DB"

echo "✅ Schema generated at $SCHEMA_FILE"
echo ""
echo "📊 Schema stats:"
wc -l "$SCHEMA_FILE"
sqlite3 "$DB_FILE" "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name;" | wc -l | xargs echo "  Tables:"
