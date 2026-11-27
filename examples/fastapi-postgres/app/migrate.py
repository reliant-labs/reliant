"""Create the schema. Idempotent: forge runs it before every rollout."""

from app.main import connect

with connect() as conn:
    conn.execute(
        "CREATE TABLE IF NOT EXISTS notes ("
        " id BIGSERIAL PRIMARY KEY,"
        " body TEXT NOT NULL,"
        " created_at TIMESTAMPTZ NOT NULL DEFAULT now())"
    )
print("migrated")
