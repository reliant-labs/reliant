# Vertex AI Driver

Driver for Google Cloud Vertex AI supporting Gemini and Claude models. See the
model catalog for the current model IDs and use a location that supports the
model you select.

## Supported Models

### Gemini Models
- **Gemini 2.5 Pro** (`vertex-gemini-2.5-pro`)
- **Gemini 2.5 Flash** (`vertex-gemini-2.5-flash`)
- **Gemini 2.0 Flash** (`vertex-gemini-2.0-flash`)
- **Gemini 1.5 Pro** (`vertex-gemini-1.5-pro`)
- **Gemini 1.5 Flash** (`vertex-gemini-1.5-flash`)

### Gemini Feature Support
- Text generation, streaming, function calling, multimodal (images via InlineData), safety settings, token counting

## Setup

Vertex AI runs on reliant's multi-tenant server roles, so it never uses the
server's own Google credentials (Application Default Credentials, the metadata
server, `GOOGLE_APPLICATION_CREDENTIALS`, a gcloud login). Each user or org
connects a **service-account key** in Settings, like an API key; calls are
billed to and placed in the key's `project_id`.

```bash
gcloud iam service-accounts create vertexai-driver --display-name="Vertex AI Driver"
gcloud projects add-iam-policy-binding YOUR_PROJECT_ID \
  --member="serviceAccount:vertexai-driver@YOUR_PROJECT_ID.iam.gserviceaccount.com" \
  --role="roles/aiplatform.user"
gcloud iam service-accounts keys create key.json \
  --iam-account=vertexai-driver@YOUR_PROJECT_ID.iam.gserviceaccount.com
# Paste the contents of key.json into Settings → Providers → Vertex AI.
```

Only `service_account` keys are accepted. Workload-identity
(`external_account`) configs are refused: their subject token comes from a
file, URL or program, which on a server would be the server's own. The key's
`token_uri` must be Google's; tokens are self-signed JWTs.

The region is the server's `VERTEXAI_LOCATION` (default `us-central1`).
`VERTEXAI_PROJECT` (`llm.vertexai_project`), when set, is the project every call
targets, as before; unset, each call goes to the connected key's project. The
call is signed by the connected key either way, so a configured project must
grant that key Vertex AI.

## Post-deploy verification and recovery

In an approved canary environment, use a dedicated test org and Google Cloud
project with Vertex AI enabled, the Claude partner model enabled in the
configured location, and a connected test service-account key. With model
fallback disabled, run a one-message chat for an available Vertex Gemini
model and one for an available Vertex Claude model. Both must stream an answer
and finish successfully with the requested Vertex model; confirm in the test
Google Cloud project's request metrics that both model calls reached that
project. Do not log the key or authorization headers. A separate test account
without that key must not be able to complete a Vertex request.

If either model fails, stop promotion and use a working, non-Vertex provider
via an existing connected account. If already promoted, stop using Vertex for
the affected accounts and roll forward with a fix that keeps the connected-key
requirement; do not roll back to a build with server ADC fallback.
`VERTEXAI_PROJECT` only redirects which project the account's key calls; it
never brings the server's credentials back. Repeat both canary checks before
resuming promotion.

## Usage

```go
import "github.com/reliant-labs/reliant/internal/llm/models"

preferences := models.Preferences{
    {ModelID: models.VertexGemini38Flash},
}

// With custom settings
preferences := models.Preferences{
    {
        ModelID:     models.VertexGemini38Flash,
        Temperature: models.TempMedium(),
        TokenBudget: genai.Ptr(int64(8000)),
    },
}
```

Safety levels via `VERTEXAI_SAFETY_LEVEL`: `"low"`, `"medium"` (default), `"high"`.

## Architecture

```
VertexAIClient
├── Gemini Provider (via google.golang.org/genai)
│   ├── SendMessages
│   ├── StreamResponse
│   └── Function Calling
└── Claude Provider
    └── Via Vertex AI Anthropic REST API
```

## Limitations

1. **Prompt caching** not yet implemented for Gemini
2. **Extended thinking** not yet supported

## Troubleshooting

### "vertex ai needs a service-account key: connect Vertex AI from Settings"
Connect a service-account key (see Setup).

### "Permission denied"
```bash
gcloud projects add-iam-policy-binding YOUR_PROJECT_ID \
  --member="user:your-email@example.com" \
  --role="roles/aiplatform.user"
```

### Model not found
Ensure you're using the correct model ID with the `vertex-` prefix and that the model is available in your region.

## Development

### Running Tests

```bash
go test ./internal/llm/drivers/vertexai/...
```

### Adding New Models

1. Add model constants to `internal/llm/models/vertexai.go`
2. Update the `SupportedModels` array in `internal/llm/drivers/vertexai/models.go`
3. Register the model in the init function

## References

- [Vertex AI Documentation](https://cloud.google.com/vertex-ai/docs)
- [Google GenAI Go SDK](https://pkg.go.dev/google.golang.org/genai)
- [Vertex AI Model Garden](https://cloud.google.com/vertex-ai/docs/model-garden)
- [Anthropic on Vertex AI](https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude)
