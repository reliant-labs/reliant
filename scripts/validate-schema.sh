#!/bin/bash
# Validate that schema.sql is in sync with migrations
# Exits with error if they don't match

set -e

MIGRATIONS_DIR="internal/db/migrations/sqlite"
SCHEMA_FILE="internal/db/schema.sql"
TEMP_DB="/tmp/reliant-schema-validate.db"
TEMP_SCHEMA="/tmp/reliant-schema-validate.sql"

echo "🔍 Validating schema.sql against migrations..."

# Clean up temp files
rm -f "$TEMP_DB" "$TEMP_SCHEMA"

# Apply all migrations to temp database
goose -dir "$MIGRATIONS_DIR" sqlite3 "$TEMP_DB" up 2>&1 | grep -v "OK" || true

# Generate schema from temp database
sqlite3 "$TEMP_DB" ".schema" > "$TEMP_SCHEMA"

# Compare schemas (ignore goose_db_version table and whitespace differences)
CURRENT_SCHEMA=$(grep -v "goose_db_version" "$SCHEMA_FILE" | grep -v "^--" | tr -s ' ' | sort)
EXPECTED_SCHEMA=$(grep -v "goose_db_version" "$TEMP_SCHEMA" | grep -v "^--" | tr -s ' ' | sort)

if [ "$CURRENT_SCHEMA" != "$EXPECTED_SCHEMA" ]; then
    echo "❌ ERROR: schema.sql is out of sync with migrations!"
    echo ""
    echo "Run this command to regenerate:"
    echo "  make schema-generate"
    echo ""

    # Show diff if available
    if command -v diff >/dev/null 2>&1; then
        echo "Differences:"
        diff <(echo "$CURRENT_SCHEMA") <(echo "$EXPECTED_SCHEMA") || true
    fi

    rm -f "$TEMP_DB" "$TEMP_SCHEMA"
    exit 1
fi

# Clean up
rm -f "$TEMP_DB" "$TEMP_SCHEMA"

echo "✅ schema.sql is in sync with migrations"
