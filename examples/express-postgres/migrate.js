// Create the schema. Idempotent: forge runs it before every rollout.
const { Client } = require("pg");

(async () => {
  const client = new Client({ connectionString: process.env.DATABASE_URL });
  await client.connect();
  await client.query(
    "CREATE TABLE IF NOT EXISTS visits (id BIGSERIAL PRIMARY KEY, at TIMESTAMPTZ NOT NULL DEFAULT now())",
  );
  await client.end();
  console.log("migrated");
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
