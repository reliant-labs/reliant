// A tiny Express + Postgres app: a visit counter.
// The schema is created by `node migrate.js`, which forge runs as a job
// before every rollout.
const express = require("express");
const { Pool } = require("pg");

const pool = new Pool({
  connectionString:
    process.env.DATABASE_URL ||
    "postgresql://postgres:postgres@localhost:5432/visits",
});

function createApp(database = pool) {
  const app = express();

  app.get("/healthz", async (_req, res, next) => {
    try {
      await database.query("SELECT 1");
      res.json({ ok: true });
    } catch (err) {
      next(err);
    }
  });

  app.get("/", async (_req, res, next) => {
    try {
      const { rows } = await database.query(
        "INSERT INTO visits DEFAULT VALUES RETURNING id",
      );
      res.json({ message: "hello from express", visit: Number(rows[0].id) });
    } catch (err) {
      next(err);
    }
  });

  app.use((err, _req, res, _next) => {
    console.error("Database request failed", err);
    res.status(503).json({ error: "Service temporarily unavailable" });
  });
  return app;
}

if (require.main === module) {
  const port = Number(process.env.PORT || 3000);
  createApp().listen(port, () => console.log(`listening on :${port}`));
}
module.exports = { createApp };
