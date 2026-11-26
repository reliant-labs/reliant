// A tiny HTTP service that asks an LLM for a haiku.
//
// It holds no provider key. forge.llm_env() (deploy/kcl/workloads.k) hands it
// OPENAI_BASE_URL and OPENAI_API_KEY for Reliant's LLM gateway, and the stock
// OpenAI SDK reads both with no configuration here. When they are blank (an
// unclaimed claimable deploy, which has no outbound internet) it answers 503
// instead of crashing.
const http = require("node:http");
const OpenAI = require("openai");

const model = process.env.LLM_MODEL || "claude-haiku-4-5";
const client = process.env.OPENAI_API_KEY ? new OpenAI() : null;

async function haiku(topic) {
  const completion = await client.chat.completions.create({
    model,
    max_tokens: 100,
    messages: [{ role: "user", content: `Write a haiku about ${topic}.` }],
  });
  return completion.choices[0].message.content;
}

// Only holders of this separate application token may spend the org's budget.
// Never use the gateway credential as the visitor credential.
function createServer({
  invoke = haiku,
  token = process.env.APP_API_TOKEN,
  available = Boolean(client),
} = {}) {
  return http.createServer(async (req, res) => {
    const url = new URL(req.url, "http://localhost");
    const send = (status, body) => {
      res.writeHead(status, { "content-type": "application/json" });
      res.end(JSON.stringify(body));
    };
    if (url.pathname === "/healthz") return send(200, { ok: true });
    if (url.pathname !== "/") return send(404, { error: "not found" });
    if (!token) return send(503, { error: "Caller access is not configured" });
    const { timingSafeEqual, createHash } = require("node:crypto");
    const digest = (value) => createHash("sha256").update(value).digest();
    if (
      !timingSafeEqual(
        digest(req.headers.authorization || ""),
        digest(`Bearer ${token}`),
      )
    ) {
      return send(401, { error: "Unauthorized" });
    }
    if (!available) {
      return send(503, {
        error:
          "LLM unavailable: no gateway key. Claim the deploy (or sign in and deploy without --claimable).",
      });
    }
    try {
      const topic = (
        url.searchParams.get("topic") || "deploying on a Friday"
      ).slice(0, 100);
      send(200, { model, topic, haiku: await invoke(topic) });
    } catch (err) {
      console.error(err);
      send(502, { error: "LLM call failed" });
    }
  });
}

if (require.main === module) {
  const port = Number(process.env.PORT || 3000);
  createServer().listen(port, () =>
    console.log(`listening on :${port} (model ${model})`),
  );
}
module.exports = { createServer };
